"use client"

import { useState } from "react"
import { MailOpen } from "lucide-react"

import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select"
import { CellLink, CellText, DataTable, type Column } from "@/components/console/data-table"
import { EmptyState } from "@/components/console/empty-state"
import { StatusBadge } from "@/components/console/status-badge"
import { TimeAgo } from "@/components/console/time-ago"
import { formatBytes } from "@/lib/format"
import { useApi } from "@/lib/hooks"
import type { PeekedMessage, Queue } from "@/lib/types"

import { nameFromArn, queueHref, queuePath } from "./common"
import { MessageDetailDialog, bodyPreview, type MessageView } from "./messages"

const STATE_TONE = { available: "success", "in-flight": "info", delayed: "neutral" } as const

/** QueueMessages lists messages without receiving them (peek does not change visibility). */
export function QueueMessages({ queue }: { queue: Queue }) {
  const [limit, setLimit] = useState("50")
  const { data, error, isLoading, isValidating, mutate } = useApi<PeekedMessage[]>(`${queuePath(queue.name)}/messages/peek`, {
    query: { limit },
    refreshInterval: 10_000,
  })
  const [open, setOpen] = useState<MessageView | null>(null)
  const hasSource = (data ?? []).some((m) => m.source_queue)
  const total = queue.approximate_number_of_messages + queue.approximate_number_of_messages_not_visible + queue.approximate_number_of_messages_delayed

  const columns: Column<PeekedMessage>[] = [
    {
      id: "id",
      header: "Message ID",
      cell: (m) => (
        <span className="text-primary block font-mono text-[13px] whitespace-nowrap hover:underline" title={m.message_id}>
          {m.message_id.slice(0, 8)}…
        </span>
      ),
      value: (m) => m.message_id,
    },
    { id: "state", header: "State", cell: (m) => <StatusBadge status={m.state} tone={STATE_TONE[m.state]} />, value: (m) => m.state },
    { id: "sent", header: "Sent", cell: (m) => <TimeAgo value={m.sent_at} />, value: (m) => m.sent_at, hideBelow: "sm" },
    { id: "receives", header: "Receives", cell: (m) => <span className="tabular-nums">{m.receive_count}</span>, value: (m) => m.receive_count, hideBelow: "md" },
    ...(queue.fifo
      ? [{ id: "group", header: "Group", cell: (m: PeekedMessage) => <span className="font-mono text-[13px]">{m.group_id || "-"}</span>, value: (m: PeekedMessage) => m.group_id, hideBelow: "md" as const }]
      : []),
    { id: "size", header: "Size", cell: (m) => formatBytes(m.size), value: (m) => m.size, hideBelow: "lg" },
    {
      id: "body",
      header: "Body",
      cell: (m) => (
        <CellText mono max="24rem" title={m.body}>
          {bodyPreview(m.body)}
        </CellText>
      ),
      value: (m) => m.body,
      sortable: false,
    },
    ...(hasSource
      ? [
          {
            id: "source",
            header: "Source queue",
            cell: (m: PeekedMessage) =>
              m.source_queue ? (
                <CellLink href={queueHref(nameFromArn(m.source_queue))} title={m.source_queue}>
                  {nameFromArn(m.source_queue)}
                </CellLink>
              ) : (
                <span className="text-muted-foreground">-</span>
              ),
            value: (m: PeekedMessage) => m.source_queue,
            hideBelow: "md" as const,
          },
        ]
      : []),
  ]

  return (
    <>
      <DataTable
        title="Messages"
        description="A read-only view of the messages in the queue. Viewing does not receive messages or change their visibility."
        data={data}
        columns={columns}
        rowId={(m) => m.message_id}
        loading={isLoading}
        error={error}
        onRefresh={() => mutate()}
        refreshing={isValidating}
        searchPlaceholder="Filter by ID, body, state or group"
        onRowClick={(m) =>
          setOpen({
            message_id: m.message_id,
            body: m.body,
            sent_at: m.sent_at,
            message_attributes: m.message_attributes,
            extra: [
              { label: "State", value: <StatusBadge status={m.state} tone={STATE_TONE[m.state]} /> },
              { label: "Receive count", value: String(m.receive_count) },
              ...(m.group_id ? [{ label: "Message group ID", value: <span className="font-mono text-[13px]">{m.group_id}</span> }] : []),
              ...(m.source_queue ? [{ label: "Dead-letter source queue", value: <span className="font-mono text-[13px] break-all">{m.source_queue}</span> }] : []),
            ],
          })
        }
        filters={
          <Select value={limit} onValueChange={setLimit}>
            <SelectTrigger size="sm" className="h-8 w-36" aria-label="Messages to show">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {["50", "100", "250", "500"].map((n) => (
                <SelectItem key={n} value={n}>
                  First {n}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        }
        empty={<EmptyState icon={MailOpen} title="The queue is empty" description="Messages appear here as soon as they are sent." />}
      />
      {data && total > data.length && (
        <p className="text-muted-foreground text-xs">
          Showing the first {data.length} of about {total} messages.
        </p>
      )}
      <MessageDetailDialog message={open} onClose={() => setOpen(null)} />
    </>
  )
}
