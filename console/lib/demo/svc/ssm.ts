import { badRequest, err, getState, type DemoService, type Router } from "../engine"
import { ACCOUNT, DAY, MIN, REGION, ago, stableId } from "../util"
import { AMI } from "../ids"
import type { SsmParameter, SsmParameterType, SsmParameterValue, SsmParameterVersion, Tags } from "@/lib/types"

interface StoredVersion {
  version: number
  type: SsmParameterType
  value: string
  last_modified: string
  modified_by: string
  labels: string[]
  description?: string
}
interface StoredParam {
  name: string
  arn: string
  key_id: string
  description: string
  tier: string
  allowed_pattern: string
  data_type: string
  tags: Tags
  history: StoredVersion[]
}
interface SsmState {
  params: StoredParam[]
}
const st = () => getState().ssm as SsmState

const kmsKeyArn = (name: string) => {
  const h = stableId("", name, 32)
  return `arn:aws:kms:${REGION}:${ACCOUNT}:key/${h.slice(0, 8)}-${h.slice(8, 12)}-4${h.slice(13, 16)}-a${h.slice(17, 20)}-${h.slice(20, 32)}`
}
const SSM_KEY = kmsKeyArn("hc-ssm")
const SECRETS_KEY = kmsKeyArn("shop-secrets")
const user = (n: string) => `arn:aws:iam::${ACCOUNT}:user/${n}`
const paramArn = (name: string) => `arn:aws:ssm:${REGION}:${ACCOUNT}:parameter${name.startsWith("/") ? "" : "/"}${name}`

const NAME_RE = /^\/?[a-zA-Z0-9_.\-/]{1,1011}$/
const LABEL_RE = /^[a-zA-Z0-9_.-]{1,100}$/
const validLabel = (l: string) => LABEL_RE.test(l) && !/^[0-9]/.test(l) && !/^(aws|ssm)/i.test(l)

function seed(): SsmState {
  const params: StoredParam[] = []
  const add = (
    name: string, type: SsmParameterType, versions: { v: string; age: number; by?: string; labels?: string[] }[],
    o: { desc?: string; key?: string; tier?: string; pattern?: string; dataType?: string; tags?: Tags } = {},
  ) => {
    params.push({
      name, arn: paramArn(name), key_id: type === "SecureString" ? (o.key ?? SSM_KEY) : "", description: o.desc ?? "", tier: o.tier ?? "Standard", allowed_pattern: o.pattern ?? "",
      data_type: o.dataType ?? "text", tags: o.tags ?? {},
      history: versions.map((v, i) => ({ version: i + 1, type, value: v.v, last_modified: ago(v.age), modified_by: user(v.by ?? "demo-admin"), labels: v.labels ?? [] })),
    })
  }
  const prod = { env: "prod", app: "shop" }
  const dev = { env: "dev", app: "shop" }
  add("/shop/prod/db/host", "String", [{ v: "shop-db.c8x2demo.us-east-1.rds.internal", age: 250 * DAY }], { desc: "Postgres endpoint", tags: prod })
  add("/shop/prod/db/port", "String", [{ v: "5432", age: 250 * DAY }], { desc: "Postgres port", pattern: "^\\d+$", tags: prod })
  add("/shop/prod/db/name", "String", [{ v: "shop", age: 250 * DAY }], { tags: prod })
  add("/shop/prod/db/password", "SecureString", [
    { v: "demo-not-a-real-secret-1", age: 250 * DAY }, { v: "demo-not-a-real-secret-2", age: 110 * DAY, by: "alice" }, { v: "demo-not-a-real-secret-3", age: 12 * DAY, by: "ci-deploy", labels: ["current"] },
  ], { desc: "Application database password", key: SECRETS_KEY, tags: prod })
  add("/shop/prod/cache/endpoint", "String", [{ v: "shop-cache.demo.0001.use1.cache.internal:6379", age: 200 * DAY }], { desc: "Redis primary endpoint", tags: prod })
  add("/shop/prod/payments/api-key", "SecureString", [{ v: "demo-not-a-real-secret", age: 40 * DAY }], { desc: "Payments provider API key", key: SECRETS_KEY, tags: prod })
  add("/shop/prod/app/log-level", "String", [
    { v: "debug", age: 220 * DAY }, { v: "info", age: 150 * DAY, labels: ["stable"] }, { v: "warn", age: 60 * DAY, by: "bob" }, { v: "info", age: 6 * DAY, by: "alice", labels: ["current"] },
  ], { desc: "Application log verbosity", pattern: "^(debug|info|warn|error)$", tags: prod })
  add("/shop/prod/app/allowed-origins", "StringList", [{ v: "https://shop.example.com,https://www.shop.example.com,https://admin.shop.example.com", age: 90 * DAY }], { desc: "CORS origins", tags: prod })
  add("/shop/prod/app/feature-flags", "StringList", [
    { v: "checkout-v2", age: 120 * DAY }, { v: "checkout-v2,express-shipping", age: 45 * DAY }, { v: "checkout-v2,express-shipping,gift-cards", age: 3 * DAY, by: "alice" },
  ], { desc: "Enabled feature flags", tags: prod })
  add("/shop/prod/app/nginx-conf", "String", [{ v: "server {\n  listen 80;\n  server_name shop.example.com;\n  location / { proxy_pass http://127.0.0.1:3000; }\n  location /health { return 200 'ok'; }\n}\n", age: 70 * DAY }], { desc: "Rendered nginx server block", tier: "Advanced", tags: prod })
  add("/shop/prod/web/ami-id", "String", [{ v: AMI.shopWeb, age: 20 * DAY, by: "ci-deploy" }], { desc: "Latest baked web AMI", dataType: "aws:ec2:image", tags: prod })
  add("/shop/dev/db/host", "String", [{ v: "shop-db-dev.c8x2demo.us-east-1.rds.internal", age: 180 * DAY }], { tags: dev })
  add("/shop/dev/db/password", "SecureString", [{ v: "demo-not-a-real-secret-dev", age: 100 * DAY, by: "bob" }], { desc: "Dev database password", tags: dev })
  add("/shop/dev/app/log-level", "String", [{ v: "debug", age: 180 * DAY }], { tags: dev })
  add("/shop/dev/app/feature-flags", "StringList", [{ v: "checkout-v2,express-shipping,gift-cards,new-search", age: 8 * DAY, by: "bob" }], { tags: dev })
  add("/shop/common/region", "String", [{ v: REGION, age: 300 * DAY }], { desc: "Default region", tags: { app: "shop" } })
  add("/shop/common/support-email", "String", [{ v: "support@example.com", age: 300 * MIN * 24 }], { tags: { app: "shop" } })
  return { params }
}

