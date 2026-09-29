"use client"

import { useState } from "react"
import Link from "next/link"
import { useRouter } from "next/navigation"
import { Plus, Workflow } from "lucide-react"

import { Button } from "@/components/ui/button"
import { ActionsMenu, type ActionItem } from "@/components/console/actions-menu"
import { DataTable, cellLinkClass, type Column } from "@/components/console/data-table"
import { EmptyState } from "@/components/console/empty-state"
import { PageHeader } from "@/components/console/page-header"
import { StatusBadge } from "@/components/console/status-badge"
import { TimeAgo } from "@/components/console/time-ago"
import { useApi } from "@/lib/hooks"
import type { StateMachineSummary } from "@/lib/types"
import { ExecutionCountsView, MACHINES_PATH, editMachineHref, machineHref, totalExecutions, useDeleteStateMachine } from "./common"

const columns: Column<StateMachineSummary>[] = [
  {
    id: "name",
    header: "Name",
    cell: (m) => (
      <Link href={machineHref(m.name)} onClick={(e) => e.stopPropagation()} className={cellLinkClass()}>
        {m.name}
      </Link>
    ),
    value: (m) => m.name,
  },
  { id: "status", header: "Status", cell: (m) => <StatusBadge status={m.status.toLowerCase()} />, value: (m) => m.status, hideBelow: "sm" },
  { id: "type", header: "Type", cell: () => "Standard", sortable: false, hideBelow: "lg" },
  {
    id: "executions",
    header: "Executions",
    cell: (m) => <ExecutionCountsView counts={m.executions} />,
    value: (m) => totalExecutions(m.executions),
  },
  { id: "created", header: "Created", cell: (m) => <TimeAgo value={m.created_at} />, value: (m) => m.created_at, hideBelow: "md" },
  { id: "updated", header: "Last updated", cell: (m) => <TimeAgo value={m.updated_at} />, value: (m) => m.updated_at, hideBelow: "md" },
]

export function StateMachinesList() {
  const router = useRouter()
  const { data, error, isLoading, isValidating, mutate } = useApi<StateMachineSummary[]>(MACHINES_PATH, {
    refreshInterval: (d) => ((d ?? []).some((m) => (m.executions.RUNNING ?? 0) > 0) ? 3000 : 15000),
  })
  const [selected, setSelected] = useState<string[]>([])
  const del = useDeleteStateMachine({ onDeleted: () => setSelected([]) })
  const single = selected.length === 1 ? selected[0] : null

  const items: ActionItem[] = [
    { label: "View details", onSelect: () => single && router.push(machineHref(single)), disabled: !single },
    { label: "Start execution", onSelect: () => single && router.push(machineHref(single, "executions") + "&start=1"), disabled: !single },
    { label: "Edit", onSelect: () => single && router.push(editMachineHref(single)), disabled: !single },
    { separator: true },
    { label: "Delete", destructive: true, onSelect: () => single && del.remove(single), disabled: !single },
  ]

  return (
    <div className="flex flex-col gap-4">
      <PageHeader
        title="State machines"
        description="Workflows written in Amazon States Language that orchestrate Lambda functions, SQS queues and SNS topics."
        breadcrumbs={[{ label: "Step Functions", href: "/sfn/" }, { label: "State machines" }]}
      />
      <DataTable
        title="State machines"
        data={data}
        columns={columns}
        rowId={(m) => m.name}
        loading={isLoading}
        error={error}
        onRefresh={() => mutate()}
        refreshing={isValidating}
        selection="single"
        selected={selected}
        onSelectedChange={setSelected}
        searchPlaceholder="Find state machines by name"
        defaultSort={{ id: "name" }}
        actions={
          <>
            <ActionsMenu items={items} disabled={!single} />
            <Button size="sm" asChild>
              <Link href="/sfn/create/">
                <Plus /> Create state machine
              </Link>
            </Button>
          </>
        }
        empty={
          <EmptyState
            icon={Workflow}
            title="No state machines"
            description="Define a workflow of Pass, Task, Choice, Wait, Parallel and Map states, then start executions from the console, the API or an EventBridge rule."
            action={
              <Button size="sm" asChild>
                <Link href="/sfn/create/">
                  <Plus /> Create state machine
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
