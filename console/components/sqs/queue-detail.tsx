"use client"

import Link from "next/link"
import { useRouter } from "next/navigation"
import { AlertCircle, ArrowLeft, Loader2, Pencil, Send, Undo2 } from "lucide-react"

import { Button } from "@/components/ui/button"
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs"
import { ActionsMenu, type ActionItem } from "@/components/console/actions-menu"
import { EmptyState } from "@/components/console/empty-state"
import { ErrorState } from "@/components/console/error-state"
import { DetailSkeleton } from "@/components/console/loading"
import { PageHeader } from "@/components/console/page-header"
import { Section } from "@/components/console/section"
import { ApiError } from "@/lib/api"
import { formatNumber } from "@/lib/format"
import { useApi, useQueryParam, useSetQueryParam } from "@/lib/hooks"
import type { Queue } from "@/lib/types"
import { cn } from "@/lib/utils"

import { DlqInfo, QUEUE_POLL, QueueTypeBadge, queuePath, useQueueActions } from "./common"
import { QueueMessages } from "./queue-messages"
import { QueueSettings } from "./queue-settings"
import { QueueTriggers } from "./queue-triggers"
import { SendReceive } from "./send-receive"

const TABS = ["send-receive", "messages", "settings", "triggers"] as const
type Tab = (typeof TABS)[number]

export function QueueDetail() {
  const router = useRouter()
  const name = useQueryParam("name")
  const tabParam = useQueryParam("tab")
  const editing = useQueryParam("edit") === "1"
  const setParam = useSetQueryParam()
  const tab: Tab = (TABS as readonly string[]).includes(tabParam) ? (tabParam as Tab) : "send-receive"

  const { data: queue, error, isLoading, isValidating, mutate } = useApi<Queue>(name ? queuePath(name) : null, { refreshInterval: QUEUE_POLL })
  const actions = useQueueActions({ onDeleted: () => router.push("/sqs/") })

  const crumbs = [{ label: "SQS", href: "/sqs/" }, { label: "Queues", href: "/sqs/" }, { label: name || "Queue" }]

  if (!name) {
    return (
      <>
        <PageHeader title="Queue" breadcrumbs={crumbs} />
        <EmptyState title="No queue selected" description="Open a queue from the queues list." action={<BackButton />} />
      </>
    )
  }
  if (error && !queue) {
    const notFound = error instanceof ApiError && error.status === 404
    return (
      <div className="flex flex-col gap-4">
        <PageHeader title={name} breadcrumbs={crumbs} />
        {notFound ? (
          <Section>
            <EmptyState icon={AlertCircle} title="Queue not found" description={`Queue ${name} does not exist. It may have been deleted.`} action={<BackButton />} />
          </Section>
        ) : (
          <ErrorState error={error} onRetry={() => mutate()} />
        )}
      </div>
    )
  }
  if (isLoading || !queue) return <DetailSkeleton />

  const isDlq = (queue.dead_letter_source_queues?.length ?? 0) > 0
  const edit = () => {
    const p = new URLSearchParams({ name, tab: "settings", edit: "1" })
    router.replace(`/sqs/queue/?${p.toString()}`, { scroll: false })
  }
  const items: ActionItem[] = [
    { label: "Edit", onSelect: edit },
    { label: "Start DLQ redrive", onSelect: () => actions.redrive(queue), disabled: !isDlq, hint: "Only for dead-letter queues" },
    { separator: true },
    { label: "Purge", onSelect: () => actions.purge(queue), destructive: true },
    { label: "Delete", onSelect: () => actions.remove(queue), destructive: true },
  ]

  return (
    <div className="flex flex-col gap-4">
      <PageHeader
        title={queue.name}
        badge={<QueueTypeBadge fifo={queue.fifo} />}
        description={<DlqInfo queue={queue} empty="" />}
        breadcrumbs={crumbs}
        actions={
          <>
            <Button variant="outline" size="sm" onClick={() => mutate()} disabled={isValidating} aria-label="Refresh">
              {isValidating ? <Loader2 className="animate-spin" /> : null}
              Refresh
            </Button>
            <Button variant="outline" size="sm" onClick={edit}>
              <Pencil /> Edit
            </Button>
            {isDlq && (
              <Button variant="outline" size="sm" onClick={() => actions.redrive(queue)}>
                <Undo2 /> Start DLQ redrive
              </Button>
            )}
            <Button size="sm" onClick={() => setParam("tab", "send-receive")}>
              <Send /> Send and receive
            </Button>
            <ActionsMenu items={items} />
          </>
        }
      />

      <div className="grid grid-cols-2 gap-3 sm:grid-cols-3 lg:grid-cols-6">
        <Stat label="Available" value={queue.approximate_number_of_messages} tone="primary" />
        <Stat label="In flight" value={queue.approximate_number_of_messages_not_visible} />
        <Stat label="Delayed" value={queue.approximate_number_of_messages_delayed} />
        <Stat label="Sent" value={queue.messages_sent} muted />
        <Stat label="Received" value={queue.messages_received} muted />
        <Stat label="Deleted" value={queue.messages_deleted} muted />
      </div>

      <Tabs
        value={tab}
        onValueChange={(v) => {
          const p = new URLSearchParams({ name })
          if (v !== "send-receive") p.set("tab", v)
          router.replace(`/sqs/queue/?${p.toString()}`, { scroll: false })
        }}
      >
        <TabsList>
          <TabsTrigger value="send-receive">Send and receive</TabsTrigger>
          <TabsTrigger value="messages">Messages</TabsTrigger>
          <TabsTrigger value="settings">Settings</TabsTrigger>
          <TabsTrigger value="triggers">Lambda triggers</TabsTrigger>
        </TabsList>
        <TabsContent value="send-receive">
          <SendReceive key={queue.name} queue={queue} />
        </TabsContent>
        <TabsContent value="messages" className="flex flex-col gap-2">
          <QueueMessages queue={queue} />
        </TabsContent>
        <TabsContent value="settings">
          <QueueSettings queue={queue} editing={editing} onEditingChange={(e) => setParam("edit", e ? "1" : null)} />
        </TabsContent>
        <TabsContent value="triggers">
          <QueueTriggers queue={queue} />
        </TabsContent>
      </Tabs>

      {actions.dialogs}
    </div>
  )
}

function Stat({ label, value, tone, muted }: { label: string; value: number; tone?: "primary"; muted?: boolean }) {
  return (
    <div className="bg-card flex flex-col gap-1 rounded-lg border px-3 py-2.5 shadow-xs">
      <span className="text-muted-foreground text-xs font-medium">
        {label}
        {muted && <span className="sr-only"> (since start)</span>}
      </span>
      <span className={cn("text-xl font-semibold tabular-nums", tone === "primary" && value > 0 && "text-primary", muted && "text-muted-foreground")}>
        {formatNumber(value)}
      </span>
    </div>
  )
}

function BackButton() {
  return (
    <Button variant="outline" size="sm" asChild>
      <Link href="/sqs/">
        <ArrowLeft /> Back to queues
      </Link>
    </Button>
  )
}
