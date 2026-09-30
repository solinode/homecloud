// CloudWatch alarms for the demo: fixtures, evaluation against the generated
// metric series, and the alarm routes.

import type { Alarm, AlarmHistoryItem, AlarmState, Datapoint } from "@/lib/types"
import { badRequest, later, notFound, type Router } from "../engine"
import { INSTANCE, NAMES } from "../ids"
import { MIN, HOUR, DAY, ago, arn, nowIso } from "../util"
import { known, queryMetric, statValue } from "./cloudwatch-metrics"

export interface AlarmsState {
  alarms: Alarm[]
  /** newest first */
  history: Record<string, AlarmHistoryItem[]>
}

const topic = (n: string) => arn("sns", n)
const alarmArn = (n: string) => arn("cloudwatch", `alarm:${n}`)
const OPS = topic(NAMES.alertsTopic)

const OP_WORDS: Record<string, string> = {
  GreaterThanThreshold: "greater than",
  GreaterThanOrEqualToThreshold: "greater than or equal to",
  LessThanThreshold: "less than",
  LessThanOrEqualToThreshold: "less than or equal to",
}
const CMP: Record<string, (a: number, b: number) => boolean> = {
  GreaterThanThreshold: (a, b) => a > b,
  GreaterThanOrEqualToThreshold: (a, b) => a >= b,
  LessThanThreshold: (a, b) => a < b,
  LessThanOrEqualToThreshold: (a, b) => a <= b,
}

/** values returns the alarm metric for its last N complete periods, oldest first (null = no data). */
function values(a: Alarm): (number | null)[] {
  const n = Math.max(a.evaluation_periods, 1)
  const periodMs = a.period * 1000
  const now = Date.now()
  const end = Math.floor(now / periodMs) * periodMs
  const stat = a.extended_statistic || a.statistic
  if (!known(a.namespace, a.metric)) return Array(n).fill(null)
  const res = queryMetric({ namespace: a.namespace, name: a.metric, dimensions: a.dimensions ?? undefined, start: new Date(end - n * periodMs).toISOString(), end: new Date(end - 1).toISOString(), period: a.period })
  const byT = new Map<number, Datapoint>(res.datapoints.map((d) => [new Date(d.timestamp).getTime(), d]))
  const out: (number | null)[] = []
  for (let i = 0; i < n; i++) {
    const d = byT.get(end - (n - i) * periodMs)
    out.push(d ? statValue(d, stat) : null)
  }
  return out
}

/** evaluate ports the API's alarm evaluation: state + reason from the recent datapoints. */
export function evaluate(a: Alarm): { state: AlarmState; reason: string } {
  const vals = values(a)
  const n = vals.length
  let m = a.datapoints_to_alarm ?? 0
  if (m <= 0 || m > n) m = n
  const cmp = CMP[a.comparison_operator]
  let breach = 0
  let missing = 0
  const recent: number[] = []
  for (const v of vals) {
    if (v === null) missing++
    else {
      if (cmp(v, a.threshold)) breach++
      recent.push(v)
    }
  }
  if (a.treat_missing_data === "breaching") breach += missing
  if (missing === n && a.treat_missing_data !== "notBreaching" && a.treat_missing_data !== "breaching")
    return { state: "INSUFFICIENT_DATA", reason: `Insufficient Data: ${n} datapoint${n === 1 ? " was" : "s were"} unknown.` }
  const list = recent.map((x) => Math.round(x * 100) / 100).join(", ")
  if (breach >= m)
    return {
      state: "ALARM",
      reason: `Threshold Crossed: ${breach} out of the last ${n} datapoints [${list}] ${breach === 1 ? "was" : "were"} ${OP_WORDS[a.comparison_operator]} the threshold (${a.threshold}) (minimum ${m} datapoint${m === 1 ? "" : "s"} for OK -> ALARM transition).`,
    }
  return {
    state: "OK",
    reason: `Threshold Crossed: ${n - breach} out of the last ${n} datapoints [${list}] ${n - breach === 1 ? "was" : "were"} not ${OP_WORDS[a.comparison_operator]} the threshold (${a.threshold}) (minimum ${m} datapoint${m === 1 ? "" : "s"} for ALARM -> OK transition).`,
  }
}

