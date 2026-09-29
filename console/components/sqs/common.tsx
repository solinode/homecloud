"use client"

import { useEffect, useState, type ReactNode } from "react"
import Link from "next/link"
import { Loader2 } from "lucide-react"
import { toast } from "sonner"

import { Button } from "@/components/ui/button"
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog"
import { Label } from "@/components/ui/label"
import { RadioGroup, RadioGroupItem } from "@/components/ui/radio-group"
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select"
import { ConfirmDialog } from "@/components/console/confirm-dialog"
import { api, errorMessage, seg } from "@/lib/api"
import { formatNumber, pluralize } from "@/lib/format"
import { revalidate, useApi } from "@/lib/hooks"
import type { Queue } from "@/lib/types"
import { cn } from "@/lib/utils"

export const QUEUES_PATH = "/api/v1/sqs/queues"

/** Queue counts change as producers and consumers run; keep them fresh. */
export const QUEUE_POLL = 5000

export const queuePath = (name: string) => `${QUEUES_PATH}/${seg(name)}`

export const queueHref = (name: string, tab?: string) => `/sqs/queue/?name=${encodeURIComponent(name)}${tab ? `&tab=${tab}` : ""}`

/** "arn:hc:sqs:local-1:123:jobs" -> "jobs" (also returns plain names unchanged). */
export const nameFromArn = (arn: string) => arn.slice(arn.lastIndexOf(":") + 1)

export function useQueues(refreshInterval = QUEUE_POLL) {
  return useApi<Queue[]>(QUEUES_PATH, { refreshInterval })
}

/** QueueTypeBadge shows Standard / FIFO. */
export function QueueTypeBadge({ fifo, className }: { fifo: boolean; className?: string }) {
  return (
    <span
      className={cn(
        "inline-flex items-center rounded-md px-1.5 py-0.5 text-xs font-medium whitespace-nowrap ring-1 ring-inset",
        fifo
          ? "bg-violet-50 text-violet-700 ring-violet-600/20 dark:bg-violet-500/10 dark:text-violet-300 dark:ring-violet-400/20"
          : "bg-muted text-muted-foreground ring-border",
        className,
      )}
    >
      {fifo ? "FIFO" : "Standard"}
    </span>
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
  const total = queue ? queue.approximate_number_of_messages + queue.approximate_number_of_messages_not_visible + queue.approximate_number_of_messages_delayed : 0

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
          <RadioGroup value={mode} onValueChange={(v) => setMode(v as "source" | "custom")} className="gap-3">
            <label className={cn("flex cursor-pointer items-start gap-3 rounded-md border p-3", mode === "source" && "border-primary bg-primary/5")}>
              <RadioGroupItem value="source" className="mt-0.5" />
              <div className="min-w-0 text-sm">
                <div className="font-medium">Redrive to source queue(s)</div>
                <p className="text-muted-foreground text-xs">
                  Each message goes back to the queue it was moved from{sources.length ? ` (${sources.join(", ")})` : ""}. Messages without a known source
                  stay here.
                </p>
              </div>
            </label>
            <label className={cn("flex cursor-pointer items-start gap-3 rounded-md border p-3", mode === "custom" && "border-primary bg-primary/5")}>
              <RadioGroupItem value="custom" className="mt-0.5" />
              <div className="flex min-w-0 flex-1 flex-col gap-2 text-sm">
                <div>
                  <div className="font-medium">Redrive to a custom destination</div>
                  <p className="text-muted-foreground text-xs">Move every message to one queue of the same type.</p>
                </div>
                {mode === "custom" && (
                  <div className="flex flex-col gap-1.5">
                    <Label htmlFor="redrive-dest" className="sr-only">
                      Destination queue
                    </Label>
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
                  </div>
                )}
              </div>
            </label>
          </RadioGroup>
          {moved !== null && (
            <p className="rounded-md border border-emerald-600/30 bg-emerald-50 p-3 text-sm text-emerald-800 dark:border-emerald-400/30 dark:bg-emerald-500/10 dark:text-emerald-300">
              Moved {pluralize(moved, "message")}.
            </p>
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
