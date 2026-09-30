import { badRequest, conflict, err, getState, notFound, type DemoService, type Router } from "../engine"
import { ACCOUNT, DAY, HOUR, MIN, REGION, ago, hex, rng, stableId } from "../util"
import { COGNITO_POOL_ID, NAMES } from "../ids"
import type { AppClient, CognitoUser, PasswordPolicy, UserPool, UserPoolGroup } from "@/lib/types"

interface StoredPool {
  id: string
  arn: string
  name: string
  password_policy: PasswordPolicy
  auto_confirm: boolean
  self_sign_up: boolean
  groups: UserPoolGroup[]
  created_at: string
}
interface CognitoState {
  pools: StoredPool[]
  clients: (AppClient & { pool_id: string })[]
  users: (CognitoUser & { pool_id: string })[]
}
const st = () => getState().cognito as CognitoState

const issuer = (id: string) => `https://demo.homecloud.example/cognito/${id}`
const sub = (s: string) => {
  const h = stableId("", s, 32)
  return `${h.slice(0, 8)}-${h.slice(8, 12)}-${h.slice(12, 16)}-${h.slice(16, 20)}-${h.slice(20, 32)}`
}

const FIRST = ["Olivia", "Liam", "Emma", "Noah", "Ava", "Elijah", "Sophia", "Lucas", "Mia", "Mason", "Isabella", "Ethan", "Amelia", "Logan", "Harper", "James", "Evelyn", "Aiden", "Abigail", "Jacob", "Emily", "Leo", "Ella", "Jack", "Grace", "Owen", "Chloe", "Henry", "Nora", "Daniel", "Zoe", "Samuel", "Lily", "Carter", "Hannah", "Wyatt", "Layla", "Julian", "Ruby", "Isaac"]
const LAST = ["Smith", "Johnson", "Garcia", "Miller", "Davis", "Martinez", "Lopez", "Wilson", "Anderson", "Thomas", "Taylor", "Moore", "Jackson", "Martin", "Lee", "Perez", "Thompson", "White", "Harris", "Clark"]
const LOCALES = ["en-US", "en-GB", "de-DE", "fr-FR", "es-ES", "en-CA"]