function mk(name: string, o: Partial<Alarm> & Pick<Alarm, "namespace" | "metric" | "threshold" | "comparison_operator">, ageMs: number): Alarm {
  return {
    name,
    arn: alarmArn(name),
    description: "",
    dimensions: null,
    statistic: "Average",
    period: 300,
    evaluation_periods: 3,
    datapoints_to_alarm: o.evaluation_periods ?? 3,
    treat_missing_data: "missing",
    alarm_actions: [OPS],
    ok_actions: [],
    insufficient_data_actions: [],
    actions_enabled: true,
    state: "OK",
    state_reason: "",
    state_updated_at: ago(ageMs),
    created_at: ago(ageMs + 40 * DAY),
    ...o,
  }
}

function hist(name: string, items: [number, string, string][]): AlarmHistoryItem[] {
  return items.map(([age, type, summary]) => ({
    alarm_name: name,
    timestamp: ago(age),
    type,
    summary,
    data: JSON.stringify({ version: "1.0", alarm: name, note: "demo" }),
  }))
}

export function seedAlarms(): AlarmsState {
  const alarms: Alarm[] = [
    mk("shop-web-cpu-high", {
      description: "Average CPU of the web Auto Scaling group is above 70 percent",
      namespace: "HC/AutoScaling",
      metric: "CPUUtilization",
      dimensions: { AutoScalingGroupName: NAMES.asg },
      threshold: 70,
      comparison_operator: "GreaterThanThreshold",
      alarm_actions: [OPS, arn("autoscaling", `scalingPolicy:demo:autoScalingGroupName/${NAMES.asg}:policyName/scale-out`)],
      ok_actions: [OPS],
    }, 3 * DAY),
    mk("shop-web-2-cpu-high", {
      description: "CPU on shop-web-2 is above 70 percent for 15 minutes",
      namespace: "HC/EC2",
      metric: "CPUUtilization",
      dimensions: { InstanceId: INSTANCE.web2 },
      threshold: 70,
      comparison_operator: "GreaterThanThreshold",
      ok_actions: [OPS],
    }, 47 * MIN),
    mk("shop-payments-errors", {
      description: "The payments Lambda function is failing: more than 4 errors in 5 minutes",
      namespace: "HC/Lambda",
      metric: "Errors",
      dimensions: { FunctionName: NAMES.paymentsFn },
      statistic: "Sum",
      threshold: 4,
      evaluation_periods: 2,
      comparison_operator: "GreaterThanThreshold",
      treat_missing_data: "notBreaching",
      alarm_actions: [OPS, topic("shop-oncall-pager")],
    }, 2 * HOUR + 12 * MIN),
    mk("shop-orders-dlq-not-empty", {
      description: "Orders are landing in the dead-letter queue",
      namespace: "HC/SQS",
      metric: "ApproximateNumberOfMessagesVisible",
      dimensions: { QueueName: NAMES.ordersDlq },
      statistic: "Maximum",
      period: 60,
      evaluation_periods: 1,
      threshold: 0,
      comparison_operator: "GreaterThanThreshold",
      treat_missing_data: "notBreaching",
    }, 9 * DAY),
    mk("shop-db-cpu-high", {
      description: "RDS CPU above 80 percent",
      namespace: "HC/RDS",
      metric: "CPUUtilization",
      dimensions: { DBInstanceIdentifier: NAMES.db },
      threshold: 80,
      evaluation_periods: 5,
      datapoints_to_alarm: 3,
      comparison_operator: "GreaterThanThreshold",
      alarm_actions: [OPS, topic("shop-oncall-pager")],
    }, 21 * DAY),
    mk("shop-db-memory-high", {
      description: "RDS memory utilization above 90 percent",
      namespace: "HC/RDS",
      metric: "MemoryUtilization",
      dimensions: { DBInstanceIdentifier: NAMES.db },
      threshold: 90,
      comparison_operator: "GreaterThanThreshold",
    }, 33 * DAY),
    mk("shop-alb-5xx", {
      description: "More than 10 HTTP 5xx responses from targets in 5 minutes",
      namespace: "HC/ApplicationELB",
      metric: "HTTPCode_Target_5XX_Count",
      dimensions: { LoadBalancer: NAMES.alb },
      statistic: "Sum",
      threshold: 10,
      evaluation_periods: 2,
      datapoints_to_alarm: 2,
      comparison_operator: "GreaterThanThreshold",
      treat_missing_data: "notBreaching",
      ok_actions: [OPS],
    }, 5 * DAY),
    mk("shop-alb-latency-p99", {
      description: "p99 target response time above 1.5 seconds",
      namespace: "HC/ApplicationELB",
      metric: "TargetResponseTime",
      dimensions: { LoadBalancer: NAMES.alb },
      extended_statistic: "p99",
      period: 60,
      evaluation_periods: 5,
      datapoints_to_alarm: 3,
      threshold: 1.5,
      unit: "Seconds",
      comparison_operator: "GreaterThanThreshold",
    }, 12 * DAY),
    mk("shop-cache-evictions", {
      description: "Redis evictions per minute (published by the cache exporter)",
      namespace: "Shop/Cache",
      metric: "EvictedKeys",
      dimensions: { CacheClusterId: NAMES.cache },
      statistic: "Sum",
      threshold: 100,
      evaluation_periods: 3,
      comparison_operator: "GreaterThanThreshold",
    }, 6 * DAY),
    mk("shop-dev-sandbox-cpu", {
      description: "Muted while the dev sandbox is used for load tests",
      namespace: "HC/EC2",
      metric: "CPUUtilization",
      dimensions: { InstanceId: INSTANCE.dev },
      threshold: 90,
      comparison_operator: "GreaterThanThreshold",
      actions_enabled: false,
    }, 15 * DAY),
    mk("shop-batch-idle", {
      description: "Batch worker has been idle for an hour",
      namespace: "HC/EC2",
      metric: "CPUUtilization",
      dimensions: { InstanceId: INSTANCE.batch },
      threshold: 1,
      period: 300,
      evaluation_periods: 12,
      datapoints_to_alarm: 12,
      comparison_operator: "LessThanThreshold",
      alarm_actions: [],
    }, 1 * DAY),
  ]
  for (const a of alarms) {
    const e = evaluate(a)
    a.state = e.state
    a.state_reason = e.reason
    if (a.extended_statistic) a.statistic = "" as Alarm["statistic"]
  }
  const history: Record<string, AlarmHistoryItem[]> = {}
  for (const a of alarms) {
    const h: [number, string, string][] = [[Date.now() - Date.parse(a.created_at), "ConfigurationUpdate", `Alarm "${a.name}" created`]]
    const since = Date.now() - Date.parse(a.state_updated_at)
    if (a.state === "ALARM") {
      h.push([since + 3 * DAY, "StateUpdate", "Alarm updated from INSUFFICIENT_DATA to OK"])
      h.push([since + 5 * HOUR, "StateUpdate", "Alarm updated from ALARM to OK"])
      h.push([since + 4 * HOUR + 30 * MIN, "StateUpdate", "Alarm updated from OK to ALARM"])
      h.push([since + 4 * HOUR + 30 * MIN - 1000, "Action", `Successfully executed action ${a.alarm_actions[0] ?? OPS}`])
      h.push([since + 3 * HOUR + 50 * MIN, "StateUpdate", "Alarm updated from ALARM to OK"])
      h.push([since, "StateUpdate", "Alarm updated from OK to ALARM"])
      h.push([since - 1000 + 1, "Action", `Successfully executed action ${a.alarm_actions[0] ?? OPS}`])
    } else if (a.state === "OK") {
      h.push([since + 1000, "StateUpdate", "Alarm updated from INSUFFICIENT_DATA to OK"])
      if (a.name === "shop-alb-5xx") {
        h.push([2 * DAY + 3 * HOUR, "StateUpdate", "Alarm updated from OK to ALARM"])
        h.push([2 * DAY + 3 * HOUR - 500, "Action", `Successfully executed action ${OPS}`])
        h.push([2 * DAY + 2 * HOUR + 40 * MIN, "StateUpdate", "Alarm updated from ALARM to OK"])
      }
    } else {
      h.push([since, "StateUpdate", "Alarm updated from OK to INSUFFICIENT_DATA"])
    }
    if (a.name === "shop-payments-errors" || a.name === "shop-web-cpu-high") h.push([20 * DAY, "ConfigurationUpdate", `Alarm "${a.name}" updated`])
    history[a.name] = hist(a.name, h).sort((x, y) => Date.parse(y.timestamp) - Date.parse(x.timestamp))
  }
  return { alarms, history }
}

