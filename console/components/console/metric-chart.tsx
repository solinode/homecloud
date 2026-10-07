"use client"

import { useMemo } from "react"
import useSWR from "swr"
import { CartesianGrid, Legend, Line, LineChart, ReferenceLine, ResponsiveContainer, Tooltip, XAxis, YAxis } from "recharts"

import { Skeleton } from "@/components/ui/skeleton"
import { api } from "@/lib/api"
import { formatTime, formatValue } from "@/lib/format"
import type { MetricQuery, MetricQueryResult, Statistic } from "@/lib/types"
import { cn } from "@/lib/utils"

/** Categorical series colors, in fixed order (validated palette, see globals.css). */
export const SERIES_COLORS = ["var(--chart-1)", "var(--chart-2)", "var(--chart-3)", "var(--chart-4)", "var(--chart-5)"]

export interface ChartQuery extends MetricQuery {
  /** legend label; defaults to the metric name */
  label?: string
}

const STAT_KEY: Record<Statistic, "average" | "sum" | "minimum" | "maximum" | "sample_count"> = {
  Average: "average",
  Sum: "sum",
  Minimum: "minimum",
  Maximum: "maximum",
  SampleCount: "sample_count",
}

/**
 * useMetricData runs POST /cloudwatch/metrics/query for a set of series over
 * the last `rangeMinutes`, polling every `refreshMs`.
 */
export function useMetricData(queries: ChartQuery[], rangeMinutes: number, period: number, stat: Statistic, refreshMs = 30_000) {
  const key = queries.length ? ["metrics", JSON.stringify(queries.map(({ label: _l, ...q }) => q)), rangeMinutes, period, stat] : null
  return useSWR<MetricQueryResult[]>(
    key,
    () => {
      const end = new Date()
      const start = new Date(end.getTime() - rangeMinutes * 60_000)
      return api.post<MetricQueryResult[]>("/api/v1/cloudwatch/metrics/query", {
        queries: queries.map(({ label: _l, ...q }) => ({ ...q, start: start.toISOString(), end: end.toISOString(), period, stat })),
      })
    },
    { refreshInterval: refreshMs, keepPreviousData: true },
  )
}

/**
 * MetricChart draws one or more CloudWatch series on a single axis with a
 * crosshair tooltip. All series should share a unit.
 */
export function MetricChart({
  queries,
  rangeMinutes = 60,
  period = 60,
  stat = "Average",
  height = 220,
  threshold,
  className,
  refreshMs,
}: {
  queries: ChartQuery[]
  rangeMinutes?: number
  period?: number
  stat?: Statistic
  height?: number
  threshold?: number
  className?: string
  refreshMs?: number
}) {
  const { data, error, isLoading } = useMetricData(queries, rangeMinutes, period, stat, refreshMs)
  const statKey = STAT_KEY[stat] ?? "average"

  const { rows, unit, hasData } = useMemo(() => {
    const byTs = new Map<number, Record<string, number>>()
    let u = ""
    let any = false
    ;(data ?? []).forEach((r, i) => {
      if (r.unit) u = r.unit
      for (const d of r.datapoints ?? []) {
        any = true
        const t = new Date(d.timestamp).getTime()
        const row = byTs.get(t) ?? { t }
        row[`s${i}`] = d[statKey]
        byTs.set(t, row)
      }
    })
    return { rows: [...byTs.values()].sort((a, b) => a.t - b.t), unit: u, hasData: any }
  }, [data, statKey])

  if (isLoading && !data) return <Skeleton className={cn("w-full rounded-md", className)} style={{ height }} />
  if (error) return <div className={cn("text-danger flex items-center justify-center text-sm", className)} style={{ height }}>{error.message}</div>
  if (!hasData)
    return (
      <div className={cn("text-muted-foreground bg-muted/30 flex flex-col items-center justify-center gap-1 rounded-lg border border-dashed text-sm", className)} style={{ height }}>
        <span>No datapoints in this time range</span>
        <span className="text-xs">Metrics are sampled every 30 seconds while a resource is running.</span>
      </div>
    )

  const labels = queries.map((q) => q.label ?? q.name)
  const now = Date.now()
  return (
    <div className={cn("w-full", className)} style={{ height }}>
      <ResponsiveContainer width="100%" height="100%">
        <LineChart data={rows} margin={{ top: 8, right: 12, bottom: 0, left: 0 }}>
          <CartesianGrid stroke="var(--chart-grid)" vertical={false} />
          <XAxis
            dataKey="t"
            type="number"
            scale="time"
            domain={[now - rangeMinutes * 60_000, now]}
            tickFormatter={(t: number) => formatTime(t).slice(0, 5)}
            stroke="var(--faint)"
            fontSize={11}
            fontFamily="var(--font-geist-mono), ui-monospace, monospace"
            tickLine={false}
            axisLine={{ stroke: "var(--border-strong)" }}
            minTickGap={40}
          />
          <YAxis
            stroke="var(--faint)"
            fontSize={11}
            fontFamily="var(--font-geist-mono), ui-monospace, monospace"
            tickLine={false}
            axisLine={false}
            width={64}
            tickFormatter={(v: number) => formatValue(v, unit)}
            domain={unit === "Percent" ? [0, (max: number) => Math.max(10, Math.ceil(max / 10) * 10)] : [0, "auto"]}
          />
          <Tooltip
            cursor={{ stroke: "var(--border-strong)" }}
            contentStyle={{
              background: "var(--popover)",
              border: "1px solid var(--border-strong)",
              boxShadow: "var(--shadow-md-v)",
              borderRadius: 10,
              fontSize: 12,
              color: "var(--popover-foreground)",
            }}
            labelFormatter={(t) => formatTime(Number(t))}
            formatter={(v, name) => {
              const idx = Number(String(name).slice(1))
              return [formatValue(Number(v), unit), labels[idx] ?? String(name)]
            }}
          />
          {queries.length > 1 && (
            <Legend
              verticalAlign="bottom"
              height={24}
              iconType="plainline"
              formatter={(value: string) => <span className="text-foreground text-xs">{labels[Number(value.slice(1))] ?? value}</span>}
            />
          )}
          {threshold !== undefined && <ReferenceLine y={threshold} stroke="var(--destructive-foreground)" strokeDasharray="4 4" />}
          {queries.map((_, i) => (
            <Line
              key={i}
              dataKey={`s${i}`}
              type="monotone"
              stroke={SERIES_COLORS[i % SERIES_COLORS.length]}
              strokeWidth={2}
              dot={false}
              activeDot={{ r: 4, strokeWidth: 2, stroke: "var(--card)" }}
              connectNulls
              isAnimationActive={false}
            />
          ))}
        </LineChart>
      </ResponsiveContainer>
    </div>
  )
}
