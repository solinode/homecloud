// Generated CloudWatch metric time-series for the demo. Any (namespace, metric,
// dimensions) the console asks about gets a stable, believable series: values are
// derived from util.timeSeries (deterministic per key and period-aligned) so the
// charts look alive but do not jump around on refresh.

import type { Datapoint, MetricQuery, MetricQueryResult, MetricSeries } from "@/lib/types"
import { INSTANCE, NAMES } from "../ids"
import { rng, timeSeries } from "../util"

type Kind = "gauge" | "count"

interface Prof {
  unit: string
  kind: Kind
  /** gauge: the value; count: events per minute */
  base: number
  amp?: number
  noise?: number
  max?: number
  spikes?: number
  integer?: boolean
  /** multiply by a per-resource factor (0.75..1.35) so resources differ */
  vary?: boolean
}

const G = (unit: string, base: number, o: Partial<Prof> = {}): Prof => ({ unit, kind: "gauge", base, vary: true, ...o })
const C = (base: number, o: Partial<Prof> = {}): Prof => ({ unit: "Count", kind: "count", base, vary: true, ...o })

const CONTAINER: Record<string, Prof> = {
  CPUUtilization: G("Percent", 24, { amp: 10, noise: 3.5, max: 100, spikes: 0.02 }),
  MemoryUtilization: G("Percent", 52, { amp: 4, noise: 1.2, max: 100 }),
  MemoryUsed: G("Bytes", 1.1e9, { amp: 5e7, noise: 2e7 }),
  NetworkIn: G("Bytes", 2.4e5, { amp: 1.2e5, noise: 6e4, spikes: 0.04 }),
  NetworkOut: G("Bytes", 4.1e5, { amp: 2e5, noise: 8e4, spikes: 0.04 }),
  DiskReadBytes: G("Bytes", 8e4, { amp: 3e4, noise: 3e4, spikes: 0.03 }),
  DiskWriteBytes: G("Bytes", 1.6e5, { amp: 6e4, noise: 5e4, spikes: 0.03 }),
  ProcessCount: G("Count", 84, { amp: 5, noise: 2, integer: true }),
}

// Namespace specific overrides of the container metrics.
const NS_TWEAK: Record<string, Record<string, Partial<Prof>>> = {
  "HC/RDS": { CPUUtilization: { base: 17, amp: 7 }, MemoryUtilization: { base: 66 }, MemoryUsed: { base: 2.6e9 }, DiskWriteBytes: { base: 9e5, noise: 2e5 }, DiskReadBytes: { base: 4e5 } },
  "HC/ElastiCache": { CPUUtilization: { base: 8, amp: 3 }, MemoryUtilization: { base: 41, amp: 2 }, MemoryUsed: { base: 4.4e8 }, ProcessCount: { base: 6 } },
  "HC/ELB": { CPUUtilization: { base: 6, amp: 3 }, MemoryUtilization: { base: 11, amp: 1 }, MemoryUsed: { base: 9e7 }, NetworkIn: { base: 1.6e6 }, NetworkOut: { base: 3.2e6 }, ProcessCount: { base: 5 } },
  "HC/S3": { CPUUtilization: { base: 4, amp: 2 }, MemoryUtilization: { base: 18, amp: 1 }, MemoryUsed: { base: 1.5e8 }, ProcessCount: { base: 9 } },
  "HC/ECS": { CPUUtilization: { base: 32, amp: 12 }, MemoryUtilization: { base: 46 }, MemoryUsed: { base: 3.5e8 }, ProcessCount: { base: 12 } },
}

