"use client"

import { useEffect, useLayoutEffect, useRef, useState } from "react"
import Link from "next/link"
import { AlertCircle, ArrowLeft, Info, Loader2, RefreshCw, ScrollText, Square } from "lucide-react"

import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert"
import { Button } from "@/components/ui/button"
import { Label } from "@/components/ui/label"
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select"
import { Switch } from "@/components/ui/switch"
import { TerminalPane, term } from "@/components/console/code-block"
import { CopyableText } from "@/components/console/copy-button"
import { EmptyState } from "@/components/console/empty-state"
import { ErrorState } from "@/components/console/error-state"
import { KeyValueGrid } from "@/components/console/key-value"
import { DetailSkeleton } from "@/components/console/loading"
import { PageHeader } from "@/components/console/page-header"
import { Section } from "@/components/console/section"
import { TimeAgo } from "@/components/console/time-ago"
import { API_BASE, ApiError, seg } from "@/lib/api"
import { formatDate, formatDuration, formatMemoryMB } from "@/lib/format"
import { useApi, useQueryParam } from "@/lib/hooks"
import type { EcsService, EcsTask, EcsTaskDefinition, EcsTaskLogs } from "@/lib/types"
import { cn } from "@/lib/utils"
import {
  HealthBadge,
  SERVICES_PATH,
  TASKS_PATH,
  TASK_DEFS_PATH,
  TaskDefLink,
  TaskStatusBadge,
  formatCpu,
  joinCommand,
  parseTdKey,
  serviceHref,
  shortId,
  targetGroupHref,
  taskTransitional,
  useTargetHealth,
} from "./common"
import { StopTaskDialog } from "./dialogs"

