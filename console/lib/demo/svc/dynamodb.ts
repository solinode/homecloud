import { badRequest, conflict, err, getState, notFound, type DemoService, type Router } from "../engine"
import type { Condition, DynamoItem, DynamoTable, KeyDef, StreamInfo, StreamViewType, TableIndex } from "@/lib/types"
import { DAY, HOUR, MIN, REGION, ago, arn, hex, rng } from "../util"

interface TableState {
  name: string
  partition_key: KeyDef
  sort_key: KeyDef | null
  global_secondary_indexes: TableIndex[]
  local_secondary_indexes: TableIndex[]
  ttl_attribute: string
  billing_mode: string
  created_at: string
  tags: Record<string, string>
  streams: StreamInfo[]
  deletion_protection: boolean
  table_class: string
  point_in_time_recovery: boolean
  sse_enabled: boolean
  items: DynamoItem[]
  /** extra bytes/item count to make big tables look big without storing every item */
  extra_items: number
  extra_bytes: number
}

const st = () => getState().dynamodb as { tables: TableState[] }
const tableArn = (n: string) => arn("dynamodb", `table/${n}`)

const S = (name: string): KeyDef => ({ name, type: "S" })
const N = (name: string): KeyDef => ({ name, type: "N" })

const NAMES_F = ["Ava", "Liam", "Noah", "Emma", "Olivia", "Mia", "Lucas", "Sofia", "Ethan", "Zoe", "Mason", "Nora", "Leo", "Ivy", "Owen", "Ruby"]
const NAMES_L = ["Nguyen", "Patel", "Garcia", "Kim", "Silva", "Novak", "Larsen", "Okafor", "Rossi", "Meyer", "Haddad", "Tanaka"]
const SKUS = [
  ["SKU-1001", "Canvas tote", 2400], ["SKU-1002", "Enamel mug", 1250], ["SKU-1003", "Desk mat", 3900], ["SKU-1004", "Sticker pack", 400],
  ["SKU-1005", "Notebook A5", 1100], ["SKU-1006", "Ceramic planter", 2800], ["SKU-1007", "Wool beanie", 1900], ["SKU-1008", "Water bottle", 2200],
] as const

