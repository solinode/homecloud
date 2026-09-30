import { badRequest, err, getState, notFound, type DemoService, type Router } from "../engine"
import { DAY, ACCOUNT, REGION, ago, arn, nowIso, stableId } from "../util"
import type { EcrImage, EcrRepository, EcrRepositoryDetail, EcrStatus } from "@/lib/types"

export const ECR_REGISTRY = `${ACCOUNT}.dkr.ecr.${REGION}.amazonaws.com`

interface EcrState {
  repos: EcrRepository[]
  images: Record<string, EcrImage[]>
}
const S = (): EcrState => getState().ecr as EcrState

const NAME_RE = /^[a-z0-9]+(?:[._-][a-z0-9]+)*(?:\/[a-z0-9]+(?:[._-][a-z0-9]+)*)*$/

const digestOf = (repo: string, tag: string) => `sha256:${stableId("", `${repo}:${tag}`, 64)}`

function image(repo: string, tags: string[], sizeMb: number, age: number, platform = "linux/amd64"): EcrImage {
  const digest = digestOf(repo, tags[0] ?? "untagged")
  return {
    platform,
    tags,
    digest,
    size_bytes: Math.round(sizeMb * 1024 * 1024),
    media_type: "application/vnd.oci.image.manifest.v1+json",
    pushed_at: ago(age),
    uri: `${ECR_REGISTRY}/${repo}@${digest}`,
  }
}

function repo(name: string, description: string, age: number, mutable = true, tags: Record<string, string> = { app: "shop" }): EcrRepository {
  return { name, arn: arn("ecr", `repository/${name}`), uri: `${ECR_REGISTRY}/${name}`, tag_mutable: mutable, created_at: ago(age), description, tags }
}

function seed(): EcrState {
  const repos = [
    repo("shop/web", "Storefront web application", 120 * DAY),
    repo("shop/worker", "Background worker for order processing", 110 * DAY),
    repo("shop/notifier", "Order notification Lambda container image", 33 * DAY, false),
    repo("shop/api", "Catalog API service", 95 * DAY),
    repo("shop/batch", "Nightly batch jobs", 60 * DAY, true, { app: "shop", team: "analytics" }),
    repo("shop/base-images/python", "Hardened Python base image", 200 * DAY, false, { app: "shop", team: "platform" }),
    repo("shop/scratch", "Empty repository for experiments", 4 * DAY),
  ]
  const images: Record<string, EcrImage[]> = {
    "shop/web": [
      image("shop/web", ["latest", "v2.14.0"], 142, 2 * DAY),
      image("shop/web", ["v2.13.2"], 141, 9 * DAY),
      image("shop/web", ["v2.13.1"], 141, 16 * DAY),
      image("shop/web", ["v2.12.0"], 138, 39 * DAY),
      image("shop/web", ["v2.11.4"], 137, 71 * DAY),
    ],
    "shop/worker": [
      image("shop/worker", ["latest", "v1.9.3"], 96, 5 * DAY),
      image("shop/worker", ["v1.9.2"], 96, 19 * DAY),
      image("shop/worker", ["v1.8.0"], 92, 63 * DAY),
    ],
    "shop/notifier": [image("shop/notifier", ["1.4.0"], 188, 6 * DAY, "linux/arm64"), image("shop/notifier", ["1.3.1"], 186, 28 * DAY, "linux/arm64")],
    "shop/api": [image("shop/api", ["latest", "v3.2.1"], 74, 8 * DAY), image("shop/api", ["v3.2.0"], 74, 25 * DAY), image("shop/api", [], 73, 50 * DAY)],
    "shop/batch": [image("shop/batch", ["latest"], 310, 3 * DAY)],
    "shop/base-images/python": [image("shop/base-images/python", ["3.12-slim"], 58, 45 * DAY), image("shop/base-images/python", ["3.11-slim"], 55, 150 * DAY)],
    "shop/scratch": [],
  }
  return { repos, images }
}

function detail(r: EcrRepository): EcrRepositoryDetail {
  const imgs = [...(S().images[r.name] ?? [])].sort((a, b) => (b.pushed_at ?? "").localeCompare(a.pushed_at ?? ""))
  return {
    repository: r,
    images: imgs,
    push_commands: [`docker build -t ${r.name} .`, `docker tag ${r.name}:latest ${r.uri}:latest`, `docker push ${r.uri}:latest`],
  }
}

function findRepo(name: string): EcrRepository {
  const r = S().repos.find((x) => x.name === name)
  if (!r) throw err(404, "RepositoryNotFoundException", `repository "${name}" does not exist`)
  return r
}

function routes(r: Router) {
  r.get("/api/v1/ecr/status", (): EcrStatus => ({
    status: "available",
    registry: ECR_REGISTRY,
    push_example: `docker tag myapp:latest ${ECR_REGISTRY}/myapp:latest && docker push ${ECR_REGISTRY}/myapp:latest`,
  }))
  r.get("/api/v1/ecr/repositories", () => [...S().repos].sort((a, b) => a.name.localeCompare(b.name)).map((x) => ({ ...x, image_tag_count: (S().images[x.name] ?? []).reduce((n, i) => n + i.tags.length, 0) })))
  r.post("/api/v1/ecr/repositories", ({ body: b }) => {
    b = b ?? {}
    const name = String(b.name ?? "")
    if (name.length < 2 || name.length > 256 || !NAME_RE.test(name)) throw badRequest("repository names are lowercase letters, digits and . _ - separators, optionally namespaced with /")
    if (S().repos.some((x) => x.name === name)) throw err(409, "RepositoryAlreadyExistsException", `repository "${name}" already exists`)
    const rep: EcrRepository = { ...repo(name, b.description ?? "", 0, b.tag_mutable !== false, b.tags ?? {}), created_at: nowIso() }
    S().repos.push(rep)
    S().images[name] = []
    return rep
  })
  r.get("/api/v1/ecr/repositories/*name", ({ params }) => detail(findRepo(params.name)))
  r.del("/api/v1/ecr/repositories/*name", ({ params, query }) => {
    const rep = findRepo(params.name)
    const n = (S().images[rep.name] ?? []).length
    if (n > 0 && query.force !== "true") throw err(409, "RepositoryNotEmptyException", `repository "${rep.name}" has ${n} images; pass force=true`)
    S().repos = S().repos.filter((x) => x.name !== rep.name)
    delete S().images[rep.name]
  })
  r.del("/api/v1/ecr/images", ({ query }) => {
    const rep = findRepo(query.repository ?? "")
    const ref = query.image ?? ""
    if (!/^([A-Za-z0-9_][A-Za-z0-9_.-]{0,127}|sha256:[a-f0-9]{64})$/.test(ref)) throw badRequest("image must be a tag or a sha256: digest")
    const list = S().images[rep.name] ?? []
    const i = list.findIndex((x) => x.digest === ref || x.tags.includes(ref))
    if (i < 0) throw err(404, "ImageNotFoundException", `image "${ref}" not found in repository "${rep.name}"`)
    const digest = list[i].digest
    list.splice(i, 1)
    return { deleted: digest }
  })
}

const service: DemoService = { name: "ecr", seed, routes }

export default service
