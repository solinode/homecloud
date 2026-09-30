import { badRequest, conflict, getState, later, notFound, type DemoService, type Router, type State } from "../engine"
import { DAY, MIN, ACCOUNT, REGION, ago, arn, clone, nowIso, rng, stableId, uuid } from "../util"
import { NAMES, SG, SUBNET, VPC_DEFAULT, VPC_SHOP } from "../ids"
import { ECR_REGISTRY } from "./ecr"
import type { EcsService, EcsServiceEvent, EcsTask, EcsTaskDefinition } from "@/lib/types"

interface EcsState {
  taskDefs: EcsTaskDefinition[]
  /** stored services; counts, endpoint, tasks and events are derived */
  services: (Omit<EcsService, "running_count" | "pending_count" | "endpoint" | "tasks" | "events"> & { events: EcsServiceEvent[] })[]
  tasks: EcsTask[]
}
const S = (): EcsState => getState().ecs as EcsState

const CLUSTER = NAMES.ecsCluster
const tdKey = (t: { family: string; revision: number }) => `${t.family}:${t.revision}`
const taskId = (seed: string) => stableId("", seed, 32)
const taskArn = (id: string) => arn("ecs", `task/${CLUSTER}/${id}`)
const ipFor = (subnet: string, seed: string) => {
  const r = Math.floor(rng(seed)() * 200) + 10
  return subnet === SUBNET.privateB ? `10.0.12.${r}` : subnet === SUBNET.privateA ? `10.0.11.${r}` : subnet === SUBNET.publicA ? `10.0.1.${r}` : subnet === SUBNET.publicB ? `10.0.2.${r}` : `172.31.${r}.${r % 250}`
}
const vpcFor = (subnet: string) => (subnet === SUBNET.defaultA || subnet === SUBNET.defaultB ? VPC_DEFAULT : VPC_SHOP)

// --------------------------------------------------------------- seed ------

function td(family: string, revision: number, image: string, o: Partial<EcsTaskDefinition>, age: number, active = true): EcsTaskDefinition {
  return {
    family,
    revision,
    arn: arn("ecs", `task-definition/${family}:${revision}`),
    image,
    cpu: 0.25,
    memory_mb: 512,
    environment: null,
    status: active ? "ACTIVE" : "INACTIVE",
    created_at: ago(age),
    ...o,
  }
}

function seedTaskDefs(): EcsTaskDefinition[] {
  const img = (n: string, t: string) => `${ECR_REGISTRY}/${n}:${t}`
  const workerEnv = { QUEUE_URL: `https://sqs.${REGION}.amazonaws.com/${ACCOUNT}/${NAMES.ordersQueue}`, LOG_LEVEL: "info", DB_HOST: "shop-db.c9demoexample.us-east-1.rds.amazonaws.com" }
  const secrets = [{ name: "DB_PASSWORD", value_from: "shop/db/password" }]
  return [
    td("shop-worker", 1, img("shop/worker", "v1.6.0"), { environment: workerEnv }, 110 * DAY, false),
    td("shop-worker", 2, img("shop/worker", "v1.7.1"), { environment: workerEnv }, 90 * DAY, false),
    td("shop-worker", 3, img("shop/worker", "v1.8.0"), { environment: workerEnv, secrets }, 63 * DAY, false),
    td("shop-worker", 4, img("shop/worker", "v1.9.2"), { environment: workerEnv, secrets, memory_mb: 1024 }, 19 * DAY),
    td("shop-worker", 5, img("shop/worker", "v1.9.3"), { environment: workerEnv, secrets, memory_mb: 1024, cpu: 0.5 }, 5 * DAY),
    td("shop-api", 1, img("shop/api", "v3.1.0"), { container_port: 8080, environment: { PORT: "8080", CACHE_URL: "redis://shop-cache:6379" } }, 80 * DAY, false),
    td("shop-api", 2, img("shop/api", "v3.2.0"), { container_port: 8080, cpu: 0.5, memory_mb: 1024, environment: { PORT: "8080", CACHE_URL: "redis://shop-cache:6379" }, secrets }, 25 * DAY, false),
    td("shop-api", 3, img("shop/api", "v3.2.1"), { container_port: 8080, cpu: 0.5, memory_mb: 1024, environment: { PORT: "8080", CACHE_URL: "redis://shop-cache:6379" }, secrets }, 8 * DAY),
    td("shop-web", 1, img("shop/web", "v2.13.2"), { container_port: 3000, cpu: 1, memory_mb: 2048, environment: { NODE_ENV: "production", API_URL: "http://shop-api.ecs.internal:8080" } }, 9 * DAY, false),
    td("shop-web", 2, img("shop/web", "v2.14.0"), { container_port: 3000, cpu: 1, memory_mb: 2048, environment: { NODE_ENV: "production", API_URL: "http://shop-api.ecs.internal:8080" } }, 2 * DAY),
    td("shop-migrate", 1, img("shop/api", "v3.2.1"), { command: ["node", "scripts/migrate.js"], cpu: 0.25, memory_mb: 512, environment: { DB_HOST: "shop-db.c9demoexample.us-east-1.rds.amazonaws.com" }, secrets }, 30 * DAY),
    td("shop-batch", 1, img("shop/batch", "latest"), { command: ["python", "-m", "batch.reconcile"], entrypoint: ["/usr/bin/tini", "--"], cpu: 2, memory_mb: 4096, environment: { REPORT_BUCKET: NAMES.bucketBackups } }, 60 * DAY),
  ]
}