function seedTables(): TableState[] {
  const r = rng("ddb-seed")
  const pick = <T,>(a: readonly T[]) => a[Math.floor(r() * a.length)]
  const base = (name: string, age: number, o: Partial<TableState>): TableState => ({
    name, partition_key: S("id"), sort_key: null, global_secondary_indexes: [], local_secondary_indexes: [], ttl_attribute: "", billing_mode: "PAY_PER_REQUEST",
    created_at: ago(age), tags: { Project: "shop", Environment: "prod" }, streams: [], deletion_protection: false, table_class: "STANDARD",
    point_in_time_recovery: false, sse_enabled: true, items: [], extra_items: 0, extra_bytes: 0, ...o,
  })

  const customers: DynamoItem[] = []
  for (let i = 0; i < 16; i++) {
    const f = pick(NAMES_F), l = pick(NAMES_L)
    customers.push({ customer_id: `cus_${4100 + i}`, name: `${f} ${l}`, email: `${f}.${l}@example.com`.toLowerCase(), country: pick(["US", "DE", "GB", "CA", "NL", "JP"]), loyalty_points: Math.floor(r() * 2500), created_at: ago((5 + i * 9) * DAY), marketing_opt_in: r() > 0.4 })
  }
  const orders: DynamoItem[] = []
  const statuses = ["PENDING", "PAID", "SHIPPED", "DELIVERED", "DELIVERED", "DELIVERED", "CANCELLED"]
  for (let i = 0; i < 40; i++) {
    const cust = customers[Math.floor(r() * customers.length)].customer_id as string
    const lines = 1 + Math.floor(r() * 3)
    const items = Array.from({ length: lines }, () => {
      const s = pick(SKUS)
      return { sku: s[0], name: s[1], qty: 1 + Math.floor(r() * 3), unit_price_cents: s[2] }
    })
    const total = items.reduce((n, x) => n + x.qty * x.unit_price_cents, 0)
    orders.push({ order_id: `ORD-${88100 + i}`, created_at: ago(i * 4 * HOUR + Math.floor(r() * HOUR)), customer_id: cust, status: pick(statuses), currency: "USD", total_cents: total, items, shipping: { method: pick(["standard", "express"]), country: "US" } })
  }
  const inventory: DynamoItem[] = []
  for (const [sku, name, price] of SKUS) for (const wh of ["us-east", "us-west"]) inventory.push({ sku, warehouse: wh, name, stock: Math.floor(r() * 400), reserved: Math.floor(r() * 20), price_cents: price, updated_at: ago(Math.floor(r() * 3 * DAY)) })
  const sessions: DynamoItem[] = []
  for (let i = 0; i < 20; i++) {
    const exp = Math.floor(Date.now() / 1000) + Math.floor((r() - 0.3) * 6 * 3600)
    sessions.push({ session_id: `sess_${hex(16)}`, customer_id: customers[i % customers.length].customer_id, ip: `203.0.113.${10 + i}`, user_agent: pick(["Mozilla/5.0 (Macintosh)", "Mozilla/5.0 (iPhone)", "Mozilla/5.0 (X11; Linux)"]), expires_at: exp })
  }
  const carts: DynamoItem[] = customers.slice(0, 8).map((c) => ({ customer_id: c.customer_id, updated_at: ago(Math.floor(r() * DAY)), items: [{ sku: pick(SKUS)[0], qty: 1 + Math.floor(r() * 3) }], coupon: r() > 0.7 ? "WELCOME10" : null }))
  const events: DynamoItem[] = []
  for (let i = 0; i < 30; i++) events.push({ pk: `order#ORD-${88100 + (i % 6)}`, sk: `${ago(i * 15 * MIN)}#${pick(["created", "paid", "packed", "shipped", "delivered"])}`, source: pick(["web", "worker", "payments"]), detail: { attempt: 1 } })

  return [
    base("shop-orders", 200 * DAY, {
      partition_key: S("order_id"), sort_key: S("created_at"),
      global_secondary_indexes: [
        { name: "customer-index", partition_key: S("customer_id"), sort_key: S("created_at"), projection: { type: "ALL" }, status: "ACTIVE" },
        { name: "status-index", partition_key: S("status"), sort_key: S("created_at"), projection: { type: "INCLUDE", non_key_attributes: ["total_cents", "customer_id"] }, status: "ACTIVE" },
      ],
      streams: [{ label: "2026-03-14T09:12:44.118", view_type: "NEW_AND_OLD_IMAGES", created: ago(200 * DAY) }],
      point_in_time_recovery: true, deletion_protection: true, items: orders, extra_items: 18240, extra_bytes: 9_400_000,
    }),
    base("shop-customers", 200 * DAY, {
      partition_key: S("customer_id"),
      global_secondary_indexes: [{ name: "email-index", partition_key: S("email"), projection: { type: "KEYS_ONLY" }, status: "ACTIVE" }],
      point_in_time_recovery: true, items: customers, extra_items: 5230, extra_bytes: 1_800_000,
    }),
    base("shop-inventory", 150 * DAY, { partition_key: S("sku"), sort_key: S("warehouse"), items: inventory, extra_items: 0 }),
    base("shop-sessions", 120 * DAY, { partition_key: S("session_id"), ttl_attribute: "expires_at", table_class: "STANDARD", sse_enabled: false, items: sessions, extra_items: 812, extra_bytes: 240_000 }),
    base("shop-carts", 120 * DAY, { partition_key: S("customer_id"), items: carts, extra_items: 96, extra_bytes: 41_000 }),
    base("shop-audit-events", 60 * DAY, {
      partition_key: S("pk"), sort_key: S("sk"), table_class: "STANDARD_INFREQUENT_ACCESS", streams: [{ label: "2026-08-01T00:00:10.004", view_type: "NEW_IMAGE", created: ago(60 * DAY) }],
      items: events, extra_items: 60300, extra_bytes: 22_000_000, tags: { Project: "shop", Environment: "prod", Team: "platform" },
    }),
  ]
}

// ---- helpers ----

const findTable = (name: string) => {
  const t = st().tables.find((x) => x.name === name)
  if (!t) throw err(404, "ResourceNotFoundException", `Requested resource not found: Table: ${name} not found`)
  return t
}

const itemSize = (it: DynamoItem) => JSON.stringify(it).length + 40

