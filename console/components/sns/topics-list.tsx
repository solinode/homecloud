"use client"

import { useEffect, useState } from "react"
import { useRouter } from "next/navigation"
import { ListOrdered, Loader2, Megaphone, Plus } from "lucide-react"
import { toast } from "sonner"

import { Button } from "@/components/ui/button"
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { Switch } from "@/components/ui/switch"
import { ActionsMenu, type ActionItem } from "@/components/console/actions-menu"
import { ConfirmDialog } from "@/components/console/confirm-dialog"
import { CopyableText } from "@/components/console/copy-button"
import { CellLink, CellText, DataTable, type Column } from "@/components/console/data-table"
import { EmptyState } from "@/components/console/empty-state"
import { Field } from "@/components/console/form-field"
import { OptionCard, OptionGroup } from "@/components/console/option-card"
import { PageHeader } from "@/components/console/page-header"
import { TagsEditor, rowsToTags, type TagRow } from "@/components/console/tags-editor"
import { TimeAgo } from "@/components/console/time-ago"
import { QueueTypeBadge } from "@/components/sqs/common"
import { api, errorMessage } from "@/lib/api"
import { formatNumber } from "@/lib/format"
import { revalidate, useQueryParam, useSetQueryParam } from "@/lib/hooks"
import type { Topic } from "@/lib/types"

import { SNS_PATH, TOPICS_PATH, TOPIC_NAME_RE, topicHref, topicPath, useTopics } from "./common"

const columns: Column<Topic>[] = [
  {
    id: "name",
    header: "Name",
    cell: (t) => <CellLink href={topicHref(t.name)}>{t.name}</CellLink>,
    value: (t) => t.name,
  },
  { id: "type", header: "Type", cell: (t) => <QueueTypeBadge fifo={t.fifo} />, value: (t) => (t.fifo ? "FIFO" : "Standard"), hideBelow: "sm" },
  {
    id: "display",
    header: "Display name",
    cell: (t) => <CellText max="16rem">{t.display_name}</CellText>,
    value: (t) => t.display_name,
    hideBelow: "md",
  },
  { id: "subs", header: "Subscriptions", cell: (t) => <span className="tabular-nums">{formatNumber(t.subscriptions)}</span>, value: (t) => t.subscriptions },
  {
    id: "published",
    header: "Messages published",
    cell: (t) => <span className="tabular-nums">{formatNumber(t.messages_published)}</span>,
    value: (t) => t.messages_published,
    hideBelow: "sm",
  },
  { id: "arn", header: "ARN", cell: (t) => <CopyableText value={t.arn} className="max-w-xs" />, value: (t) => t.arn, sortable: false, hideBelow: "lg" },
  { id: "created", header: "Created", cell: (t) => <TimeAgo value={t.created_at} />, value: (t) => t.created_at, hideBelow: "md" },
]

export function TopicsList() {
  const router = useRouter()
  const { data, error, isLoading, isValidating, mutate } = useTopics()
  const [selected, setSelected] = useState<string[]>([])
  const [creating, setCreating] = useState(false)
  const [deleting, setDeleting] = useState<Topic | null>(null)
  const createParam = useQueryParam("create")
  const setParam = useSetQueryParam()

  useEffect(() => {
    if (createParam === "1") setCreating(true)
  }, [createParam])

  const onCreateOpenChange = (o: boolean) => {
    setCreating(o)
    if (!o && createParam) setParam("create", null)
  }

  const sel = data?.find((t) => t.name === selected[0]) ?? null
  const items: ActionItem[] = [
    { label: "Publish message", onSelect: () => sel && router.push(topicHref(sel.name, "publish")), disabled: !sel },
    { label: "View subscriptions", onSelect: () => sel && router.push(topicHref(sel.name)), disabled: !sel },
    { separator: true },
    { label: "Delete", onSelect: () => sel && setDeleting(sel), disabled: !sel, destructive: true },
  ]

  return (
    <div className="flex flex-col gap-4">
      <PageHeader
        title="Topics"
        description="Publish/subscribe topics that fan messages out to SQS queues, Lambda functions and HTTP(S) endpoints."
        breadcrumbs={[{ label: "SNS", href: "/sns/" }, { label: "Topics" }]}
      />
      <DataTable
        title="Topics"
        data={data}
        columns={columns}
        rowId={(t) => t.name}
        loading={isLoading}
        error={error}
        onRefresh={() => mutate()}
        refreshing={isValidating}
        selection="single"
        selected={selected}
        onSelectedChange={setSelected}
        searchPlaceholder="Search topics"
        defaultSort={{ id: "name" }}
        actions={
          <>
            <ActionsMenu items={items} disabled={!sel} />
            <Button size="sm" onClick={() => setCreating(true)}>
              <Plus /> Create topic
            </Button>
          </>
        }
        empty={
          <EmptyState
            icon={Megaphone}
            title="No topics"
            description="Create a topic, subscribe endpoints to it, then publish messages to all of them at once."
            action={
              <Button size="sm" onClick={() => setCreating(true)}>
                <Plus /> Create topic
              </Button>
            }
          />
        }
      />
      <CreateTopicDialog open={creating} onOpenChange={onCreateOpenChange} existing={(data ?? []).map((t) => t.name)} />
      <ConfirmDialog
        open={!!deleting}
        onOpenChange={(o) => !o && setDeleting(null)}
        title={`Delete ${deleting?.name ?? "topic"}?`}
        description={
          <>
            The topic and its {deleting ? formatNumber(deleting.subscriptions) : ""} subscription{deleting?.subscriptions === 1 ? "" : "s"} are deleted.
            Subscribed queues and functions are not affected.
          </>
        }
        confirmText={deleting?.name}
        onConfirm={async () => {
          if (!deleting) return
          await api.del(topicPath(deleting.name))
          await revalidate(SNS_PATH)
          toast.success(`Deleted topic ${deleting.name}`)
        }}
      />
    </div>
  )
}

