"use client"

import { useMemo, useState } from "react"
import { ListChecks, Play, Square } from "lucide-react"

import { Button } from "@/components/ui/button"
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select"
import { CellLink, CellText, DataTable, type Column } from "@/components/console/data-table"
import { EmptyState } from "@/components/console/empty-state"
import { PageHeader } from "@/components/console/page-header"
import { TimeAgo } from "@/components/console/time-ago"
import { useApi } from "@/lib/hooks"
import type { EcsTask } from "@/lib/types"
import { TASKS_PATH, TaskDefLink, TaskStatusBadge, serviceHref, shortId, taskHref, taskTransitional } from "./common"
import { StopTaskDialog } from "./dialogs"
import { RunTaskDialog } from "./run-task-dialog"

const STATUSES = ["all", "active", "RUNNING", "PROVISIONING", "STOPPED"] as const
const STATUS_LABEL: Record<string, string> = {
  all: "All statuses",
  active: "Running and provisioning",
  RUNNING: "Running",
  PROVISIONING: "Provisioning",
  STOPPED: "Stopped",
}

const columns: Column<EcsTask>[] = [
  {
    id: "id",
    header: "Task",
    cell: (t) => (
      <CellLink href={taskHref(t.id)} mono title={t.id}>
        {shortId(t.id)}
      </CellLink>
    ),
    value: (t) => t.id,
  },
  { id: "status", header: "Last status", cell: (t) => <TaskStatusBadge status={t.last_status} />, value: (t) => t.last_status },
  {
    id: "group",
    header: "Group",
    cell: (t) =>
      t.service ? (
        <CellLink href={serviceHref(t.service)} max="16rem">
          {`service:${t.service}`}
        </CellLink>
      ) : (
        <span className="text-muted-foreground text-sm">standalone</span>
      ),
    value: (t) => t.service || "standalone",
  },
  { id: "td", header: "Task definition", cell: (t) => <TaskDefLink value={t.task_definition} />, value: (t) => t.task_definition },
  {
    id: "ip",
    header: "Private IP",
    cell: (t) => <CellText mono>{t.private_ip}</CellText>,
    value: (t) => t.private_ip,
    hideBelow: "sm",
  },
  { id: "started", header: "Started", cell: (t) => <TimeAgo value={t.started_at ?? t.created_at} />, value: (t) => t.started_at ?? t.created_at, hideBelow: "md" },
  {
    id: "reason",
    header: "Stopped reason",
    cell: (t) =>
      t.stop_reason ? (
        <span className="text-muted-foreground line-clamp-2 max-w-72 text-xs" title={t.stop_reason}>
          {t.stop_reason}
        </span>
      ) : (
        <span className="text-muted-foreground">-</span>
      ),
    value: (t) => t.stop_reason ?? "",
    hideBelow: "lg",
  },
]

export function TasksList() {
  const { data, error, isLoading, isValidating, mutate } = useApi<EcsTask[]>(TASKS_PATH, {
    refreshInterval: (d) => ((d ?? []).some(taskTransitional) ? 2000 : 10_000),
  })
  const [status, setStatus] = useState<string>("active")
  const [selected, setSelected] = useState<string[]>([])
  const [stopping, setStopping] = useState<EcsTask | null>(null)
  const [running, setRunning] = useState(false)

  const rows = useMemo(() => {
    const all = data ?? []
    if (status === "all") return all
    if (status === "active") return all.filter((t) => t.last_status !== "STOPPED")
    return all.filter((t) => t.last_status === status)
  }, [data, status])
  const counts = useMemo(() => {
    const c: Record<string, number> = {}
    for (const t of data ?? []) c[t.last_status] = (c[t.last_status] ?? 0) + 1
    return c
  }, [data])
  const sel = rows.find((t) => t.id === selected[0])

  return (
    <div className="flex flex-col gap-4">
      <PageHeader
        title="Tasks"
        description="Running containers started by services or run as standalone tasks. Stopped tasks stay visible for an hour."
        breadcrumbs={[{ label: "ECS", href: "/ecs/" }, { label: "Tasks" }]}
      />
      <DataTable
        title="Tasks"
        data={data ? rows : undefined}
        columns={columns}
        rowId={(t) => t.id}
        loading={isLoading}
        error={error}
        onRefresh={() => mutate()}
        refreshing={isValidating}
        selection="single"
        selected={selected}
        onSelectedChange={setSelected}
        searchPlaceholder="Find tasks by ID, service, task definition or IP"
        defaultSort={{ id: "started", desc: true }}
        filters={
          <Select value={status} onValueChange={(v) => v && setStatus(v)}>
            <SelectTrigger size="sm" className="h-8 w-56" aria-label="Status filter">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {STATUSES.map((s) => (
                <SelectItem key={s} value={s}>
                  {STATUS_LABEL[s]}
                  {s !== "all" && s !== "active" && counts[s] ? <span className="text-muted-foreground text-xs">({counts[s]})</span> : null}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        }
        actions={
          <>
            <Button variant="outline" size="sm" disabled={!sel || sel.last_status === "STOPPED"} onClick={() => sel && setStopping(sel)}>
              <Square /> Stop
            </Button>
            <Button size="sm" onClick={() => setRunning(true)}>
              <Play /> Run task
            </Button>
          </>
        }
        empty={
          (data?.length ?? 0) > 0 ? (
            <EmptyState
              icon={ListChecks}
              title="No tasks match this filter"
              action={
                <Button variant="outline" size="sm" onClick={() => setStatus("all")}>
                  Show all tasks
                </Button>
              }
            />
          ) : (
            <EmptyState
              icon={ListChecks}
              title="No tasks"
              description="Run a task definition once as a standalone task, or create a service to keep tasks running."
              action={
                <Button size="sm" onClick={() => setRunning(true)}>
                  <Play /> Run task
                </Button>
              }
            />
          )
        }
      />
      <StopTaskDialog task={stopping} onClose={() => setStopping(null)} />
      <RunTaskDialog open={running} onClose={() => setRunning(false)} />
    </div>
  )
}
