import { badRequest, conflict, err, getState, notFound, type DemoService, type Router } from "../engine"
import { ACCOUNT, DAY, HOUR, REGION, ago, ahead, hex, stableId, uuid } from "../util"
import { NAMES } from "../ids"
import type { Secret, SecretValue, SecretVersion } from "@/lib/types"

interface SecretsState {
  secrets: Secret[]
  /** secret name -> version id -> value */
  values: Record<string, Record<string, string>>
}
const st = () => getState().secrets as SecretsState

const KMS_SECRETS_ARN = `arn:aws:kms:${REGION}:${ACCOUNT}:key/${(() => {
  const h = stableId("", "shop-secrets", 32)
  return `${h.slice(0, 8)}-${h.slice(8, 12)}-4${h.slice(13, 16)}-a${h.slice(17, 20)}-${h.slice(20, 32)}`
})()}`
const DEFAULT_KEY = "alias/aws/secretsmanager"
const arnFor = (name: string) => `arn:aws:secretsmanager:${REGION}:${ACCOUNT}:secret:${name}-${hex(6)}`
const vid = (seedName: string) => {
  const h = stableId("", seedName, 32)
  return `${h.slice(0, 8)}-${h.slice(8, 12)}-4${h.slice(13, 16)}-a${h.slice(17, 20)}-${h.slice(20, 32)}`
}

const NAME_RE = /^[\w/+=.@-]{1,512}$/

