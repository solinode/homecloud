"use client"

import { useState } from "react"
import Link from "next/link"
import { useRouter } from "next/navigation"
import { Inbox, Plus } from "lucide-react"

import { Button } from "@/components/ui/button"
import { ActionsMenu, type ActionItem } from "@/components/console/actions-menu"
import { CellLink, DataTable, type Column } from "@/components/console/data-table"
import { EmptyState } from "@/components/console/empty-state"
import { PageHeader } from "@/components/console/page-header"
import { TimeAgo } from "@/components/console/time-ago"
import { formatNumber } from "@/lib/format"
import type { Queue } from "@/lib/types"

import { DlqInfo, QueueTypeBadge, queueHref, useQueueActions, useQueues } from "./common"

const num = (n: number) => <span className={n ? "tabular-nums" : "text-muted-foreground tabular-nums"}>{formatNumber(n)}</span>

const columns: Column<Queue>[] = [
  {
    id: "name",
    header: "Name",
    cell: (q) => <CellLink href={queueHref(q.name)}>{q.name}</CellLink>,
    value: (q) => q.name,
  },
  { id: "type", header: "Type", cell: (q) => <QueueTypeBadge fifo={q.fifo} />, value: (q) => (q.fifo ? "FIFO" : "Standard") },
  { id: "available", header: "Messages available", cell: (q) => num(q.approximate_number_of_messages), value: (q) => q.approximate_number_of_messages },
  {
    id: "inflight",
    header: "Messages in flight",
    cell: (q) => num(q.approximate_number_of_messages_not_visible),
    value: (q) => q.approximate_number_of_messages_not_visible,
    hideBelow: "sm",
  },
  {
    id: "delayed",
    header: "Delayed",
    cell: (q) => num(q.approximate_number_of_messages_delayed),
    value: (q) => q.approximate_number_of_messages_delayed,
    hideBelow: "md",
  },
  {
    id: "dlq",
    header: "Dead-letter queue",
    cell: (q) => <DlqInfo queue={q} />,
    value: (q) => [q.redrive_policy?.dead_letter_queue, ...(q.dead_letter_source_queues ?? [])].filter(Boolean).join(" "),
    sortable: false,
    hideBelow: "md",
  },
  { id: "created", header: "Created", cell: (q) => <TimeAgo value={q.created_at} />, value: (q) => q.created_at, hideBelow: "lg" },
]

export function QueuesList() {
  const router = useRouter()
  const { data, error, isLoading, isValidating, mutate } = useQueues()
  const [selected, setSelected] = useState<string[]>([])
  const actions = useQueueActions()

  const sel = data?.find((q) => q.name === selected[0]) ?? null

  const items: ActionItem[] = [
    { label: "Send and receive messages", onSelect: () => sel && router.push(queueHref(sel.name, "send-receive")), disabled: !sel },
    { label: "View messages", onSelect: () => sel && router.push(queueHref(sel.name, "messages")), disabled: !sel },
    { label: "Edit", onSelect: () => sel && router.push(`${queueHref(sel.name, "settings")}&edit=1`), disabled: !sel },
    {
      label: "Start DLQ redrive",
      onSelect: () => sel && actions.redrive(sel),
      disabled: !sel?.dead_letter_source_queues?.length,
      hint: "Select a queue that is another queue's dead-letter queue",
    },
    { separator: true },
    { label: "Purge", onSelect: () => sel && actions.purge(sel), disabled: !sel, destructive: true },
    { label: "Delete", onSelect: () => sel && actions.remove(sel), disabled: !sel, destructive: true },
  ]

  return (
    <div className="flex flex-col gap-4">
      <PageHeader
        title="Queues"
        description="Message queues with visibility timeouts, long polling, FIFO ordering and dead-letter queues."
        breadcrumbs={[{ label: "SQS", href: "/sqs/" }, { label: "Queues" }]}
      />
      <DataTable
        title="Queues"
        data={data}
        columns={columns}
        rowId={(q) => q.name}
        loading={isLoading}
        error={error}
        onRefresh={() => mutate()}
        refreshing={isValidating}
        selection="single"
        selected={selected}
        onSelectedChange={setSelected}
        searchPlaceholder="Search queues by name"
        defaultSort={{ id: "name" }}
        actions={
          <>
            <ActionsMenu items={items} disabled={!sel} />
            <Button size="sm" asChild>
              <Link href="/sqs/create/">
                <Plus /> Create queue
              </Link>
            </Button>
          </>
        }
        empty={
          <EmptyState
            icon={Inbox}
            title="No queues"
            description="Create a standard or FIFO queue to decouple producers from consumers. Lambda functions and SNS topics can use it too."
            action={
              <Button size="sm" asChild>
                <Link href="/sqs/create/">
                  <Plus /> Create queue
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
