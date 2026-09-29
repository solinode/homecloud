"use client"

import Link from "next/link"
import { Zap } from "lucide-react"

import { Button } from "@/components/ui/button"
import { DataTable, cellLinkClass, type Column } from "@/components/console/data-table"
import { EmptyState } from "@/components/console/empty-state"
import { StatusBadge } from "@/components/console/status-badge"
import { TimeAgo } from "@/components/console/time-ago"
import { useApi } from "@/lib/hooks"
import type { EventSourceMapping, Queue } from "@/lib/types"

const functionHref = (name: string, tab?: string) => `/lambda/function/?name=${encodeURIComponent(name)}${tab ? `&tab=${tab}` : ""}`

const columns: Column<EventSourceMapping>[] = [
  {
    id: "function",
    header: "Function",
    cell: (m) => (
      <Link href={functionHref(m.function_name, "triggers")} className={cellLinkClass()}>
        {m.function_name}
      </Link>
    ),
    value: (m) => m.function_name,
  },
  { id: "batch", header: "Batch size", cell: (m) => <span className="tabular-nums">{m.batch_size}</span>, value: (m) => m.batch_size },
  {
    id: "state",
    header: "State",
    cell: (m) => <StatusBadge status={m.enabled ? "enabled" : "disabled"} />,
    value: (m) => (m.enabled ? "enabled" : "disabled"),
  },
  {
    id: "result",
    header: "Last result",
    cell: (m) =>
      m.last_processing_result ? (
        <span className={m.last_processing_result === "OK" ? "" : "text-destructive"}>{m.last_processing_result}</span>
      ) : (
        <span className="text-muted-foreground">-</span>
      ),
    value: (m) => m.last_processing_result,
    hideBelow: "sm",
  },
  { id: "invoked", header: "Last invoked", cell: (m) => <TimeAgo value={m.last_invoked_at} />, value: (m) => m.last_invoked_at, hideBelow: "md" },
  {
    id: "uuid",
    header: "Mapping ID",
    cell: (m) => <span className="font-mono text-[13px]">{m.id}</span>,
    value: (m) => m.id,
    hideBelow: "lg",
  },
]

/** QueueTriggers lists the Lambda event source mappings that poll this queue. */
export function QueueTriggers({ queue }: { queue: Queue }) {
  const { data, error, isLoading, isValidating, mutate } = useApi<EventSourceMapping[]>("/api/v1/lambda/event-source-mappings", {
    refreshInterval: 15_000,
  })
  const rows = data?.filter((m) => m.queue_name === queue.name)
  return (
    <DataTable
      title="Lambda triggers"
      description="Functions that receive batches of messages from this queue. Messages are deleted when the function succeeds."
      data={rows}
      columns={columns}
      rowId={(m) => m.id}
      loading={isLoading}
      error={error}
      onRefresh={() => mutate()}
      refreshing={isValidating}
      noSearch
      empty={
        <EmptyState
          icon={Zap}
          title="No Lambda triggers"
          description="Add an SQS trigger from a function's Triggers tab to process messages from this queue automatically."
          action={
            <Button size="sm" variant="outline" asChild>
              <Link href="/lambda/">Go to Lambda functions</Link>
            </Button>
          }
        />
      }
    />
  )
}