const mkTask = (o: Partial<EcsTask> & { id: string; task_definition: string }, age: number, subnet = SUBNET.privateA): EcsTask => ({
  arn: taskArn(o.id),
  container_id: stableId("", `c-${o.id}`, 64),
  vpc_id: vpcFor(subnet),
  subnet_id: subnet,
  private_ip: ipFor(subnet, o.id),
  last_status: "RUNNING",
  desired_status: "RUNNING",
  created_at: ago(age),
  started_at: ago(age - 6000),
  stopped_at: null,
  ...o,
})

function seed(): EcsState {
  const taskDefs = seedTaskDefs()
  const tasks: EcsTask[] = []
  const svcTasks = (svc: string, tdk: string, n: number, age: number, subnets = [SUBNET.privateA, SUBNET.privateB]) => {
    for (let i = 0; i < n; i++) tasks.push(mkTask({ id: taskId(`${svc}-${i}`), service: svc, task_definition: tdk }, age - i * 13 * MIN, subnets[i % subnets.length]))
  }
  svcTasks(NAMES.ecsWorker, "shop-worker:5", 2, 5 * DAY)
  svcTasks("shop-api", "shop-api:3", 3, 8 * DAY)
  svcTasks("shop-web", "shop-web:2", 2, 2 * DAY)
  // stopped tasks kept as history
  tasks.push(
    mkTask({ id: taskId("worker-old-1"), service: NAMES.ecsWorker, task_definition: "shop-worker:4", last_status: "STOPPED", desired_status: "STOPPED", stop_reason: "Scaling activity initiated by deployment", exit_code: 143, stopped_at: ago(5 * DAY - 2 * MIN) }, 19 * DAY),
    mkTask({ id: taskId("api-old-1"), service: "shop-api", task_definition: "shop-api:2", last_status: "STOPPED", desired_status: "STOPPED", stop_reason: "Scaling activity initiated by deployment", exit_code: 143, stopped_at: ago(8 * DAY - MIN) }, 25 * DAY),
    mkTask({ id: taskId("migrate-ok"), task_definition: "shop-migrate:1", last_status: "STOPPED", desired_status: "STOPPED", stop_reason: "Essential container in task exited", exit_code: 0, stopped_at: ago(9 * DAY - 40_000) }, 9 * DAY),
    mkTask({ id: taskId("batch-fail"), task_definition: "shop-batch:1", last_status: "STOPPED", desired_status: "STOPPED", stop_reason: "Essential container in task exited", exit_code: 1, stopped_at: ago(3 * DAY - 11 * MIN) }, 3 * DAY, SUBNET.privateB),
    mkTask({ id: taskId("batch-run"), task_definition: "shop-batch:1", public_ports: { "9100/tcp": 32771 } }, 40 * MIN, SUBNET.privateB),
  )
  const ev = (msg: string, age: number): EcsServiceEvent => ({ time: ago(age), message: msg })
  const steady = (n: string, age: number) => ev(`(service ${n}) has reached a steady state.`, age)
  const services: EcsState["services"] = [
    {
      name: NAMES.ecsWorker,
      arn: arn("ecs", `service/${CLUSTER}/${NAMES.ecsWorker}`),
      task_definition: "shop-worker:5",
      desired_count: 2,
      subnet_id: SUBNET.privateA,
      security_groups: [SG.web],
      load_balancer: null,
      status: "ACTIVE",
      created_at: ago(110 * DAY),
      tags: { app: "shop", team: "orders" },
      events: [steady(NAMES.ecsWorker, 5 * DAY - 4 * MIN), ev(`(service ${NAMES.ecsWorker}) has started 2 tasks: (task ${taskId("shop-worker-0").slice(0, 12)}) (task ${taskId("shop-worker-1").slice(0, 12)}).`, 5 * DAY - 6 * MIN), ev(`(service ${NAMES.ecsWorker}) has begun draining connections on 2 tasks.`, 5 * DAY - 2 * MIN)],
    },
    {
      name: "shop-api",
      arn: arn("ecs", `service/${CLUSTER}/shop-api`),
      task_definition: "shop-api:3",
      desired_count: 3,
      subnet_id: SUBNET.privateA,
      security_groups: [SG.web],
      load_balancer: { target_group: NAMES.tgWeb, container_port: 8080 },
      status: "ACTIVE",
      created_at: ago(95 * DAY),
      tags: { app: "shop", team: "web" },
      events: [steady("shop-api", 8 * DAY - 5 * MIN), ev("(service shop-api) registered 3 targets in (target-group shop-web-tg)", 8 * DAY - 7 * MIN)],
    },
    {
      name: "shop-web",
      arn: arn("ecs", `service/${CLUSTER}/shop-web`),
      task_definition: "shop-web:2",
      desired_count: 2,
      subnet_id: SUBNET.privateB,
      security_groups: [SG.web],
      load_balancer: { target_group: NAMES.tgWeb, container_port: 3000 },
      status: "ACTIVE",
      created_at: ago(70 * DAY),
      tags: { app: "shop", team: "web" },
      events: [steady("shop-web", 2 * DAY - 3 * MIN), ev("(service shop-web) registered 2 targets in (target-group shop-web-tg)", 2 * DAY - 5 * MIN)],
    },
    {
      name: "shop-cron",
      arn: arn("ecs", `service/${CLUSTER}/shop-cron`),
      task_definition: "shop-batch:1",
      desired_count: 0,
      subnet_id: SUBNET.privateB,
      security_groups: [SG.web],
      load_balancer: null,
      status: "ACTIVE",
      created_at: ago(45 * DAY),
      tags: { app: "shop" },
      events: [steady("shop-cron", 12 * DAY)],
    },
  ]
  return { taskDefs, services, tasks }
}

