"use client"

import { useMemo, useState } from "react"
import Link from "next/link"
import { useRouter } from "next/navigation"
import { AlertCircle, ArrowLeft, History, ListChecks, Loader2, Pencil, RefreshCw, Square, Trash2 } from "lucide-react"

import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert"
import { Button } from "@/components/ui/button"
import { Label } from "@/components/ui/label"
import { Switch } from "@/components/ui/switch"
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs"
import { ActionsMenu } from "@/components/console/actions-menu"
import { CopyableText } from "@/components/console/copy-button"
import { CellLink, CellText, DataTable, type Column } from "@/components/console/data-table"
import { EmptyState } from "@/components/console/empty-state"
import { ErrorState } from "@/components/console/error-state"
import { KeyValueGrid } from "@/components/console/key-value"
import { DetailSkeleton } from "@/components/console/loading"
import { PageHeader } from "@/components/console/page-header"
import { Section } from "@/components/console/section"
import { StatTile } from "@/components/console/stat-tile"
import { StatusBadge } from "@/components/console/status-badge"
import { TagList } from "@/components/console/tags-editor"
import { TimeAgo } from "@/components/console/time-ago"
import { ApiError, seg } from "@/lib/api"
import { formatDate, formatMemoryMB } from "@/lib/format"
import { useApi, useQueryParam, useSetQueryParam } from "@/lib/hooks"
import type { EcsService, EcsTask, EcsTaskDefinition } from "@/lib/types"
import { cn } from "@/lib/utils"
import {
  HealthBadge,
  SERVICES_PATH,
  ServiceStatusBadge,
  TASK_DEFS_PATH,
  TaskDefLink,
  TaskStatusBadge,
  deploymentInProgress,
  formatCpu,
  parseTdKey,
  shortId,
  targetGroupHref,
  taskHref,
  useTargetHealth,
  type TargetGroupTarget,
} from "./common"
import { DeleteServiceDialog, StopTaskDialog, UpdateServiceDialog } from "./dialogs"

const TABS = ["tasks", "events", "deployments", "configuration"] as const
type Tab = (typeof TABS)[number]

