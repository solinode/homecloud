"use client"

import { useState } from "react"
import Link from "next/link"
import { ExternalLink } from "lucide-react"

import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select"
import { MetricChart, type ChartQuery } from "@/components/console/metric-chart"
import { Section } from "@/components/console/section"

const NS = "HC/EC2"

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

export function InstanceMonitoring({ id }: { id: string }) {
  const [range, setRange] = useState("60")
  const [period, setPeriod] = useState("60")
  const dims = { InstanceId: id }
  const q = (name: string, label?: string): ChartQuery => ({ namespace: NS, name, dimensions: dims, label })

  const charts: { title: string; unit: string; queries: ChartQuery[] }[] = [
    { title: "CPU utilization", unit: "Percent", queries: [q("CPUUtilization")] },
    { title: "Memory utilization", unit: "Percent", queries: [q("MemoryUtilization")] },
    { title: "Network", unit: "Bytes per period", queries: [q("NetworkIn", "Network in"), q("NetworkOut", "Network out")] },
    { title: "Disk I/O", unit: "Bytes per period", queries: [q("DiskReadBytes", "Disk read"), q("DiskWriteBytes", "Disk write")] },
  ]

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
          href={`/cloudwatch/metrics/?namespace=${encodeURIComponent(NS)}`}
          className="text-primary ml-auto inline-flex items-center gap-1 text-sm hover:underline"
        >
          View in CloudWatch <ExternalLink className="size-3.5" />
        </Link>
      </div>
      <div className="grid grid-cols-1 gap-4 lg:grid-cols-2">
        {charts.map((c) => (
          <Section key={c.title} title={c.title} description={c.unit}>
            <MetricChart queries={c.queries} rangeMinutes={Number(range)} period={Number(period)} height={220} refreshMs={30_000} />
          </Section>
        ))}
      </div>
    </div>
  )
}
