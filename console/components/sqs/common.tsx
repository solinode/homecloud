"use client"

import { useEffect, useState, type ReactNode } from "react"
import Link from "next/link"
import { ArrowRightLeft, CheckCircle2, Loader2, Undo2 } from "lucide-react"
import { toast } from "sonner"

import { Alert, AlertDescription } from "@/components/ui/alert"
import { Button } from "@/components/ui/button"
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog"
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select"
import { ConfirmDialog } from "@/components/console/confirm-dialog"
import { Field } from "@/components/console/form-field"
import { OptionCard, OptionGroup } from "@/components/console/option-card"
import { Tag } from "@/components/console/tag"
import { api, errorMessage, seg } from "@/lib/api"
import { formatNumber, pluralize } from "@/lib/format"
import { revalidate, useApi } from "@/lib/hooks"
import type { Queue } from "@/lib/types"

export const QUEUES_PATH = "/api/v1/sqs/queues"

/** Queue counts change as producers and consumers run; keep them fresh. */
export const QUEUE_POLL = 5000

export const queuePath = (name: string) => `${QUEUES_PATH}/${seg(name)}`

export const queueHref = (name: string, tab?: string) => `/sqs/queue/?name=${encodeURIComponent(name)}${tab ? `&tab=${tab}` : ""}`

/** "arn:aws:sqs:us-east-1:123:jobs" -> "jobs" (also returns plain names unchanged). */
export const nameFromArn = (arn: string) => arn.slice(arn.lastIndexOf(":") + 1)

export function useQueues(refreshInterval = QUEUE_POLL) {
  return useApi<Queue[]>(QUEUES_PATH, { refreshInterval })
}

/** QueueTypeBadge shows Standard / FIFO. */
export function QueueTypeBadge({ fifo, className }: { fifo: boolean; className?: string }) {
  return (
    <Tag accent={fifo ? "violet" : "neutral"} className={className}>
      {fifo ? "FIFO" : "Standard"}
    </Tag>
  )
}

/** humanSeconds renders an attribute duration in its largest whole unit ("4 days", "30 seconds"). */
export function humanSeconds(s: number): string {
  if (s === 0) return "0 seconds"
  if (s % 86400 === 0) return pluralize(s / 86400, "day")
  if (s % 3600 === 0) return pluralize(s / 3600, "hour")
  if (s % 60 === 0) return pluralize(s / 60, "minute")
  return pluralize(s, "second")
}

/** DLQ relationship of a queue for list/summary display. */
export function DlqInfo({ queue, empty = "-" }: { queue: Queue; empty?: ReactNode }) {
  const sources = queue.dead_letter_source_queues ?? []
  if (!queue.redrive_policy && !sources.length) return <span className="text-muted-foreground">{empty}</span>
  return (
    <span className="flex flex-col gap-0.5 text-[13px]">
      {queue.redrive_policy && (
        <span className="whitespace-nowrap">
          <span className="text-muted-foreground">DLQ: </span>
          <Link href={queueHref(queue.redrive_policy.dead_letter_queue)} onClick={(e) => e.stopPropagation()} className="text-primary hover:underline">
            {queue.redrive_policy.dead_letter_queue}
          </Link>
        </span>
      )}
      {sources.length > 0 && (
        <span className="whitespace-nowrap">
          <span className="text-muted-foreground">DLQ for </span>
          {sources.map((s, i) => (
            <span key={s}>
              {i > 0 && ", "}
              <Link href={queueHref(s)} onClick={(e) => e.stopPropagation()} className="text-primary hover:underline">
                {s}
              </Link>
            </span>
          ))}
        </span>
      )}
    </span>
  )
}

export interface QueueActions {
  purge: (q: Queue) => void
  remove: (q: Queue) => void
  redrive: (q: Queue) => void
  dialogs: ReactNode
}

/** useQueueActions provides purge / delete / DLQ redrive with their dialogs (list and detail pages). */
export function useQueueActions(opts: { onDeleted?: () => void } = {}): QueueActions {
  const [purging, setPurging] = useState<Queue | null>(null)
  const [deleting, setDeleting] = useState<Queue | null>(null)
  const [redriving, setRedriving] = useState<Queue | null>(null)

  const dialogs = (
    <>
      <ConfirmDialog
        open={!!purging}
        onOpenChange={(o) => !o && setPurging(null)}
        title={`Purge ${purging?.name ?? "queue"}?`}
        description={
          <>
            Deletes every message in the queue, including{" "}
            {purging ? pluralize(purging.approximate_number_of_messages_not_visible, "in-flight message") : "in-flight messages"}. This cannot be
            undone.
          </>
        }
        actionLabel="Purge"
        onConfirm={async () => {
          if (!purging) return
          await api.post(`${queuePath(purging.name)}/purge`)
          await revalidate(QUEUES_PATH)
          toast.success(`Purged ${purging.name}`)
        }}
      />
      <ConfirmDialog
        open={!!deleting}
        onOpenChange={(o) => !o && setDeleting(null)}
        title={`Delete ${deleting?.name ?? "queue"}?`}
        description={
          <>
            The queue and all of its messages are deleted permanently.
            {deleting?.dead_letter_source_queues?.length ? (
              <span className="text-destructive mt-2 block">
                This queue is the dead-letter queue of {deleting.dead_letter_source_queues.join(", ")}. Remove their redrive policy first.
              </span>
            ) : null}
          </>
        }
        confirmText={deleting?.name}
        onConfirm={async () => {
          if (!deleting) return
          await api.del(queuePath(deleting.name))
          await revalidate(QUEUES_PATH)
          toast.success(`Deleted queue ${deleting.name}`)
          opts.onDeleted?.()
        }}
      />
      <RedriveDialog queue={redriving} onClose={() => setRedriving(null)} />
    </>
  )

  return { purge: setPurging, remove: setDeleting, redrive: setRedriving, dialogs }
}