export function ServiceDetail() {
  const router = useRouter()
  const name = useQueryParam("name")
  const tabParam = useQueryParam("tab")
  const setParam = useSetQueryParam()
  const tab: Tab = (TABS as readonly string[]).includes(tabParam) ? (tabParam as Tab) : "tasks"

  const { data: svc, error, isLoading, isValidating, mutate } = useApi<EcsService>(name ? `${SERVICES_PATH}/${seg(name)}` : null, {
    refreshInterval: (d) => (d && deploymentInProgress(d) ? 2000 : 10_000),
  })
  const [updating, setUpdating] = useState(false)
  const [deleting, setDeleting] = useState(false)

  const crumbs = [{ label: "ECS", href: "/ecs/" }, { label: "Services", href: "/ecs/" }, { label: name || "Service" }]

  if (!name) {
    return (
      <>
        <PageHeader title="Service" breadcrumbs={crumbs} />
        <EmptyState title="No service selected" description="Open a service from the services list." action={<BackButton />} />
      </>
    )
  }
  if (error && !svc) {
    const notFound = error instanceof ApiError && error.status === 404
    return (
      <div className="flex flex-col gap-4">
        <PageHeader title={name} breadcrumbs={crumbs} />
        {notFound ? (
          <Section>
            <EmptyState
              icon={AlertCircle}
              title="Service not found"
              description={`Service ${name} does not exist. Deleted services disappear once their tasks have stopped.`}
              action={<BackButton />}
            />
          </Section>
        ) : (
          <ErrorState error={error} onRetry={() => mutate()} />
        )}
      </div>
    )
  }
  if (isLoading || !svc) return <DetailSkeleton />

  const active = svc.status === "ACTIVE"
  const inProgress = deploymentInProgress(svc)

  return (
    <div className="flex flex-col gap-4">
      <PageHeader
        title={svc.name}
        badge={<ServiceStatusBadge status={svc.status} />}
        description={<CopyableText value={svc.endpoint} />}
        breadcrumbs={crumbs}
        actions={
          <>
            <Button variant="outline" size="sm" onClick={() => mutate()} disabled={isValidating}>
              <RefreshCw className={cn(isValidating && "animate-spin")} />
              Refresh
            </Button>
            <ActionsMenu
              disabled={!active}
              items={[
                { label: "Update service", icon: <Pencil />, onSelect: () => setUpdating(true) },
                { separator: true },
                { label: "Delete service", icon: <Trash2 />, destructive: true, onSelect: () => setDeleting(true) },
              ]}
            />
            <Button size="sm" onClick={() => setUpdating(true)} disabled={!active}>
              <Pencil /> Update service
            </Button>
          </>
        }
      />

      {svc.status === "DRAINING" ? (
        <Alert variant="warning">
          <Loader2 className="animate-spin" />
          <AlertTitle>Deleting</AlertTitle>
          <AlertDescription>The service is being deleted. It disappears once all of its tasks have stopped.</AlertDescription>
        </Alert>
      ) : inProgress ? (
        <Alert variant="info">
          <Loader2 className="animate-spin" />
          <AlertTitle>Deployment in progress</AlertTitle>
          <AlertDescription>This page refreshes automatically.</AlertDescription>
        </Alert>
      ) : null}

      <div className="grid grid-cols-2 gap-4 lg:grid-cols-4">
        <StatTile
          label="Running tasks"
          value={svc.running_count}
          unit={`/ ${svc.desired_count}`}
          tone={svc.running_count === svc.desired_count && svc.pending_count === 0 ? "success" : "warning"}
          caption={`${svc.desired_count} desired`}
        />
        <StatTile label="Pending tasks" value={svc.pending_count} caption="Provisioning or starting" />
        <StatTile
          label="Task definition"
          value={parseTdKey(svc.task_definition).revision || "-"}
          unit="rev"
          caption={<TaskDefLink value={svc.task_definition} />}
        />
        <StatTile
          label="Load balancing"
          value={svc.load_balancer ? "On" : "Off"}
          caption={
            svc.load_balancer ? (
              <Link href={targetGroupHref(svc.load_balancer.target_group)} className="text-primary hover:underline">
                {svc.load_balancer.target_group}:{svc.load_balancer.container_port}
              </Link>
            ) : (
              "No target group"
            )
          }
        />
      </div>

      <Tabs value={tab} onValueChange={(v) => setParam("tab", v === "tasks" ? null : v)}>
        <TabsList>
          <TabsTrigger value="tasks">Tasks</TabsTrigger>
          <TabsTrigger value="events">Events</TabsTrigger>
          <TabsTrigger value="deployments">Deployments</TabsTrigger>
          <TabsTrigger value="configuration">Configuration</TabsTrigger>
        </TabsList>
        <TabsContent value="tasks">
          <TasksTab svc={svc} refreshing={isValidating} onRefresh={() => mutate()} />
        </TabsContent>
        <TabsContent value="events">
          <EventsTab svc={svc} />
        </TabsContent>
        <TabsContent value="deployments">
          <DeploymentsTab svc={svc} />
        </TabsContent>
        <TabsContent value="configuration">
          <ConfigurationTab svc={svc} />
        </TabsContent>
      </Tabs>

      <UpdateServiceDialog service={updating ? svc : null} onClose={() => setUpdating(false)} />
      <DeleteServiceDialog service={deleting ? svc : null} onClose={() => setDeleting(false)} onDeleted={() => router.push("/ecs/")} />
    </div>
  )
}

function BackButton() {
  return (
    <Button variant="outline" size="sm" asChild>
      <Link href="/ecs/">
        <ArrowLeft /> Back to services
      </Link>
    </Button>
  )
}

function taskColumns(health: Map<string, TargetGroupTarget> | null): Column<EcsTask>[] {
  const cols: Column<EcsTask>[] = [
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
  ]
  if (health) {
    cols.push({
      id: "health",
      header: "Health",
      cell: (t) => (t.last_status === "RUNNING" ? <HealthBadge target={health.get(t.id)} /> : <span className="text-muted-foreground">-</span>),
      value: (t) => health.get(t.id)?.health ?? "",
    })
  }
  cols.push(
    { id: "td", header: "Task definition", cell: (t) => <TaskDefLink value={t.task_definition} />, value: (t) => t.task_definition, hideBelow: "md" },
    {
      id: "ip",
      header: "Private IP",
      cell: (t) => <CellText mono>{t.private_ip}</CellText>,
      value: (t) => t.private_ip,
      hideBelow: "sm",
    },
    { id: "started", header: "Started", cell: (t) => <TimeAgo value={t.started_at ?? t.created_at} />, value: (t) => t.started_at ?? t.created_at },
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
  )
  return cols
}

