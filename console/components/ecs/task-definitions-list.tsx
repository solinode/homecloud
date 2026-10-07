"use client"

import { useMemo, useState } from "react"
import Link from "next/link"
import { ChevronDown, ChevronRight, FileBox, Plus } from "lucide-react"

import { Button } from "@/components/ui/button"
import { Label } from "@/components/ui/label"
import { Switch } from "@/components/ui/switch"
import { CellLink, CellText, DataTable, type Column } from "@/components/console/data-table"
import { EmptyState } from "@/components/console/empty-state"
import { PageHeader } from "@/components/console/page-header"
import { StatusBadge } from "@/components/console/status-badge"
import { TimeAgo } from "@/components/console/time-ago"
import { formatMemoryMB } from "@/lib/format"
import type { EcsTaskDefinition } from "@/lib/types"
import { formatCpu, groupFamilies, registerHref, taskDefHref, tdKey, useTaskDefinitions } from "./common"
import { DeregisterDialog } from "./dialogs"

interface FamilyRow {
  family: string
  revisions: EcsTaskDefinition[]
  latest: EcsTaskDefinition
  active: number
}

export function TaskDefinitionsList() {
  const [showInactive, setShowInactive] = useState(false)
  const { data, error, isLoading, isValidating, mutate } = useTaskDefinitions(showInactive)
  const [open, setOpen] = useState<string[]>([])
  const [deregistering, setDeregistering] = useState<EcsTaskDefinition | null>(null)

  const rows = useMemo<FamilyRow[] | undefined>(
    () =>
      data &&
      groupFamilies(data).map(([family, revisions]) => {
        const active = revisions.filter((r) => r.status === "ACTIVE")
        return { family, revisions, latest: active[0] ?? revisions[0], active: active.length }
      }),
    [data],
  )
  const toggle = (f: string) => setOpen((o) => (o.includes(f) ? o.filter((x) => x !== f) : [...o, f]))

  const columns: Column<FamilyRow>[] = [
    {
      id: "family",
      header: "Family",
      cell: (r) => (
        <span className="flex items-center gap-1.5">
          {open.includes(r.family) ? <ChevronDown className="text-muted-foreground size-4" /> : <ChevronRight className="text-muted-foreground size-4" />}
          <CellText mono className="font-medium" max="18rem">
            {r.family}
          </CellText>
        </span>
      ),
      value: (r) => r.family,
    },
    {
      id: "latest",
      header: "Latest revision",
      cell: (r) => (
        <CellLink href={taskDefHref(r.family, r.latest.revision)} mono>
          {tdKey(r.latest)}
        </CellLink>
      ),
      value: (r) => r.latest.revision,
    },
    {
      id: "count",
      header: "Revisions",
      cell: (r) => (
        <span className="tabular-nums">
          {r.revisions.length}
          {showInactive && r.active !== r.revisions.length && <span className="text-muted-foreground text-xs"> ({r.active} active)</span>}
        </span>
      ),
      value: (r) => r.revisions.length,
    },
    {
      id: "status",
      header: "Status",
      cell: (r) => <StatusBadge status={r.active ? "active" : "inactive"} />,
      value: (r) => (r.active ? "ACTIVE" : "INACTIVE"),
    },
    {
      id: "image",
      header: "Image",
      cell: (r) => (
        <CellText mono muted max="20rem">
          {r.latest.image}
        </CellText>
      ),
      value: (r) => r.latest.image,
      hideBelow: "md",
    },
    {
      id: "size",
      header: "CPU / memory",
      cell: (r) => (
        <span className="text-sm whitespace-nowrap">
          {formatCpu(r.latest.cpu)}, {formatMemoryMB(r.latest.memory_mb)}
        </span>
      ),
      value: (r) => r.latest.cpu,
      hideBelow: "lg",
    },
    { id: "created", header: "Last registered", cell: (r) => <TimeAgo value={r.revisions[0].created_at} />, value: (r) => r.revisions[0].created_at, hideBelow: "lg" },
  ]

  return (
    <div className="flex flex-col gap-4">
      <PageHeader
        title="Task definitions"
        description="A task definition describes a container: image, CPU and memory, port, command, environment and secrets. Each change registers a new revision."
        breadcrumbs={[{ label: "ECS", href: "/ecs/" }, { label: "Task definitions" }]}
      />
      <DataTable
        title="Task definition families"
        data={rows}
        columns={columns}
        rowId={(r) => r.family}
        loading={isLoading}
        error={error}
        onRefresh={() => mutate()}
        refreshing={isValidating}
        onRowClick={(r) => toggle(r.family)}
        searchPlaceholder="Find families or images"
        filter={(r, q) => r.family.toLowerCase().includes(q) || r.revisions.some((x) => x.image.toLowerCase().includes(q))}
        defaultSort={{ id: "family" }}
        expanded={(r) => (open.includes(r.family) ? <Revisions revisions={r.revisions} onDeregister={setDeregistering} /> : null)}
        filters={
          <div className="flex items-center gap-2">
            <Switch id="show-inactive" checked={showInactive} onCheckedChange={setShowInactive} />
            <Label htmlFor="show-inactive" className="text-sm font-normal whitespace-nowrap">
              Show inactive
            </Label>
          </div>
        }
        actions={
          <Button size="sm" asChild>
            <Link href={registerHref()}>
              <Plus /> Register task definition
            </Link>
          </Button>
        }
        empty={
          <EmptyState
            icon={FileBox}
            title={showInactive ? "No task definitions" : "No active task definitions"}
            description="Register a task definition from an image in ECR or any registry, then run it as a task or a service."
            action={
              <Button size="sm" asChild>
                <Link href={registerHref()}>
                  <Plus /> Register task definition
                </Link>
              </Button>
            }
          />
        }
      />
      <DeregisterDialog td={deregistering} onClose={() => setDeregistering(null)} />
    </div>
  )
}

