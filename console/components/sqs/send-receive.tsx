"use client"

import { useEffect, useRef, useState } from "react"
import { Eraser, Loader2, Radio, Send, Square, Timer, Trash2 } from "lucide-react"
import { toast } from "sonner"

import { Button } from "@/components/ui/button"
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog"
import { Input } from "@/components/ui/input"
import { Textarea } from "@/components/ui/textarea"
import { CopyableText } from "@/components/console/copy-button"
import { CellText, DataTable, type Column } from "@/components/console/data-table"
import { EmptyState } from "@/components/console/empty-state"
import { Field } from "@/components/console/form-field"
import { Section } from "@/components/console/section"
import { StatusBadge } from "@/components/console/status-badge"
import { api, errorMessage, request } from "@/lib/api"
import { formatDate, formatTime, pluralize } from "@/lib/format"
import { revalidate, useNow } from "@/lib/hooks"
import type { Queue, ReceivedMessage, SendMessageInput, SendMessageResult } from "@/lib/types"
import { cn } from "@/lib/utils"

import { QUEUES_PATH, queuePath } from "./common"
import { AttributesEditor, MessageDetailDialog, attrRowsError, attrRowsToMap, bodyPreview, prettyBody, type AttrRow } from "./messages"

export function SendReceive({ queue }: { queue: Queue }) {
  return (
    <div className="flex flex-col gap-4">
      <SendPanel queue={queue} />
      <ReceivePanel queue={queue} />
    </div>
  )
}

// ---- send ----

