"use client"

import { useState } from "react"
import Link from "next/link"
import { Plus, Send, Workflow } from "lucide-react"

import { Button } from "@/components/ui/button"
import { ActionsMenu, type ActionItem } from "@/components/console/actions-menu"
import { CellLink, CellText, DataTable, type Column } from "@/components/console/data-table"
import { EmptyState } from "@/components/console/empty-state"
import { PageHeader } from "@/components/console/page-header"
import { StatusBadge } from "@/components/console/status-badge"
import { TimeAgo } from "@/components/console/time-ago"
import { formatNumber } from "@/lib/format"
import { useApi, useQueryParam, useSetQueryParam } from "@/lib/hooks"
import type { EventRule } from "@/lib/types"
import { BusSelect } from "./bus-select"
import { RULES_PATH, RuleTypeTag, busQuery, describeSchedule, hasNextRun, isSchedule, patternSummary, ruleHref, useRuleActions } from "./common"

const columns: Column<EventRule>[] = [
  {
    id: "name",
    header: "Name",
    cell: (r) => <CellLink href={ruleHref(r.name, r.event_bus)}>{r.name}</CellLink>,
    value: (r) => r.name,
  },
  { id: "state", header: "Status", cell: (r) => <StatusBadge status={r.state} />, value: (r) => r.state },
  {
    id: "type",
    header: "Type",
    cell: (r) => <RuleTypeTag rule={r} />,
    value: (r) => (isSchedule(r) ? "schedule" : "event pattern"),
    hideBelow: "sm",
  },
  {
    id: "expr",
    header: "Schedule / pattern",
    cell: (r) =>
      isSchedule(r) ? (
        <CellText mono title={describeSchedule(r.schedule_expression!)}>
          {r.schedule_expression}
        </CellText>
      ) : (
        <CellText mono muted max="20rem" className="text-[12.5px]" title={JSON.stringify(r.event_pattern)}>
          {patternSummary(r.event_pattern)}
        </CellText>
      ),
    value: (r) => r.schedule_expression || JSON.stringify(r.event_pattern ?? {}),
    hideBelow: "md",
  },
  { id: "targets", header: "Targets", cell: (r) => r.targets?.length ?? 0, value: (r) => r.targets?.length ?? 0, hideBelow: "md" },
  {
    id: "next",
    header: "Next run",
    cell: (r) => (isSchedule(r) && r.state === "ENABLED" && hasNextRun(r) ? <TimeAgo value={r.next_run} /> : <span className="text-muted-foreground">-</span>),
    value: (r) => (isSchedule(r) && r.state === "ENABLED" && hasNextRun(r) ? r.next_run : ""),
    hideBelow: "lg",
  },
  { id: "last", header: "Last triggered", cell: (r) => <TimeAgo value={r.last_triggered} />, value: (r) => r.last_triggered ?? "", hideBelow: "lg" },
  {
    id: "invocations",
    header: "Invocations",
    cell: (r) => (
      <span className="tabular-nums whitespace-nowrap">
        {formatNumber(r.invocations)}
        {r.failed_invocations > 0 && <span className="text-danger"> ({formatNumber(r.failed_invocations)} failed)</span>}
      </span>
    ),
    value: (r) => r.invocations,
    hideBelow: "sm",
  },
]

export function RulesList() {
  const bus = useQueryParam("bus") || "default"
  const setParam = useSetQueryParam()
  const { data, error, isLoading, isValidating, mutate } = useApi<EventRule[]>(RULES_PATH, { refreshInterval: 15_000, query: busQuery(bus) })
  const [selected, setSelected] = useState<string[]>([])
  const actions = useRuleActions()

  const sel = (data ?? []).filter((r) => selected.includes(r.name))
  const names = sel.map((r) => r.name)
  const disabled = actions.busy || !sel.length

  const items: ActionItem[] = [
    { label: "Enable", onSelect: () => actions.enable(names), disabled: disabled || sel.every((r) => r.state === "ENABLED") },
    { label: "Disable", onSelect: () => actions.disable(names), disabled: disabled || sel.every((r) => r.state === "DISABLED") },
    { label: "Run now", onSelect: () => actions.run(names), disabled, hint: "Invoke the targets now with a Scheduled Event" },
    { separator: true },
    { label: "Delete", destructive: true, onSelect: () => actions.remove(sel), disabled },
  ]

  return (
    <div className="flex flex-col gap-4">
      <PageHeader
        title="Rules"
        description="Rules run on a schedule or when a published event matches their pattern, and deliver to Lambda functions, SQS queues and SNS topics."
        breadcrumbs={[{ label: "EventBridge", href: "/events/" }, { label: "Rules" }]}
      />
      <DataTable
        title="Rules"
        data={data}
        columns={columns}
        rowId={(r) => r.name}
        loading={isLoading}
        error={error}
        onRefresh={() => mutate()}
        refreshing={isValidating}
        selection="multi"
        selected={selected}
        onSelectedChange={setSelected}
        filters={<BusSelect size="sm" className="w-full md:w-56" value={bus} onChange={(b) => setParam("bus", b === "default" ? null : b)} />}
        searchPlaceholder="Find rules by name, schedule or pattern"
        defaultSort={{ id: "name" }}
        actions={
          <>
            <ActionsMenu items={items} disabled={!sel.length} />
            <Button variant="outline" size="sm" asChild>
              <Link href="/events/send/">
                <Send /> Send events
              </Link>
            </Button>
            <Button size="sm" asChild>
              <Link href={bus === "default" ? "/events/create/" : `/events/create/?bus=${encodeURIComponent(bus)}`}>
                <Plus /> Create rule
              </Link>
            </Button>
          </>
        }
        empty={
          <EmptyState
            icon={Workflow}
            title="No rules"
            description="Create a rule to run a function every few minutes, or to route events such as order.paid to queues and topics."
            action={
              <Button size="sm" asChild>
                <Link href="/events/create/">
                  <Plus /> Create rule
                </Link>
              </Button>
            }
          />
        }
      />
      {actions.dialogs}
    </div>
  )
}