export function TaskDetail() {
  const id = useQueryParam("id")
  const { data: task, error, isLoading, isValidating, mutate } = useApi<EcsTask>(id ? `${TASKS_PATH}/${seg(id)}` : null, {
    refreshInterval: (d) => (d && taskTransitional(d) ? 2000 : d?.last_status === "STOPPED" ? 0 : 10_000),
  })
  const { family, revision } = parseTdKey(task?.task_definition ?? "")
  const td = useApi<EcsTaskDefinition>(revision ? `${TASK_DEFS_PATH}/${seg(`${family}:${revision}`)}` : null, { revalidateOnFocus: false })
  const svc = useApi<EcsService>(task?.service ? `${SERVICES_PATH}/${seg(task.service)}` : null, { revalidateOnFocus: false })
  const tg = svc.data?.load_balancer?.target_group
  const health = useTargetHealth(task?.last_status === "RUNNING" ? tg : null)
  const [stopping, setStopping] = useState(false)

  const crumbs = [{ label: "ECS", href: "/ecs/" }, { label: "Tasks", href: "/ecs/tasks/" }, { label: id ? shortId(id) : "Task" }]

  if (!id) {
    return (
      <>
        <PageHeader title="Task" breadcrumbs={crumbs} />
        <EmptyState title="No task selected" description="Open a task from the tasks list." action={<BackButton />} />
      </>
    )
  }
  if (error && !task) {
    const notFound = error instanceof ApiError && error.status === 404
    return (
      <div className="flex flex-col gap-4">
        <PageHeader title={shortId(id)} breadcrumbs={crumbs} />
        {notFound ? (
          <Section>
            <EmptyState
              icon={AlertCircle}
              title="Task not found"
              description={`Task ${id} does not exist. Stopped tasks are removed an hour after they stop.`}
              action={<BackButton />}
            />
          </Section>
        ) : (
          <ErrorState error={error} onRetry={() => mutate()} />
        )}
      </div>
    )
  }
  if (isLoading || !task) return <DetailSkeleton />

  const stopped = task.last_status === "STOPPED"
  const userStop = task.stop_reason === "Task stopped by user" || task.stop_reason === "Service deleted" || task.stop_reason?.startsWith("Deployment:")
  const ports = Object.entries(task.public_ports ?? {})
  const host = publicHost()
  const runFor = task.started_at ? ((task.stopped_at ? new Date(task.stopped_at).getTime() : Date.now()) - new Date(task.started_at).getTime()) / 1000 : null

  return (
    <div className="flex flex-col gap-4">
      <PageHeader
        title={`Task ${shortId(task.id)}`}
        badge={<TaskStatusBadge status={task.last_status} />}
        description={
          task.service ? (
            <>
              Started by service{" "}
              <Link href={serviceHref(task.service)} className="text-primary hover:underline">
                {task.service}
              </Link>
            </>
          ) : (
            "Standalone task"
          )
        }
        breadcrumbs={crumbs}
        actions={
          <>
            <Button variant="outline" size="sm" onClick={() => mutate()} disabled={isValidating}>
              <RefreshCw className={cn(isValidating && "animate-spin")} />
              Refresh
            </Button>
            <Button variant="outline" size="sm" onClick={() => setStopping(true)} disabled={stopped}>
              <Square /> Stop task
            </Button>
          </>
        }
      />

      {task.stop_reason && (
        <Alert variant={userStop || task.exit_code === 0 ? "info" : "destructive"}>
          {userStop || task.exit_code === 0 ? <Info /> : <AlertCircle />}
          <AlertTitle>Stopped reason</AlertTitle>
          <AlertDescription>
            <span className="font-mono text-[13px] break-words">{task.stop_reason}</span>
            {task.exit_code !== undefined && task.exit_code !== null && <span> (exit code {task.exit_code})</span>}
          </AlertDescription>
        </Alert>
      )}

      <Section title="Task summary">
        <KeyValueGrid
          columns={3}
          items={[
            { label: "Task ID", value: <CopyableText value={task.id} /> },
            { label: "Last status", value: <TaskStatusBadge status={task.last_status} /> },
            { label: "Desired status", value: <TaskStatusBadge status={task.desired_status} /> },
            { label: "Task definition", value: <TaskDefLink value={task.task_definition} /> },
            {
              label: "Service",
              value: task.service ? (
                <Link href={serviceHref(task.service)} className="text-primary hover:underline">
                  {task.service}
                </Link>
              ) : (
                "Standalone"
              ),
            },
            {
              label: "Health",
              value: tg ? (
                <span className="flex flex-wrap items-center gap-2">
                  {stopped ? "-" : <HealthBadge target={health.get(task.id)} />}
                  <Link href={targetGroupHref(tg)} className="text-primary text-xs hover:underline">
                    {tg}
                  </Link>
                </span>
              ) : (
                "No load balancer"
              ),
            },
            { label: "Created", value: formatDate(task.created_at) },
            { label: "Started", value: task.started_at ? <span>{formatDate(task.started_at)} (<TimeAgo value={task.started_at} />)</span> : "" },
            { label: "Stopped", value: task.stopped_at ? formatDate(task.stopped_at) : "" },
            { label: stopped ? "Ran for" : "Uptime", value: runFor !== null ? formatDuration(runFor) : "" },
            { label: "Exit code", value: task.exit_code !== undefined && task.exit_code !== null ? String(task.exit_code) : "" },
            { label: "Container ID", value: task.container_id ? <CopyableText value={task.container_id} display={task.container_id.slice(0, 12)} /> : "" },
            { label: "Task ARN", value: <CopyableText value={task.arn} />, wide: true },
          ]}
        />
      </Section>

      <Section title="Container">
        {td.error ? (
          <ErrorState error={td.error} onRetry={() => td.mutate()} />
        ) : (
          <KeyValueGrid
            columns={3}
            items={[
              { label: "Image", value: td.data ? <CopyableText value={td.data.image} /> : "", wide: true },
              { label: "CPU", value: td.data ? formatCpu(td.data.cpu) : "" },
              { label: "Memory", value: td.data ? formatMemoryMB(td.data.memory_mb) : "" },
              { label: "Container port", value: td.data?.container_port ? `${td.data.container_port}/tcp` : td.data ? "None" : "" },
              { label: "Command", value: td.data ? (td.data.command?.length ? <CopyableText value={joinCommand(td.data.command)} /> : "Image default") : "" },
              {
                label: "Environment",
                value: td.data ? `${Object.keys(td.data.environment ?? {}).length} variables, ${(td.data.secrets ?? []).length} secrets` : "",
              },
            ]}
          />
        )}
      </Section>

      <Section title="Networking">
        <KeyValueGrid
          columns={3}
          items={[
            { label: "Private IP", value: task.private_ip ? <CopyableText value={task.private_ip} /> : "" },
            {
              label: "VPC",
              value: (
                <Link href="/vpc/" className="text-primary font-mono text-[13px] hover:underline">
                  {task.vpc_id}
                </Link>
              ),
            },
            {
              label: "Subnet",
              value: (
                <Link href="/vpc/subnets/" className="text-primary font-mono text-[13px] hover:underline">
                  {task.subnet_id}
                </Link>
              ),
            },
            { label: "DNS names", value: <span className="font-mono text-[13px] break-all">{[task.id, task.service && `${task.service}.ecs.internal`].filter(Boolean).join(", ")}</span>, wide: true },
            {
              label: "Public ports",
              wide: true,
              value: ports.length ? (
                <span className="flex flex-wrap gap-x-3 gap-y-1">
                  {ports.map(([k, p]) => (
                    <a key={k} href={`http://${host}:${p}`} target="_blank" rel="noreferrer" className="text-primary font-mono text-[13px] hover:underline">
                      {k} → {host}:{p}
                    </a>
                  ))}
                </span>
              ) : task.service ? (
                "Service tasks are not published on the host; reach them through the service endpoint or a load balancer."
              ) : (
                "None"
              ),
            },
          ]}
        />
      </Section>

      <TaskLogs task={task} />

      <StopTaskDialog task={stopping ? task : null} onClose={() => setStopping(false)} />
    </div>
  )
}