function SendPanel({ queue }: { queue: Queue }) {
  const [body, setBody] = useState("")
  const [attrs, setAttrs] = useState<AttrRow[]>([])
  const [delay, setDelay] = useState("")
  const [groupId, setGroupId] = useState("")
  const [dedupId, setDedupId] = useState("")
  const [submitted, setSubmitted] = useState(false)
  const [pending, setPending] = useState(false)
  const [last, setLast] = useState<(SendMessageResult & { at: number }) | null>(null)

  const size = new TextEncoder().encode(body).length
  const errors: Record<string, string | null | undefined> = {
    body: !body ? "Enter a message body" : size > queue.max_message_size ? `The body exceeds the queue's ${queue.max_message_size / 1024} KB limit` : null,
    attrs: attrRowsError(attrs),
    delay: !queue.fifo && delay && !(/^\d+$/.test(delay) && Number(delay) <= 900) ? "Enter 0-900 seconds" : null,
    group: queue.fifo && !groupId.trim() ? "A message group ID is required for FIFO queues" : null,
    dedup: queue.fifo && !queue.content_based_deduplication && !dedupId.trim() ? "A deduplication ID is required (content-based deduplication is off)" : null,
  }
  const err = (k: string) => (submitted ? errors[k] : undefined)
  const valid = Object.values(errors).every((v) => !v)
  const json = prettyBody(body).json

  const send = async (e: React.FormEvent) => {
    e.preventDefault()
    setSubmitted(true)
    if (!valid) return
    const input: SendMessageInput = { body, message_attributes: attrRowsToMap(attrs) }
    if (!queue.fifo && delay) input.delay_seconds = Number(delay)
    if (queue.fifo) {
      input.group_id = groupId.trim()
      if (dedupId.trim()) input.dedup_id = dedupId.trim()
    }
    setPending(true)
    try {
      const res = await api.post<SendMessageResult>(`${queuePath(queue.name)}/messages`, input)
      setLast({ ...res, at: Date.now() })
      if (res.duplicate) toast.info(`Duplicate message dropped (deduplication ID seen in the last 5 minutes)`)
      else toast.success(`Sent message ${res.message_id}`)
      setSubmitted(false)
      if (queue.fifo) setDedupId("")
      await revalidate(QUEUES_PATH)
    } catch (e) {
      toast.error(errorMessage(e))
    } finally {
      setPending(false)
    }
  }

  return (
    <Section title="Send message" description={`Send a message to ${queue.name}.`}>
      <form onSubmit={send} className="flex flex-col gap-4">
        <Field
          label="Message body"
          htmlFor="send-body"
          error={err("body")}
          help={
            <span className="flex flex-wrap items-center gap-x-3">
              <span>
                {size.toLocaleString()} bytes of {queue.max_message_size.toLocaleString()}
              </span>
              {json && <span className="text-success">Valid JSON</span>}
            </span>
          }
        >
          <Textarea
            id="send-body"
            rows={6}
            value={body}
            onChange={(e) => setBody(e.target.value)}
            placeholder={'{"order_id": 1234, "status": "paid"}'}
            className="font-mono text-[13px]"
            spellCheck={false}
            aria-invalid={!!err("body")}
          />
        </Field>
        {json && (
          <div className="-mt-2">
            <Button type="button" variant="ghost" size="sm" onClick={() => setBody(prettyBody(body).text)}>
              Format JSON
            </Button>
          </div>
        )}
        {queue.fifo ? (
          <div className="grid grid-cols-1 gap-4 md:grid-cols-2">
            <Field label="Message group ID" htmlFor="send-group" error={err("group")} help="Messages in the same group are delivered in order, one at a time.">
              <Input id="send-group" value={groupId} onChange={(e) => setGroupId(e.target.value)} placeholder="orders" className="h-9" />
            </Field>
            <Field
              label="Message deduplication ID"
              htmlFor="send-dedup"
              optional={queue.content_based_deduplication}
              error={err("dedup")}
              help={
                queue.content_based_deduplication
                  ? "Leave empty to use a hash of the body (content-based deduplication is on)."
                  : "Messages with an ID seen in the last 5 minutes are accepted but not delivered."
              }
            >
              <Input id="send-dedup" value={dedupId} onChange={(e) => setDedupId(e.target.value)} className="h-9" />
            </Field>
          </div>
        ) : (
          <Field
            label="Delivery delay"
            htmlFor="send-delay"
            optional
            error={err("delay")}
            help={`0-900 seconds. Leave empty to use the queue default (${queue.delay_seconds} seconds).`}
          >
            <div className="flex items-center gap-2">
              <Input id="send-delay" inputMode="numeric" value={delay} onChange={(e) => setDelay(e.target.value)} className="h-9 w-32" />
              <span className="text-muted-foreground text-sm">seconds</span>
            </div>
          </Field>
        )}
        <Field label="Message attributes" optional error={err("attrs")}>
          <AttributesEditor rows={attrs} onChange={setAttrs} />
        </Field>
        <div className="flex flex-wrap items-center justify-end gap-3 border-t pt-4">
          {last && (
            <span className="text-muted-foreground mr-auto flex min-w-0 flex-wrap items-center gap-1 text-sm">
              {last.duplicate ? "Duplicate dropped" : "Last sent"} {formatTime(last.at)}:
              <CopyableText value={last.message_id} />
              {last.sequence_number && <span className="font-mono text-xs">seq {last.sequence_number}</span>}
            </span>
          )}
          <Button type="button" variant="outline" onClick={() => (setBody(""), setAttrs([]), setDelay(""), setSubmitted(false))} disabled={pending}>
            <Eraser /> Clear
          </Button>
          <Button type="submit" disabled={pending}>
            {pending ? <Loader2 className="animate-spin" /> : <Send />}
            Send message
          </Button>
        </div>
      </form>
    </Section>
  )
}

// ---- receive ----

interface Held extends ReceivedMessage {
  /** when this receive's visibility timeout ends (ms) */
  visibleAt: number
}

