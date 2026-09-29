"use client"

import { useEffect, useState } from "react"
import Link from "next/link"
import { Loader2, Pencil, Tags as TagsIcon } from "lucide-react"
import { toast } from "sonner"

import { Button } from "@/components/ui/button"
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog"
import { CopyableText } from "@/components/console/copy-button"
import { KeyValueGrid } from "@/components/console/key-value"
import { Section } from "@/components/console/section"
import { TagList, TagsEditor, rowsToTags, tagsToRows, type TagRow } from "@/components/console/tags-editor"
import { TimeAgo } from "@/components/console/time-ago"
import { api, errorMessage } from "@/lib/api"
import { formatDate } from "@/lib/format"
import { revalidate } from "@/lib/hooks"
import type { Queue } from "@/lib/types"

import { QUEUES_PATH, QueueTypeBadge, humanSeconds, nameFromArn, queueHref, queuePath, useQueues } from "./common"
import {
  AccessPolicyFields,
  ConfigurationFields,
  DeadLetterFields,
  EncryptionFields,
  RedriveAllowFields,
  configFromQueue,
  configToInput,
  parseRedriveAllow,
  prettyJson,
  sseMode,
  validateConfig,
  type QueueConfig,
} from "./queue-config"

function RedriveAllowSummary({ queue }: { queue: Queue }) {
  const p = parseRedriveAllow(queue.redrive_allow_policy)
  if (!p || p.redrivePermission === "allowAll") return <span>Allow all</span>
  if (p.redrivePermission === "denyAll") return <span>Deny all</span>
  return (
    <span className="flex flex-col gap-0.5">
      <span>By queue:</span>
      <span className="flex flex-wrap gap-x-3 gap-y-1">
        {(p.sourceQueueArns ?? []).map((a) => (
          <Link key={a} href={queueHref(nameFromArn(a))} className="text-primary hover:underline" title={a}>
            {nameFromArn(a)}
          </Link>
        ))}
      </span>
    </span>
  )
}

export function QueueSettings({ queue, editing, onEditingChange }: { queue: Queue; editing: boolean; onEditingChange: (e: boolean) => void }) {
  const [tagging, setTagging] = useState(false)
  return (
    <div className="flex flex-col gap-4">
      {editing ? (
        <EditForm queue={queue} onDone={() => onEditingChange(false)} />
      ) : (
        <Section
          title="Details"
          actions={
            <Button variant="outline" size="sm" onClick={() => onEditingChange(true)}>
              <Pencil /> Edit
            </Button>
          }
        >
          <KeyValueGrid
            columns={3}
            items={[
              { label: "Name", value: queue.name },
              { label: "Type", value: <QueueTypeBadge fifo={queue.fifo} /> },
              { label: "Created", value: <span>{formatDate(queue.created_at)} (<TimeAgo value={queue.created_at} />)</span> },
              { label: "Visibility timeout", value: humanSeconds(queue.visibility_timeout) },
              { label: "Message retention period", value: humanSeconds(queue.message_retention_seconds) },
              { label: "Last modified", value: <span>{formatDate(queue.last_modified)} (<TimeAgo value={queue.last_modified} />)</span> },
              { label: "Delivery delay", value: humanSeconds(queue.delay_seconds) },
              {
                label: "Receive message wait time",
                value: queue.receive_wait_time_seconds ? `${humanSeconds(queue.receive_wait_time_seconds)} (long polling)` : "0 seconds (short polling)",
              },
              { label: "Maximum message size", value: `${Math.round(queue.max_message_size / 1024)} KB` },
              ...(queue.fifo
                ? [
                    { label: "Content-based deduplication", value: queue.content_based_deduplication ? "Enabled" : "Disabled" },
                    { label: "Deduplication scope", value: queue.deduplication_scope === "messageGroup" ? "Message group" : "Queue" },
                    { label: "FIFO throughput limit", value: queue.fifo_throughput_limit === "perMessageGroupId" ? "Per message group ID" : "Per queue" },
                  ]
                : []),
              { label: "URL", value: <CopyableText value={queue.url} />, wide: true },
              { label: "ARN", value: <CopyableText value={queue.arn} />, wide: true },
            ]}
          />
        </Section>
      )}

      {!editing && (
        <Section title="Dead-letter queue">
          <KeyValueGrid
            columns={3}
            items={[
              {
                label: "Redrive policy",
                value: queue.redrive_policy ? (
                  <span>
                    Move to{" "}
                    <Link href={queueHref(queue.redrive_policy.dead_letter_queue)} className="text-primary hover:underline">
                      {queue.redrive_policy.dead_letter_queue}
                    </Link>{" "}
                    after {queue.redrive_policy.max_receive_count} receives
                  </span>
                ) : (
                  "Disabled"
                ),
              },
              {
                label: "Dead-letter queue for",
                value: queue.dead_letter_source_queues?.length ? (
                  <span className="flex flex-wrap gap-x-3 gap-y-1">
                    {queue.dead_letter_source_queues.map((s) => (
                      <Link key={s} href={queueHref(s)} className="text-primary hover:underline">
                        {s}
                      </Link>
                    ))}
                  </span>
                ) : (
                  "No queues"
                ),
              },
              { label: "Redrive allow policy", value: <RedriveAllowSummary queue={queue} /> },
            ]}
          />
        </Section>
      )}

      {!editing && (
        <Section title="Encryption">
          <KeyValueGrid
            columns={3}
            items={[
              {
                label: "Server-side encryption",
                value: { sqs: "Enabled (SSE-SQS)", kms: "Enabled (SSE-KMS)", none: "Disabled" }[sseMode(queue)],
              },
              ...(queue.kms_master_key_id
                ? [
                    { label: "KMS key", value: <CopyableText value={queue.kms_master_key_id} /> },
                    { label: "Data key reuse period", value: humanSeconds(queue.kms_data_key_reuse_period_seconds ?? 300) },
                  ]
                : []),
            ]}
          />
        </Section>
      )}

      {!editing && (
        <Section
          title="Access policy"
          description={queue.policy ? "Stored with the queue and returned by GetQueueAttributes; HomeCloud authorizes requests with IAM identity policies." : undefined}
        >
          {queue.policy ? (
            <pre className="bg-muted/50 max-h-80 overflow-auto rounded-md border p-3 font-mono text-xs">{prettyJson(queue.policy)}</pre>
          ) : (
            <p className="text-muted-foreground text-sm">No access policy.</p>
          )}
        </Section>
      )}

      <Section
        title="Tags"
        actions={
          <Button variant="outline" size="sm" onClick={() => setTagging(true)}>
            <TagsIcon /> Manage tags
          </Button>
        }
      >
        <TagList tags={queue.tags} />
      </Section>
      <TagsDialog queue={tagging ? queue : null} onClose={() => setTagging(false)} />
    </div>
  )
}

