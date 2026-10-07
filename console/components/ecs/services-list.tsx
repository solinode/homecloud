"use client"

import { useState } from "react"
import Link from "next/link"
import { Container, Plus } from "lucide-react"

import { Button } from "@/components/ui/button"
import { ActionsMenu, type ActionItem } from "@/components/console/actions-menu"
import { CopyableText } from "@/components/console/copy-button"
import { CellLink, DataTable, type Column } from "@/components/console/data-table"
import { EmptyState } from "@/components/console/empty-state"
import { PageHeader } from "@/components/console/page-header"
import { TimeAgo } from "@/components/console/time-ago"
import { useApi } from "@/lib/hooks"
import type { EcsService } from "@/lib/types"
import { SERVICES_PATH, ServiceStatusBadge, TaskDefLink, serviceBusy, serviceHref, targetGroupHref } from "./common"
import { DeleteServiceDialog, UpdateServiceDialog } from "./dialogs"

export function TaskCounts({ s }: { s: Pick<EcsService, "desired_count" | "running_count" | "pending_count"> }) {
  const ok = s.running_count === s.desired_count && s.pending_count === 0
  return (
    <span className="text-sm whitespace-nowrap tabular-nums">
      <span className={ok ? "text-success font-medium" : "text-warning font-medium"}>{s.running_count}</span>
      <span className="text-muted-foreground"> / {s.desired_count} running</span>
      {s.pending_count > 0 && <span className="text-muted-foreground">, {s.pending_count} pending</span>}
    </span>
  )
}

const columns: Column<EcsService>[] = [
  {
    id: "name",
    header: "Service name",
    cell: (s) => (
      <CellLink href={serviceHref(s.name)} max="18rem">
        {s.name}
      </CellLink>
    ),
    value: (s) => s.name,
  },
  { id: "status", header: "Status", cell: (s) => <ServiceStatusBadge status={s.status} />, value: (s) => s.status },
  {
    id: "tasks",
    header: "Tasks",
    cell: (s) => <TaskCounts s={s} />,
    value: (s) => s.running_count,
  },
  { id: "td", header: "Task definition", cell: (s) => <TaskDefLink value={s.task_definition} />, value: (s) => s.task_definition },
  {
    id: "endpoint",
    header: "Service endpoint",
    cell: (s) => <CopyableText value={s.endpoint || `${s.name}.ecs.internal`} />,
    value: (s) => s.endpoint,
    hideBelow: "md",
  },
  {
    id: "lb",
    header: "Target group",
    cell: (s) =>
      s.load_balancer ? (
        <span className="flex items-center whitespace-nowrap">
          <CellLink href={targetGroupHref(s.load_balancer.target_group)} max="14rem">
            {s.load_balancer.target_group}
          </CellLink>
          <span className="text-muted-foreground font-mono text-[13px]">:{s.load_balancer.container_port}</span>
        </span>
      ) : (
        <span className="text-muted-foreground">-</span>
      ),
    value: (s) => s.load_balancer?.target_group ?? "",
    hideBelow: "lg",
  },
  { id: "created", header: "Created", cell: (s) => <TimeAgo value={s.created_at} />, value: (s) => s.created_at, hideBelow: "lg" },
]

export function ServicesList() {
  const { data, error, isLoading, isValidating, mutate } = useApi<EcsService[]>(SERVICES_PATH, {
    refreshInterval: (d) => ((d ?? []).some(serviceBusy) ? 2000 : 15_000),
  })
  const [selected, setSelected] = useState<string[]>([])
  const [deleting, setDeleting] = useState<EcsService | null>(null)
  const [updating, setUpdating] = useState<EcsService | null>(null)

  const sel = (data ?? []).find((s) => s.name === selected[0]) ?? null
  const items: ActionItem[] = [
    { label: "Update service", onSelect: () => sel && setUpdating(sel), disabled: !sel || sel.status !== "ACTIVE" },
    { separator: true },
    { label: "Delete service", destructive: true, onSelect: () => sel && setDeleting(sel), disabled: !sel || sel.status !== "ACTIVE" },
  ]

  return (
    <div className="flex flex-col gap-4">
      <PageHeader
        title="Services"
        description="Services keep a desired number of container tasks running, roll out new task definition revisions and register tasks with a load balancer."
        breadcrumbs={[{ label: "ECS", href: "/ecs/" }, { label: "Services" }]}
      />
      <DataTable
        title="Services"
        data={data}
        columns={columns}
        rowId={(s) => s.name}
        loading={isLoading}
        error={error}
        onRefresh={() => mutate()}
        refreshing={isValidating}
        selection="single"
        selected={selected}
        onSelectedChange={setSelected}
        searchPlaceholder="Find services"
        defaultSort={{ id: "name" }}
        actions={
          <>
            <ActionsMenu items={items} disabled={!sel} />
            <Button size="sm" asChild>
              <Link href="/ecs/create/">
                <Plus /> Create service
              </Link>
            </Button>
          </>
        }
        empty={
          <EmptyState
            icon={Container}
            title="No services"
            description="A service runs a task definition as a set of long-lived containers, reachable inside the VPC at <name>.ecs.internal and optionally behind a load balancer."
            action={
              <Button size="sm" asChild>
                <Link href="/ecs/create/">
                  <Plus /> Create service
                </Link>
              </Button>
            }
          />
        }
      />
      <DeleteServiceDialog service={deleting} onClose={() => setDeleting(null)} onDeleted={() => setSelected([])} />
      <UpdateServiceDialog service={updating} onClose={() => setUpdating(null)} />
    </div>
  )
}