const OTHER: Record<string, Prof> = {
  // Lambda
  "HC/Lambda/Invocations": C(14, { amp: 8, noise: 3, spikes: 0.03, integer: true }),
  "HC/Lambda/Errors": C(0.3, { amp: 0.15, noise: 0.15, spikes: 0.05, integer: true }),
  "HC/Lambda/Throttles": C(0.02, { amp: 0.01, noise: 0.02, integer: true }),
  "HC/Lambda/Duration": G("Milliseconds", 180, { amp: 35, noise: 28, spikes: 0.03 }),
  "HC/Lambda/ConcurrentExecutions": G("Count", 3, { amp: 1.5, noise: 0.8, integer: true }),
  // SQS
  "HC/SQS/NumberOfMessagesSent": C(9, { amp: 6, noise: 2, integer: true }),
  "HC/SQS/NumberOfMessagesReceived": C(9, { amp: 6, noise: 2, integer: true }),
  "HC/SQS/NumberOfMessagesDeleted": C(9, { amp: 6, noise: 2, integer: true }),
  "HC/SQS/ApproximateNumberOfMessagesVisible": G("Count", 6, { amp: 4, noise: 2, integer: true }),
  "HC/SQS/ApproximateAgeOfOldestMessage": G("Seconds", 4, { amp: 2, noise: 1.5 }),
  // Application load balancer
  "HC/ApplicationELB/RequestCount": C(320, { amp: 190, noise: 40, integer: true }),
  "HC/ApplicationELB/HTTPCode_Target_2XX_Count": C(300, { amp: 180, noise: 35, integer: true }),
  "HC/ApplicationELB/HTTPCode_Target_4XX_Count": C(11, { amp: 5, noise: 3, integer: true }),
  "HC/ApplicationELB/HTTPCode_Target_5XX_Count": C(0.6, { amp: 0.3, noise: 0.4, spikes: 0.02, integer: true }),
  "HC/ApplicationELB/TargetResponseTime": G("Seconds", 0.09, { amp: 0.03, noise: 0.02, spikes: 0.02 }),
  "HC/ApplicationELB/ActiveConnectionCount": G("Count", 140, { amp: 90, noise: 15, integer: true }),
  // Auto Scaling
  "HC/AutoScaling/GroupInServiceInstances": G("Count", 2, { amp: 0, noise: 0, integer: true, vary: false }),
  "HC/AutoScaling/GroupDesiredCapacity": G("Count", 2, { amp: 0, noise: 0, integer: true, vary: false }),
  "HC/AutoScaling/CPUUtilization": G("Percent", 41, { amp: 13, noise: 3, max: 100, vary: false }),
  // RDS
  "HC/RDS/DatabaseConnections": G("Count", 18, { amp: 8, noise: 3, integer: true }),
  "HC/RDS/ReadIOPS": G("Count", 34, { amp: 15, noise: 8, integer: true }),
  "HC/RDS/WriteIOPS": G("Count", 61, { amp: 25, noise: 12, integer: true }),
  // DynamoDB
  "HC/DynamoDB/ConsumedReadCapacityUnits": C(42, { amp: 20, noise: 8, integer: true }),
  "HC/DynamoDB/ConsumedWriteCapacityUnits": C(15, { amp: 8, noise: 4, integer: true }),
  // custom metrics published by log metric filters
  "Shop/Payments/PaymentErrors": C(1.1, { amp: 0.4, noise: 0.5, spikes: 0.04, integer: true }),
  "Shop/Payments/OrdersPlaced": C(6, { amp: 4, noise: 1.5, integer: true }),
  "Shop/Api/Http5xx": C(0.4, { amp: 0.2, noise: 0.3, spikes: 0.03, integer: true }),
}