function seed(): SecretsState {
  const values: Record<string, Record<string, string>> = {}
  const secrets: Secret[] = []
  const add = (
    name: string,
    o: Partial<Secret> & { age: number; updated?: number; vals: string[]; accessed?: number },
  ) => {
    const { age, updated, vals, accessed, ...rest } = o
    const versions: SecretVersion[] = vals.map((_, i) => {
      const cur = i === vals.length - 1
      const stages = cur ? ["AWSCURRENT"] : i === vals.length - 2 ? ["AWSPREVIOUS"] : []
      return {
        id: vid(`${name}-v${i}`), stages, created_at: ago(cur ? (updated ?? age) : age - (i + 1) * ((age - (updated ?? age)) / vals.length)),
        ...(cur && accessed !== undefined ? { last_accessed: ago(accessed) } : {}), ...(rest.kms_key_id ? { kms_key: rest.kms_key_id } : {}),
      }
    })
    versions.filter((v) => v.stages.length).forEach(() => undefined)
    values[name] = Object.fromEntries(vals.map((v, i) => [versions[i].id, v]))
    secrets.push({
      name, arn: arnFor(name), description: "", versions: versions.filter((v) => v.stages.length), created_at: ago(age), updated_at: ago(updated ?? age),
      last_accessed: accessed !== undefined ? ago(accessed) : undefined, tags: { app: "shop" }, ...rest,
    })
  }
  const dbJson = (host: string, pw: string) => JSON.stringify({ username: "shop_app", password: pw, engine: "postgres", host, port: 5432, dbname: "shop" }, null, 2)
  add("shop/prod/db-password", {
    age: 300 * DAY, updated: 12 * DAY, accessed: 4 * 60_000, description: "Master credentials of the shop-db Postgres instance", kms_key_id: KMS_SECRETS_ARN,
    vals: [dbJson("shop-db.demo.internal", "demo-not-a-real-secret-v1"), dbJson("shop-db.demo.internal", "demo-not-a-real-secret-v2"), dbJson("shop-db.demo.internal", "demo-not-a-real-secret-v3")],
    rotation_enabled: true, rotation_lambda_arn: `arn:aws:lambda:${REGION}:${ACCOUNT}:function:shop-rotate-db-secret`, rotation_rules: { AutomaticallyAfterDays: 30 }, last_rotated: ago(12 * DAY), next_rotation: ahead(18 * DAY),
    tags: { app: "shop", env: "prod", tier: "data" },
  })
  add("shop/prod/payments-api-key", {
    age: 200 * DAY, updated: 40 * DAY, accessed: 11 * 60_000, description: "API key for the payments provider (used by shop-payments)", kms_key_id: KMS_SECRETS_ARN,
    vals: ["demo-not-a-real-secret-old", "demo-not-a-real-secret"],
    resource_policy: JSON.stringify({
      Version: "2012-10-17",
      Statement: [{ Sid: "AllowPaymentsLambda", Effect: "Allow", Principal: { AWS: `arn:aws:iam::${ACCOUNT}:role/shop-payments-lambda-role` }, Action: "secretsmanager:GetSecretValue", Resource: "*" }],
    }, null, 2),
    tags: { app: "shop", env: "prod", owner: "payments" },
  })
  add("shop/prod/jwt-signing-secret", { age: 150 * DAY, accessed: 3 * 60_000, description: "HS256 secret for session tokens", vals: ["demo-not-a-real-secret"], tags: { app: "shop", env: "prod" } })
  add("shop/prod/redis-auth-token", {
    age: 120 * DAY, updated: 20 * DAY, accessed: 2 * HOUR, description: `Auth token for the ${NAMES.cache} cluster`, vals: ["demo-not-a-real-secret-a", "demo-not-a-real-secret-b"],
    tags: { app: "shop", env: "prod" },
  })
  add("shop/prod/smtp-credentials", {
    age: 90 * DAY, accessed: 3 * DAY, description: "SMTP relay login for order emails", vals: [JSON.stringify({ username: "orders@example.com", password: "demo-not-a-real-secret" }, null, 2)],
    rotation_enabled: true, rotation_lambda_arn: `arn:aws:lambda:${REGION}:${ACCOUNT}:function:shop-rotate-smtp-secret`, rotation_rules: { AutomaticallyAfterDays: 60 }, last_rotated: ago(70 * DAY), next_rotation: ago(10 * DAY),
    rotation_error: "Rotation Lambda shop-rotate-smtp-secret timed out during the setSecret step", tags: { app: "shop", env: "prod" },
  })
  add("shop/dev/db-password", {
    age: 200 * DAY, updated: 90 * DAY, accessed: 5 * HOUR, description: "Dev database credentials",
    vals: [dbJson("shop-db-dev.demo.internal", "demo-not-a-real-secret-dev1"), dbJson("shop-db-dev.demo.internal", "demo-not-a-real-secret-dev2")], tags: { app: "shop", env: "dev" },
  })
  add("shop/dev/stripe-test-key", { age: 60 * DAY, accessed: 6 * DAY, description: "Payments provider sandbox key", vals: ["demo-not-a-real-secret-sandbox"], tags: { app: "shop", env: "dev" } })
  add("shop/legacy/ftp-password", {
    age: 400 * DAY, accessed: 200 * DAY, description: "Old fulfilment partner FTP login (scheduled for deletion)", vals: ["demo-not-a-real-secret"], tags: { app: "shop", status: "retired" },
    deleted_at: ago(2 * DAY), deletion_date: ahead(5 * DAY),
  })
  return { secrets, values }
}

const find = (name: string): Secret => st().secrets.find((s) => s.name === name || s.arn === name) ?? (() => { throw err(404, "ResourceNotFoundException", "Secrets Manager can't find the specified secret.") })()
const live = (s: Secret) => {
  if (s.deleted_at) throw err(400, "InvalidRequestException", "You can't perform this operation on the secret because it was marked for deletion.")
  return s
}
const touch = (s: Secret) => {
  s.updated_at = new Date().toISOString()
}

function putVersion(s: Secret, value: string, extraStages: string[] = []): SecretVersion {
  const vals = (st().values[s.name] ??= {})
  const prevCur = s.versions.find((v) => v.stages.includes("AWSCURRENT"))
  const prevPrev = s.versions.find((v) => v.stages.includes("AWSPREVIOUS"))
  if (prevPrev) prevPrev.stages = prevPrev.stages.filter((x) => x !== "AWSPREVIOUS")
  if (prevCur) prevCur.stages = prevCur.stages.filter((x) => x !== "AWSCURRENT").concat("AWSPREVIOUS")
  const v: SecretVersion = { id: uuid(), stages: ["AWSCURRENT", ...extraStages], created_at: new Date().toISOString(), ...(s.kms_key_id ? { kms_key: s.kms_key_id } : {}) }
  vals[v.id] = value
  s.versions = s.versions.filter((x) => x.stages.length).concat(v)
  for (const id of Object.keys(vals)) if (!s.versions.some((x) => x.id === id)) delete vals[id]
  touch(s)
  return v
}