function publicHost() {
  try {
    return new URL(API_BASE || window.location.origin).hostname || "localhost"
  } catch {
    return "localhost"
  }
}

const TAILS = [100, 500, 1000, 5000]

/** Splits a Docker log line "2026-09-29T10:30:32.640600645Z message" into time and message. */
function splitLine(line: string): [string, string] {
  const m = /^(\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2})(\.\d+)?Z\s?(.*)$/.exec(line)
  if (!m) return ["", line]
  const d = new Date(`${m[1]}Z`)
  const pad = (n: number) => String(n).padStart(2, "0")
  const ms = (m[2] ?? ".000").slice(1, 4).padEnd(3, "0")
  return [`${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${pad(d.getHours())}:${pad(d.getMinutes())}:${pad(d.getSeconds())}.${ms}`, m[3]]
}

function TaskLogs({ task }: { task: EcsTask }) {
  const [tail, setTail] = useState(500)
  const [live, setLive] = useState(task.last_status === "RUNNING")
  const [wrap, setWrap] = useState(true)
  const [atBottom, setAtBottom] = useState(true)
  const scroller = useRef<HTMLDivElement>(null)
  const logs = useApi<EcsTaskLogs>(`${TASKS_PATH}/${seg(task.id)}/logs`, {
    query: { tail },
    refreshInterval: live && task.last_status !== "STOPPED" ? 3000 : 0,
    revalidateOnFocus: !live,
  })
  const lines = (logs.data?.output ?? "").split("\n").filter((l, i, a) => l || i < a.length - 1)

  useLayoutEffect(() => {
    const el = scroller.current
    if (el && atBottom) el.scrollTop = el.scrollHeight
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [logs.data])

  // TerminalPane forwards its ref to the scrolling body; track whether the user scrolled up.
  const hasLines = lines.length > 0
  useEffect(() => {
    const el = scroller.current
    if (!el) return
    const onScroll = () => setAtBottom(el.scrollHeight - el.scrollTop - el.clientHeight < 40)
    el.addEventListener("scroll", onScroll)
    return () => el.removeEventListener("scroll", onScroll)
  }, [hasLines, logs.error])

  return (
    <Section
      title="Logs"
      description="Container stdout and stderr"
      actions={
        <>
          <Select value={String(tail)} onValueChange={(v) => v && setTail(Number(v))}>
            <SelectTrigger size="sm" aria-label="Number of lines">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {TAILS.map((t) => (
                <SelectItem key={t} value={String(t)}>
                  Last {t} lines
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
          <div className="flex items-center gap-2">
            <Switch id="logs-wrap" checked={wrap} onCheckedChange={setWrap} />
            <Label htmlFor="logs-wrap" className="text-sm font-normal">
              Wrap
            </Label>
          </div>
          <div className="flex items-center gap-2">
            <Switch id="logs-live" checked={live} onCheckedChange={setLive} disabled={task.last_status === "STOPPED"} />
            <Label htmlFor="logs-live" className="text-sm font-normal">
              Live tail
            </Label>
          </div>
          <Button variant="outline" size="sm" onClick={() => logs.mutate()} disabled={logs.isValidating}>
            <RefreshCw className={cn(logs.isValidating && "animate-spin")} /> Refresh
          </Button>
        </>
      }
    >
      {logs.error ? (
        <ErrorState error={logs.error} onRetry={() => logs.mutate()} />
      ) : !logs.data ? (
        <div className="text-muted-foreground flex items-center gap-2 text-sm">
          <Loader2 className="size-4 animate-spin" /> Loading logs...
        </div>
      ) : lines.length === 0 ? (
        <EmptyState
          icon={ScrollText}
          title="No log output"
          description={
            task.container_id ? "The container has not written anything to stdout or stderr yet." : "The task never started a container, so there are no logs."
          }
        />
      ) : (
        <TerminalPane
          ref={scroller}
          title={`${shortId(task.id)} · ${lines.length} lines`}
          copyValue={logs.data.output ?? ""}
          height="min(60vh, 36rem)"
          bodyClassName="px-0 py-1"
        >
          {lines.map((l, i) => {
            const [ts, msg] = splitLine(l)
            return (
              <div key={i} className="flex gap-3 px-4 hover:bg-white/5">
                {ts && <span className={cn(term.muted, "shrink-0 select-none")}>{ts}</span>}
                <span className={cn("min-w-0", wrap ? "break-all whitespace-pre-wrap" : "whitespace-pre")}>{msg}</span>
              </div>
            )
          })}
        </TerminalPane>
      )}
    </Section>
  )
}

function BackButton() {
  return (
    <Button variant="outline" size="sm" asChild>
      <Link href="/ecs/tasks/">
        <ArrowLeft /> Back to tasks
      </Link>
    </Button>
  )
}