function view(t: TableState): DynamoTable {
  const active = [...t.streams].reverse().find((s) => !s.disabled)
  const out: DynamoTable = {
    name: t.name, arn: tableArn(t.name), partition_key: t.partition_key, sort_key: t.sort_key,
    global_secondary_indexes: t.global_secondary_indexes.length ? t.global_secondary_indexes : null,
    local_secondary_indexes: t.local_secondary_indexes.length ? t.local_secondary_indexes : null,
    ttl_attribute: t.ttl_attribute || undefined, status: "ACTIVE", billing_mode: t.billing_mode, created_at: t.created_at,
    tags: Object.keys(t.tags).length ? t.tags : null,
    item_count: t.items.length + t.extra_items, size_bytes: t.items.reduce((n, i) => n + itemSize(i), 0) + t.extra_bytes,
    streams: t.streams.length ? t.streams : null, deletion_protection: t.deletion_protection, table_class: t.table_class,
    point_in_time_recovery: t.point_in_time_recovery, sse_enabled: t.sse_enabled,
  }
  if (active) {
    out.stream_arn = `${tableArn(t.name)}/stream/${active.label}`
    out.stream_view_type = active.view_type
  }
  return out
}

const isNil = (v: unknown) => v === undefined || v === null

function cmp(a: unknown, b: unknown): number {
  if (typeof a === "number" && typeof b === "number") return a - b
  const x = String(a), y = String(b)
  return x < y ? -1 : x > y ? 1 : 0
}

// key values may arrive as "42" for N keys
function coerce(def: KeyDef | null | undefined, v: unknown) {
  if (!def || isNil(v)) return v
  if (def.type === "N" && typeof v === "string" && v.trim() !== "" && !Number.isNaN(Number(v))) return Number(v)
  if (def.type === "S" && typeof v === "number") return String(v)
  return v
}

function check(it: DynamoItem, c: Condition): boolean {
  const a = c.attr ? it[c.attr] : undefined
  switch (c.op) {
    case "exists": return !isNil(a)
    case "not_exists": return isNil(a)
    case "eq": return !isNil(a) && cmp(a, c.value) === 0
    case "ne": return isNil(a) || cmp(a, c.value) !== 0
    case "lt": return !isNil(a) && cmp(a, c.value) < 0
    case "le": return !isNil(a) && cmp(a, c.value) <= 0
    case "gt": return !isNil(a) && cmp(a, c.value) > 0
    case "ge": return !isNil(a) && cmp(a, c.value) >= 0
    case "between": return !isNil(a) && cmp(a, c.value) >= 0 && cmp(a, c.value2) <= 0
    case "begins_with": return typeof a === "string" && a.startsWith(String(c.value))
    case "contains":
      if (Array.isArray(a)) return a.some((x) => cmp(x, c.value) === 0)
      return typeof a === "string" && a.includes(String(c.value))
    default: throw badRequest(`unknown condition operator "${c.op}"`)
  }
}

function keyDefs(t: TableState, index?: string): { pk: KeyDef; sk: KeyDef | null } {
  if (!index) return { pk: t.partition_key, sk: t.sort_key }
  const ix = [...t.global_secondary_indexes, ...t.local_secondary_indexes].find((i) => i.name === index)
  if (!ix) throw badRequest(`index "${index}" does not exist`)
  return { pk: ix.partition_key, sk: ix.sort_key ?? null }
}

function project(t: TableState, it: DynamoItem, index: string | undefined, projection?: string[]): DynamoItem {
  let out = it
  if (index) {
    const ix = [...t.global_secondary_indexes, ...t.local_secondary_indexes].find((i) => i.name === index)
    const p = ix?.projection
    if (p && p.type !== "ALL") {
      const keep = new Set([t.partition_key.name, t.sort_key?.name, ix?.partition_key.name, ix?.sort_key?.name, ...(p.type === "INCLUDE" ? (p.non_key_attributes ?? []) : [])].filter(Boolean) as string[])
      out = Object.fromEntries(Object.entries(it).filter(([k]) => keep.has(k)))
    }
  }
  if (projection?.length) out = Object.fromEntries(Object.entries(out).filter(([k]) => projection.includes(k)))
  return out
}