function routes(r: Router) {
  const P = "/api/v1/secrets"
  r.get(P, () => st().secrets)
  r.post(P, ({ body }) => {
    const name = body?.name
    if (typeof name !== "string" || !NAME_RE.test(name)) throw badRequest("Secret name must be 1-512 characters: letters, digits and /_+=.@-")
    if (st().secrets.some((s) => s.name === name)) throw err(409, "ResourceExistsException", `The operation failed because the secret ${name} already exists.`)
    if (typeof body.value !== "string" || !body.value) throw badRequest("A secret value is required")
    const now = new Date().toISOString()
    const s: Secret = { name, arn: arnFor(name), description: body.description ?? "", versions: [], created_at: now, updated_at: now, tags: body.tags ?? {} }
    if (body.kms_key_id && body.kms_key_id !== DEFAULT_KEY) s.kms_key_id = body.kms_key_id
    st().secrets.push(s)
    putVersion(s, body.value)
    return s
  })
  r.get(`${P}/:name`, ({ params }) => find(params.name))
  r.patch(`${P}/:name`, ({ params, body }) => {
    const s = live(find(params.name))
    if (typeof body?.description === "string") s.description = body.description
    if (typeof body?.kms_key_id === "string") {
      if (body.kms_key_id === DEFAULT_KEY || body.kms_key_id === "") delete s.kms_key_id
      else s.kms_key_id = body.kms_key_id
    }
    if (body?.tags) s.tags = body.tags
    touch(s)
    return s
  })
  r.get(`${P}/:name/value`, ({ params, query }): SecretValue => {
    const s = live(find(params.name))
    const v = query.version_id ? s.versions.find((x) => x.id === query.version_id) : s.versions.find((x) => x.stages.includes(query.version_stage || "AWSCURRENT"))
    if (!v) throw err(404, "ResourceNotFoundException", "Secrets Manager can't find the specified secret value for the requested version.")
    v.last_accessed = new Date().toISOString()
    s.last_accessed = v.last_accessed
    return { name: s.name, value: st().values[s.name]?.[v.id] ?? "demo-not-a-real-secret", version_id: v.id, stages: v.stages, created_at: v.created_at }
  })
  r.put(`${P}/:name/value`, ({ params, body }) => {
    const s = live(find(params.name))
    if (typeof body?.value !== "string" || !body.value) throw badRequest("A secret value is required")
    putVersion(s, body.value)
    return s
  })
  r.del(`${P}/:name`, ({ params, query }) => {
    const s = find(params.name)
    if (query.force === "true") {
      st().secrets = st().secrets.filter((x) => x !== s)
      delete st().values[s.name]
      return undefined
    }
    const days = query.recovery_days ? Number(query.recovery_days) : 7
    if (!(days >= 7 && days <= 30)) throw badRequest("recovery_days must be between 7 and 30")
    s.deleted_at = new Date().toISOString()
    s.deletion_date = ahead(days * DAY)
    return s
  })
  r.post(`${P}/:name/restore`, ({ params }) => {
    const s = find(params.name)
    if (!s.deleted_at) throw badRequest("The secret is not scheduled for deletion.")
    delete s.deleted_at
    delete s.deletion_date
    return s
  })
  r.post(`${P}/:name/rotate`, ({ params, body }) => {
    const s = live(find(params.name))
    const lambda = body?.rotation_lambda_arn || s.rotation_lambda_arn
    if (!lambda) throw badRequest("A rotation Lambda function is required to configure rotation.")
    s.rotation_lambda_arn = lambda
    s.rotation_enabled = true
    let days = Number(body?.automatically_after_days) || s.rotation_rules?.AutomaticallyAfterDays || 30
    const m = /^rate\((\d+) days?\)$/.exec(body?.schedule_expression ?? "")
    if (m) days = Number(m[1])
    s.rotation_rules = body?.schedule_expression && !m ? { ScheduleExpression: body.schedule_expression } : { AutomaticallyAfterDays: days }
    delete s.rotation_error
    let versionId = ""
    if (body?.rotate_immediately !== false) {
      const cur = s.versions.find((v) => v.stages.includes("AWSCURRENT"))
      const old = (cur && st().values[s.name]?.[cur.id]) || "demo-not-a-real-secret"
      const nv = putVersion(s, old.replace(/(-r\d+)?$/, "") + `-r${Math.floor(Math.random() * 900 + 100)}`)
      versionId = nv.id
      s.last_rotated = new Date().toISOString()
    }
    s.next_rotation = ahead(days * DAY)
    touch(s)
    return { secret: s, version_id: versionId }
  })
  r.post(`${P}/:name/cancel-rotation`, ({ params }) => {
    const s = live(find(params.name))
    s.rotation_enabled = false
    delete s.next_rotation
    delete s.rotation_error
    touch(s)
    return s
  })
  r.get(`${P}/:name/policy`, ({ params }) => {
    const s = find(params.name)
    return { name: s.name, arn: s.arn, policy: s.resource_policy ?? "" }
  })
  r.put(`${P}/:name/policy`, ({ params, body }) => {
    const s = live(find(params.name))
    let d: { Statement?: unknown; }
    try {
      d = JSON.parse(body?.policy)
    } catch {
      throw err(400, "MalformedPolicyDocumentException", "This resource policy contains invalid JSON.")
    }
    if (!d?.Statement) throw err(400, "MalformedPolicyDocumentException", "This resource policy is missing a Statement.")
    if (body.block_public_policy !== false && /"Principal"\s*:\s*("\*"|\{\s*"AWS"\s*:\s*"\*"\s*\})/.test(body.policy)) {
      throw err(400, "MalformedPolicyDocumentException", "This resource policy grants public access and Block public policy is enabled.")
    }
    s.resource_policy = body.policy
    touch(s)
    return s
  })
  r.del(`${P}/:name/policy`, ({ params }) => {
    const s = live(find(params.name))
    delete s.resource_policy
    touch(s)
    return s
  })
  r.put(`${P}/:name/stages`, ({ params, body }) => {
    const s = live(find(params.name))
    const stage = body?.stage
    if (!stage) throw badRequest("stage is required")
    const from = body.remove_from_version_id ? s.versions.find((v) => v.id === body.remove_from_version_id) : undefined
    const to = body.move_to_version_id ? s.versions.find((v) => v.id === body.move_to_version_id) : undefined
    if (body.remove_from_version_id && !from) throw notFound("Version", body.remove_from_version_id)
    if (body.move_to_version_id && !to) throw notFound("Version", body.move_to_version_id)
    if (from && !from.stages.includes(stage)) throw badRequest(`Version ${from.id} does not have the staging label ${stage}`)
    if (from) from.stages = from.stages.filter((x) => x !== stage)
    if (to) {
      to.stages = [...to.stages.filter((x) => x !== stage), stage]
      if (stage === "AWSCURRENT" && from) {
        s.versions.forEach((v) => (v.stages = v.stages.filter((x) => x !== "AWSPREVIOUS")))
        from.stages = [...from.stages, "AWSPREVIOUS"]
      }
    }
    s.versions = s.versions.filter((v) => v.stages.length)
    if (!s.versions.some((v) => v.stages.includes("AWSCURRENT"))) throw conflict("A secret must always have an AWSCURRENT version")
    touch(s)
    return s
  })
  r.post(`${P}/random-password`, ({ query }) => {
    const n = Number(query.length) || 32
    if (n < 8 || n > 4096) throw badRequest("length must be between 8 and 4096")
    const cs = "ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz23456789"
    return { password: Array.from({ length: n }, () => cs[Math.floor(Math.random() * cs.length)]).join("") }
  })
}

const service: DemoService = { name: "secrets", seed, routes }
export default service
