"use client"

import { useMemo } from "react"

import { StatusBadge } from "@/components/console/status-badge"
import { useApi } from "@/lib/hooks"
import type { Alarm, AlarmState, ComparisonOperator, Instance, Statistic } from "@/lib/types"

export const STATISTICS: Statistic[] = ["Average", "Sum", "Minimum", "Maximum", "SampleCount"]
export const PERIODS: { value: number; label: string }[] = [
  { value: 60, label: "1 minute" },
  { value: 300, label: "5 minutes" },
  { value: 900, label: "15 minutes" },
  { value: 3600, label: "1 hour" },
]

export const OPERATORS: { value: ComparisonOperator; symbol: string; label: string }[] = [
  { value: "GreaterThanThreshold", symbol: ">", label: "Greater than" },
  { value: "GreaterThanOrEqualToThreshold", symbol: ">=", label: "Greater than or equal to" },
  { value: "LessThanThreshold", symbol: "<", label: "Less than" },
  { value: "LessThanOrEqualToThreshold", symbol: "<=", label: "Less than or equal to" },
]

export function operatorSymbol(op: ComparisonOperator | string): string {
  return OPERATORS.find((o) => o.value === op)?.symbol ?? op
}

export function periodLabel(seconds: number): string {
  if (seconds % 3600 === 0) return `${seconds / 3600} hour${seconds === 3600 ? "" : "s"}`
  if (seconds % 60 === 0) return `${seconds / 60} minute${seconds === 60 ? "" : "s"}`
  return `${seconds} seconds`
}

/** alarmCondition renders "CPUUtilization > 80 for 3 datapoints within 3 minutes". */
export function alarmCondition(a: Pick<Alarm, "metric" | "comparison_operator" | "threshold" | "evaluation_periods" | "period" | "statistic">): string {
  const within = periodLabel(a.period * a.evaluation_periods)
  const dp = a.evaluation_periods === 1 ? "1 datapoint" : `${a.evaluation_periods} datapoints`
  const stat = a.statistic && a.statistic !== "Average" ? ` (${a.statistic})` : ""
  return `${a.metric}${stat} ${operatorSymbol(a.comparison_operator)} ${a.threshold} for ${dp} within ${within}`
}

export function dimsText(d: Record<string, string> | null | undefined): string {
  if (!d) return ""
  return Object.entries(d)
    .sort(([a], [b]) => a.localeCompare(b))
    .map(([k, v]) => `${k}=${v}`)
    .join(", ")
}

export function dimsKey(d: Record<string, string> | null | undefined): string {
  return dimsText(d)
}

export const ALARM_STATE_LABEL: Record<AlarmState, string> = {
  ALARM: "In alarm",
  OK: "OK",
  INSUFFICIENT_DATA: "Insufficient data",
}

export function AlarmStateBadge({ state }: { state: AlarmState }) {
  const tone = state === "ALARM" ? "danger" : state === "OK" ? "success" : "neutral"
  return <StatusBadge status={state.toLowerCase()} tone={tone} label={ALARM_STATE_LABEL[state] ?? state} />
}

/** useInstanceNames maps instance IDs to names (for friendly metric/log labels). */
export function useInstanceNames() {
  const { data } = useApi<Instance[]>("/api/v1/ec2/instances", { refreshInterval: 60_000 })
  return useMemo(() => {
    const m = new Map<string, string>()
    for (const i of data ?? []) if (i.name) m.set(i.id, i.name)
    return m
  }, [data])
}

/** seriesLabel names a metric series "web (i-123)" / "CPUUtilization web". */
export function friendlyDims(d: Record<string, string> | null | undefined, names: Map<string, string>): string {
  if (!d) return ""
  return Object.entries(d)
    .map(([k, v]) => {
      const n = k === "InstanceId" ? names.get(v) : undefined
      return n ? `${n} (${v})` : v
    })
    .join(", ")
}

export const RANGES: { value: number; label: string }[] = [
  { value: 15, label: "15m" },
  { value: 60, label: "1h" },
  { value: 180, label: "3h" },
  { value: 720, label: "12h" },
  { value: 1440, label: "24h" },
]

export const logGroupHref = (name: string) => `/cloudwatch/logs/group/?name=${encodeURIComponent(name)}`

export const RETENTION_CHOICES = [1, 3, 5, 7, 14, 30, 60, 90, 120, 150, 180, 365]

export function retentionLabel(days: number): string {
  if (!days) return "Never expire"
  if (days === 1) return "1 day"
  return `${days} days`
}

/** describeContainerGroup explains a "/hc/<service>/<resource>" group. */
export function describeContainerGroup(name: string, names: Map<string, string>): string {
  const m = /^\/hc\/([^/]+)\/(.+)$/.exec(name)
  if (!m) return ""
  const [, svc, res] = m
  if (svc === "ec2") {
    const n = names.get(res)
    return n ? `Console output of EC2 instance ${n}` : `Console output of EC2 instance ${res}`
  }
  if (svc === "s3") return "HomeCloud S3 server output"
  const kinds: Record<string, string> = { rds: "RDS database", elasticache: "ElastiCache cluster", lambda: "Lambda function" }
  return `Container output of ${kinds[svc] ?? svc} ${res}`
}