// ------------------------------------------------------------- helpers -----

function svcView(s: EcsState["services"][number], full: boolean): EcsService {
  const tasks = S().tasks.filter((t) => t.service === s.name)
  const { events, ...rest } = s
  const out: EcsService = {
    ...clone(rest),
    running_count: tasks.filter((t) => t.last_status === "RUNNING").length,
    pending_count: tasks.filter((t) => t.last_status === "PROVISIONING").length,
    endpoint: `${s.name}.ecs.internal`,
  }
  if (full) {
    out.tasks = clone(tasks).sort((a, b) => b.created_at.localeCompare(a.created_at))
    out.events = clone(events)
  }
  return out
}

function lookupTD(key: string): EcsTaskDefinition {
  const all = S().taskDefs
  if (key.includes(":")) {
    const t = all.find((x) => tdKey(x) === key)
    if (!t) throw notFound("task definition", key)
    return t
  }
  const best = all.filter((x) => x.family === key && x.status === "ACTIVE").sort((a, b) => b.revision - a.revision)[0]
  if (!best) throw notFound("task definition", key)
  return best
}

function svc(name: string) {
  const s = S().services.find((x) => x.name === name && x.status !== "INACTIVE")
  if (!s) throw notFound("service", name)
  return s
}

function addEvent(s: EcsState["services"][number], message: string) {
  s.events.unshift({ time: nowIso(), message })
  s.events.splice(20)
}

function launch(tdk: string, o: { service?: string; subnet?: string; command?: string[]; env?: Record<string, string> }): EcsTask {
  const id = uuid().replace(/-/g, "")
  const subnet = o.subnet || SUBNET.privateA
  const def = lookupTD(tdk)
  const t: EcsTask = {
    id,
    arn: taskArn(id),
    service: o.service,
    task_definition: tdk,
    container_id: stableId("", `c-${id}`, 64),
    vpc_id: vpcFor(subnet),
    subnet_id: subnet,
    private_ip: ipFor(subnet, id),
    last_status: "PROVISIONING",
    desired_status: "RUNNING",
    created_at: nowIso(),
    started_at: null,
    stopped_at: null,
    ...(!o.service && def.container_port ? { public_ports: { [`${def.container_port}/tcp`]: 32768 + Math.floor(Math.random() * 2000) } } : {}),
  }
  S().tasks.push(t)
  later(2500, () => {
    const x = S().tasks.find((y) => y.id === id)
    if (x && x.last_status === "PROVISIONING") {
      x.last_status = "RUNNING"
      x.started_at = nowIso()
    }
  })
  return t
}