function Revisions({ revisions, onDeregister }: { revisions: EcsTaskDefinition[]; onDeregister: (td: EcsTaskDefinition) => void }) {
  return (
    <div className="bg-card overflow-x-auto rounded-lg border">
      <table className="w-full text-sm">
        <thead>
          <tr className="bg-muted/60 border-b">
            <th className="text-muted-foreground px-3 py-1.5 text-left text-xs font-semibold">Revision</th>
            <th className="text-muted-foreground px-3 py-1.5 text-left text-xs font-semibold">Status</th>
            <th className="text-muted-foreground hidden px-3 py-1.5 text-left text-xs font-semibold md:table-cell">Image</th>
            <th className="text-muted-foreground hidden px-3 py-1.5 text-left text-xs font-semibold sm:table-cell">Registered</th>
            <th className="px-3 py-1.5" />
          </tr>
        </thead>
        <tbody>
          {revisions.map((td) => (
            <tr key={td.revision} className="border-b last:border-0">
              <td className="px-3 py-1.5">
                <CellLink href={taskDefHref(td.family, td.revision)} mono>
                  {tdKey(td)}
                </CellLink>
              </td>
              <td className="px-3 py-1.5">
                <StatusBadge status={td.status.toLowerCase()} />
              </td>
              <td className="hidden max-w-96 truncate px-3 py-1.5 font-mono text-xs md:table-cell" title={td.image}>
                {td.image}
              </td>
              <td className="hidden px-3 py-1.5 sm:table-cell">
                <TimeAgo value={td.created_at} />
              </td>
              <td className="px-3 py-1.5 text-right whitespace-nowrap">
                <Button variant="ghost" size="sm" className="h-7" asChild>
                  <Link href={registerHref(tdKey(td))}>New revision</Link>
                </Button>
                {td.status === "ACTIVE" && (
                  <Button variant="ghost" size="sm" className="text-destructive h-7" onClick={() => onDeregister(td)}>
                    Deregister
                  </Button>
                )}
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  )
}