const normName = (n: unknown): string => {
  if (typeof n !== "string" || !n) throw badRequest("name is required")
  return n
}
const find = (name: string): StoredParam => {
  const n = name.startsWith("arn:") ? name.slice(name.indexOf(":parameter") + 10) : name
  const p = st().params.find((x) => x.name === n || x.name === "/" + n)
  if (!p) throw err(400, "ParameterNotFound", `Parameter ${name} not found.`)
  return p
}
const cur = (p: StoredParam) => p.history[p.history.length - 1]
const cipher = (v: string) => btoa(unescape(encodeURIComponent("DEMO-CIPHERTEXT|" + v)))

const summary = (p: StoredParam): SsmParameter => {
  const c = cur(p)
  return {
    name: p.name, arn: p.arn, type: c.type, key_id: c.type === "SecureString" ? p.key_id : "", description: p.description, version: c.version, last_modified: c.last_modified,
    last_modified_by: c.modified_by, data_type: p.data_type, tier: p.tier, allowed_pattern: p.allowed_pattern, tags: p.tags,
  }
}
const render = (p: StoredParam, v: StoredVersion, decrypt: boolean): SsmParameterValue => ({
  name: p.name, arn: p.arn, type: v.type, value: v.type === "SecureString" && !decrypt ? cipher(v.value) : v.value, version: v.version, last_modified: v.last_modified, data_type: p.data_type,
  labels: v.labels,
})