function seed(): CognitoState {
  const pools: StoredPool[] = [
    {
      id: COGNITO_POOL_ID, arn: `arn:aws:cognito-idp:${REGION}:${ACCOUNT}:userpool/${COGNITO_POOL_ID}`, name: NAMES.userPool,
      password_policy: { min_length: 10, require_uppercase: true, require_lowercase: true, require_numbers: true, require_symbols: false }, auto_confirm: false, self_sign_up: true,
      groups: [
        { name: "vip", description: "Loyalty members with free shipping", precedence: 1 },
        { name: "wholesale", description: "Trade customers with bulk pricing", precedence: 5 },
        { name: "support", description: "Customer support agents", precedence: 10 },
        { name: "customers", description: "Everyone else", precedence: 50 },
      ],
      created_at: ago(270 * DAY),
    },
    {
      id: "us-east-1_5D7B0E4A1", arn: `arn:aws:cognito-idp:${REGION}:${ACCOUNT}:userpool/us-east-1_5D7B0E4A1`, name: "shop-staff",
      password_policy: { min_length: 12, require_uppercase: true, require_lowercase: true, require_numbers: true, require_symbols: true }, auto_confirm: true, self_sign_up: false,
      groups: [{ name: "admins", description: "Back-office administrators", precedence: 1 }], created_at: ago(150 * DAY),
    },
  ]
  const clients: CognitoState["clients"] = [
    { id: stableId("", "web-client", 26), pool_id: COGNITO_POOL_ID, name: "shop-web", has_secret: false, access_token_minutes: 60, refresh_token_days: 30, created_at: ago(268 * DAY) },
    { id: stableId("", "ios-client", 26), pool_id: COGNITO_POOL_ID, name: "shop-ios", has_secret: false, access_token_minutes: 30, refresh_token_days: 90, created_at: ago(200 * DAY) },
    { id: stableId("", "android-client", 26), pool_id: COGNITO_POOL_ID, name: "shop-android", has_secret: false, access_token_minutes: 30, refresh_token_days: 90, created_at: ago(200 * DAY) },
    { id: stableId("", "backend-client", 26), pool_id: COGNITO_POOL_ID, name: "shop-backend", has_secret: true, access_token_minutes: 15, refresh_token_days: 1, created_at: ago(120 * DAY) },
    { id: stableId("", "staff-client", 26), pool_id: "us-east-1_5D7B0E4A1", name: "staff-console", has_secret: true, access_token_minutes: 60, refresh_token_days: 7, created_at: ago(149 * DAY) },
  ]
  const users: CognitoState["users"] = []
  const r = rng("cognito-users")
  for (let i = 0; i < 40; i++) {
    const first = FIRST[i % FIRST.length]
    const last = LAST[Math.floor(r() * LAST.length)]
    const username = `${first}.${last}${i % 3 === 0 ? Math.floor(r() * 90 + 10) : ""}`.toLowerCase()
    const roll = r()
    const status = i === 7 || i === 23 ? "UNCONFIRMED" : i === 31 ? "FORCE_CHANGE_PASSWORD" : "CONFIRMED"
    const groups = i < 6 ? ["vip"] : i >= 6 && i < 9 ? ["wholesale"] : i === 9 || i === 10 ? ["support"] : roll < 0.7 ? ["customers"] : []
    const created = Math.floor((5 + r() * 250) * DAY)
    const attrs: Record<string, string> = {
      email: `${username}@example.com`, email_verified: status === "UNCONFIRMED" ? "false" : "true", name: `${first} ${last}`, locale: LOCALES[Math.floor(r() * LOCALES.length)],
    }
    if (r() < 0.4) attrs.phone_number = `+1555${String(Math.floor(r() * 9000000 + 1000000))}`
    if (groups.includes("wholesale")) attrs["custom:company"] = `${last} Trading Co`
    users.push({
      pool_id: COGNITO_POOL_ID, username, sub: sub(username), attributes: attrs, status, enabled: i !== 15 && i !== 36, groups, created_at: ago(created),
      last_sign_in: status === "CONFIRMED" ? ago(Math.floor(r() * 20 * DAY) + 2 * MIN) : null,
    })
  }
  const staff = [["admin.ops", "Ops Admin"], ["priya.sharma", "Priya Sharma"], ["tom.becker", "Tom Becker"]]
  for (const [u, n] of staff) {
    users.push({
      pool_id: "us-east-1_5D7B0E4A1", username: u, sub: sub("staff-" + u), attributes: { email: `${u}@example.com`, email_verified: "true", name: n }, status: "CONFIRMED", enabled: true,
      groups: ["admins"], created_at: ago(140 * DAY), last_sign_in: ago(Math.floor(r() * 3 * DAY) + HOUR),
    })
  }
  return { pools, clients, users }
}

const pool = (id: string): StoredPool => st().pools.find((p) => p.id === id) ?? (() => { throw err(404, "ResourceNotFoundException", `user pool "${id}" does not exist`) })()
const poolView = (p: StoredPool): UserPool => ({
  ...p, users: st().users.filter((u) => u.pool_id === p.id).length, clients: st().clients.filter((c) => c.pool_id === p.id).length, issuer: issuer(p.id), jwks_uri: issuer(p.id) + "/.well-known/jwks.json",
})
const userOf = (poolId: string, name: string) => {
  pool(poolId)
  const u = st().users.find((x) => x.pool_id === poolId && x.username.toLowerCase() === name.toLowerCase())
  if (!u) throw err(404, "UserNotFoundException", `user "${name}" does not exist`)
  return u
}
const userView = (u: CognitoState["users"][number]): CognitoUser => {
  const { pool_id: _p, ...rest } = u
  return rest
}
function checkPassword(p: PasswordPolicy, pw: string) {
  const missing: string[] = []
  if ([...pw].length < p.min_length) missing.push(`at least ${p.min_length} characters`)
  if (p.require_uppercase && !/[A-Z]/.test(pw)) missing.push("an uppercase letter")
  if (p.require_lowercase && !/[a-z]/.test(pw)) missing.push("a lowercase letter")
  if (p.require_numbers && !/[0-9]/.test(pw)) missing.push("a number")
  if (p.require_symbols && !/[^A-Za-z0-9]/.test(pw)) missing.push("a symbol")
  if (missing.length) throw err(400, "InvalidPasswordException", `password must contain ${missing.join(", ")}`)
}
const checkAttrs = (a: Record<string, string> | undefined) => {
  for (const k of Object.keys(a ?? {})) {
    if (!k || k.length > 64 || ["sub", "aud", "iss", "exp", "iat", "username", "scope"].includes(k) || k.startsWith("cognito:") || k.startsWith("hc:")) throw badRequest(`attribute name "${k}" is reserved or invalid`)
  }
}
const checkGroups = (p: StoredPool, groups: string[]) => {
  for (const g of groups) if (!p.groups.some((x) => x.name === g)) throw notFound("group", g)
}
const checkPolicy = (pp: PasswordPolicy) => {
  if (pp.min_length < 6 || pp.min_length > 99) throw badRequest("password_policy.min_length must be 6-99")
}