/** re-evaluates a single alarm (after create/update) and records the transition. */
function settle(st: AlarmsState, name: string) {
  const a = st.alarms.find((x) => x.name === name)
  if (!a) return
  const e = evaluate(a)
  if (e.state === a.state && e.reason === a.state_reason) return
  const prev = a.state
  a.state = e.state
  a.state_reason = e.reason
  a.state_updated_at = nowIso()
  if (prev !== e.state) {
    const h = st.history[name] ?? (st.history[name] = [])
    h.unshift({ alarm_name: name, timestamp: nowIso(), type: "StateUpdate", summary: `Alarm updated from ${prev} to ${e.state}`, data: JSON.stringify({ version: "1.0", newState: { stateValue: e.state, stateReason: e.reason } }) })
    if (a.actions_enabled !== false) {
      const targets = e.state === "ALARM" ? a.alarm_actions : e.state === "OK" ? a.ok_actions : (a.insufficient_data_actions ?? [])
      for (const t of targets) h.unshift({ alarm_name: name, timestamp: nowIso(), type: "Action", summary: `Successfully executed action ${t}`, data: JSON.stringify({ actionState: "Succeeded", notificationResource: t }) })
    }
  }
}

export function alarmRoutes(r: Router, st: () => AlarmsState) {
  r.get("/api/v1/cloudwatch/alarms", () => st().alarms)
  r.get("/api/v1/cloudwatch/alarms/:name", ({ params }) => {
    const a = st().alarms.find((x) => x.name === params.name)
    if (!a) throw notFound("alarm", params.name)
    return a
  })
  r.get("/api/v1/cloudwatch/alarms/:name/history", ({ params }) => {
    if (!st().alarms.some((x) => x.name === params.name)) throw notFound("alarm", params.name)
    return st().history[params.name] ?? []
  })
  r.put("/api/v1/cloudwatch/alarms/:name", ({ params, body }) => {
    const s = st()
    const name = params.name
    const b = (body ?? {}) as Partial<Alarm>
    if (!name || name.length > 255 || name.includes("/")) throw badRequest("alarm names are 1-255 characters without control characters or slashes")
    for (const t of [...(b.alarm_actions ?? []), ...(b.ok_actions ?? []), ...(b.insufficient_data_actions ?? [])])
      if (!t.startsWith("arn:aws:sns:") && !/^https?:\/\//.test(t)) throw badRequest(`action "${t}" must be an SNS topic ARN or an http(s) URL`)
    if (b.metrics === undefined || !b.metrics?.length) {
      if (!b.namespace || !b.metric) throw badRequest("namespace and metric are required")
    }
    if (!b.comparison_operator || !(b.comparison_operator in CMP))
      throw badRequest("comparison_operator must be one of GreaterThanThreshold, GreaterThanOrEqualToThreshold, LessThanThreshold, LessThanOrEqualToThreshold")
    if (typeof b.threshold !== "number" || !isFinite(b.threshold)) throw badRequest("threshold must be a number")
    const evalN = Math.max(1, Number(b.evaluation_periods) || 1)
    if (b.datapoints_to_alarm && b.datapoints_to_alarm > evalN) throw badRequest("DatapointsToAlarm must not exceed EvaluationPeriods")
    const old = s.alarms.find((x) => x.name === name)
    const next: Alarm = {
      name,
      arn: alarmArn(name),
      description: b.description ?? "",
      namespace: b.namespace ?? "",
      metric: b.metric ?? "",
      dimensions: b.dimensions && Object.keys(b.dimensions).length ? b.dimensions : null,
      statistic: b.extended_statistic ? ("" as Alarm["statistic"]) : (b.statistic ?? "Average"),
      period: !b.period || b.period < 30 ? 60 : b.period,
      evaluation_periods: evalN,
      datapoints_to_alarm: b.datapoints_to_alarm || undefined,
      treat_missing_data: b.treat_missing_data ?? "missing",
      extended_statistic: b.extended_statistic,
      unit: b.unit,
      metrics: b.metrics,
      actions_enabled: b.actions_enabled,
      threshold: b.threshold,
      comparison_operator: b.comparison_operator,
      alarm_actions: b.alarm_actions ?? [],
      ok_actions: b.ok_actions ?? [],
      insufficient_data_actions: b.insufficient_data_actions,
      state: old?.state ?? "INSUFFICIENT_DATA",
      state_reason: old?.state_reason ?? "Unchecked: Initial alarm creation",
      state_updated_at: old?.state_updated_at ?? nowIso(),
      created_at: old?.created_at ?? nowIso(),
    }
    if (old) s.alarms[s.alarms.indexOf(old)] = next
    else s.alarms.push(next)
    const h = s.history[name] ?? (s.history[name] = [])
    h.unshift({ alarm_name: name, timestamp: nowIso(), type: "ConfigurationUpdate", summary: `Alarm "${name}" ${old ? "updated" : "created"}`, data: JSON.stringify({ version: "1.0", type: "Update", updatedAlarm: name }) })
    later(old ? 1500 : 2500, () => settle(st(), name))
    return next
  })
  r.del("/api/v1/cloudwatch/alarms/:name", ({ params }) => {
    const s = st()
    const i = s.alarms.findIndex((x) => x.name === params.name)
    if (i < 0) throw notFound("alarm", params.name)
    s.alarms.splice(i, 1)
    delete s.history[params.name]
    return { ok: true }
  })
}