/** Resource specific overrides: "<namespace>|<dimension value>|<metric>". */
const HOT: Record<string, Partial<Prof>> = {
  // shop-web-2 is the noisy neighbour that drives the CPU alarm
  [`HC/EC2|${INSTANCE.web2}|CPUUtilization`]: { base: 84, amp: 6, noise: 4, vary: false, spikes: 0.03 },
  [`HC/EC2|${INSTANCE.batch}|CPUUtilization`]: { base: 3, amp: 1, noise: 1, vary: false },
  [`HC/EC2|${INSTANCE.dev}|CPUUtilization`]: { base: 2, amp: 1, noise: 1, vary: false },
  [`HC/EC2|${INSTANCE.bastion}|CPUUtilization`]: { base: 1.5, amp: 0.6, noise: 0.5, vary: false },
  // the dead-letter queue is (nearly) always empty
  [`HC/SQS|${NAMES.ordersDlq}|ApproximateNumberOfMessagesVisible`]: { base: 0, amp: 0, noise: 0, vary: false },
  [`HC/SQS|${NAMES.ordersDlq}|NumberOfMessagesSent`]: { base: 0, amp: 0, noise: 0, vary: false },
  [`HC/SQS|${NAMES.ordersDlq}|NumberOfMessagesReceived`]: { base: 0, amp: 0, noise: 0, vary: false },
  [`HC/SQS|${NAMES.ordersDlq}|NumberOfMessagesDeleted`]: { base: 0, amp: 0, noise: 0, vary: false },
  [`HC/SQS|${NAMES.ordersDlq}|ApproximateAgeOfOldestMessage`]: { base: 0, amp: 0, noise: 0, vary: false },
  // payments is the busiest and most error prone function
  [`HC/Lambda|${NAMES.paymentsFn}|Invocations`]: { base: 22, amp: 12 },
  [`HC/Lambda|${NAMES.paymentsFn}|Errors`]: { base: 1.8, amp: 0.3, noise: 0.5, vary: false },
  [`HC/Lambda|${NAMES.paymentsFn}|Duration`]: { base: 240, amp: 45, vary: false },
}

function profileFor(ns: string, name: string, dimValue: string): Prof | null {
  let p: Prof | undefined = OTHER[`${ns}/${name}`]
  if (!p && ns in NS_TWEAK) {
    const b = CONTAINER[name]
    if (b) p = { ...b, ...(NS_TWEAK[ns][name] ?? {}) }
  } else if (!p && ns === "HC/EC2") {
    p = CONTAINER[name]
  }
  if (!p) return null
  const hot = HOT[`${ns}|${dimValue}|${name}`]
  return hot ? { ...p, ...hot } : p
}

/** known reports whether a series exists at all (custom metrics nobody published have no data). */
export function known(ns: string, name: string): boolean {
  return profileFor(ns, name, "") !== null
}

const firstDim = (d?: Record<string, string> | null) => (d ? Object.values(d).sort().join(",") : "")

interface Bucket {
  t: number
  v: number
}

/** series returns the raw per-period values (per-sample value for gauges, period total for counts). */
export function series(ns: string, name: string, dims: Record<string, string> | null | undefined, start: number, end: number, periodSec: number): { unit: string; kind: Kind; pts: Bucket[] } | null {
  const dv = firstDim(dims)
  const p = profileFor(ns, name, dv)
  if (!p) return null
  const scale = p.vary ? 0.75 + rng(`${ns}|${dv}|scale`)() * 0.6 : 1
  const f = p.kind === "count" ? periodSec / 60 : 1
  const base = p.base * scale * f
  const amp = (p.amp ?? p.base * 0.3) * scale * f
  const noise = (p.noise ?? p.base * 0.08) * scale * Math.sqrt(f)
  const pts = timeSeries(`${ns}|${dv}|${name}|${periodSec}`, {
    start,
    end,
    periodMs: periodSec * 1000,
    base,
    amp,
    noise,
    min: 0,
    max: p.max ?? Infinity,
    spikes: p.spikes,
    integer: p.integer && p.kind === "count",
  })
  return { unit: p.unit, kind: p.kind, pts }
}

/** queryMetric answers one entry of POST /cloudwatch/metrics/query. */
export function queryMetric(q: MetricQuery): MetricQueryResult {
  const end = q.end ? new Date(q.end).getTime() : Date.now()
  const start = q.start ? new Date(q.start).getTime() : end - 3600_000
  const period = q.period && q.period > 0 ? q.period : 60
  const s = series(q.namespace, q.name, q.dimensions, start, end, period)
  const out: MetricQueryResult = { namespace: q.namespace, name: q.name, dimensions: q.dimensions ?? null, unit: s?.unit ?? "", datapoints: [] }
  if (!s) return out
  const n = Math.max(1, Math.round(period / 30))
  const r = (x: number) => Math.round(x * 1000) / 1000
  out.datapoints = s.pts
    .filter((b) => b.t >= start - period * 1000 && b.t <= end)
    .map((b): Datapoint => {
      const ts = new Date(b.t).toISOString()
      if (s.kind === "gauge") {
        const spread = 0.08 + rng(`${b.t}${q.name}`)() * 0.12
        return { timestamp: ts, average: b.v, sum: r(b.v * n), minimum: r(b.v * (1 - spread)), maximum: r(b.v * (1 + spread * 1.5)), sample_count: n }
      }
      const avg = b.v / n
      return { timestamp: ts, average: r(avg), sum: b.v, minimum: Math.floor(avg * 0.4), maximum: Math.ceil(avg * 1.7), sample_count: n }
    })
  return out
}

