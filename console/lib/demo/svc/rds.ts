import { badRequest, conflict, getState, later, notFound, unavailable, err, type DemoService, type Router, type State } from "../engine"
import type { DbClass, DbEngine, DbEngines, DbInstance, DbSnapshot } from "@/lib/types"
import { DAY, HOUR, MIN, AZ_A, AZ_B, ago, arn, nowIso, rng } from "../util"
import { NAMES, SG, SUBNET, VPC_SHOP } from "../ids"

interface RdsState {
  instances: DbInstance[]
  snapshots: DbSnapshot[]
}

const st = () => getState().rds as RdsState

const ENGINES: DbEngine[] = [
  { name: "postgres", label: "PostgreSQL", kind: "relational", versions: ["17", "16", "15", "14"], default_port: 5432, has_users: true, has_database: true, has_password: true },
  { name: "mysql", label: "MySQL", kind: "relational", versions: ["8.4", "8.0"], default_port: 3306, has_users: true, has_database: true, has_password: true },
  { name: "mariadb", label: "MariaDB", kind: "relational", versions: ["11.4", "10.11"], default_port: 3306, has_users: true, has_database: true, has_password: true },
  { name: "redis", label: "Redis", kind: "cache", versions: ["7.4", "7.2"], default_port: 6379, has_users: false, has_database: false, has_password: true },
  { name: "valkey", label: "Valkey", kind: "cache", versions: ["8"], default_port: 6379, has_users: false, has_database: false, has_password: true },
  { name: "memcached", label: "Memcached", kind: "cache", versions: ["1.6"], default_port: 11211, has_users: false, has_database: false, has_password: false },
  { name: "mongodb", label: "MongoDB (DocumentDB compatible)", kind: "document", versions: ["8.0", "7.0"], default_port: 27017, has_users: true, has_database: true, has_password: true },
]
const CLASSES: DbClass[] = [
  { name: "db.t3.micro", vcpus: 1, memory_mb: 1024, kind: "db" },
  { name: "db.t3.small", vcpus: 1, memory_mb: 2048, kind: "db" },
  { name: "db.t3.medium", vcpus: 2, memory_mb: 4096, kind: "db" },
  { name: "db.t3.large", vcpus: 2, memory_mb: 8192, kind: "db" },
  { name: "db.m5.large", vcpus: 2, memory_mb: 8192, kind: "db" },
  { name: "db.m5.xlarge", vcpus: 4, memory_mb: 16384, kind: "db" },
  { name: "db.r5.large", vcpus: 2, memory_mb: 16384, kind: "db" },
  { name: "cache.t3.micro", vcpus: 1, memory_mb: 512, kind: "cache" },
  { name: "cache.t3.small", vcpus: 1, memory_mb: 1536, kind: "cache" },
  { name: "cache.t3.medium", vcpus: 2, memory_mb: 3072, kind: "cache" },
  { name: "cache.m5.large", vcpus: 2, memory_mb: 6144, kind: "cache" },
]

const dbArn = (id: string) => arn("rds", `db:${id}`)
const snapArn = (id: string) => arn("rds", `snapshot:${id}`)
const TRANSITIONAL = ["creating", "starting", "stopping", "rebooting", "restoring", "backing-up", "modifying"]

function connectHint(i: Pick<DbInstance, "engine" | "endpoint" | "master_username" | "db_name">): string {
  const { address, port } = i.endpoint
  switch (i.engine) {
    case "postgres": return `psql "postgresql://${i.master_username}@${address}:${port}/${i.db_name}"`
    case "mysql":
    case "mariadb": return `mysql -h ${address} -P ${port} -u ${i.master_username} -p ${i.db_name}`
    case "mongodb": return `mongosh "mongodb://${i.master_username}@${address}:${port}/${i.db_name}"`
    case "memcached": return `telnet ${address} ${port}`
    default: return `redis-cli -h ${address} -p ${port} -a <auth-token>`
  }
}