function ReceivePanel({ queue }: { queue: Queue }) {
  const [max, setMax] = useState("10")
  const [wait, setWait] = useState(String(queue.receive_wait_time_seconds || 5))
  const [vis, setVis] = useState("")
  const [polling, setPolling] = useState(false)
  const [progress, setProgress] = useState<{ start: number; until: number } | null>(null)
  const [held, setHeld] = useState<Held[]>([])
  const [open, setOpen] = useState<Held | null>(null)
  const [changing, setChanging] = useState<Held | null>(null)
  const [deleting, setDeleting] = useState(false)
  const abort = useRef<AbortController | null>(null)
  const now = useNow(1000)

  useEffect(() => () => abort.current?.abort(), [])

  const errors = {
    max: /^\d+$/.test(max) && Number(max) >= 1 && Number(max) <= 10 ? null : "1-10",
    wait: /^\d+$/.test(wait) && Number(wait) <= 20 ? null : "0-20 seconds",
    vis: !vis || (/^\d+$/.test(vis) && Number(vis) <= 43200) ? null : "0-43200 seconds",
  }
  const valid = !errors.max && !errors.wait && !errors.vis

  const poll = async () => {
    if (!valid || polling) return
    const want = Number(max)
    const waitS = Number(wait)
    const visS = vis ? Number(vis) : queue.visibility_timeout
    const ctrl = new AbortController()
    abort.current = ctrl
    const start = Date.now()
    const deadline = start + waitS * 1000
    setPolling(true)
    setProgress({ start, until: deadline })
    let got = 0
    try {
      // Long-poll repeatedly until we have the requested count or the wait time is used up.
      do {
        const remaining = Math.max(0, Math.ceil((deadline - Date.now()) / 1000))
        const msgs = await request<ReceivedMessage[]>("POST", `${queuePath(queue.name)}/messages/receive`, {
          body: { max_messages: want - got, wait_seconds: Math.min(20, remaining), visibility_timeout: vis ? visS : undefined },
          signal: ctrl.signal,
        })
        if (ctrl.signal.aborted) break
        if (msgs.length) {
          const at = Date.now()
          got += msgs.length
          setHeld((h) => {
            const ids = new Set(msgs.map((m) => m.message_id))
            return [...msgs.map((m) => ({ ...m, visibleAt: at + visS * 1000 })), ...h.filter((x) => !ids.has(x.message_id))]
          })
        }
      } while (got < want && Date.now() < deadline && !ctrl.signal.aborted)
      if (!ctrl.signal.aborted) toast.success(got ? `Received ${pluralize(got, "message")}` : "No messages available")
    } catch (e) {
      if (!(e instanceof DOMException && e.name === "AbortError")) toast.error(errorMessage(e))
    } finally {
      setPolling(false)
      setProgress(null)
      abort.current = null
      revalidate(QUEUES_PATH)
    }
  }

  const stop = () => abort.current?.abort()

  const deleteMsgs = async (list: Held[]) => {
    if (!list.length) return
    setDeleting(true)
    try {
      const res = await api.post<{ deleted: number; failed: string[] }>(`${queuePath(queue.name)}/messages/delete`, {
        receipt_handles: list.map((m) => m.receipt_handle),
      })
      const failed = new Set(res.failed ?? [])
      const done = new Set(list.filter((m) => !failed.has(m.receipt_handle)).map((m) => m.message_id))
      setHeld((h) => h.filter((m) => !done.has(m.message_id)))
      if (res.deleted) toast.success(list.length === 1 ? `Deleted message ${list[0].message_id}` : `Deleted ${pluralize(res.deleted, "message")}`)
      if (failed.size)
        toast.error(`${pluralize(failed.size, "receipt handle")} expired or invalid. The message was received again or its visibility timeout ended.`)
      setOpen((o) => (o && done.has(o.message_id) ? null : o))
      await revalidate(QUEUES_PATH)
    } catch (e) {
      toast.error(errorMessage(e))
    } finally {
      setDeleting(false)
    }
  }

  const elapsed = progress ? Math.min(1, (now - progress.start) / Math.max(1, progress.until - progress.start)) : 0

  const columns: Column<Held>[] = [
    {
      id: "id",
      header: "Message ID",
      cell: (m) => (
        <span className="text-primary block truncate font-mono text-[13px] whitespace-nowrap hover:underline" title={m.message_id}>
          {m.message_id.slice(0, 8)}…
        </span>
      ),
      value: (m) => m.message_id,
    },
    {
      id: "sent",
      header: "Sent",
      cell: (m) => <span className="whitespace-nowrap">{formatDate(Number(m.attributes?.SentTimestamp))}</span>,
      value: (m) => Number(m.attributes?.SentTimestamp) || 0,
      hideBelow: "md",
    },
    {
      id: "receives",
      header: "Receives",
      cell: (m) => <span className="tabular-nums">{m.attributes?.ApproximateReceiveCount ?? "-"}</span>,
      value: (m) => Number(m.attributes?.ApproximateReceiveCount) || 0,
      hideBelow: "sm",
    },
    {
      id: "body",
      header: "Body",
      cell: (m) => (
        <CellText mono max="28rem" title={m.body}>
          {bodyPreview(m.body)}
        </CellText>
      ),
      value: (m) => m.body,
    },
    {
      id: "visibility",
      header: "Visibility",
      cell: (m) => {
        const left = Math.ceil((m.visibleAt - now) / 1000)
        return left > 0 ? (
          <span className="text-muted-foreground whitespace-nowrap">Hidden for {left}s</span>
        ) : (
          <StatusBadge status="visible" label="Visible again" tone="warning" />
        )
      },
      value: (m) => m.visibleAt,
      hideBelow: "lg",
    },
    {
      id: "actions",
      header: <span className="sr-only">Actions</span>,
      sortable: false,
      className: "text-right",
      cell: (m) => (
        <span className="inline-flex whitespace-nowrap" onClick={(e) => e.stopPropagation()}>
          <Button variant="ghost" size="icon" className="size-7" aria-label="Change visibility" title="Change visibility" onClick={() => setChanging(m)}>
            <Timer />
          </Button>
          <Button
            variant="ghost"
            size="icon"
            className="text-destructive size-7"
            aria-label="Delete message"
            title="Delete message"
            disabled={deleting}
            onClick={() => deleteMsgs([m])}
          >
            <Trash2 />
          </Button>
        </span>
      ),
    },
  ]

  return (
    <>
    <Section
      title="Receive messages"
      description="Receiving hides messages from other consumers for the visibility timeout. Delete them once processed, or they become visible again."
    >
      <div className="flex flex-col gap-4">
        <div className="flex flex-wrap items-end gap-4">
          <Field label="Maximum messages" htmlFor="recv-max" error={errors.max}>
            <Input id="recv-max" inputMode="numeric" value={max} onChange={(e) => setMax(e.target.value)} className="h-9 w-24" disabled={polling} />
          </Field>
          <Field label="Polling duration" htmlFor="recv-wait" error={errors.wait}>
            <div className="flex items-center gap-2">
              <Input id="recv-wait" inputMode="numeric" value={wait} onChange={(e) => setWait(e.target.value)} className="h-9 w-24" disabled={polling} />
              <span className="text-muted-foreground text-sm">seconds</span>
            </div>
          </Field>
          <Field label="Visibility timeout" htmlFor="recv-vis" optional error={errors.vis}>
            <div className="flex items-center gap-2">
              <Input
                id="recv-vis"
                inputMode="numeric"
                value={vis}
                onChange={(e) => setVis(e.target.value)}
                placeholder={String(queue.visibility_timeout)}
                className="h-9 w-24"
                disabled={polling}
              />
              <span className="text-muted-foreground text-sm">seconds</span>
            </div>
          </Field>
          <div className="flex gap-2">
            {polling ? (
              <Button type="button" variant="outline" onClick={stop}>
                <Square /> Stop polling
              </Button>
            ) : (
              <Button type="button" onClick={poll} disabled={!valid}>
                <Radio /> Poll for messages
              </Button>
            )}
          </div>
        </div>
        {polling && (
          <div className="flex flex-col gap-1.5">
            <div className="text-muted-foreground flex items-center gap-2 text-sm">
              <Loader2 className="size-4 animate-spin" />
              Polling {queue.name}...{" "}
              {progress && progress.until > progress.start && `${Math.max(0, Math.ceil((progress.until - now) / 1000))}s left`}
            </div>
            <div className="bg-muted h-1 overflow-hidden rounded-full">
              <div className="bg-primary h-full transition-[width] duration-1000 ease-linear" style={{ width: `${elapsed * 100}%` }} />
            </div>
          </div>
        )}
      </div>

    </Section>

    <DataTable
      title="Received messages"
      data={held}
      columns={columns}
      rowId={(m) => m.message_id}
      selection="none"
      noSearch={held.length === 0}
      searchPlaceholder="Search received messages"
      onRowClick={(m) => setOpen(m)}
      actions={
        <>
          <Button variant="outline" size="sm" onClick={() => setHeld([])} disabled={!held.length || deleting}>
            <Eraser /> Clear list
          </Button>
          <Button variant="outline" size="sm" onClick={() => deleteMsgs(held)} disabled={!held.length || deleting}>
            {deleting ? <Loader2 className="animate-spin" /> : <Trash2 />}
            Delete all
          </Button>
        </>
      }
      empty={
        <EmptyState
          icon={Radio}
          title="No messages received"
          description="Poll for messages to receive them from the queue. Received messages stay hidden from other consumers until you delete them or the visibility timeout ends."
          action={
            !polling && (
              <Button size="sm" onClick={poll} disabled={!valid}>
                <Radio /> Poll for messages
              </Button>
            )
          }
        />
      }
    />

      <MessageDetailDialog
        message={open}
        onClose={() => setOpen(null)}
        actions={
          open && (
            <>
              <Button variant="outline" size="sm" onClick={() => setChanging(open)}>
                <Timer /> Change visibility
              </Button>
              <Button variant="destructive" size="sm" disabled={deleting} onClick={() => deleteMsgs([open])}>
                {deleting ? <Loader2 className="animate-spin" /> : <Trash2 />}
                Delete
              </Button>
            </>
          )
        }
      />
      <VisibilityDialog
        queue={queue}
        message={changing}
        onClose={() => setChanging(null)}
        onChanged={(m, seconds) => {
          const at = Date.now() + seconds * 1000
          setHeld((h) => h.map((x) => (x.message_id === m.message_id ? { ...x, visibleAt: at } : x)))
          setOpen((o) => (o?.message_id === m.message_id ? { ...o, visibleAt: at } : o))
        }}
      />
    </>
  )
}