function routes(r: Router) {
  const P = "/api/v1/cognito/user-pools"
  r.get(P, () => st().pools.map(poolView))
  r.post(P, ({ body }) => {
    if (!/^[\w\s+=,.@-]{1,128}$/.test(body?.name ?? "")) throw badRequest("pool name must be 1-128 characters")
    const pp: PasswordPolicy = { min_length: 8, require_uppercase: false, require_lowercase: true, require_numbers: true, require_symbols: false, ...(body.password_policy ?? {}) }
    if (!pp.min_length) pp.min_length = 8
    checkPolicy(pp)
    const id = `${REGION}_${hex(9).toUpperCase()}`
    const p: StoredPool = {
      id, arn: `arn:aws:cognito-idp:${REGION}:${ACCOUNT}:userpool/${id}`, name: body.name, password_policy: pp, auto_confirm: body.auto_confirm ?? true, self_sign_up: body.self_sign_up ?? true,
      groups: [], created_at: new Date().toISOString(),
    }
    st().pools.push(p)
    return poolView(p)
  })
  r.get(`${P}/:pool`, ({ params }) => poolView(pool(params.pool)))
  r.patch(`${P}/:pool`, ({ params, body }) => {
    const p = pool(params.pool)
    if (body?.password_policy) {
      const pp = { ...body.password_policy }
      if (!pp.min_length) pp.min_length = 8
      checkPolicy(pp)
      p.password_policy = pp
    }
    if (typeof body?.auto_confirm === "boolean") p.auto_confirm = body.auto_confirm
    if (typeof body?.self_sign_up === "boolean") p.self_sign_up = body.self_sign_up
    return poolView(p)
  })
  r.del(`${P}/:pool`, ({ params }) => {
    const p = pool(params.pool)
    st().pools = st().pools.filter((x) => x !== p)
    st().users = st().users.filter((u) => u.pool_id !== p.id)
    st().clients = st().clients.filter((c) => c.pool_id !== p.id)
  })

  // app clients
  r.get(`${P}/:pool/clients`, ({ params }) => {
    pool(params.pool)
    return st().clients.filter((c) => c.pool_id === params.pool).map(({ pool_id: _p, ...c }) => c)
  })
  r.post(`${P}/:pool/clients`, ({ params, body }) => {
    pool(params.pool)
    if (!body?.name) throw badRequest("name is required")
    const at = Number(body.access_token_minutes) || 60
    const rt = Number(body.refresh_token_days) || 30
    if (at < 5 || at > 1440 || rt < 1 || rt > 3650) throw badRequest("access_token_minutes must be 5-1440 and refresh_token_days 1-3650")
    const c = { id: hex(26), pool_id: params.pool, name: body.name, has_secret: !!body.generate_secret, access_token_minutes: at, refresh_token_days: rt, created_at: new Date().toISOString() }
    st().clients.push(c)
    return { id: c.id, name: c.name, access_token_minutes: at, refresh_token_days: rt, ...(body.generate_secret ? { client_secret: "demo-not-a-real-secret-" + hex(24) } : {}) }
  })
  r.del(`${P}/:pool/clients/:client`, ({ params }) => {
    const c = st().clients.find((x) => x.id === params.client && x.pool_id === params.pool)
    if (!c) throw err(404, "ResourceNotFoundException", `client "${params.client}" does not exist`)
    st().clients = st().clients.filter((x) => x !== c)
  })

  // users
  r.get(`${P}/:pool/users`, ({ params, query }) => {
    pool(params.pool)
    const f = (query.filter ?? "").toLowerCase()
    return st()
      .users.filter((u) => u.pool_id === params.pool && (!f || `${u.username} ${u.attributes?.email ?? ""}`.toLowerCase().includes(f)))
      .map(userView)
  })
  r.post(`${P}/:pool/users`, ({ params, body }) => {
    const p = pool(params.pool)
    const username = String(body?.username ?? "")
    if (!username || username.length > 128 || /\s/.test(username)) throw badRequest("invalid username")
    if (st().users.some((u) => u.pool_id === p.id && u.username.toLowerCase() === username.toLowerCase())) throw err(409, "UsernameExistsException", `user "${username}" already exists`)
    let password: string = body.password ?? ""
    let temporary = !!body.temporary_password
    let generated = ""
    if (!password) {
      generated = hex(Math.max(p.password_policy.min_length, 12)) + "aA1!"
      password = generated
      temporary = true
    }
    checkPassword(p.password_policy, password)
    checkAttrs(body.attributes)
    checkGroups(p, body.groups ?? [])
    const u = {
      pool_id: p.id, username, sub: sub(username + hex(6)), attributes: { ...(body.attributes ?? {}) } as Record<string, string>, status: temporary ? "FORCE_CHANGE_PASSWORD" : "CONFIRMED", enabled: true,
      groups: (body.groups ?? []) as string[], created_at: new Date().toISOString(), last_sign_in: null,
    }
    st().users.push(u)
    return { ...userView(u), ...(generated ? { temporary_password: generated } : {}) }
  })
  r.get(`${P}/:pool/users/:user`, ({ params }) => userView(userOf(params.pool, params.user)))
  r.del(`${P}/:pool/users/:user`, ({ params }) => {
    const u = userOf(params.pool, params.user)
    st().users = st().users.filter((x) => x !== u)
  })
  r.patch(`${P}/:pool/users/:user`, ({ params, body }) => {
    const p = pool(params.pool)
    const u = userOf(params.pool, params.user)
    checkAttrs(body?.attributes)
    if (body?.groups) checkGroups(p, body.groups)
    for (const [k, v] of Object.entries((body?.attributes ?? {}) as Record<string, string>)) {
      if (v === "") delete u.attributes?.[k]
      else u.attributes = { ...(u.attributes ?? {}), [k]: v }
    }
    if (typeof body?.enabled === "boolean") u.enabled = body.enabled
    if (body?.groups) u.groups = body.groups
    if (body?.confirm && u.status === "UNCONFIRMED") u.status = "CONFIRMED"
    return userView(u)
  })
  r.post(`${P}/:pool/users/:user/password`, ({ params, body }) => {
    const p = pool(params.pool)
    const u = userOf(params.pool, params.user)
    checkPassword(p.password_policy, String(body?.password ?? ""))
    u.status = body?.permanent ? "CONFIRMED" : "FORCE_CHANGE_PASSWORD"
    return userView(u)
  })
  r.post(`${P}/:pool/users/:user/sign-out`, ({ params }) => {
    userOf(params.pool, params.user)
  })

  // groups
  r.post(`${P}/:pool/groups`, ({ params, body }) => {
    const p = pool(params.pool)
    if (!/^[\w\s+=,.@-]{1,128}$/.test(body?.name ?? "")) throw badRequest("invalid group name")
    if (p.groups.some((g) => g.name === body.name)) throw conflict(`group "${body.name}" already exists`)
    p.groups.push({ name: body.name, description: body.description ?? "", precedence: Number(body.precedence) || 0 })
    return poolView(p)
  })
  r.del(`${P}/:pool/groups/:group`, ({ params }) => {
    const p = pool(params.pool)
    if (!p.groups.some((g) => g.name === params.group)) throw notFound("group", params.group)
    p.groups = p.groups.filter((g) => g.name !== params.group)
    for (const u of st().users) if (u.pool_id === p.id) u.groups = (u.groups ?? []).filter((g) => g !== params.group)
    return poolView(p)
  })
}

const service: DemoService = { name: "cognito", seed, routes }
export default service