function TasksTab({ svc, refreshing, onRefresh }: { svc: EcsService; refreshing: boolean; onRefresh: () => void }) {
  const [showStopped, setShowStopped] = useState(false)
  const [selected, setSelected] = useState<string[]>([])
  const [stopping, setStopping] = useState<EcsTask | null>(null)
  const health = useTargetHealth(svc.load_balancer?.target_group)
  const columns = useMemo(() => taskColumns(svc.load_balancer ? health : null), [svc.load_balancer, health])

  const all = svc.tasks ?? []
  const stoppedCount = all.filter((t) => t.last_status === "STOPPED").length
  const rows = showStopped ? all : all.filter((t) => t.last_status !== "STOPPED")
  const sel = rows.find((t) => t.id === selected[0])

  return (
    <>
      <DataTable
        title="Tasks"
        data={rows}
        columns={columns}
        rowId={(t) => t.id}
        onRefresh={onRefresh}
        refreshing={refreshing}
        selection="single"
        selected={selected}
        onSelectedChange={setSelected}
        searchPlaceholder="Find tasks by ID, IP or status"
        defaultSort={{ id: "started", desc: true }}
        filters={
          <div className="flex items-center gap-2">
            <Switch id="show-stopped" checked={showStopped} onCheckedChange={setShowStopped} />
            <Label htmlFor="show-stopped" className="text-sm font-normal whitespace-nowrap">
              Show stopped{stoppedCount ? ` (${stoppedCount})` : ""}
            </Label>
          </div>
        }
        actions={
          <Button variant="outline" size="sm" disabled={!sel || sel.last_status === "STOPPED"} onClick={() => sel && setStopping(sel)}>
            <Square /> Stop task
          </Button>
        }
        empty={
          <EmptyState
            icon={ListChecks}
            title={all.length ? "No running tasks" : "No tasks"}
            description={
              svc.desired_count === 0
                ? "The desired count is 0. Update the service to start tasks."
                : "Tasks start within a few seconds. Check the Events tab if they keep stopping."
            }
          />
        }
      />
      <StopTaskDialog task={stopping} onClose={() => setStopping(null)} />
    </>
  )
}

function EventsTab({ svc }: { svc: EcsService }) {
  const events = [...(svc.events ?? [])].sort((a, b) => b.time.localeCompare(a.time))
  if (!events.length) {
    return (
      <Section>
        <EmptyState icon={History} title="No events" description="Service events record task starts, stops and deployments." />
      </Section>
    )
  }
  return (
    <Section title="Events" description="The 50 most recent service events, newest first.">
      <ol className="relative flex flex-col gap-0">
        {events.map((e, i) => {
          const bad = /fail|error|exited|disappeared/i.test(e.message)
          return (
            <li key={`${e.time}-${i}`} className="relative flex gap-3 pb-4 pl-5 last:pb-0">
              <span className={`absolute top-1.5 left-0 size-2.5 rounded-full ${bad ? "bg-destructive" : "bg-primary/70"}`} />
              {i < events.length - 1 && <span className="bg-border absolute top-4 bottom-0 left-[4.5px] w-px" />}
              <div className="flex min-w-0 flex-col gap-0.5 sm:flex-row sm:gap-4">
                <span className="text-muted-foreground w-44 shrink-0 text-xs tabular-nums sm:pt-0.5" title={e.time}>
                  {formatDate(e.time)}
                </span>
                <span className={`text-sm break-words ${bad ? "text-destructive" : ""}`}>{e.message}</span>
              </div>
            </li>
          )
        })}
      </ol>
    </Section>
  )
}

interface Deployment {
  key: string
  primary: boolean
  running: number
  pending: number
  started?: string
}

