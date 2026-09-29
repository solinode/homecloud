"use client"

import Link from "next/link"
import { AlertTriangle, ArrowRight, CheckCircle2, CircleHelp, Plus, ScrollText } from "lucide-react"

import { Button } from "@/components/ui/button"
import { Skeleton } from "@/components/ui/skeleton"
import { ErrorState } from "@/components/console/error-state"
import { MetricChart, type ChartQuery } from "@/components/console/metric-chart"
import { PageHeader } from "@/components/console/page-header"
import { Section } from "@/components/console/section"
import { TimeAgo } from "@/components/console/time-ago"
import { useApi } from "@/lib/hooks"
import type { Alarm, AlarmState, Instance, LogGroup } from "@/lib/types"
import { cn } from "@/lib/utils"

import { alarmCondition, AlarmStateBadge, describeContainerGroup, logGroupHref, useInstanceNames } from "./common"

const CARDS: { state: AlarmState; label: string; icon: typeof AlertTriangle; cls: string }[] = [
  { state: "ALARM", label: "In alarm", icon: AlertTriangle, cls: "text-red-600 dark:text-red-400" },
  { state: "OK", label: "OK", icon: CheckCircle2, cls: "text-emerald-600 dark:text-emerald-400" },
  { state: "INSUFFICIENT_DATA", label: "Insufficient data", icon: CircleHelp, cls: "text-slate-500 dark:text-slate-400" },
]