function keyOf(t: TableState, it: DynamoItem, index?: string): DynamoItem {
  const names = new Set([t.partition_key.name, t.sort_key?.name].filter(Boolean) as string[])
  const d = keyDefs(t, index)
  names.add(d.pk.name)
  if (d.sk) names.add(d.sk.name)
  return Object.fromEntries([...names].filter((n) => !isNil(it[n])).map((n) => [n, it[n]]))
}

const sameKey = (t: TableState, a: DynamoItem, b: DynamoItem) => cmp(a[t.partition_key.name], b[t.partition_key.name]) === 0 && (!t.sort_key || cmp(a[t.sort_key.name], b[t.sort_key.name]) === 0)

function readPage(t: TableState, body: any, mode: "scan" | "query") {
  const index: string | undefined = body?.index || undefined
  const { pk, sk } = keyDefs(t, index)
  let rows = t.items.filter((it) => (index ? !isNil(it[pk.name]) && (!sk || !isNil(it[sk.name])) : true))
  if (mode === "query") {
    if (isNil(body?.partition_value) || body.partition_value === "") throw badRequest("partition_value is required")
    const pv = coerce(pk, body.partition_value)
    rows = rows.filter((it) => cmp(it[pk.name], pv) === 0)
    const sc = body.sort_condition as Condition | undefined
    if (sc && sk) {
      const c: Condition = { attr: sk.name, op: sc.op || "eq", value: coerce(sk, sc.value), value2: coerce(sk, sc.value2) }
      if (!["eq", "lt", "le", "gt", "ge", "between", "begins_with"].includes(c.op)) throw badRequest("sort_condition op must be eq, lt, le, gt, ge, between or begins_with")
      rows = rows.filter((it) => check(it, c))
    }
    rows = [...rows].sort((a, b) => (sk ? cmp(a[sk.name], b[sk.name]) : 0))
    if (body.forward === false) rows.reverse()
  } else if (index) {
    rows = [...rows].sort((a, b) => cmp(a[pk.name], b[pk.name]) || (sk ? cmp(a[sk.name], b[sk.name]) : 0))
  }
  let start = 0
  if (body?.start_key) {
    const sk0 = body.start_key as DynamoItem
    const i = rows.findIndex((it) => cmp(it[t.partition_key.name], coerce(t.partition_key, sk0[t.partition_key.name])) === 0 && (!t.sort_key || cmp(it[t.sort_key.name], coerce(t.sort_key, sk0[t.sort_key.name])) === 0))
    start = i >= 0 ? i + 1 : 0
  }
  const limit = Math.min(Math.max(Number(body?.limit) || 1000, 1), 1000)
  const window = rows.slice(start, start + limit)
  const filters = (body?.filter ?? []) as Condition[]
  const matched = window.filter((it) => filters.every((f) => check(it, f)))
  const out: Record<string, unknown> = {
    items: matched.map((it) => project(t, it, index, body?.projection)), count: matched.length, scanned_count: window.length,
  }
  if (start + limit < rows.length && window.length) out.last_evaluated_key = keyOf(t, window[window.length - 1], index)
  return out
}

function validateItem(t: TableState, it: DynamoItem) {
  if (!it || typeof it !== "object" || Array.isArray(it)) throw badRequest("item is required")
  for (const d of [t.partition_key, t.sort_key]) {
    if (!d) continue
    it[d.name] = coerce(d, it[d.name])
    const v = it[d.name]
    if (isNil(v) || v === "") throw err(400, "ValidationException", `One or more parameter values are not valid. Missing the key ${d.name} in the item`)
    if ((d.type === "N") !== (typeof v === "number")) throw err(400, "ValidationException", `One or more parameter values are not valid. The provided key element does not match the schema (${d.name} must be of type ${d.type})`)
  }
  if (itemSize(it) > 400 * 1024) throw err(400, "ValidationException", "Item size has exceeded the maximum allowed size")
}

function condFail(): never {
  throw err(400, "ConditionalCheckFailedException", "The conditional request failed")
}

function keyFrom(t: TableState, key: DynamoItem): DynamoItem {
  if (!key) throw badRequest("key is required")
  const out: DynamoItem = { [t.partition_key.name]: coerce(t.partition_key, key[t.partition_key.name]) }
  if (isNil(out[t.partition_key.name])) throw err(400, "ValidationException", `The provided key element does not match the schema`)
  if (t.sort_key) {
    out[t.sort_key.name] = coerce(t.sort_key, key[t.sort_key.name])
    if (isNil(out[t.sort_key.name])) throw err(400, "ValidationException", `The provided key element does not match the schema`)
  }
  return out
}