function EditForm({ queue, onDone }: { queue: Queue; onDone: () => void }) {
  const queues = useQueues(0)
  const [config, setConfig] = useState<QueueConfig>(() => configFromQueue(queue))
  const [submitted, setSubmitted] = useState(false)
  const [pending, setPending] = useState(false)
  const errors = submitted ? validateConfig(config) : {}
  const onChange = (p: Partial<QueueConfig>) => setConfig((c) => ({ ...c, ...p }))

  const save = async (e: React.FormEvent) => {
    e.preventDefault()
    setSubmitted(true)
    if (Object.keys(validateConfig(config)).length) return
    setPending(true)
    try {
      await api.patch(queuePath(queue.name), configToInput(config, queue.fifo, !!queue.redrive_policy))
      toast.success(`Saved ${queue.name}`)
      await revalidate(QUEUES_PATH)
      onDone()
    } catch (err) {
      toast.error(errorMessage(err))
    } finally {
      setPending(false)
    }
  }

  return (
    <form onSubmit={save} className="flex flex-col gap-4">
      <Section title="Edit configuration" description="Changes apply to messages already in the queue as well as new ones.">
        <ConfigurationFields config={config} onChange={onChange} errors={errors} fifo={queue.fifo} />
      </Section>
      <Section title="Encryption" description="Server-side encryption of messages at rest.">
        <EncryptionFields config={config} onChange={onChange} errors={errors} />
      </Section>
      <Section title="Dead-letter queue">
        <DeadLetterFields config={config} onChange={onChange} errors={errors} fifo={queue.fifo} queues={queues.data} self={queue.name} />
      </Section>
      <Section title="Redrive allow policy" description="Which source queues can use this queue as their dead-letter queue.">
        <RedriveAllowFields config={config} onChange={onChange} errors={errors} fifo={queue.fifo} queues={queues.data} self={queue.name} />
      </Section>
      <Section title="Access policy" description="Optional resource policy document.">
        <AccessPolicyFields config={config} onChange={onChange} errors={errors} arn={queue.arn} />
      </Section>
      <div className="flex flex-wrap justify-end gap-2">
        <Button type="button" variant="outline" onClick={onDone} disabled={pending}>
          Cancel
        </Button>
        <Button type="submit" disabled={pending}>
          {pending && <Loader2 className="animate-spin" />}
          Save
        </Button>
      </div>
    </form>
  )
}

function TagsDialog({ queue, onClose }: { queue: Queue | null; onClose: () => void }) {
  const [rows, setRows] = useState<TagRow[]>([])
  const [pending, setPending] = useState(false)
  const [err, setErr] = useState<string | null>(null)
  useEffect(() => {
    if (queue) {
      setRows(tagsToRows(queue.tags))
      setErr(null)
    }
  }, [queue])

  const submit = async (e: React.FormEvent) => {
    e.preventDefault()
    if (!queue) return
    const keys = rows.map((r) => r.key.trim()).filter(Boolean)
    if (new Set(keys).size !== keys.length) {
      setErr("Tag keys must be unique")
      return
    }
    setPending(true)
    try {
      await api.patch(queuePath(queue.name), { tags: rowsToTags(rows) ?? {} })
      toast.success(`Saved tags for ${queue.name}`)
      await revalidate(QUEUES_PATH)
      onClose()
    } catch (e) {
      toast.error(errorMessage(e))
    } finally {
      setPending(false)
    }
  }

  return (
    <Dialog open={!!queue} onOpenChange={(o) => !o && !pending && onClose()}>
      <DialogContent className="sm:max-w-xl">
        <form onSubmit={submit} className="flex flex-col gap-4">
          <DialogHeader>
            <DialogTitle>Manage tags</DialogTitle>
            <DialogDescription>Tags are key/value labels for organizing queues.</DialogDescription>
          </DialogHeader>
          <TagsEditor rows={rows} onChange={(r) => (setRows(r), setErr(null))} />
          {err && <p className="text-destructive text-xs">{err}</p>}
          <DialogFooter>
            <Button type="button" variant="outline" onClick={onClose} disabled={pending}>
              Cancel
            </Button>
            <Button type="submit" disabled={pending}>
              {pending && <Loader2 className="animate-spin" />}
              Save tags
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}