/** statValue picks the statistic of a datapoint (percentiles fall back to the maximum). */
export function statValue(d: Datapoint, stat: string): number {
  switch (stat) {
    case "Sum":
      return d.sum
    case "Minimum":
      return d.minimum
    case "Maximum":
      return d.maximum
    case "SampleCount":
      return d.sample_count
    default:
      return /^p\d/.test(stat) ? d.maximum : d.average
  }
}

// ---- catalog (GET /cloudwatch/metrics) ----

const CONTAINER_NAMES = Object.keys(CONTAINER)

export function catalog(): MetricSeries[] {
  const out: MetricSeries[] = []
  const add = (ns: string, dimKey: string, dimVal: string, names: string[]) => {
    for (const name of names) {
      const p = profileFor(ns, name, dimVal)
      if (p) out.push({ namespace: ns, name, dimensions: { [dimKey]: dimVal }, unit: p.unit })
    }
  }
  for (const id of [INSTANCE.web1, INSTANCE.web2, INSTANCE.bastion, INSTANCE.batch, INSTANCE.dev]) add("HC/EC2", "InstanceId", id, CONTAINER_NAMES)
  add("HC/RDS", "DBInstanceIdentifier", NAMES.db, [...CONTAINER_NAMES, "DatabaseConnections", "ReadIOPS", "WriteIOPS"])
  add("HC/ElastiCache", "CacheClusterId", NAMES.cache, CONTAINER_NAMES)
  add("HC/ELB", "LoadBalancer", NAMES.alb, CONTAINER_NAMES)
  add("HC/S3", "Service", "s3", CONTAINER_NAMES)
  add("HC/ApplicationELB", "LoadBalancer", NAMES.alb, ["RequestCount", "HTTPCode_Target_2XX_Count", "HTTPCode_Target_4XX_Count", "HTTPCode_Target_5XX_Count", "TargetResponseTime", "ActiveConnectionCount"])
  add("HC/AutoScaling", "AutoScalingGroupName", NAMES.asg, ["GroupInServiceInstances", "GroupDesiredCapacity", "CPUUtilization"])
  for (const fn of LAMBDAS) add("HC/Lambda", "FunctionName", fn, ["Invocations", "Errors", "Throttles", "Duration", "ConcurrentExecutions"])
  for (const q of [NAMES.ordersQueue, NAMES.ordersDlq])
    add("HC/SQS", "QueueName", q, ["NumberOfMessagesSent", "NumberOfMessagesReceived", "NumberOfMessagesDeleted", "ApproximateNumberOfMessagesVisible", "ApproximateAgeOfOldestMessage"])
  add("HC/DynamoDB", "TableName", "shop-orders", ["ConsumedReadCapacityUnits", "ConsumedWriteCapacityUnits"])
  out.push({ namespace: "Shop/Payments", name: "PaymentErrors", dimensions: null, unit: "Count" })
  out.push({ namespace: "Shop/Payments", name: "OrdersPlaced", dimensions: null, unit: "Count" })
  out.push({ namespace: "Shop/Api", name: "Http5xx", dimensions: null, unit: "Count" })
  return out.sort((a, b) => key(a).localeCompare(key(b)))
}

const key = (s: MetricSeries) => `${s.namespace}|${s.name}|${firstDim(s.dimensions)}`

export const LAMBDAS = [NAMES.paymentsFn, "shop-order-notifier", "shop-image-resizer", "shop-nightly-cleanup"]