const validStream = (v: string) => ["KEYS_ONLY", "NEW_IMAGE", "OLD_IMAGE", "NEW_AND_OLD_IMAGES"].includes(v)
const streamLabel = () => new Date().toISOString().replace("Z", "").slice(0, 23)

function setStream(t: TableState, on: boolean, view?: StreamViewType) {
  const now = new Date().toISOString()
  for (const s of t.streams) if (!s.disabled) s.disabled = now
  if (on && view) t.streams.push({ label: streamLabel(), view_type: view, created: now })
}

function routes(r: Router) {
  const base = "/api/v1/dynamodb/tables"
  r.get(base, () => st().tables.map(view))
  r.post(base, ({ body }) => {
    const name = String(body?.name ?? "")
    if (!/^[A-Za-z0-9_.-]{3,255}$/.test(name)) throw err(400, "ValidationException", "table name must be 3-255 characters of letters, digits, underscore, hyphen and dot")
    if (st().tables.some((x) => x.name === name)) throw err(409, "ResourceInUseException", `Table already exists: ${name}`)
    if (!body?.partition_key?.name) throw err(400, "ValidationException", "partition_key is required")
    const fill = (k?: KeyDef | null): KeyDef | null => (k?.name ? { name: k.name, type: k.type || "S" } : null)
    const ix = (l?: TableIndex[]): TableIndex[] => (l ?? []).map((i) => ({ ...i, partition_key: fill(i.partition_key)!, sort_key: fill(i.sort_key), projection: i.projection ?? { type: "ALL" }, status: "ACTIVE" }))
    const t: TableState = {
      name, partition_key: fill(body.partition_key)!, sort_key: fill(body.sort_key), global_secondary_indexes: ix(body.global_secondary_indexes),
      local_secondary_indexes: ix(body.local_secondary_indexes), ttl_attribute: body.ttl_attribute ?? "", billing_mode: "PAY_PER_REQUEST",
      created_at: new Date().toISOString(), tags: { ...(body.tags ?? {}) }, streams: [], deletion_protection: !!body.deletion_protection,
      table_class: "STANDARD", point_in_time_recovery: false, sse_enabled: true, items: [], extra_items: 0, extra_bytes: 0,
    }
    if (body.stream_view_type) {
      if (!validStream(body.stream_view_type)) throw err(400, "ValidationException", "invalid stream_view_type")
      setStream(t, true, body.stream_view_type)
    }
    st().tables.push(t)
    return view(t)
  })
  r.get(`${base}/:name`, ({ params }) => view(findTable(params.name)))
  r.patch(`${base}/:name`, ({ params, body }) => {
    const t = findTable(params.name)
    if (body?.ttl_attribute !== undefined) t.ttl_attribute = body.ttl_attribute
    if (body?.add_index) {
      const ix = body.add_index as TableIndex
      if (t.global_secondary_indexes.some((i) => i.name === ix.name)) throw conflict(`index "${ix.name}" already exists`)
      t.global_secondary_indexes.push({ ...ix, partition_key: { name: ix.partition_key.name, type: ix.partition_key.type || "S" }, sort_key: ix.sort_key?.name ? { name: ix.sort_key.name, type: ix.sort_key.type || "S" } : null, projection: ix.projection ?? { type: "ALL" }, status: "ACTIVE" })
    }
    if (body?.remove_index) {
      const n = t.global_secondary_indexes.length
      t.global_secondary_indexes = t.global_secondary_indexes.filter((i) => i.name !== body.remove_index)
      if (t.global_secondary_indexes.length === n) throw err(400, "ValidationException", `index "${body.remove_index}" does not exist`)
    }
    if (body?.tags) t.tags = { ...body.tags }
    if (body?.deletion_protection !== undefined) t.deletion_protection = !!body.deletion_protection
    if (body?.stream_view_type !== undefined) {
      const v = body.stream_view_type as StreamViewType | ""
      const cur = [...t.streams].reverse().find((s) => !s.disabled)
      if (!v) {
        if (cur) setStream(t, false)
      } else {
        if (!validStream(v)) throw err(400, "ValidationException", "invalid stream_view_type")
        if (!cur || cur.view_type !== v) setStream(t, true, v)
      }
    }
    return view(t)
  })
  r.del(`${base}/:name`, ({ params }) => {
    const t = findTable(params.name)
    if (t.deletion_protection) throw err(400, "ValidationException", `Resource cannot be deleted as it has deletion protection enabled: ${t.name}`)
    st().tables = st().tables.filter((x) => x !== t)
  })

  r.post(`${base}/:name/items`, ({ params, body }) => {
    const t = findTable(params.name)
    const it = body?.item as DynamoItem
    validateItem(t, it)
    const old = t.items.find((x) => sameKey(t, x, it))
    for (const c of (body?.condition ?? []) as Condition[]) if (!check(old ?? {}, c)) condFail()
    if (old) t.items[t.items.indexOf(old)] = it
    else t.items.push(it)
    return body?.return_old ? { old_item: old ?? null } : {}
  })
  r.post(`${base}/:name/items/get`, ({ params, body }) => {
    const t = findTable(params.name)
    const key = keyFrom(t, body?.key)
    const it = t.items.find((x) => sameKey(t, x, key))
    return { item: it ? project(t, it, undefined, body?.projection) : null }
  })
  r.post(`${base}/:name/items/update`, ({ params, body }) => {
    const t = findTable(params.name)
    const key = keyFrom(t, body?.key)
    const cur = t.items.find((x) => sameKey(t, x, key))
    for (const c of (body?.condition ?? []) as Condition[]) if (!check(cur ?? {}, c)) condFail()
    const keyNames = [t.partition_key.name, t.sort_key?.name]
    const touched = [...Object.keys(body?.set ?? {}), ...(body?.remove ?? []), ...Object.keys(body?.add ?? {})]
    if (touched.some((a) => keyNames.includes(a))) throw err(400, "ValidationException", "key attributes cannot be updated")
    const nu: DynamoItem = { ...(cur ?? key) }
    for (const [a, v] of Object.entries(body?.set ?? {})) nu[a] = v
    for (const a of (body?.remove ?? []) as string[]) delete nu[a]
    for (const [a, v] of Object.entries(body?.add ?? {})) {
      if (typeof v !== "number") throw err(400, "ValidationException", "add values must be numbers")
      nu[a] = (typeof nu[a] === "number" ? (nu[a] as number) : 0) + v
    }
    if (cur) t.items[t.items.indexOf(cur)] = nu
    else t.items.push(nu)
    return { item: nu }
  })
  r.post(`${base}/:name/items/delete`, ({ params, body }) => {
    const t = findTable(params.name)
    const key = keyFrom(t, body?.key)
    const cur = t.items.find((x) => sameKey(t, x, key))
    for (const c of (body?.condition ?? []) as Condition[]) if (!check(cur ?? {}, c)) condFail()
    if (cur) t.items.splice(t.items.indexOf(cur), 1)
    return { old_item: cur ?? null }
  })
  r.post(`${base}/:name/batch-write`, ({ params, body }) => {
    const t = findTable(params.name)
    const puts = (body?.puts ?? []) as DynamoItem[]
    const dels = (body?.deletes ?? []) as DynamoItem[]
    if (puts.length + dels.length > 1000) throw err(400, "ValidationException", "a batch holds at most 1000 writes")
    for (const it of puts) {
      validateItem(t, it)
      const i = t.items.findIndex((x) => sameKey(t, x, it))
      if (i >= 0) t.items[i] = it
      else t.items.push(it)
    }
    for (const k of dels) {
      const key = keyFrom(t, k)
      const i = t.items.findIndex((x) => sameKey(t, x, key))
      if (i >= 0) t.items.splice(i, 1)
    }
    return { written: puts.length, deleted: dels.length }
  })
  r.post(`${base}/:name/query`, ({ params, body }) => readPage(findTable(params.name), body, "query"))
  r.post(`${base}/:name/scan`, ({ params, body }) => readPage(findTable(params.name), body, "scan"))
}

const service: DemoService = {
  name: "dynamodb",
  seed: () => ({ tables: seedTables() }),
  routes,
}

void REGION
void notFound
export default service
