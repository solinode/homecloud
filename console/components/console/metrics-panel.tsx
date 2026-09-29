"use client"

import { useState } from "react"
import Link from "next/link"
import { ExternalLink } from "lucide-react"

import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select"
import { MetricChart, type ChartQuery } from "@/components/console/metric-chart"
import { Section } from "@/components/console/section"
import type { Statistic } from "@/lib/types"

const RANGES = [
  { v: "60", label: "1 hour" },
  { v: "180", label: "3 hours" },
  { v: "720", label: "12 hours" },
  { v: "1440", label: "24 hours" },
]

const PERIODS = [
  { v: "60", label: "1 minute" },
  { v: "300", label: "5 minutes" },
]

export interface MetricsPanelChart {
  title: string
  /** subtitle, usually the unit ("Percent", "Count per period") */
  description?: string
  /** metric names (and optional legend labels) in the panel's namespace */
  metrics: { name: string; label?: string }[]
  /** statistic for this chart (default Average) */
  stat?: Statistic
}

/**
 * MetricsPanel is a service "Monitoring" tab: a time range and period picker
 * over a grid of CloudWatch charts for one resource (namespace + dimensions).
 */
export function MetricsPanel({
  namespace,
  dimensions,
  charts,
  note,
}: {
  namespace: string
  dimensions: Record<string, string>
  charts: MetricsPanelChart[]
  note?: React.ReactNode
}) {
  const [range, setRange] = useState("60")
  const [period, setPeriod] = useState("60")

  return (
    <div className="flex flex-col gap-4">
      <div className="flex flex-wrap items-center gap-2">
        <Select value={range} onValueChange={setRange}>
          <SelectTrigger size="sm" className="w-36" aria-label="Time range">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            {RANGES.map((r) => (
              <SelectItem key={r.v} value={r.v}>
                Last {r.label}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
        <Select value={period} onValueChange={setPeriod}>
          <SelectTrigger size="sm" className="w-40" aria-label="Period">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            {PERIODS.map((p) => (
              <SelectItem key={p.v} value={p.v}>
                Period: {p.label}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
        <Link
          href={`/cloudwatch/metrics/?namespace=${encodeURIComponent(namespace)}`}
          className="text-primary ml-auto inline-flex items-center gap-1 text-sm hover:underline"
        >
          View in CloudWatch <ExternalLink className="size-3.5" />
        </Link>
      </div>
      {note}
      <div className="grid grid-cols-1 gap-4 lg:grid-cols-2">
        {charts.map((c) => {
          const queries: ChartQuery[] = c.metrics.map((m) => ({ namespace, name: m.name, dimensions, label: m.label }))
          return (
            <Section key={c.title} title={c.title} description={c.description}>
              <MetricChart queries={queries} rangeMinutes={Number(range)} period={Number(period)} stat={c.stat ?? "Average"} height={220} refreshMs={30_000} />
            </Section>
          )
        })}
      </div>
    </div>
  )
}

/** Standard container charts (CPU, memory, network, disk) used by EC2, RDS and ElastiCache. */
export const CONTAINER_CHARTS: MetricsPanelChart[] = [
  { title: "CPU utilization", description: "Percent", metrics: [{ name: "CPUUtilization" }] },
  { title: "Memory utilization", description: "Percent", metrics: [{ name: "MemoryUtilization" }] },
  {
    title: "Network",
    description: "Bytes per period",
    metrics: [
      { name: "NetworkIn", label: "Network in" },
      { name: "NetworkOut", label: "Network out" },
    ],
  },
  {
    title: "Disk I/O",
    description: "Bytes per period",
    metrics: [
      { name: "DiskReadBytes", label: "Disk read" },
      { name: "DiskWriteBytes", label: "Disk write" },
    ],
  },
]