export function CloudWatchOverview() {
  const alarms = useApi<Alarm[]>("/api/v1/cloudwatch/alarms", { refreshInterval: 15_000 })
  const instances = useApi<Instance[]>("/api/v1/ec2/instances", { refreshInterval: 30_000 })
  const groups = useApi<LogGroup[]>("/api/v1/logs/groups")
  const names = useInstanceNames()

  const running = (instances.data ?? []).filter((i) => i.state === "running").slice(0, 5)
  const q = (name: string): ChartQuery[] =>
    running.map((i) => ({ namespace: "HC/EC2", name, dimensions: { InstanceId: i.id }, label: i.name || i.id }))
  const inAlarm = (alarms.data ?? []).filter((a) => a.state === "ALARM")
  const totalRunning = (instances.data ?? []).filter((i) => i.state === "running").length

  return (
    <div className="flex flex-col gap-6">
      <PageHeader
        breadcrumbs={[{ label: "CloudWatch" }, { label: "Overview" }]}
        title="CloudWatch"
        description="Monitor your resources: metrics are sampled every 30 seconds, alarms watch a metric and notify webhooks or SNS topics, and logs collect application and container output."
        actions={
          <Button size="sm" asChild>
            <Link href="/cloudwatch/alarms/?create=1">
              <Plus /> Create alarm
            </Link>
          </Button>
        }
      />

      <div className="grid grid-cols-1 gap-4 sm:grid-cols-3">
        {CARDS.map((c) => {
          const n = (alarms.data ?? []).filter((a) => a.state === c.state).length
          return (
            <Link
              key={c.state}
              href={`/cloudwatch/alarms/?state=${c.state}`}
              className="bg-card flex items-center justify-between gap-3 rounded-lg border p-4 shadow-xs transition-shadow hover:shadow-md"
            >
              <div className="flex flex-col gap-1">
                <span className="text-muted-foreground text-sm font-medium">{c.label}</span>
                {alarms.isLoading ? (
                  <Skeleton className="h-8 w-12" />
                ) : (
                  <span className={cn("text-3xl font-semibold tabular-nums", n > 0 || c.state !== "ALARM" ? c.cls : "text-foreground")}>{alarms.error ? "-" : n}</span>
                )}
              </div>
              <c.icon className={cn("size-8 opacity-80", c.cls)} />
            </Link>
          )
        })}
      </div>

      <Section
        flush
        title="Alarms in ALARM state"
        actions={
          <Link href="/cloudwatch/alarms/" className="text-primary text-sm hover:underline">
            View all alarms
          </Link>
        }
      >
        {alarms.error ? (
          <div className="p-4">
            <ErrorState error={alarms.error} onRetry={() => alarms.mutate()} />
          </div>
        ) : alarms.isLoading ? (
          <div className="flex flex-col gap-2 p-4">
            <Skeleton className="h-5 w-full" />
            <Skeleton className="h-5 w-2/3" />
          </div>
        ) : inAlarm.length === 0 ? (
          <p className="text-muted-foreground flex items-center gap-2 p-4 text-sm">
            <CheckCircle2 className="size-4 text-emerald-600 dark:text-emerald-400" />
            No alarms in ALARM state.
            {alarms.data?.length === 0 && (
              <Link href="/cloudwatch/alarms/?create=1" className="text-primary hover:underline">
                Create your first alarm
              </Link>
            )}
          </p>
        ) : (
          <ul className="divide-y">
            {inAlarm.map((a) => (
              <li key={a.name} className="flex flex-col gap-1 px-4 py-3 sm:flex-row sm:items-center sm:justify-between">
                <div className="min-w-0">
                  <div className="flex items-center gap-2">
                    <AlarmStateBadge state={a.state} />
                    <Link href={`/cloudwatch/alarms/?name=${encodeURIComponent(a.name)}`} className="text-primary truncate font-medium hover:underline">
                      {a.name}
                    </Link>
                  </div>
                  <p className="text-muted-foreground mt-1 truncate text-xs">{alarmCondition(a)}</p>
                </div>
                <span className="text-muted-foreground shrink-0 text-xs">
                  since <TimeAgo value={a.state_updated_at} />
                </span>
              </li>
            ))}
          </ul>
        )}
      </Section>

      <Section
        title="Resource metrics"
        description={
          totalRunning > 5
            ? `Last hour for the first 5 of ${totalRunning} running EC2 instances`
            : "Last hour for your running EC2 instances"
        }
        actions={
          <Link href="/cloudwatch/metrics/?namespace=HC/EC2" className="text-primary flex items-center gap-1 text-sm hover:underline">
            Explore metrics <ArrowRight className="size-3.5" />
          </Link>
        }
      >
        {instances.error ? (
          <ErrorState error={instances.error} onRetry={() => instances.mutate()} />
        ) : instances.isLoading ? (
          <div className="grid grid-cols-1 gap-6 lg:grid-cols-2">
            <Skeleton className="h-56" />
            <Skeleton className="h-56" />
          </div>
        ) : running.length === 0 ? (
          <p className="text-muted-foreground py-6 text-center text-sm">
            No running instances.{" "}
            <Link href="/ec2/launch/" className="text-primary hover:underline">
              Launch an instance
            </Link>{" "}
            to see CPU and memory metrics here.
          </p>
        ) : (
          <div className="grid grid-cols-1 gap-6 lg:grid-cols-2">
            <div className="flex flex-col gap-2">
              <h3 className="text-sm font-medium">CPU utilization (%)</h3>
              <MetricChart queries={q("CPUUtilization")} rangeMinutes={60} period={60} height={240} />
            </div>
            <div className="flex flex-col gap-2">
              <h3 className="text-sm font-medium">Memory utilization (%)</h3>
              <MetricChart queries={q("MemoryUtilization")} rangeMinutes={60} period={60} height={240} />
            </div>
          </div>
        )}
      </Section>

      <Section
        flush
        title="Log groups"
        actions={
          <Link href="/cloudwatch/logs/" className="text-primary text-sm hover:underline">
            View all log groups
          </Link>
        }
      >
        {groups.error ? (
          <div className="p-4">
            <ErrorState error={groups.error} onRetry={() => groups.mutate()} />
          </div>
        ) : groups.isLoading ? (
          <div className="flex flex-col gap-2 p-4">
            <Skeleton className="h-5 w-full" />
            <Skeleton className="h-5 w-2/3" />
          </div>
        ) : !groups.data?.length ? (
          <p className="text-muted-foreground p-4 text-sm">No log groups yet.</p>
        ) : (
          <ul className="divide-y">
            {[...groups.data]
              .sort((a, b) => b.created_at.localeCompare(a.created_at))
              .slice(0, 8)
              .map((g) => (
                <li key={g.name} className="flex items-center justify-between gap-3 px-4 py-2.5">
                  <div className="flex min-w-0 items-center gap-2">
                    <ScrollText className="text-muted-foreground size-4 shrink-0" />
                    <Link href={logGroupHref(g.name)} className="text-primary truncate font-mono text-[13px] hover:underline">
                      {g.name}
                    </Link>
                    {g.source === "container" && (
                      <span className="text-muted-foreground hidden truncate text-xs sm:inline">{describeContainerGroup(g.name, names)}</span>
                    )}
                  </div>
                  <span className="text-muted-foreground shrink-0 text-xs">
                    <TimeAgo value={g.created_at} />
                  </span>
                </li>
              ))}
          </ul>
        )}
      </Section>
    </div>
  )
}
