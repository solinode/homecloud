"use client"

import { useState } from "react"
import Link from "next/link"
import { useRouter } from "next/navigation"
import { Layers, Plus, Trash2 } from "lucide-react"

import { Button } from "@/components/ui/button"
import { ActionsMenu, type ActionItem } from "@/components/console/actions-menu"
import { CellLink, CellText, DataTable, type Column } from "@/components/console/data-table"
import { EmptyState } from "@/components/console/empty-state"
import { PageHeader } from "@/components/console/page-header"
import { TimeAgo } from "@/components/console/time-ago"
import { useApi } from "@/lib/hooks"
import type { StackSummary } from "@/lib/types"
import { CREATE_HREF, STACKS_PATH, StackStatusBadge, canDelete, canUpdate, pollInterval, stackHref, updateStackHref, useDeleteStack } from "./common"

const columns: Column<StackSummary>[] = [
  {
    id: "name",
    header: "Stack name",
    cell: (s) => <CellLink href={stackHref(s.name)}>{s.name}</CellLink>,
    value: (s) => s.name,
  },
  { id: "status", header: "Status", cell: (s) => <StackStatusBadge status={s.status} />, value: (s) => s.status },
  {
    id: "reason",
    header: "Status reason",
    cell: (s) =>
      s.status_reason ? (
        <span className="text-muted-foreground line-clamp-2 max-w-md text-xs break-words" title={s.status_reason}>
          {s.status_reason}
        </span>
      ) : (
        <span className="text-muted-foreground">-</span>
      ),
    value: (s) => s.status_reason,
    hideBelow: "md",
  },
  {
    id: "description",
    header: "Description",
    cell: (s) => (
      <CellText muted max="20rem">
        {s.description}
      </CellText>
    ),
    value: (s) => s.description,
    hideBelow: "lg",
  },
  { id: "resources", header: "Resources", cell: (s) => <span className="tabular-nums">{s.resources}</span>, value: (s) => s.resources, hideBelow: "sm" },
  { id: "created", header: "Created", cell: (s) => <TimeAgo value={s.created_at} />, value: (s) => s.created_at, hideBelow: "sm" },
  { id: "updated", header: "Last updated", cell: (s) => <TimeAgo value={s.updated_at} />, value: (s) => s.updated_at, hideBelow: "lg" },
]

export function StacksList() {
  const router = useRouter()
  const { data, error, isLoading, isValidating, mutate } = useApi<StackSummary[]>(STACKS_PATH, {
    refreshInterval: (d) => pollInterval((d ?? []).map((s) => s.status)),
  })
  const [selected, setSelected] = useState<string[]>([])
  const del = useDeleteStack(() => setSelected([]))
  const sel = (data ?? []).find((s) => s.name === selected[0]) ?? null

  const items: ActionItem[] = [
    { label: "View details", onSelect: () => sel && router.push(stackHref(sel.name)), disabled: !sel },
    { label: "View events", onSelect: () => sel && router.push(stackHref(sel.name, "events")), disabled: !sel },
    {
      label: "Update stack",
      onSelect: () => sel && router.push(updateStackHref(sel.name)),
      disabled: !sel || !canUpdate(sel.status),
      hint: sel?.status === "ROLLBACK_COMPLETE" ? "A rolled-back stack can only be deleted" : undefined,
    },
    { separator: true },
    { label: "Delete stack", destructive: true, icon: <Trash2 />, onSelect: () => sel && del.open(sel.name, sel.resources), disabled: !sel || !canDelete(sel.status) },
  ]

  return (
    <div className="flex flex-col gap-4">
      <PageHeader
        title="Stacks"
        description="A stack is a set of resources, from any HomeCloud service, declared in one template and created, updated and deleted together."
        breadcrumbs={[{ label: "CloudFormation", href: "/cloudformation/" }, { label: "Stacks" }]}
      />
      <DataTable
        title="Stacks"
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
        searchPlaceholder="Filter by name, status or description"
        defaultSort={{ id: "created", desc: true }}
        actions={
          <>
            <ActionsMenu items={items} disabled={!sel} />
            <Button size="sm" asChild>
              <Link href={CREATE_HREF}>
                <Plus /> Create stack
              </Link>
            </Button>
          </>
        }
        empty={
          <EmptyState
            icon={Layers}
            title="No stacks"
            description="Create a stack from a YAML or JSON template to provision queues, buckets, functions, databases and more in one step."
            action={
              <Button size="sm" asChild>
                <Link href={CREATE_HREF}>
                  <Plus /> Create stack
                </Link>
              </Button>
            }
          />
        }
      />
      {del.dialog}
    </div>
  )
}