function stopTask(t: EcsTask, reason: string) {
  if (t.last_status === "STOPPED") return
  t.last_status = "STOPPED"
  t.desired_status = "STOPPED"
  t.stop_reason = reason
  t.exit_code = 143
  t.stopped_at = nowIso()
  t.public_ports = null
}

/** reconcile brings a service's running tasks in line with desired_count and task_definition. */
function reconcile(s: EcsState["services"][number], rollout: boolean) {
  const st = S()
  const live = () => st.tasks.filter((t) => t.service === s.name && t.last_status !== "STOPPED")
  if (rollout) {
    const olds = live()
    const n = Math.max(s.desired_count, 0)
    const fresh: EcsTask[] = []
    for (let i = 0; i < n; i++) fresh.push(launch(s.task_definition, { service: s.name, subnet: i % 2 ? SUBNET.privateB : s.subnet_id }))
    for (const o of olds) o.task_definition = `${o.task_definition} (redeploy)`
    addEvent(s, `(service ${s.name}) has started ${n} tasks.`)
    later(4500, () => {
      for (const o of st.tasks.filter((t) => t.service === s.name && t.task_definition.endsWith("(redeploy)"))) stopTask(o, "Scaling activity initiated by deployment")
      const x = st.services.find((y) => y.name === s.name)
      if (x) addEvent(x, `(service ${s.name}) has reached a steady state.`)
    })
    void fresh
    return
  }
  const cur = live()
  if (cur.length < s.desired_count) {
    const k = s.desired_count - cur.length
    for (let i = 0; i < k; i++) launch(s.task_definition, { service: s.name, subnet: (cur.length + i) % 2 ? SUBNET.privateB : s.subnet_id })
    addEvent(s, `(service ${s.name}) has started ${k} task${k > 1 ? "s" : ""}.`)
    later(3000, () => {
      const x = st.services.find((y) => y.name === s.name)
      if (x) addEvent(x, `(service ${s.name}) has reached a steady state.`)
    })
  } else if (cur.length > s.desired_count) {
    const extra = [...cur].sort((a, b) => b.created_at.localeCompare(a.created_at)).slice(0, cur.length - s.desired_count)
    for (const t of extra) stopTask(t, "Scaling activity initiated by service")
    addEvent(s, `(service ${s.name}) has stopped ${extra.length} running task${extra.length > 1 ? "s" : ""}.`)
  }
}

// ------------------------------------------------------------- logs --------

function logLines(t: EcsTask, tail: number): string {
  const def = S().taskDefs.find((d) => tdKey(d) === t.task_definition.replace(" (redeploy)", ""))
  const family = def?.family ?? t.task_definition.split(":")[0]
  const start = new Date(t.started_at ?? t.created_at).getTime()
  const end = t.last_status === "STOPPED" && t.stopped_at ? new Date(t.stopped_at).getTime() : Date.now()
  const r = rng(t.id)
  const msgs: Record<string, string[]> = {
    "shop-worker": ["polled queue shop-orders: 10 messages", "processed order ord_%N% in %M%ms", "acknowledged batch of 10 messages", "db pool: 4 active, 6 idle connections", "queue depth 0, sleeping 2s"],
    "shop-api": ['GET /products 200 %M%ms', 'GET /products/p_101 200 %M%ms', 'POST /orders 201 %M%ms', "cache hit ratio 0.93", 'GET /healthz 200 1ms'],
    "shop-web": ['GET / 200 %M%ms', 'GET /products?page=2 200 %M%ms', 'GET /_next/static/chunks/main.js 200 2ms', 'GET /cart 200 %M%ms', 'GET /healthz 200 1ms'],
    "shop-migrate": ["applying migration 0042_add_order_notes", "applying migration 0043_index_orders_created_at", "migrations complete: 2 applied, 41 skipped"],
    "shop-batch": ["reconcile: loaded 4213 orders", "reconcile: 12 mismatches found", "uploading report to s3", "done"],
  }
  const pool = msgs[family] ?? ["started"]
  const boot = [`server starting (${family}, pid 1)`, `listening on ${def?.container_port ? `:${def.container_port}` : "worker mode"}`]
  const lines: string[] = []
  const iso = (ms: number) => `${new Date(ms).toISOString().slice(0, -1)}${String(Math.floor(r() * 1000)).padStart(3, "0")}Z`
  boot.forEach((m, i) => lines.push(`${iso(start + i * 400)} ${m}`))
  const span = Math.max(end - start - 1200, 0)
  const count = Math.min(tail, 400)
  for (let i = 0; i < count; i++) {
    const ts = start + 1200 + Math.floor((span * (i + 1)) / (count + 1))
    const m = pool[Math.floor(r() * pool.length)].replace("%N%", String(10000 + Math.floor(r() * 900))).replace("%M%", String(3 + Math.floor(r() * 90)))
    lines.push(`${iso(ts)} ${m}`)
  }
  if (t.last_status === "STOPPED" && t.exit_code === 1) lines.push(`${iso(end)} ERROR reconcile failed: connection to warehouse timed out after 30s`)
  else if (t.last_status === "STOPPED") lines.push(`${iso(end)} received SIGTERM, shutting down gracefully`)
  return lines.slice(-tail).join("\n") + "\n"
}

