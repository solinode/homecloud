import type { Datapoint, MetricQuery, MetricQueryResult, MetricSeries } from "@/lib/types"
import { badRequest, getState, type DemoService, type Router } from "../engine"
import { alarmRoutes, seedAlarms, type AlarmsState } from "./cloudwatch-alarms"
import { insightsRoutes } from "./cloudwatch-insights"
import { logRoutes, seedLogs, type LogsState } from "./cloudwatch-logs"
import { catalog, queryMetric } from "./cloudwatch-metrics"

interface Custom {
  namespace: string
  name: string
  dimensions: Record<string, string> | null
  unit: string
  points: { t: number; v: number }[]
}

interface CwState {
  alarms: AlarmsState
  logs: LogsState
  /** metrics published through PutMetricData */
  custom: Custom[]
}

const st = () => getState().cloudwatch as CwState
const dimKey = (d?: Record<string, string> | null) => JSON.stringify(Object.entries(d ?? {}).sort())

function customQuery(c: Custom, q: MetricQuery): MetricQueryResult {
  const end = q.end ? Date.parse(q.end) : Date.now()
  const start = q.start ? Date.parse(q.start) : end - 3600_000
  const pms = (q.period || 60) * 1000
  const buckets = new Map<number, number[]>()
  for (const p of c.points) {
    if (p.t < start || p.t > end) continue
    const k = Math.floor(p.t / pms) * pms
    ;(buckets.get(k) ?? buckets.set(k, []).get(k)!).push(p.v)
  }
  const datapoints: Datapoint[] = [...buckets.entries()]
    .sort((a, b) => a[0] - b[0])
    .map(([t, vs]) => {
      const sum = vs.reduce((x, y) => x + y, 0)
      return { timestamp: new Date(t).toISOString(), average: sum / vs.length, sum, minimum: Math.min(...vs), maximum: Math.max(...vs), sample_count: vs.length }
    })
  return { namespace: c.namespace, name: c.name, dimensions: c.dimensions, unit: c.unit, datapoints }
}

function routes(r: Router) {
  r.get("/api/v1/cloudwatch/metrics", ({ query }) => {
    const all: MetricSeries[] = [...catalog(), ...st().custom.map((c) => ({ namespace: c.namespace, name: c.name, dimensions: c.dimensions, unit: c.unit }))]
    return all.filter((m) => !query.namespace || m.namespace === query.namespace)
  })
  r.post("/api/v1/cloudwatch/metrics", ({ body }) => {
    const ns = String(body?.namespace ?? "")
    if (!ns || ns.startsWith("HC/")) throw badRequest("namespace is required and may not start with the reserved prefix HC/")
    const now = Date.now()
    for (const d of (body?.data ?? []) as { name: string; dimensions?: Record<string, string>; value: number; unit?: string; timestamp?: string }[]) {
      if (!d.name) throw badRequest("metric name is required")
      const dims = d.dimensions && Object.keys(d.dimensions).length ? d.dimensions : null
      let c = st().custom.find((x) => x.namespace === ns && x.name === d.name && dimKey(x.dimensions) === dimKey(dims))
      if (!c) st().custom.push((c = { namespace: ns, name: d.name, dimensions: dims, unit: d.unit && d.unit !== "None" ? d.unit : "", points: [] }))
      const t = d.timestamp ? Date.parse(d.timestamp) : now
      if (t < now - 15 * 86400_000 || t > now + 2 * 3600_000) continue
      c.points.push({ t, v: Number(d.value) })
      if (c.points.length > 5000) c.points.shift()
    }
    return { ok: true }
  })
  r.post("/api/v1/cloudwatch/metrics/query", ({ body }) => {
    const qs = (body?.queries ?? []) as MetricQuery[]
    return qs.map((q) => {
      const c = st().custom.find((x) => x.namespace === q.namespace && x.name === q.name && dimKey(x.dimensions) === dimKey(q.dimensions))
      return c ? customQuery(c, q) : queryMetric(q)
    })
  })
  alarmRoutes(r, () => st().alarms)
  logRoutes(r, () => st().logs)
  insightsRoutes(r, () => st().logs)
}

const service: DemoService = {
  name: "cloudwatch",
  seed: (): CwState => ({ alarms: seedAlarms(), logs: seedLogs(), custom: [] }),
  routes,
}

export default service