export function CreateTopicDialog({ open, onOpenChange, existing }: { open: boolean; onOpenChange: (o: boolean) => void; existing: string[] }) {
  const router = useRouter()
  const [fifo, setFifo] = useState(false)
  const [dedup, setDedup] = useState(false)
  const [name, setName] = useState("")
  const [display, setDisplay] = useState("")
  const [tags, setTags] = useState<TagRow[]>([])
  const [touched, setTouched] = useState(false)
  const [pending, setPending] = useState(false)

  useEffect(() => {
    if (open) {
      setFifo(false)
      setDedup(false)
      setName("")
      setDisplay("")
      setTags([])
      setTouched(false)
    }
  }, [open])

  const tagKeys = tags.map((r) => r.key.trim()).filter(Boolean)
  const setType = (f: boolean) => {
    if (f === fifo) return
    setFifo(f)
    setName((n) => (f ? (n && !n.endsWith(".fifo") ? `${n}.fifo` : n) : n.replace(/\.fifo$/, "")))
  }
  const nameErr = !TOPIC_NAME_RE.test(name)
    ? "Use 1-256 letters, digits, hyphens (-) or underscores (_)"
    : fifo !== name.endsWith(".fifo")
      ? fifo
        ? "FIFO topic names must end in .fifo"
        : "Only FIFO topic names end in .fifo"
      : existing.includes(name)
      ? `A topic named ${name} already exists`
      : null
  const tagErr = new Set(tagKeys).size !== tagKeys.length ? "Tag keys must be unique" : null
  const displayErr = display.length > 100 ? "Display names are at most 100 characters" : null

  const submit = async (e: React.FormEvent) => {
    e.preventDefault()
    setTouched(true)
    if (nameErr || tagErr || displayErr) return
    setPending(true)
    try {
      await api.post(TOPICS_PATH, {
        name,
        display_name: display.trim(),
        tags: rowsToTags(tags),
        ...(fifo ? { attributes: { FifoTopic: "true" }, content_based_deduplication: dedup } : {}),
      })
      toast.success(`Created topic ${name}`)
      await revalidate(SNS_PATH)
      onOpenChange(false)
      router.push(topicHref(name))
    } catch (err) {
      toast.error(errorMessage(err))
    } finally {
      setPending(false)
    }
  }

  return (
    <Dialog open={open} onOpenChange={(o) => !pending && onOpenChange(o)}>
      <DialogContent className="max-h-[90vh] overflow-y-auto sm:max-w-xl">
        <form onSubmit={submit} className="flex flex-col gap-4">
          <DialogHeader>
            <DialogTitle>Create topic</DialogTitle>
            <DialogDescription>A topic is a communication channel: publishers send to it, and every subscription receives a copy.</DialogDescription>
          </DialogHeader>
          <Field label="Type" help="You can't change the topic type after you create it.">
            <OptionGroup label="Topic type">
              <OptionCard
                selected={!fifo}
                onSelect={() => setType(false)}
                icon={Megaphone}
                title="Standard"
                description="Best-effort ordering, at-least-once delivery; all protocols."
              />
              <OptionCard
                selected={fifo}
                onSelect={() => setType(true)}
                icon={ListOrdered}
                title="FIFO"
                description="Strict ordering and deduplication; delivers to SQS queues."
              />
            </OptionGroup>
          </Field>
          <Field
            label="Name"
            htmlFor="topic-name"
            error={touched || name ? nameErr : null}
            help={`Letters, digits, hyphens and underscores; up to 256 characters${fifo ? ", ending in .fifo" : ""}.`}
          >
            <Input
              id="topic-name"
              autoFocus
              autoComplete="off"
              spellCheck={false}
              value={name}
              onChange={(e) => setName(e.target.value.trim())}
              onBlur={() => fifo && name && !name.endsWith(".fifo") && setName(`${name}.fifo`)}
              placeholder={fifo ? "order-events.fifo" : "order-events"}
              aria-invalid={!!(touched && nameErr)}
            />
          </Field>
          <Field label="Display name" htmlFor="topic-display" optional error={displayErr} help="A friendly name, used as the sender name for notifications.">
            <Input id="topic-display" value={display} onChange={(e) => setDisplay(e.target.value)} placeholder="Order events" />
          </Field>
          {fifo && (
            <div className="flex items-start justify-between gap-4 rounded-md border p-3">
              <div>
                <Label htmlFor="topic-dedup" className="font-medium">
                  Content-based deduplication
                </Label>
                <p className="text-muted-foreground mt-0.5 text-xs">
                  Use a hash of the message body as the deduplication ID, so publishers don&apos;t have to provide one.
                </p>
              </div>
              <Switch id="topic-dedup" checked={dedup} onCheckedChange={setDedup} />
            </div>
          )}
          <Field label="Tags" optional error={touched ? tagErr : null}>
            <TagsEditor rows={tags} onChange={setTags} />
          </Field>
          <DialogFooter>
            <Button type="button" variant="outline" onClick={() => onOpenChange(false)} disabled={pending}>
              Cancel
            </Button>
            <Button type="submit" disabled={pending}>
              {pending && <Loader2 className="animate-spin" />}
              Create topic
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}