function VisibilityDialog({
  queue,
  message,
  onClose,
  onChanged,
}: {
  queue: Queue
  message: Held | null
  onClose: () => void
  onChanged: (m: Held, seconds: number) => void
}) {
  const [value, setValue] = useState("0")
  const [pending, setPending] = useState(false)
  useEffect(() => {
    if (message) setValue("0")
  }, [message])
  const valid = /^\d+$/.test(value) && Number(value) <= 43200

  const submit = async (e: React.FormEvent) => {
    e.preventDefault()
    if (!message || !valid) return
    setPending(true)
    try {
      await api.post(`${queuePath(queue.name)}/messages/visibility`, { receipt_handle: message.receipt_handle, visibility_timeout: Number(value) })
      toast.success(Number(value) === 0 ? "The message is visible again" : `The message is hidden for ${value} more seconds`)
      onChanged(message, Number(value))
      await revalidate(QUEUES_PATH)
      onClose()
    } catch (err) {
      toast.error(errorMessage(err))
    } finally {
      setPending(false)
    }
  }

  return (
    <Dialog open={!!message} onOpenChange={(o) => !o && !pending && onClose()}>
      <DialogContent className="sm:max-w-md">
        <form onSubmit={submit} className="flex flex-col gap-4">
          <DialogHeader>
            <DialogTitle>Change visibility timeout</DialogTitle>
            <DialogDescription>
              Sets how much longer this message stays hidden, counted from now. 0 makes it visible to consumers immediately.
            </DialogDescription>
          </DialogHeader>
          <Field label="Visibility timeout" htmlFor="chg-vis" error={valid ? undefined : "Enter 0-43200 seconds"}>
            <div className="flex items-center gap-2">
              <Input id="chg-vis" autoFocus inputMode="numeric" value={value} onChange={(e) => setValue(e.target.value)} className="h-9 w-32" />
              <span className="text-muted-foreground text-sm">seconds</span>
            </div>
          </Field>
          <DialogFooter>
            <Button type="button" variant="outline" onClick={onClose} disabled={pending}>
              Cancel
            </Button>
            <Button type="submit" disabled={pending || !valid}>
              {pending && <Loader2 className="animate-spin" />}
              Save
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}
