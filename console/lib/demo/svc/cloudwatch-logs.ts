// CloudWatch Logs for the demo: log groups, generated (but stable) log events
// per group, user-appended events, metric and subscription filters.

import type { LogEvent, LogGroup, LogStream, MetricFilter, SubscriptionFilter } from "@/lib/types"
import { badRequest, conflict, notFound, type Router } from "../engine"
import { INSTANCE, NAMES } from "../ids"
import { DAY, HOUR, MIN, ago, arn, nowIso, rng } from "../util"

export interface LogsState {
  groups: LogGroup[]
  /** events written through PutLogEvents (or seeded by hand), per group */
  put: Record<string, LogEvent[]>
  metricFilters: Record<string, MetricFilter[]>
  subFilters: Record<string, SubscriptionFilter[]>
}

type R = () => number
const pick = <T,>(r: R, xs: T[]): T => xs[Math.floor(r() * xs.length)]
const hx = (r: R, n: number) => {
  let s = ""
  for (let i = 0; i < n; i++) s += "0123456789abcdef"[Math.floor(r() * 16)]
  return s
}
const uuid = (r: R) => `${hx(r, 8)}-${hx(r, 4)}-4${hx(r, 3)}-a${hx(r, 3)}-${hx(r, 12)}`
const num = (r: R, lo: number, hi: number) => Math.round(lo + r() * (hi - lo))
const pad = (n: number) => String(n).padStart(2, "0")
const MON = ["Jan", "Feb", "Mar", "Apr", "May", "Jun", "Jul", "Aug", "Sep", "Oct", "Nov", "Dec"]
const nginxTime = (t: number) => {
  const d = new Date(t)
  return `${pad(d.getUTCDate())}/${MON[d.getUTCMonth()]}/${d.getUTCFullYear()}:${pad(d.getUTCHours())}:${pad(d.getUTCMinutes())}:${pad(d.getUTCSeconds())} +0000`
}
const pgTime = (t: number) => new Date(t).toISOString().replace("T", " ").slice(0, 19) + " UTC"
const dayPath = (t: number) => {
  const d = new Date(t)
  return `${d.getUTCFullYear()}/${pad(d.getUTCMonth() + 1)}/${pad(d.getUTCDate())}`
}

interface Line {
  dt: number
  msg: string
}
interface Prof {
  every: number
  p: number
  stream: (r: R, t: number) => string
  gen: (r: R, t: number) => Line[]
  streams: (now: number) => { name: string; last: number }[]
}

const json = (o: unknown) => JSON.stringify(o)

// ---- profiles ----

function lambdaProf(fn: string, p: number, errRate: number, ok: (r: R) => object, bad: (r: R) => object, mem = 256): Prof {
  const block = (t: number) => Math.floor(t / HOUR)
  const streamName = (b: number) => `${dayPath(b * HOUR)}/[$LATEST]${hx(rng(`${fn}:${b}`), 32)}`
  return {
    every: 30_000,
    p,
    stream: (_r, t) => streamName(block(t)),
    gen: (r, t) => {
      const id = uuid(r)
      const dur = num(r, 40, 420) + r()
      const failed = r() < errRate
      const timeout = failed && r() < 0.15
      const lines: Line[] = [{ dt: 0, msg: `START RequestId: ${id} Version: $LATEST` }]
      if (failed) {
        lines.push({ dt: 3, msg: json({ level: "error", ...bad(r) }) })
        if (timeout) lines.push({ dt: 9, msg: `${new Date(t + 9).toISOString()} ${id} Task timed out after 10.01 seconds` })
      } else {
        lines.push({ dt: 2, msg: json({ level: "info", ...ok(r), duration_ms: Math.round(dur) }) })
      }
      lines.push({ dt: 6 + Math.round(dur) % 50, msg: `END RequestId: ${id}` })
      lines.push({
        dt: 7 + Math.round(dur) % 50,
        msg: `REPORT RequestId: ${id}\tDuration: ${dur.toFixed(2)} ms\tBilled Duration: ${Math.ceil(dur)} ms\tMemory Size: ${mem} MB\tMax Memory Used: ${num(r, 62, 140)} MB${failed ? "\tStatus: error" : ""}`,
      })
      return lines
    },
    streams: (now) => {
      const b0 = block(now)
      return Array.from({ length: 6 }, (_, i) => ({ name: streamName(b0 - i), last: Math.min(now - 20_000 - i * 3000, (b0 - i + 1) * HOUR - 1000) }))
    },
  }
}