// ------------------------------------------------------------ routes -------

const FAMILY_RE = /^[a-zA-Z0-9_-]{1,255}$/

function routes(r: Router) {
  // task definitions
  r.get("/api/v1/ecs/task-definitions", ({ query }) =>
    S()
      .taskDefs.filter((t) => (!query.family || t.family === query.family) && (query.status === "all" || t.status === "ACTIVE"))
      .sort((a, b) => a.family.localeCompare(b.family) || b.revision - a.revision),
  )
  r.post("/api/v1/ecs/task-definitions", ({ body: b }) => {
    b = b ?? {}
    if (!FAMILY_RE.test(String(b.family ?? ""))) throw badRequest("family must be 1-255 letters, digits, hyphens or underscores")
    if (!String(b.image ?? "").trim()) throw badRequest("image is required")
    const cpu = b.cpu ?? 0.25
    const mem = b.memory_mb ?? 512
    if (cpu < 0.125 || cpu > 16 || mem < 64 || mem > 122880) throw badRequest("cpu must be 0.125-16 vCPU and memory_mb 64-122880")
    const rev = Math.max(0, ...S().taskDefs.filter((t) => t.family === b.family).map((t) => t.revision)) + 1
    const t: EcsTaskDefinition = {
      family: b.family,
      revision: rev,
      arn: arn("ecs", `task-definition/${b.family}:${rev}`),
      image: b.image.trim(),
      command: b.command ?? null,
      entrypoint: b.entrypoint ?? null,
      cpu,
      memory_mb: mem,
      container_port: b.container_port,
      environment: b.environment && Object.keys(b.environment).length ? b.environment : null,
      secrets: b.secrets ?? null,
      status: "ACTIVE",
      created_at: nowIso(),
    }
    S().taskDefs.push(t)
    return t
  })
  r.get("/api/v1/ecs/task-definitions/:key", ({ params }) => lookupTD(params.key))
  r.del("/api/v1/ecs/task-definitions/:key", ({ params }) => {
    const t = lookupTD(params.key)
    t.status = "INACTIVE"
    return t
  })

  // services
  r.get("/api/v1/ecs/services", () =>
    S()
      .services.filter((s) => s.status !== "INACTIVE")
      .sort((a, b) => a.name.localeCompare(b.name))
      .map((s) => svcView(s, false)),
  )
  r.post("/api/v1/ecs/services", ({ body: b }) => {
    b = b ?? {}
    const name = String(b.name ?? "")
    if (!FAMILY_RE.test(name)) throw badRequest("service names are 1-255 letters, digits, hyphens or underscores")
    if (S().services.some((s) => s.name === name && s.status !== "INACTIVE")) throw conflict(`service "${name}" already exists`)
    const def = lookupTD(b.task_definition ?? "")
    if (def.status !== "ACTIVE") throw badRequest(`task definition ${b.task_definition} is inactive`)
    const n = b.desired_count ?? 1
    if (!Number.isInteger(n) || n < 0 || n > 50) throw badRequest("desired_count must be 0-50")
    if (b.load_balancer && !b.load_balancer.container_port && !def.container_port) throw badRequest("load_balancer.container_port is required")
    const s: EcsState["services"][number] = {
      name,
      arn: arn("ecs", `service/${CLUSTER}/${name}`),
      task_definition: tdKey(def),
      desired_count: n,
      subnet_id: b.subnet_id || SUBNET.privateA,
      security_groups: b.security_groups?.length ? b.security_groups : [SG.web],
      load_balancer: b.load_balancer ? { target_group: b.load_balancer.target_group, container_port: b.load_balancer.container_port ?? def.container_port ?? 0 } : null,
      status: "ACTIVE",
      created_at: nowIso(),
      tags: b.tags ?? {},
      events: [],
    }
    S().services.push(s)
    addEvent(s, `(service ${name}) has started ${n} tasks.`)
    reconcile(s, false)
    return svcView(s, true)
  })
  r.get("/api/v1/ecs/services/:name", ({ params }) => svcView(svc(params.name), true))
  r.patch("/api/v1/ecs/services/:name", ({ params, body: b }) => {
    const s = svc(params.name)
    b = b ?? {}
    if (s.status !== "ACTIVE") throw conflict(`service ${s.name} is ${s.status}`)
    let rollout = !!b.force_new_deployment
    if (b.task_definition) {
      const def = lookupTD(b.task_definition)
      if (def.status !== "ACTIVE") throw badRequest(`task definition ${b.task_definition} is inactive`)
      if (tdKey(def) !== s.task_definition) rollout = true
      s.task_definition = tdKey(def)
    }
    if (b.desired_count !== undefined) {
      if (!Number.isInteger(b.desired_count) || b.desired_count < 0 || b.desired_count > 50) throw badRequest("desired_count must be 0-50")
      s.desired_count = b.desired_count
    }
    reconcile(s, rollout && s.desired_count > 0)
    return svcView(s, true)
  })
  r.del("/api/v1/ecs/services/:name", ({ params }) => {
    const s = svc(params.name)
    s.status = "DRAINING"
    s.desired_count = 0
    for (const t of S().tasks.filter((x) => x.service === s.name)) stopTask(t, "Service deleted")
    later(2500, () => {
      S().services = S().services.filter((x) => x.name !== s.name)
      S().tasks = S().tasks.filter((x) => x.service !== s.name)
    })
    return svcView(s, false)
  })

  // tasks
  r.get("/api/v1/ecs/tasks", ({ query }) =>
    S()
      .tasks.filter((t) => !query.service || t.service === query.service)
      .sort((a, b) => b.created_at.localeCompare(a.created_at)),
  )
  r.post("/api/v1/ecs/tasks", ({ body: b }) => {
    b = b ?? {}
    const def = lookupTD(b.task_definition ?? "")
    if (def.status !== "ACTIVE") throw badRequest(`task definition ${b.task_definition} is inactive`)
    return launch(tdKey(def), { subnet: b.subnet_id, command: b.command, env: b.environment })
  })
  r.get("/api/v1/ecs/tasks/:id", ({ params }) => {
    const t = S().tasks.find((x) => x.id === params.id)
    if (!t) throw notFound("task", params.id)
    return t
  })
  r.post("/api/v1/ecs/tasks/:id/stop", ({ params }) => {
    const t = S().tasks.find((x) => x.id === params.id)
    if (!t) throw notFound("task", params.id)
    stopTask(t, "Task stopped by user")
    const s = t.service ? S().services.find((x) => x.name === t.service) : undefined
    if (s && s.status === "ACTIVE") later(1500, () => reconcile(s, false))
    return t
  })
  r.get("/api/v1/ecs/tasks/:id/logs", ({ params, query }) => {
    const t = S().tasks.find((x) => x.id === params.id)
    if (!t) throw notFound("task", params.id)
    if (t.last_status === "PROVISIONING") return { output: "" }
    return { output: logLines(t, Math.max(1, Math.min(Number(query.tail) || 500, 5000))) }
  })
}

function onLoad(s: State) {
  const st = s.ecs as EcsState
  for (const t of st.tasks) {
    if (t.last_status === "PROVISIONING") {
      t.last_status = "RUNNING"
      t.started_at = nowIso()
    }
    if (t.task_definition.endsWith("(redeploy)")) stopTask(t, "Scaling activity initiated by deployment")
  }
  st.services = st.services.filter((x) => x.status !== "DRAINING")
}

const service: DemoService = { name: "ecs", seed, routes, onLoad }

export default service