/** RedriveDialog moves the messages of a dead-letter queue back to their source queue(s) or to a chosen queue. */
function RedriveDialog({ queue, onClose }: { queue: Queue | null; onClose: () => void }) {
  const { data: queues } = useApi<Queue[]>(queue ? QUEUES_PATH : null)
  const [mode, setMode] = useState<"source" | "custom">("source")
  const [dest, setDest] = useState("")
  const [pending, setPending] = useState(false)
  const [moved, setMoved] = useState<number | null>(null)

  useEffect(() => {
    if (queue) {
      setMode("source")
      setDest("")
      setMoved(null)
    }
  }, [queue])

  const sources = queue?.dead_letter_source_queues ?? []
  const candidates = (queues ?? []).filter((q) => q.name !== queue?.name && q.fifo === queue?.fifo)
  // Prefer the refreshed list entry so the count updates after a redrive.
  const live = queues?.find((q) => q.name === queue?.name) ?? queue
  const total = live ? live.approximate_number_of_messages + live.approximate_number_of_messages_not_visible + live.approximate_number_of_messages_delayed : 0

  const submit = async (e: React.FormEvent) => {
    e.preventDefault()
    if (!queue || (mode === "custom" && !dest)) return
    setPending(true)
    try {
      const res = await api.post<{ moved: number }>(`${queuePath(queue.name)}/redrive`, mode === "custom" ? { destination: dest } : {})
      setMoved(res.moved)
      toast.success(`Moved ${pluralize(res.moved, "message")} out of ${queue.name}`)
      await revalidate(QUEUES_PATH)
    } catch (err) {
      toast.error(errorMessage(err))
    } finally {
      setPending(false)
    }
  }

  return (
    <Dialog open={!!queue} onOpenChange={(o) => !o && !pending && onClose()}>
      <DialogContent className="sm:max-w-lg">
        <form onSubmit={submit} className="flex flex-col gap-4">
          <DialogHeader>
            <DialogTitle>Start DLQ redrive</DialogTitle>
            <DialogDescription>
              Moves the messages in <span className="font-mono">{queue?.name}</span> ({formatNumber(total)} now) to another queue. Their receive count is
              reset.
            </DialogDescription>
          </DialogHeader>
          <OptionGroup label="Redrive destination" columns={1}>
            <OptionCard
              selected={mode === "source"}
              onSelect={() => setMode("source")}
              icon={Undo2}
              title="Redrive to source queue(s)"
              description={
                <>
                  Each message goes back to the queue it was moved from{sources.length ? ` (${sources.join(", ")})` : ""}. Messages without a known source
                  stay here.
                </>
              }
            />
            <OptionCard
              selected={mode === "custom"}
              onSelect={() => setMode("custom")}
              icon={ArrowRightLeft}
              title="Redrive to a custom destination"
              description="Move every message to one queue of the same type."
            />
          </OptionGroup>
          {mode === "custom" && (
            <Field label="Destination queue" htmlFor="redrive-dest" help={candidates.length ? "Queues of the same type (standard or FIFO)." : "No other queue of the same type exists."}>
              <Select value={dest} onValueChange={setDest}>
                <SelectTrigger id="redrive-dest" className="w-full">
                  <SelectValue placeholder="Choose a queue" />
                </SelectTrigger>
                <SelectContent>
                  {candidates.map((q) => (
                    <SelectItem key={q.name} value={q.name}>
                      {q.name}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </Field>
          )}
          {moved !== null && (
            <Alert variant="success">
              <CheckCircle2 />
              <AlertDescription>Moved {pluralize(moved, "message")}.</AlertDescription>
            </Alert>
          )}
          <DialogFooter>
            <Button type="button" variant="outline" onClick={onClose} disabled={pending}>
              {moved !== null ? "Close" : "Cancel"}
            </Button>
            <Button type="submit" disabled={pending || (mode === "custom" && !dest)}>
              {pending && <Loader2 className="animate-spin" />}
              {moved !== null ? "Redrive again" : "Redrive messages"}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}