const paymentsOk = (r: R) => ({ msg: pick(r, ["payment authorized", "charge captured", "refund issued"]), order_id: `ord_${hx(r, 10)}`, amount: num(r, 8, 260) + 0.99, currency: "USD", provider: "stripe-sandbox" })
const paymentsBad = (r: R) => ({ msg: pick(r, ["card declined", "gateway timeout", "3ds challenge failed"]), order_id: `ord_${hx(r, 10)}`, code: pick(r, ["card_declined", "upstream_timeout", "authentication_required"]), latency_ms: num(r, 800, 9000) })

const nginxAccess = (streams: string[]): Prof => ({
  every: 4000,
  p: 0.85,
  stream: (r) => pick(r, streams),
  gen: (r, t) => {
    const path = pick(r, ["/", "/products", `/products/${num(r, 1, 480)}`, "/cart", "/checkout", "/api/orders", "/static/app.3fa1c2.js", "/static/site.css", "/healthz", `/search?q=${pick(r, ["shoes", "hat", "mug", "poster"])}`])
    const status = r() < 0.04 ? pick(r, [404, 404, 500, 502]) : r() < 0.06 ? 304 : 200
    const ua = pick(r, ["Mozilla/5.0 (Macintosh; Intel Mac OS X 14_5) AppleWebKit/605.1.15 Safari/605.1.15", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) Chrome/126.0 Safari/537.36", "Mozilla/5.0 (iPhone; CPU iPhone OS 17_5 like Mac OS X) Mobile/15E148", "ELB-HealthChecker/2.0"])
    const ip = path === "/healthz" ? `10.20.1.${num(r, 10, 40)}` : `${pick(r, [203, 198, 192, 185, 91])}.${num(r, 1, 254)}.${num(r, 1, 254)}.${num(r, 1, 254)}`
    const method = path === "/api/orders" && r() < 0.5 ? "POST" : "GET"
    return [{ dt: num(r, 0, 3500), msg: `${ip} - - [${nginxTime(t)}] "${method} ${path} HTTP/1.1" ${status} ${num(r, 180, 48000)} "-" "${ua}"` }]
  },
  streams: (now) => streams.map((s, i) => ({ name: s, last: now - 4000 * (i + 1) })),
})

const nginxError = (streams: string[]): Prof => ({
  every: 90_000,
  p: 0.35,
  stream: (r) => pick(r, streams),
  gen: (r, t) => {
    const d = new Date(t)
    const ts = `${d.getUTCFullYear()}/${pad(d.getUTCMonth() + 1)}/${pad(d.getUTCDate())} ${pad(d.getUTCHours())}:${pad(d.getUTCMinutes())}:${pad(d.getUTCSeconds())}`
    const m = pick(r, [
      `upstream timed out (110: Connection timed out) while reading response header from upstream, client: 203.0.113.${num(r, 2, 250)}, server: shop.example.com, request: "GET /api/orders HTTP/1.1", upstream: "http://127.0.0.1:8080/api/orders"`,
      `open() "/usr/share/nginx/html/favicon.ico" failed (2: No such file or directory), client: 198.51.100.${num(r, 2, 250)}, server: shop.example.com, request: "GET /favicon.ico HTTP/1.1"`,
      `connect() failed (111: Connection refused) while connecting to upstream, client: 192.0.2.${num(r, 2, 250)}, server: shop.example.com, request: "POST /api/checkout HTTP/1.1"`,
      `*${num(r, 1000, 99999)} client intended to send too large body: 12582913 bytes`,
    ])
    return [{ dt: num(r, 0, 80_000), msg: `${ts} [${m.startsWith("open()") ? "error" : "error"}] ${num(r, 10, 900)}#${num(r, 10, 900)}: ${m}` }]
  },
  streams: (now) => streams.map((s, i) => ({ name: s, last: now - 60_000 * (i + 2) })),
})

const ecsWorker: Prof = {
  every: 15_000,
  p: 0.8,
  stream: (r) => pick(r, ["worker/shop-worker/9d1c04ab6e7f4c1c8a3e", "worker/shop-worker/4be07f3d21a94e56b0c8"]),
  gen: (r, t) => {
    const oid = `ord_${hx(r, 10)}`
    const lines: Line[] = [{ dt: 0, msg: json({ level: "info", msg: "received message from queue", queue: NAMES.ordersQueue, order_id: oid }) }]
    if (r() < 0.05) lines.push({ dt: 400, msg: json({ level: "warn", msg: "inventory service slow, retrying", order_id: oid, attempt: 2, latency_ms: num(r, 1500, 4000) }) })
    if (r() < 0.02) lines.push({ dt: 900, msg: json({ level: "error", msg: "failed to reserve stock", order_id: oid, sku: `SKU-${num(r, 1000, 9999)}`, error: "insufficient inventory" }) })
    else lines.push({ dt: 300, msg: json({ level: "info", msg: "order processed", order_id: oid, items: num(r, 1, 6), duration_ms: num(r, 60, 700) }) })
    return lines
  },
  streams: (now) => [
    { name: "worker/shop-worker/9d1c04ab6e7f4c1c8a3e", last: now - 9000 },
    { name: "worker/shop-worker/4be07f3d21a94e56b0c8", last: now - 15_000 },
  ],
}

const pgProf = (streamName: string, container = false): Prof => ({
  every: 20_000,
  p: 0.7,
  stream: () => streamName,
  gen: (r, t) => {
    const pid = num(r, 1200, 9800)
    const q = pick(r, [
      "SELECT o.id, o.total FROM orders o WHERE o.customer_id = $1 ORDER BY o.created_at DESC LIMIT 20",
      "UPDATE inventory SET stock = stock - $1 WHERE sku = $2",
      "INSERT INTO orders (customer_id, total, status) VALUES ($1, $2, 'pending')",
      "SELECT count(*) FROM order_items WHERE order_id = $1",
    ])
    const lines: Line[] = []
    if (r() < 0.35) lines.push({ dt: 100, msg: `${pgTime(t)}:10.20.${num(r, 10, 20)}.${num(r, 4, 200)}(${num(r, 30000, 60000)}):shop@shop:[${pid}]:LOG:  duration: ${(r() * 900 + 100).toFixed(3)} ms  statement: ${q}` })
    else lines.push({ dt: 100, msg: `${pgTime(t)} :::[${pid}]:LOG:  connection authorized: user=shop database=shop SSL enabled (protocol=TLSv1.3)` })
    if (r() < 0.03) lines.push({ dt: 3000, msg: `${pgTime(t)}:[${pid}]:LOG:  checkpoint starting: time` })
    if (r() < 0.01) lines.push({ dt: 5000, msg: `${pgTime(t)}:10.20.11.7(51422):shop@shop:[${pid}]:ERROR:  duplicate key value violates unique constraint "orders_pkey"` })
    return container ? lines.map((l) => ({ ...l, msg: l.msg.replace(/^[^:]+:[^:]+:\d\d UTC:?/, "") })) : lines
  },
  streams: (now) => [{ name: streamName, last: now - 20_000 }],
})

const apiAccess: Prof = {
  every: 6000,
  p: 0.75,
  stream: () => "prod-access",
  gen: (r, t) => {
    const route = pick(r, ["GET /orders", "GET /orders/{id}", "POST /orders", "GET /products", "GET /products/{id}", "POST /payments/webhook"])
    const [method, path] = route.split(" ")
    const status = r() < 0.03 ? pick(r, [500, 502, 504]) : r() < 0.05 ? pick(r, [401, 403, 404, 429]) : method === "POST" ? 201 : 200
    return [
      {
        dt: num(r, 0, 5500),
        msg: json({ requestId: uuid(r), ip: `203.0.113.${num(r, 2, 250)}`, requestTime: new Date(t).toISOString(), httpMethod: method, path, status, responseLatency: num(r, 12, status >= 500 ? 3000 : 260), integrationLatency: num(r, 8, 200), userAgent: "shop-mobile/4.2.1" }),
      },
    ]
  },
  streams: (now) => [{ name: "prod-access", last: now - 6000 }],
}

const sfnProf: Prof = {
  every: 120_000,
  p: 0.6,
  stream: () => "states/executions",
  gen: (r, t) => {
    const id = `${hx(r, 8)}-${hx(r, 4)}-${hx(r, 4)}`
    const arnx = arn("states", `execution:${NAMES.stateMachine}:${id}`)
    const bad = r() < 0.06
    const steps = ["ValidateOrder", "ReserveInventory", "ChargePayment", "CreateShipment", "NotifyCustomer"]
    const ev = (dt: number, type: string, extra: object = {}) => ({ dt, msg: json({ id: `${arnx}`, type, timestamp: new Date(t + dt).toISOString(), ...extra }) })
    const out: Line[] = [ev(0, "ExecutionStarted", { input: { order_id: `ord_${hx(r, 10)}` } })]
    steps.forEach((s, i) => {
      if (bad && i === 2) {
        out.push(ev(1000 * (i + 1), "TaskFailed", { resource: s, error: "PaymentDeclined", cause: "card declined" }))
      } else if (!bad || i < 2) {
        out.push(ev(1000 * (i + 1), "TaskStateExited", { name: s }))
      }
    })
    out.push(ev(6000, bad ? "ExecutionFailed" : "ExecutionSucceeded"))
    return out
  },
  streams: (now) => [{ name: "states/executions", last: now - 120_000 }],
}

const auditProf: Prof = {
  every: 5 * MIN,
  p: 0.3,
  stream: () => "audit",
  gen: (r) => [
    {
      dt: num(r, 0, 250_000),
      msg: json({ actor: pick(r, ["alice", "bob", "ci-deploy"]), event: pick(r, ["config.changed", "feature_flag.toggled", "price.updated", "user.role_changed"]), target: pick(r, ["checkout", "catalog", "promo-banner", "inventory"]) }),
    },
  ],
  streams: (now) => [{ name: "audit", last: now - 6 * MIN }],
}

const syslogProf = (id: string): Prof => ({
  every: 60_000,
  p: 0.5,
  stream: () => "stdout",
  gen: (r, t) => {
    const d = new Date(t)
    const ts = `${MON[d.getUTCMonth()]} ${pad(d.getUTCDate())} ${pad(d.getUTCHours())}:${pad(d.getUTCMinutes())}:${pad(d.getUTCSeconds())}`
    const host = `ip-10-20-${1 + Math.floor(rng(id)() * 19)}-${10 + Math.floor(rng(id + "h")() * 190)}`
    const m = pick(r, [
      "systemd[1]: Started Daily apt download activities.",
      "CRON[2481]: (root) CMD (/usr/local/bin/logrotate-app)",
      "sshd[1177]: Accepted publickey for ubuntu from 10.20.1.15 port 52214 ssh2",
      "systemd[1]: Starting Cleanup of Temporary Directories...",
      "nginx[812]: worker process reload complete",
      "systemd-resolved[402]: Using degraded feature set UDP instead of UDP+EDNS0",
    ])
    return [{ dt: num(r, 0, 55_000), msg: `${ts} ${host} ${m}` }]
  },
  streams: (now) => [{ name: "stdout", last: now - 60_000 }],
})

const redisProf: Prof = {
  every: 90_000,
  p: 0.6,
  stream: () => "stdout",
  gen: (r, t) => {
    const d = new Date(t)
    const ts = `1:M ${pad(d.getUTCDate())} ${MON[d.getUTCMonth()]} ${d.getUTCFullYear()} ${pad(d.getUTCHours())}:${pad(d.getUTCMinutes())}:${pad(d.getUTCSeconds())}.${num(r, 100, 999)}`
    const m = pick(r, ["* 100 changes in 300 seconds. Saving...", "* Background saving started by pid 214", "* DB saved on disk", "* Background saving terminated with success"])
    return [{ dt: num(r, 0, 80_000), msg: `${ts} ${m}` }]
  },
  streams: (now) => [{ name: "stdout", last: now - 90_000 }],
}

const runtimeProf = (fn: string): Prof => ({
  every: 45_000,
  p: 0.6,
  stream: () => "stdout",
  gen: (r) => [{ dt: 0, msg: pick(r, [`[runtime] ${fn}: handler invoked (cold=${r() < 0.1})`, `[runtime] ${fn}: handler returned in ${num(r, 30, 400)} ms`, "[runtime] container healthy"]) }],
  streams: (now) => [{ name: "stdout", last: now - 45_000 }],
})

const noProf: Prof = { every: HOUR, p: 0, stream: () => "", gen: () => [], streams: () => [] }

const WEB_STREAMS = [INSTANCE.web1, INSTANCE.web2]

function profileFor(group: string): Prof {
  switch (group) {
    case `/aws/lambda/${NAMES.paymentsFn}`:
      return lambdaProf(NAMES.paymentsFn, 0.7, 0.09, paymentsOk, paymentsBad)
    case "/aws/lambda/shop-order-notifier":
      return lambdaProf("shop-order-notifier", 0.45, 0.02, (r) => ({ msg: "notification sent", channel: pick(r, ["email", "sms", "push"]), order_id: `ord_${hx(r, 10)}` }), (r) => ({ msg: "smtp relay rejected message", order_id: `ord_${hx(r, 10)}`, code: 550 }), 128)
    case "/aws/lambda/shop-image-resizer":
      return lambdaProf("shop-image-resizer", 0.25, 0.03, (r) => ({ msg: "image resized", key: `uploads/${hx(r, 8)}.jpg`, width: pick(r, [320, 640, 1280]) }), (r) => ({ msg: "unsupported image format", key: `uploads/${hx(r, 8)}.bmp` }), 1024)
    case "/aws/lambda/shop-nightly-cleanup":
      return { ...lambdaProf("shop-nightly-cleanup", 0.004, 0.05, (r) => ({ msg: "expired carts removed", removed: num(r, 20, 400) }), () => ({ msg: "cleanup failed", error: "throttled" }), 256), every: 5 * MIN }
    case "/aws/ecs/shop-cluster/shop-worker":
      return ecsWorker
    case `/aws/rds/instance/${NAMES.db}/postgresql`:
      return pgProf(`${NAMES.db}.postgresql.log`)
    case `/aws/apigateway/${NAMES.api}/prod`:
      return apiAccess
    case `/aws/states/${NAMES.stateMachine}`:
      return sfnProf
    case "/shop/web/nginx-access":
      return nginxAccess(WEB_STREAMS)
    case "/shop/web/nginx-error":
      return nginxError(WEB_STREAMS)
    case "/shop/audit":
      return auditProf
    case `/hc/rds/${NAMES.db}`:
      return pgProf("stdout", true)
    case `/hc/elasticache/${NAMES.cache}`:
      return redisProf
  }
  let m = /^\/hc\/ec2\/(.+)$/.exec(group)
  if (m) return syslogProf(m[1])
  m = /^\/hc\/lambda\/(.+)$/.exec(group)
  if (m) return runtimeProf(m[1])
  m = /^\/aws\/lambda\/(.+)$/.exec(group)
  if (m) return lambdaProf(m[1], 0.3, 0.03, (r) => ({ msg: "invocation ok", request: hx(r, 6) }), (r) => ({ msg: "unhandled exception", request: hx(r, 6) }))
  if (group.startsWith("/hc/")) return syslogProf(group)
  return noProf
}

const contains = (f: string) => {
  const q = f.trim().replace(/^"(.*)"$/, "$1").toLowerCase()
  return q ? (m: string) => m.toLowerCase().includes(q) : undefined
}

/** genEvents returns generated events of a group within [start,end], oldest first, keeping the newest `limit`. */
export function genEvents(group: string, start: number, end: number, o: { stream?: string; match?: (m: string) => boolean; limit: number }): LogEvent[] {
  const p = profileFor(group)
  if (p.p <= 0) return []
  const now = Date.now()
  end = Math.min(end, now)
  start = Math.max(start, now - 30 * DAY)
  const kmin = Math.floor(start / p.every)
  const chunks: LogEvent[][] = []
  let n = 0
  let scanned = 0
  for (let k = Math.floor(end / p.every); k >= kmin && n < o.limit && scanned < 40_000; k--, scanned++) {
    const r = rng(`${group}:${k}`)
    if (r() > p.p) continue
    const t0 = k * p.every
    const stream = p.stream(r, t0)
    if (o.stream && stream !== o.stream) continue
    const evs: LogEvent[] = []
    for (const l of p.gen(r, t0)) {
      const ts = t0 + l.dt
      if (ts < start || ts > end) continue
      if (o.match && !o.match(l.msg)) continue
      evs.push({ timestamp: new Date(ts).toISOString(), message: l.msg, stream })
    }
    if (evs.length) {
      chunks.push(evs)
      n += evs.length
    }
  }
  chunks.reverse()
  return chunks.flat().slice(-o.limit)
}

// ---- seed ----

const grp = (name: string, retention: number, ageDays: number, mb: number, source: LogGroup["source"] = "stored"): LogGroup => ({
  name,
  arn: arn("logs", `log-group:${name}`),
  retention_days: retention,
  created_at: ago(ageDays * DAY),
  source,
  stored_bytes: Math.round(mb * 1024 * 1024),
})

export function seedLogs(): LogsState {
  const groups: LogGroup[] = [
    grp(`/aws/lambda/${NAMES.paymentsFn}`, 30, 88, 18.4),
    grp("/aws/lambda/shop-order-notifier", 14, 88, 6.2),
    grp("/aws/lambda/shop-image-resizer", 14, 61, 2.7),
    grp("/aws/lambda/shop-nightly-cleanup", 7, 47, 0.3),
    grp("/aws/ecs/shop-cluster/shop-worker", 14, 70, 24.9),
    grp(`/aws/rds/instance/${NAMES.db}/postgresql`, 7, 92, 41.6),
    grp(`/aws/apigateway/${NAMES.api}/prod`, 30, 66, 33.1),
    grp(`/aws/states/${NAMES.stateMachine}`, 30, 52, 4.4),
    grp("/shop/web/nginx-access", 14, 92, 212.5),
    grp("/shop/web/nginx-error", 14, 92, 3.8),
    grp("/shop/audit", 0, 80, 0.9),
    grp("/shop/app/scratch", 1, 3, 0.01),
    grp(`/hc/ec2/${INSTANCE.web1}`, 0, 74, 1.1, "container"),
    grp(`/hc/ec2/${INSTANCE.web2}`, 0, 74, 1.0, "container"),
    grp(`/hc/ec2/${INSTANCE.bastion}`, 0, 74, 0.2, "container"),
    grp(`/hc/ec2/${INSTANCE.batch}`, 0, 30, 0.4, "container"),
    grp(`/hc/rds/${NAMES.db}`, 0, 92, 9.7, "container"),
    grp(`/hc/elasticache/${NAMES.cache}`, 0, 92, 0.8, "container"),
    grp(`/hc/lambda/${NAMES.paymentsFn}`, 0, 88, 1.3, "container"),
  ]
  const scratch = `/shop/app/scratch`
  const put: Record<string, LogEvent[]> = {
    [scratch]: [
      { timestamp: ago(2 * HOUR), message: "smoke test: hello from alice", stream: "manual" },
      { timestamp: ago(2 * HOUR - 4000), message: "smoke test: second line", stream: "manual" },
      { timestamp: ago(50 * MIN), message: "ERROR sample exception for the metric filter demo", stream: "manual" },
    ],
  }
  const fnArn = (n: string) => arn("lambda", `function:${n}`)
  const now = Date.now()
  return {
    groups,
    put,
    metricFilters: {
      [`/aws/lambda/${NAMES.paymentsFn}`]: [
        {
          filterName: "payment-errors",
          filterPattern: '{ $.level = "error" }',
          metricTransformations: [{ metricName: "PaymentErrors", metricNamespace: "Shop/Payments", metricValue: "1", defaultValue: 0, unit: "Count" }],
          creationTime: now - 60 * DAY,
          logGroupName: `/aws/lambda/${NAMES.paymentsFn}`,
        },
        {
          filterName: "orders-placed",
          filterPattern: '{ $.msg = "payment authorized" }',
          metricTransformations: [{ metricName: "OrdersPlaced", metricNamespace: "Shop/Payments", metricValue: "1", unit: "Count" }],
          creationTime: now - 60 * DAY,
          logGroupName: `/aws/lambda/${NAMES.paymentsFn}`,
        },
      ],
      [`/aws/apigateway/${NAMES.api}/prod`]: [
        {
          filterName: "http-5xx",
          filterPattern: "{ $.status >= 500 }",
          metricTransformations: [{ metricName: "Http5xx", metricNamespace: "Shop/Api", metricValue: "1", unit: "Count" }],
          creationTime: now - 40 * DAY,
          logGroupName: `/aws/apigateway/${NAMES.api}/prod`,
        },
      ],
    },
    subFilters: {
      "/shop/web/nginx-error": [
        {
          filterName: "errors-to-notifier",
          logGroupName: "/shop/web/nginx-error",
          filterPattern: "[error]",
          destinationArn: fnArn("shop-order-notifier"),
          distribution: "ByLogStream",
          creationTime: now - 25 * DAY,
        },
      ],
    },
  }
}

// ---- helpers ----

const bytesOf = (msgs: number) => msgs * 190

function streamsOf(st: LogsState, g: LogGroup): LogStream[] {
  const now = Date.now()
  const out = new Map<string, LogStream>()
  if (g.source === "container") return [{ name: "stdout", last_event_time: new Date(now).toISOString(), stored_bytes: 0 }]
  for (const s of profileFor(g.name).streams(now)) out.set(s.name, { name: s.name, last_event_time: new Date(s.last).toISOString(), stored_bytes: Math.round((g.stored_bytes / Math.max(1, profileFor(g.name).streams(now).length)) * 0.9) })
  for (const e of st.put[g.name] ?? []) {
    const cur = out.get(e.stream) ?? { name: e.stream, last_event_time: e.timestamp, stored_bytes: 0 }
    cur.stored_bytes += e.message.length + 26
    if (Date.parse(e.timestamp) > Date.parse(cur.last_event_time)) cur.last_event_time = e.timestamp
    out.set(e.stream, cur)
  }
  return [...out.values()].sort((a, b) => Date.parse(b.last_event_time) - Date.parse(a.last_event_time))
}

/** groupOrVirtual finds a group; unknown /aws/lambda/<fn> groups (created by the Lambda demo) are synthesized. */
function findGroup(st: LogsState, name: string): LogGroup | undefined {
  const g = st.groups.find((x) => x.name === name)
  if (g) return g
  if (name.startsWith("/aws/lambda/")) return { name, arn: arn("logs", `log-group:${name}`), retention_days: 0, created_at: ago(7 * DAY), source: "stored", stored_bytes: 120_000 }
  return undefined
}

/** eventsFor merges generated and user-put events. */
export function eventsFor(st: LogsState, group: string, start: number, end: number, stream: string | undefined, filter: string, limit: number): LogEvent[] {
  const match = contains(filter)
  const g = findGroup(st, group)
  if (!g) return []
  const cutMs = g.retention_days > 0 ? Date.now() - g.retention_days * DAY : 0
  const gen = genEvents(group, Math.max(start, cutMs), end, { stream, match, limit })
  const put = (st.put[group] ?? []).filter((e) => {
    const t = Date.parse(e.timestamp)
    return t >= Math.max(start, cutMs) && t <= end && (!stream || e.stream === stream) && (!match || match(e.message))
  })
  return [...gen, ...put].sort((a, b) => Date.parse(a.timestamp) - Date.parse(b.timestamp)).slice(-limit)
}

const withBytes = (st: LogsState, g: LogGroup): LogGroup => ({ ...g, stored_bytes: g.stored_bytes + bytesOf((st.put[g.name] ?? []).length) })

/** appendLogEvents lets other demo services write to a log group (creating it when missing). */
export function appendLogEvents(st: LogsState, group: string, stream: string, messages: string[]) {
  if (!st.groups.some((g) => g.name === group)) {
    st.groups.push({ name: group, arn: arn("logs", `log-group:${group}`), retention_days: 0, created_at: nowIso(), source: "stored", stored_bytes: 0 })
  }
  const list = st.put[group] ?? (st.put[group] = [])
  const t = Date.now()
  messages.forEach((m, i) => list.push({ timestamp: new Date(t + i).toISOString(), message: m, stream }))
}

const GROUP_RE = /^[A-Za-z0-9_\-/.#]{1,512}$/

export function logRoutes(r: Router, st: () => LogsState) {
  const stored = (name: string): LogGroup => {
    const g = st().groups.find((x) => x.name === name)
    if (!g || g.source === "container") {
      const v = findGroup(st(), name)
      if (v && !g) return v
      throw notFound("log group", name)
    }
    return g
  }

  r.get("/api/v1/logs/groups", ({ query }) => {
    const p = query.prefix
    return st()
      .groups.filter((g) => !p || g.name.startsWith(p))
      .map((g) => withBytes(st(), g))
      .sort((a, b) => a.name.localeCompare(b.name))
  })
  r.post("/api/v1/logs/groups", ({ body }) => {
    const s = st()
    const name = String(body?.name ?? "")
    const days = Number(body?.retention_days ?? 0)
    if (!GROUP_RE.test(name)) throw badRequest("log group names are 1-512 characters: letters, digits and _ - / . #")
    if (name.startsWith("/hc/")) throw badRequest("log group names may not start with the reserved prefix /hc/")
    if (days < 0 || !Number.isFinite(days)) throw badRequest("retention must be 0 (never expire) or more days")
    if (s.groups.some((g) => g.name === name)) throw conflict(`log group "${name}" already exists`)
    const g: LogGroup = { name, arn: arn("logs", `log-group:${name}`), retention_days: days, created_at: nowIso(), source: "stored", stored_bytes: 0 }
    s.groups.push(g)
    return g
  })
  r.del("/api/v1/logs/groups/:group", ({ params }) => {
    const s = st()
    const i = s.groups.findIndex((g) => g.name === params.group && g.source === "stored")
    if (i < 0) throw notFound("log group", params.group)
    s.groups.splice(i, 1)
    delete s.put[params.group]
    delete s.metricFilters[params.group]
    delete s.subFilters[params.group]
    return { ok: true }
  })
  r.put("/api/v1/logs/groups/:group/retention", ({ params, body }) => {
    const g = stored(params.group)
    const days = Number(body?.retention_days ?? 0)
    if (!Number.isFinite(days) || days < 0) throw badRequest("retention_days must be 0 (never expire) or more")
    const real = st().groups.find((x) => x.name === g.name)
    if (real) real.retention_days = days
    return withBytes(st(), real ?? g)
  })
  r.get("/api/v1/logs/groups/:group/streams", ({ params }) => {
    const g = findGroup(st(), params.group)
    if (!g) throw notFound("log group", params.group)
    return streamsOf(st(), g)
  })
  r.get("/api/v1/logs/groups/:group/events", ({ params, query }) => {
    if (!findGroup(st(), params.group)) throw notFound("log group", params.group)
    const start = query.start ? Number(query.start) || Date.parse(query.start) || 0 : 0
    const end = query.end ? Number(query.end) || Date.parse(query.end) || Date.now() : Date.now()
    const limit = Math.max(1, Math.min(10000, Number(query.limit) || 1000))
    return eventsFor(st(), params.group, start, end, query.stream || undefined, query.filter ?? "", limit)
  })
  r.post("/api/v1/logs/groups/:group/streams/:stream/events", ({ params, body }) => {
    const s = st()
    const g = s.groups.find((x) => x.name === params.group)
    if (params.group.startsWith("/hc/")) throw badRequest("container log groups are read-only")
    if (!g) throw notFound("log group", params.group)
    const evs = (body?.events ?? []) as { timestamp?: string | number; message: string }[]
    if (evs.length > 10000) throw badRequest("at most 10000 events per call")
    const list = s.put[g.name] ?? (s.put[g.name] = [])
    for (const e of evs) list.push({ timestamp: e.timestamp ? new Date(e.timestamp).toISOString() : nowIso(), message: String(e.message ?? ""), stream: params.stream })
    return { accepted: evs.length }
  })

  // metric filters
  r.get("/api/v1/logs/groups/:group/metric-filters", ({ params }) => {
    stored(params.group)
    return st().metricFilters[params.group] ?? []
  })
  r.put("/api/v1/logs/groups/:group/metric-filters/:name", ({ params, body }) => {
    stored(params.group)
    const mt = (body?.metricTransformations ?? []) as MetricFilter["metricTransformations"]
    if (!mt.length || !mt[0].metricName || !mt[0].metricNamespace) throw badRequest("metricTransformations needs a metric name and namespace")
    if (/^(AWS|HC)\//.test(mt[0].metricNamespace)) throw badRequest("the namespace may not start with the reserved prefix AWS/ or HC/")
    const list = st().metricFilters[params.group] ?? (st().metricFilters[params.group] = [])
    const f: MetricFilter = { filterName: params.name, filterPattern: String(body?.filterPattern ?? ""), metricTransformations: mt, creationTime: Date.now(), logGroupName: params.group }
    const i = list.findIndex((x) => x.filterName === params.name)
    if (i >= 0) list[i] = { ...f, creationTime: list[i].creationTime }
    else list.push(f)
    return { ok: true }
  })
  r.del("/api/v1/logs/groups/:group/metric-filters/:name", ({ params }) => {
    stored(params.group)
    const list = st().metricFilters[params.group] ?? []
    const i = list.findIndex((x) => x.filterName === params.name)
    if (i < 0) throw notFound("metric filter", params.name)
    list.splice(i, 1)
    return { ok: true }
  })

  // subscription filters
  r.get("/api/v1/logs/groups/:group/subscription-filters", ({ params }) => {
    stored(params.group)
    return st().subFilters[params.group] ?? []
  })
  r.put("/api/v1/logs/groups/:group/subscription-filters/:name", ({ params, body }) => {
    stored(params.group)
    const dest = String(body?.destinationArn ?? "")
    if (!dest.startsWith("arn:aws:lambda:")) throw badRequest("destinationArn must be the ARN of a Lambda function")
    const list = st().subFilters[params.group] ?? (st().subFilters[params.group] = [])
    if (!list.some((x) => x.filterName === params.name) && list.length >= 2) throw badRequest("a log group can have at most 2 subscription filters")
    const f: SubscriptionFilter = { filterName: params.name, logGroupName: params.group, filterPattern: String(body?.filterPattern ?? ""), destinationArn: dest, distribution: "ByLogStream", creationTime: Date.now() }
    const i = list.findIndex((x) => x.filterName === params.name)
    if (i >= 0) list[i] = { ...f, creationTime: list[i].creationTime }
    else list.push(f)
    return { ok: true }
  })
  r.del("/api/v1/logs/groups/:group/subscription-filters/:name", ({ params }) => {
    stored(params.group)
    const list = st().subFilters[params.group] ?? []
    const i = list.findIndex((x) => x.filterName === params.name)
    if (i < 0) throw notFound("subscription filter", params.name)
    list.splice(i, 1)
    return { ok: true }
  })
}