function DeploymentsTab({ svc }: { svc: EcsService }) {
  const deployments = useMemo(() => {
    const m = new Map<string, Deployment>()
    m.set(svc.task_definition, { key: svc.task_definition, primary: true, running: 0, pending: 0 })
    for (const t of svc.tasks ?? []) {
      if (t.last_status === "STOPPED") continue
      const d = m.get(t.task_definition) ?? { key: t.task_definition, primary: false, running: 0, pending: 0 }
      if (t.last_status === "RUNNING") d.running++
      else d.pending++
      if (!d.started || (t.created_at && t.created_at < d.started)) d.started = t.created_at
      m.set(t.task_definition, d)
    }
    return [...m.values()]
  }, [svc])
  const rolling = deployments.length > 1

  return (
    <DataTable
      title="Deployments"
      description="Derived from the revision each running task uses. The primary deployment is the service's current task definition."
      data={deployments}
      rowId={(d) => d.key}
      noSearch
      columns={[
        {
          id: "deployment",
          header: "Deployment",
          cell: (d) => <span className="font-medium whitespace-nowrap">{d.primary ? "Primary" : parseTdKey(d.key).redeploy ? "Replaced (forced)" : "Active"}</span>,
        },
        { id: "td", header: "Task definition", cell: (d) => <TaskDefLink value={d.key} /> },
        {
          id: "running",
          header: "Running",
          cell: (d) => (
            <span className="tabular-nums">
              {d.running}
              {d.primary && <span className="text-muted-foreground"> / {svc.desired_count}</span>}
            </span>
          ),
        },
        { id: "pending", header: "Pending", cell: (d) => <span className="tabular-nums">{d.pending}</span> },
        {
          id: "rollout",
          header: "Rollout",
          cell: (d) => {
            const done = d.primary && !rolling && d.running === svc.desired_count && d.pending === 0
            return !d.primary ? (
              <StatusBadge status="stopping" label="Draining" />
            ) : done ? (
              <StatusBadge status="completed" tone="success" label="Completed" />
            ) : (
              <StatusBadge status="updating" label="In progress" />
            )
          },
        },
      ]}
    />
  )
}

function ConfigurationTab({ svc }: { svc: EcsService }) {
  const { family, revision } = parseTdKey(svc.task_definition)
  const td = useApi<EcsTaskDefinition>(revision ? `${TASK_DEFS_PATH}/${seg(`${family}:${revision}`)}` : null, { revalidateOnFocus: false })
  const subnet = svc.subnet_id || (svc.tasks ?? []).find((t) => t.subnet_id)?.subnet_id
  return (
    <div className="flex flex-col gap-4">
      <Section title="Service">
        <KeyValueGrid
          columns={3}
          items={[
            { label: "Service name", value: svc.name },
            { label: "Status", value: <ServiceStatusBadge status={svc.status} /> },
            { label: "Desired tasks", value: String(svc.desired_count) },
            { label: "Service endpoint", value: <CopyableText value={svc.endpoint} /> },
            { label: "Created", value: formatDate(svc.created_at) },
            { label: "Launch type", value: "Fargate (container)" },
            { label: "Service ARN", value: <CopyableText value={svc.arn} />, wide: true },
          ]}
        />
      </Section>
      <Section title="Task definition">
        <KeyValueGrid
          columns={3}
          items={[
            { label: "Revision", value: <TaskDefLink value={svc.task_definition} /> },
            { label: "Image", value: td.data ? <CopyableText value={td.data.image} /> : "" },
            { label: "Status", value: td.data ? <StatusBadge status={td.data.status.toLowerCase()} /> : "" },
            { label: "CPU", value: td.data ? formatCpu(td.data.cpu) : "" },
            { label: "Memory", value: td.data ? formatMemoryMB(td.data.memory_mb) : "" },
            { label: "Container port", value: td.data?.container_port ? String(td.data.container_port) : "" },
          ]}
        />
      </Section>
      <Section title="Networking">
        <KeyValueGrid
          columns={3}
          items={[
            {
              label: "Subnet",
              value: subnet ? (
                <Link href="/vpc/subnets/" className="text-primary font-mono text-[13px] hover:underline">
                  {subnet}
                </Link>
              ) : (
                "Default subnet"
              ),
            },
            {
              label: "Security groups",
              value: svc.security_groups?.length ? (
                <span className="flex flex-wrap gap-x-3 gap-y-1">
                  {svc.security_groups.map((g) => (
                    <Link key={g} href={`/vpc/security-group/?id=${encodeURIComponent(g)}`} className="text-primary font-mono text-[13px] hover:underline">
                      {g}
                    </Link>
                  ))}
                </span>
              ) : (
                "None"
              ),
            },
            { label: "Service discovery", value: <span className="font-mono text-[13px]">{svc.endpoint}</span> },
          ]}
        />
      </Section>
      <Section title="Load balancing">
        {svc.load_balancer ? (
          <KeyValueGrid
            columns={3}
            items={[
              {
                label: "Target group",
                value: (
                  <Link href={targetGroupHref(svc.load_balancer.target_group)} className="text-primary hover:underline">
                    {svc.load_balancer.target_group}
                  </Link>
                ),
              },
              { label: "Container port", value: String(svc.load_balancer.container_port) },
              { label: "Registration", value: "Running tasks are registered automatically" },
            ]}
          />
        ) : (
          <p className="text-muted-foreground text-sm">This service is not attached to a load balancer.</p>
        )}
      </Section>
      <Section title="Tags">
        <TagList tags={svc.tags} />
      </Section>
    </div>
  )
}