function mkInstance(o: Partial<DbInstance> & { id: string; engine: string; engine_version: string; class: string; kind: DbInstance["kind"] }): DbInstance {
  const c = CLASSES.find((x) => x.name === o.class) ?? CLASSES[0]
  const e = ENGINES.find((x) => x.name === o.engine) ?? ENGINES[0]
  const cache = o.kind === "cache"
  const label = cache ? "elasticache" : "rds"
  const r = rng(o.id)
  const i: DbInstance = {
    arn: cache ? arn("elasticache", `cluster:${o.id}`) : dbArn(o.id), vcpus: c.vcpus, memory_mb: c.memory_mb,
    storage_gb: cache ? 0 : 20, status: "available", master_username: e.has_users ? "shop_admin" : undefined,
    db_name: e.has_database ? "shop" : undefined, secret_name: e.has_password ? `rds!${o.id}` : undefined,
    endpoint: { address: `${o.id}.${label}.internal`, port: e.default_port, public_host: "", public_port: 0, private_ip: `10.0.${cache ? 11 : 10}.${20 + Math.floor(r() * 200)}`, connect_hint: "" },
    vpc_id: VPC_SHOP, subnet_id: SUBNET.privateA, availability_zone: AZ_A, publicly_accessible: false,
    backup_retention_days: cache ? 1 : 7, deletion_protection: false, container_id: `hc-${o.id}`.padEnd(24, "0").slice(0, 24),
    created_at: nowIso(), tags: { Project: "shop", Environment: "prod" }, ...o,
  }
  i.endpoint.connect_hint = connectHint(i)
  return i
}

function mkSnap(inst: DbInstance, id: string, type: "manual" | "automated", age: number, size: number): DbSnapshot {
  return {
    id, arn: snapArn(id), source_instance: inst.id, kind: inst.kind, engine: inst.engine, engine_version: inst.engine_version, type, status: "available",
    size_bytes: size, master_username: inst.master_username, db_name: inst.db_name, storage_gb: inst.storage_gb, created_at: ago(age),
  }
}

function seed(): RdsState {
  const db = mkInstance({
    id: NAMES.db, kind: "relational", engine: "postgres", engine_version: "16", class: "db.m5.large", storage_gb: 100, deletion_protection: true,
    backup_retention_days: 14, latest_backup: ago(5 * HOUR), created_at: ago(190 * DAY), subnet_id: SUBNET.privateA,
    tags: { Project: "shop", Environment: "prod", Team: "platform", "backup": "daily" },
  })
  const replica = mkInstance({
    id: "shop-db-reporting", kind: "relational", engine: "postgres", engine_version: "16", class: "db.t3.medium", storage_gb: 100, restored_from: "shop-db-pre-migration-2026-08",
    backup_retention_days: 1, latest_backup: ago(20 * HOUR), created_at: ago(35 * DAY), subnet_id: SUBNET.privateB, availability_zone: AZ_B, tags: { Project: "shop", Environment: "prod", Purpose: "reporting" },
  })
  const staging = mkInstance({
    id: "shop-db-staging", kind: "relational", engine: "mysql", engine_version: "8.4", class: "db.t3.small", storage_gb: 20, status: "stopped",
    master_username: "staging_admin", db_name: "shop_staging", backup_retention_days: 3, latest_backup: ago(6 * DAY), created_at: ago(120 * DAY), subnet_id: SUBNET.privateB, availability_zone: AZ_B,
    tags: { Project: "shop", Environment: "staging" },
  })
  const cache = mkInstance({ id: NAMES.cache, kind: "cache", engine: "redis", engine_version: "7.4", class: "cache.t3.medium", latest_backup: ago(8 * HOUR), created_at: ago(180 * DAY), tags: { Project: "shop", Environment: "prod" } })
  const sessions = mkInstance({ id: "shop-sessions-cache", kind: "cache", engine: "valkey", engine_version: "8", class: "cache.t3.small", backup_retention_days: 0, created_at: ago(60 * DAY), subnet_id: SUBNET.privateB, availability_zone: AZ_B, tags: { Project: "shop", Environment: "prod" } })
  const mc = mkInstance({ id: "shop-page-cache", kind: "cache", engine: "memcached", engine_version: "1.6", class: "cache.t3.micro", backup_retention_days: 0, created_at: ago(90 * DAY), tags: { Project: "shop", Environment: "prod" } })
  void SG

  const snaps: DbSnapshot[] = [
    mkSnap(db, "shop-db-pre-migration-2026-08", "manual", 45 * DAY, 96_400_000_000),
    mkSnap(db, "shop-db-release-0.2.0", "manual", 6 * DAY, 101_200_000_000),
    mkSnap(db, `rds:${NAMES.db}-${new Date(Date.now() - 5 * HOUR).toISOString().slice(0, 10)}-03-00`, "automated", 5 * HOUR, 102_000_000_000),
    mkSnap(db, `rds:${NAMES.db}-${new Date(Date.now() - 29 * HOUR).toISOString().slice(0, 10)}-03-00`, "automated", 29 * HOUR, 101_700_000_000),
    mkSnap(db, `rds:${NAMES.db}-${new Date(Date.now() - 53 * HOUR).toISOString().slice(0, 10)}-03-00`, "automated", 53 * HOUR, 101_500_000_000),
    mkSnap(staging, "shop-db-staging-before-upgrade", "manual", 30 * DAY, 3_100_000_000),
    mkSnap(cache, `${NAMES.cache}-2026-09-30-03-00`, "automated", 8 * HOUR, 41_500_000),
    mkSnap(cache, "shop-cache-manual-before-flush", "manual", 12 * DAY, 39_800_000),
  ]
  return { instances: [db, replica, staging, cache, sessions, mc], snapshots: snaps }
}