function routes(r: Router) {
  const P = "/api/v1/ssm"
  r.get(`${P}/parameters`, ({ query }) => st().params.filter((p) => !query.prefix || p.name.startsWith(query.prefix)).map(summary))
  r.get(`${P}/parameter`, ({ query }) => render(find(normName(query.name)), cur(find(normName(query.name))), query.with_decryption === "true"))
  r.put(`${P}/parameter`, ({ body }) => {
    const name = normName(body?.name)
    if (!NAME_RE.test(name)) throw badRequest("Parameter name: can't be prefixed with 'aws' or 'ssm' and may only contain a-zA-Z0-9_.-/")
    if (typeof body.value !== "string" || !body.value) throw badRequest("Value is required")
    const existing = st().params.find((x) => x.name === name)
    if (existing && !body.overwrite) throw err(400, "ParameterAlreadyExists", "The parameter already exists. To overwrite this value, set the overwrite option in the request to true.")
    const type: SsmParameterType = body.type || (existing ? cur(existing).type : "String")
    const advanced = body.tier === "Advanced" || (existing?.tier === "Advanced")
    if (!advanced && body.value.length > 4096) throw err(400, "ValidationException", "Standard tier parameters are limited to 4 KB. Use the Advanced tier for values up to 8 KB.")
    if (body.value.length > 8192) throw err(400, "ValidationException", "Parameter values are limited to 8 KB.")
    const pattern = body.allowed_pattern ?? existing?.allowed_pattern ?? ""
    if (pattern) {
      let ok = true
      try {
        ok = new RegExp(pattern).test(body.value)
      } catch {
        ok = true
      }
      if (!ok) throw err(400, "ParameterPatternMismatchException", `Parameter value doesn't match the allowed pattern ${pattern}`)
    }
    const now = new Date().toISOString()
    const by = user("demo-admin")
    if (existing) {
      const v: StoredVersion = { version: cur(existing).version + 1, type, value: body.value, last_modified: now, modified_by: by, labels: [], description: body.description }
      existing.history.push(v)
      if (existing.history.length > 100) existing.history.shift()
      if (typeof body.description === "string") existing.description = body.description
      if (body.tier && body.tier !== "Standard") existing.tier = body.tier
      existing.allowed_pattern = pattern
      if (type === "SecureString" && body.key_id) existing.key_id = body.key_id === "alias/aws/ssm" ? SSM_KEY : body.key_id
      return { version: v.version, tier: existing.tier }
    }
    const p: StoredParam = {
      name, arn: paramArn(name), key_id: type === "SecureString" ? (!body.key_id || body.key_id === "alias/aws/ssm" ? SSM_KEY : body.key_id) : "", description: body.description ?? "",
      tier: body.tier === "Intelligent-Tiering" ? "Standard" : (body.tier ?? "Standard"), allowed_pattern: pattern, data_type: body.data_type ?? "text", tags: body.tags ?? {},
      history: [{ version: 1, type, value: body.value, last_modified: now, modified_by: by, labels: [], description: body.description }],
    }
    st().params.push(p)
    return { version: 1, tier: p.tier }
  })
  r.del(`${P}/parameter`, ({ query }) => {
    const p = find(normName(query.name))
    st().params = st().params.filter((x) => x !== p)
  })
  r.get(`${P}/parameter/history`, ({ query }): SsmParameterVersion[] => {
    const p = find(normName(query.name))
    const decrypt = query.with_decryption === "true"
    return p.history.map((v) => ({
      version: v.version, type: v.type, value: v.type === "SecureString" && !decrypt ? "****" : v.value, last_modified: v.last_modified, modified_by: v.modified_by, labels: v.labels,
      description: v.description ?? p.description,
    }))
  })
  r.post(`${P}/parameter/labels`, ({ body }) => {
    const p = find(normName(body?.name))
    const v = p.history.find((x) => x.version === (body.version || cur(p).version))
    if (!v) throw err(400, "ParameterVersionNotFound", `Version ${body.version} of ${p.name} not found.`)
    const labels = (body.labels ?? []) as string[]
    if (!labels.length) throw badRequest("labels is required")
    for (const l of labels) if (!validLabel(l)) throw badRequest(`label "${l}" is invalid (letters, digits, . - _; not starting with a digit, aws or ssm)`)
    if (v.labels.length + labels.length > 10) throw err(400, "ParameterVersionLabelLimitExceeded", "A version can have at most 10 labels.")
    for (const h of p.history) h.labels = h.labels.filter((x) => !labels.includes(x))
    v.labels = [...v.labels, ...labels]
    return summary(p)
  })
  r.post(`${P}/parameter/unlabel`, ({ body }) => {
    const p = find(normName(body?.name))
    const v = p.history.find((x) => x.version === body.version)
    if (!v) throw err(400, "ParameterVersionNotFound", `Version ${body.version} of ${p.name} not found.`)
    const wanted = (body.labels ?? []) as string[]
    const removed = wanted.filter((l) => v.labels.includes(l))
    v.labels = v.labels.filter((l) => !removed.includes(l))
    return { removed_labels: removed, invalid_labels: wanted.filter((l) => !removed.includes(l)) }
  })
  r.get(`${P}/parameters-by-path`, ({ query }) => {
    const path = query.path ?? ""
    if (!path.startsWith("/")) throw badRequest(`The parameter path must begin with /: "${path}"`)
    const base = path.endsWith("/") ? path : path + "/"
    return st()
      .params.filter((p) => p.name.startsWith(base) && (query.recursive === "true" || !p.name.slice(base.length).includes("/")))
      .map((p) => render(p, cur(p), query.with_decryption === "true"))
  })
}

const service: DemoService = { name: "ssm", seed, routes }
export default service