function findInst(id: string) {
  const i = st().instances.find((x) => x.id === id)
  if (!i) throw notFound("database instance", id)
  return i
}
const findSnap = (id: string) => {
  const s = st().snapshots.find((x) => x.id === id)
  if (!s) throw notFound("snapshot", id)
  return s
}

const ID_RE = /^[a-z][a-z0-9-]{0,62}$/

function setStatus(id: string, status: string, reason?: string) {
  later(2500 + Math.random() * 1500, () => {
    const i = st().instances.find((x) => x.id === id)
    if (i && TRANSITIONAL.includes(i.status)) {
      i.status = status
      i.status_reason = reason
    }
  })
}

function provision(body: any, snap?: DbSnapshot, status: "creating" | "restoring" = "creating"): DbInstance {
  const id = String(body?.id ?? "")
  if (!ID_RE.test(id)) throw badRequest("identifier must start with a lowercase letter and contain only lowercase letters, digits and hyphens (max 63)")
  if (st().instances.some((x) => x.id === id)) throw conflict(`database instance ${id} already exists`)
  const engineName = snap?.engine ?? body?.engine
  const e = ENGINES.find((x) => x.name === engineName)
  if (!e) throw badRequest(`unknown engine ${engineName}`)
  const version = snap?.engine_version ?? body?.engine_version ?? e.versions[0]
  if (!e.versions.includes(version)) throw badRequest(`${e.label} version ${version} is not available`)
  const cls = body?.class || (e.kind === "cache" ? "cache.t3.micro" : "db.t3.micro")
  const c = CLASSES.find((x) => x.name === cls)
  if (!c) throw badRequest(`unknown instance class ${cls}`)
  if (e.has_users && body?.master_username === "root" && e.kind === "relational") throw badRequest("root is reserved; choose another name")
  const pub = !!body?.publicly_accessible
  const inst = mkInstance({
    id, kind: e.kind, engine: e.name, engine_version: version, class: cls, status, created_at: nowIso(),
    storage_gb: e.kind === "cache" ? 0 : Number(body?.storage_gb) || snap?.storage_gb || 20,
    master_username: e.has_users ? (snap?.master_username ?? (body?.master_username || "admin")) : undefined,
    db_name: e.has_database ? (snap?.db_name ?? (body?.db_name || "app")) : undefined,
    subnet_id: body?.subnet_id || SUBNET.privateA, publicly_accessible: pub,
    backup_retention_days: body?.backup_retention_days ?? (e.kind === "cache" ? 0 : 7), deletion_protection: !!body?.deletion_protection,
    tags: { ...(body?.tags ?? {}) }, restored_from: snap?.id, latest_backup: undefined,
  })
  if (pub) {
    inst.endpoint.public_host = "homecloud.local"
    inst.endpoint.public_port = body?.port || 15000 + Math.floor(Math.random() * 2000)
  }
  inst.endpoint.connect_hint = connectHint(inst)
  st().instances.push(inst)
  setStatus(id, "available")
  return inst
}

function newSnap(inst: DbInstance, id: string): DbSnapshot {
  const s = mkSnap(inst, id, "manual", 0, Math.max(inst.storage_gb, 1) * 1_000_000_000 * 0.4 + 2_000_000)
  s.status = "creating"
  st().snapshots.push(s)
  later(2500, () => {
    const x = st().snapshots.find((y) => y.id === id)
    if (x) x.status = "available"
    const i = st().instances.find((y) => y.id === inst.id)
    if (i && i.status === "backing-up") i.status = "available"
  })
  return s
}

function routes(r: Router) {
  const base = "/api/v1/rds"
  r.get(`${base}/engines`, (): DbEngines => ({ engines: ENGINES, classes: CLASSES }))
  r.get(`${base}/instances`, ({ query }) => st().instances.filter((i) => !query.kind || i.kind === query.kind))
  r.post(`${base}/instances`, ({ body }) => provision(body))
  r.get(`${base}/instances/:id`, ({ params }) => findInst(params.id))
  r.patch(`${base}/instances/:id`, ({ params, body }) => {
    const i = findInst(params.id)
    if (body?.class && body.class !== i.class) {
      const c = CLASSES.find((x) => x.name === body.class)
      if (!c) throw badRequest(`unknown instance class ${body.class}`)
      if (!["available", "stopped"].includes(i.status)) throw conflict(`instance is ${i.status}; wait until it is available or stopped`)
      const was = i.status
      i.class = c.name
      i.vcpus = c.vcpus
      i.memory_mb = c.memory_mb
      if (was === "available") {
        i.status = "modifying"
        setStatus(i.id, "available")
      }
    }
    if (body?.storage_gb) i.storage_gb = Math.max(i.storage_gb, Number(body.storage_gb))
    if (body?.backup_retention_days !== undefined && body.backup_retention_days !== null) i.backup_retention_days = body.backup_retention_days
    if (body?.deletion_protection !== undefined && body.deletion_protection !== null) i.deletion_protection = !!body.deletion_protection
    if (body?.tags) i.tags = { ...body.tags }
    return i
  })
  r.del(`${base}/instances/:id`, ({ params, query }) => {
    const i = findInst(params.id)
    if (i.deletion_protection) throw conflict(`deletion protection is enabled for ${i.id}; turn it off first`)
    if (query.final_snapshot === "true" && i.status === "available") {
      const id = `${i.id}-final-${new Date().toISOString().replace(/\D/g, "").slice(0, 14)}`
      const s = mkSnap(i, id, "manual", 0, Math.max(i.storage_gb, 1) * 400_000_000)
      st().snapshots.push(s)
    }
    st().snapshots = st().snapshots.filter((s) => !(s.source_instance === i.id && s.type === "automated"))
    st().instances = st().instances.filter((x) => x !== i)
  })
  const action = (name: string, from: string[], during: string, to: string) =>
    r.post(`${base}/instances/:id/${name}`, ({ params }) => {
      const i = findInst(params.id)
      if (!from.includes(i.status)) throw conflict(`cannot ${name} ${i.id} while it is ${i.status}`)
      i.status = during
      setStatus(i.id, to)
      return i
    })
  action("start", ["stopped"], "starting", "available")
  action("stop", ["available"], "stopping", "stopped")
  action("reboot", ["available"], "rebooting", "available")
  r.post(`${base}/instances/:id/password`, ({ params, body }) => {
    const i = findInst(params.id)
    if (!i.secret_name || !["postgres", "mysql", "mariadb", "mongodb"].includes(i.engine)) throw badRequest(`${i.engine} does not support password reset`)
    if (body?.password && String(body.password).length < 8) throw badRequest("password must be at least 8 characters")
    return { secret_name: i.secret_name }
  })
  r.post(`${base}/instances/:id/query`, ({ params }) => {
    const i = findInst(params.id)
    throw unavailable(i.kind === "cache" ? "The command console" : "The SQL query editor")
  })
  r.get(`${base}/instances/:id/logs`, ({ params, query }) => {
    const i = findInst(params.id)
    return { output: logsFor(i, Number(query.tail) || 500) }
  })
  r.post(`${base}/instances/:id/snapshots`, ({ params, body }) => {
    const i = findInst(params.id)
    if (i.engine === "memcached") throw badRequest("memcached does not support snapshots")
    if (i.status !== "available") throw conflict(`${i.id} is ${i.status}; snapshots need an available instance`)
    const id = String(body?.id || `${i.id}-snap-${new Date().toISOString().replace(/\D/g, "").slice(0, 14)}`)
    if (!ID_RE.test(id)) throw badRequest("snapshot identifier must start with a lowercase letter and contain only lowercase letters, digits and hyphens")
    if (st().snapshots.some((s) => s.id === id)) throw conflict(`snapshot ${id} already exists`)
    i.status = "backing-up"
    i.latest_backup = nowIso()
    return newSnap(i, id)
  })
  r.get(`${base}/snapshots`, ({ query }) =>
    st().snapshots.filter((s) => !query.instance || s.source_instance === query.instance).sort((a, b) => (a.created_at < b.created_at ? 1 : -1)),
  )
  r.del(`${base}/snapshots/:snap`, ({ params }) => {
    const s = findSnap(params.snap)
    st().snapshots = st().snapshots.filter((x) => x !== s)
  })
  r.post(`${base}/snapshots/:snap/restore`, ({ params, body }) => {
    const s = findSnap(params.snap)
    if (s.status !== "available") throw conflict(`snapshot ${s.id} is ${s.status}`)
    return provision(body, s, "restoring")
  })
}

function logsFor(i: DbInstance, tail: number): string {
  const lines: string[] = []
  const t = (m: number) => new Date(Date.now() - m * MIN).toISOString().replace("T", " ").replace("Z", " UTC")
  const stamp = (m: number) => new Date(Date.now() - m * MIN).toISOString()
  const f = (m: number, s: string) => `${stamp(m)} ${s}`
  if (i.engine === "postgres") {
    lines.push(f(600, `${t(600)} [1] LOG:  database system is ready to accept connections`))
    for (let k = 0; k < 40; k++) {
      lines.push(f(300 - k * 6, `${t(300 - k * 6)} [${100 + k}] LOG:  checkpoint starting: time`))
      lines.push(f(299 - k * 6, `${t(299 - k * 6)} [${100 + k}] LOG:  checkpoint complete: wrote ${120 + k * 7} buffers (0.7%); 0 WAL file(s) added, 0 removed, 1 recycled`))
    }
    lines.push(f(14, `${t(14)} [431] LOG:  duration: 1204.331 ms  statement: SELECT o.id, o.total FROM orders o JOIN customers c ON c.id = o.customer_id WHERE c.country = 'US' ORDER BY o.created_at DESC LIMIT 500`))
    lines.push(f(3, `${t(3)} [502] LOG:  connection authorized: user=${i.master_username} database=${i.db_name}`))
  } else if (i.engine === "mysql" || i.engine === "mariadb") {
    lines.push(f(700, `[Note] [MY-010931] [Server] /usr/sbin/mysqld: ready for connections. Version: '${i.engine_version}'  socket: '/var/run/mysqld/mysqld.sock'  port: ${i.endpoint.port}`))
    for (let k = 0; k < 20; k++) lines.push(f(400 - k * 15, `[Note] [MY-011953] [InnoDB] Page cleaner took ${20 + k}ms to flush ${k + 3} pages`))
  } else if (i.engine === "memcached") {
    lines.push(f(900, "memcached started, listening on 0.0.0.0:11211"))
  } else {
    lines.push(f(900, `Ready to accept connections tcp`))
    for (let k = 0; k < 30; k++) lines.push(f(600 - k * 15, `${k % 2 ? "Background saving started by pid" : "DB saved on disk"} ${1000 + k}`))
  }
  return lines.slice(-tail).join("\n") + "\n"
}

const service: DemoService = {
  name: "rds",
  seed,
  routes,
  onLoad: (s: State) => {
    const rs = s.rds as RdsState
    for (const i of rs.instances) {
      if (TRANSITIONAL.includes(i.status)) {
        i.status = i.status === "stopping" ? "stopped" : "available"
        i.status_reason = undefined
      }
    }
    for (const sn of rs.snapshots) if (sn.status === "creating") sn.status = "available"
  },
}

void err
export default service
