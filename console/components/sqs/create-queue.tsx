"use client"

import { useState } from "react"
import Link from "next/link"
import { useRouter } from "next/navigation"
import { CheckCircle2, Circle, Loader2, Plus } from "lucide-react"
import { toast } from "sonner"

import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Field } from "@/components/console/form-field"
import { PageHeader } from "@/components/console/page-header"
import { Section } from "@/components/console/section"
import { TagsEditor, rowsToTags, type TagRow } from "@/components/console/tags-editor"
import { api, errorMessage } from "@/lib/api"
import { revalidate } from "@/lib/hooks"
import type { CreateQueueInput, Queue } from "@/lib/types"
import { cn } from "@/lib/utils"

import { QUEUES_PATH, humanSeconds, queueHref, useQueues } from "./common"
import {
  AccessPolicyFields,
  ConfigurationFields,
  DEFAULT_CONFIG,
  DeadLetterFields,
  EncryptionFields,
  RedriveAllowFields,
  configToInput,
  retentionSeconds,
  validateConfig,
  type QueueConfig,
} from "./queue-config"

const BASE_RE = /^[a-zA-Z0-9_-]{1,80}$/

function nameChecks(name: string, fifo: boolean) {
  const base = name.endsWith(".fifo") ? name.slice(0, -5) : name
  return [
    { label: "1-80 characters", ok: base.length >= 1 && base.length <= 80 },
    { label: "Letters, digits, hyphens (-) and underscores (_) only", ok: base.length > 0 && BASE_RE.test(base) },
    { label: fifo ? "Ends in .fifo" : "Does not end in .fifo", ok: fifo === name.endsWith(".fifo") },
  ]
}

export function CreateQueue() {
  const router = useRouter()
  const queues = useQueues(0)
  const [fifo, setFifo] = useState(false)
  const [name, setName] = useState("")
  const [config, setConfig] = useState<QueueConfig>(DEFAULT_CONFIG)
  const [tagRows, setTagRows] = useState<TagRow[]>([])
  const [submitted, setSubmitted] = useState(false)
  const [pending, setPending] = useState(false)

  const setType = (f: boolean) => {
    if (f === fifo) return
    setFifo(f)
    // Keep the name consistent with the type, and drop a DLQ of the other type.
    setName((n) => (f ? (n && !n.endsWith(".fifo") ? `${n}.fifo` : n) : n.replace(/\.fifo$/, "")))
    setConfig((c) => ({ ...c, dlq: "" }))
  }

  const checks = nameChecks(name, fifo)
  const exists = (queues.data ?? []).some((q) => q.name === name)
  const errors: Record<string, string | undefined> = { ...validateConfig(config) }
  if (!checks.every((c) => c.ok)) errors.name = "Enter a valid queue name"
  else if (exists) errors.name = `A queue named ${name} already exists`
  const tagKeys = tagRows.map((r) => r.key.trim()).filter(Boolean)
  if (new Set(tagKeys).size !== tagKeys.length) errors.tags = "Tag keys must be unique"
  const err = (k: string) => (submitted ? errors[k] : undefined)
  const valid = Object.values(errors).every((v) => !v)

  const create = async () => {
    setSubmitted(true)
    if (!valid) {
      toast.error("Fix the highlighted fields before creating the queue")
      return
    }
    const body: CreateQueueInput = { name, fifo, ...configToInput(config, fifo), tags: rowsToTags(tagRows) }
    setPending(true)
    try {
      const q = await api.post<Queue>(QUEUES_PATH, body)
      toast.success(`Created queue ${q.name}`)
      await revalidate(QUEUES_PATH)
      router.push(queueHref(q.name))
    } catch (e) {
      toast.error(errorMessage(e))
      setPending(false)
    }
  }

  const cfgErrors = submitted ? validateConfig(config) : {}
  const onChange = (p: Partial<QueueConfig>) => setConfig((c) => ({ ...c, ...p }))
  // The new queue's ARN, from the prefix of an existing one (for the example access policy).
  const sample = queues.data?.[0]?.arn
  const arn = sample && name ? `${sample.slice(0, sample.lastIndexOf(":") + 1)}${name}` : ""
  const ret = retentionSeconds(config)

  return (
    <div className="flex flex-col gap-4">
      <PageHeader
        title="Create queue"
        breadcrumbs={[{ label: "SQS", href: "/sqs/" }, { label: "Queues", href: "/sqs/" }, { label: "Create queue" }]}
        description="Queues store messages until a consumer receives and deletes them."
      />
      <form
        onSubmit={(e) => {
          e.preventDefault()
          create()
        }}
        className="grid grid-cols-1 items-start gap-4 xl:grid-cols-[minmax(0,1fr)_320px]"
      >
        <div className="flex min-w-0 flex-col gap-4">
          <Section title="Details">
            <div className="flex flex-col gap-5">
              <div className="flex flex-col gap-2">
                <span className="text-sm font-medium">Type</span>
                <div role="radiogroup" aria-label="Queue type" className="grid grid-cols-1 gap-3 md:grid-cols-2">
                  <TypeCard
                    selected={!fifo}
                    onSelect={() => setType(false)}
                    title="Standard"
                    points={["At-least-once delivery, message duplicates are possible", "Best-effort ordering", "Per-message delivery delays"]}
                  />
                  <TypeCard
                    selected={fifo}
                    onSelect={() => setType(true)}
                    title="FIFO"
                    points={["Exactly-once processing within a 5 minute deduplication window", "First-in-first-out delivery per message group"]}
                  />
                </div>
                <p className="text-muted-foreground text-xs">You can&apos;t change the queue type after you create a queue.</p>
              </div>
              <Field label="Name" htmlFor="queue-name" error={err("name") && exists ? err("name") : undefined}>
                <Input
                  id="queue-name"
                  autoFocus
                  autoComplete="off"
                  spellCheck={false}
                  value={name}
                  onChange={(e) => setName(e.target.value.trim())}
                  onBlur={() => fifo && name && !name.endsWith(".fifo") && setName(`${name}.fifo`)}
                  placeholder={fifo ? "my-queue.fifo" : "my-queue"}
                  aria-invalid={!!err("name")}
                  className="max-w-md"
                />
                <ul className="mt-1 flex flex-col gap-1 text-xs">
                  {checks.map((c) => (
                    <li
                      key={c.label}
                      className={cn(
                        "flex items-center gap-1.5",
                        c.ok ? "text-emerald-600 dark:text-emerald-400" : submitted || name ? "text-destructive" : "text-muted-foreground",
                      )}
                    >
                      {c.ok ? <CheckCircle2 className="size-3.5" /> : <Circle className="size-3.5" />}
                      {c.label}
                    </li>
                  ))}
                </ul>
              </Field>
            </div>
          </Section>

          <Section title="Configuration" description="Set the visibility timeout, message retention period and other attributes.">
            <ConfigurationFields config={config} onChange={onChange} errors={cfgErrors} fifo={fifo} />
          </Section>

          <Section title="Encryption" description="Server-side encryption of messages at rest.">
            <EncryptionFields config={config} onChange={onChange} errors={cfgErrors} />
          </Section>

          <Section title="Dead-letter queue" description="Move messages that can't be processed to another queue for inspection.">
            <DeadLetterFields config={config} onChange={onChange} errors={cfgErrors} fifo={fifo} queues={queues.data} self={name} />
          </Section>

          <Section title="Redrive allow policy" description="Which source queues can use this queue as their dead-letter queue.">
            <RedriveAllowFields config={config} onChange={onChange} errors={cfgErrors} fifo={fifo} queues={queues.data} self={name} />
          </Section>

          <Section title="Access policy" description="Optional resource policy document.">
            <AccessPolicyFields config={config} onChange={onChange} errors={cfgErrors} arn={arn} />
          </Section>

          <Section title="Tags" description="Key/value labels for organizing and finding queues.">
            <div className="flex flex-col gap-2">
              <TagsEditor rows={tagRows} onChange={setTagRows} />
              {err("tags") && <p className="text-destructive text-xs">{err("tags")}</p>}
            </div>
          </Section>
        </div>

        <aside className="xl:sticky xl:top-4">
          <Section title="Summary">
            <div className="flex flex-col gap-4">
              <dl className="flex flex-col gap-3 text-sm">
                <SummaryItem label="Name">
                  <span className="font-mono text-[13px] break-all">{name || "-"}</span>
                </SummaryItem>
                <SummaryItem label="Type">{fifo ? "FIFO" : "Standard"}</SummaryItem>
                <SummaryItem label="Visibility timeout">{cfgValue(config.visibility, humanSeconds)}</SummaryItem>
                <SummaryItem label="Retention period">{Number.isInteger(ret) ? humanSeconds(ret) : "-"}</SummaryItem>
                <SummaryItem label="Delivery delay">{cfgValue(config.delay, humanSeconds)}</SummaryItem>
                <SummaryItem label="Receive wait time">{cfgValue(config.wait, (n) => (n ? humanSeconds(n) : "Short polling"))}</SummaryItem>
                <SummaryItem label="Maximum message size">{config.maxSizeKB ? `${config.maxSizeKB} KB` : "-"}</SummaryItem>
                {fifo && <SummaryItem label="Content-based deduplication">{config.dedup ? "Enabled" : "Disabled"}</SummaryItem>}
                {fifo && (
                  <SummaryItem label="Deduplication / throughput">
                    {config.dedupScope === "queue" ? "Queue" : "Message group"} / {config.throughputLimit === "perQueue" ? "per queue" : "per message group"}
                  </SummaryItem>
                )}
                <SummaryItem label="Encryption">
                  {config.sse === "sqs" ? "SSE-SQS" : config.sse === "kms" ? <span className="break-all">SSE-KMS ({config.kmsKey || "?"})</span> : "Disabled"}
                </SummaryItem>
                <SummaryItem label="Dead-letter queue">
                  {config.dlqEnabled ? (config.dlq ? `${config.dlq} after ${config.maxReceives || "?"} receives` : "Not chosen") : "Disabled"}
                </SummaryItem>
                <SummaryItem label="Redrive allow policy">
                  {config.allow === "byQueue" ? `By queue (${config.allowSources.length})` : config.allow === "denyAll" ? "Deny all" : "Allow all"}
                </SummaryItem>
              </dl>
              {submitted && !valid && <p className="text-destructive text-xs">Some settings need attention. Check the highlighted fields.</p>}
              <div className="flex flex-col gap-2 border-t pt-4">
                <Button type="submit" disabled={pending}>
                  {pending ? <Loader2 className="animate-spin" /> : <Plus />}
                  Create queue
                </Button>
                <Button type="button" variant="outline" asChild>
                  <Link href="/sqs/">Cancel</Link>
                </Button>
              </div>
            </div>
          </Section>
        </aside>
      </form>
    </div>
  )
}

function cfgValue(s: string, f: (n: number) => string) {
  return /^\d+$/.test(s.trim()) ? f(Number(s)) : "-"
}

function TypeCard({ selected, onSelect, title, points }: { selected: boolean; onSelect: () => void; title: string; points: string[] }) {
  return (
    <button
      type="button"
      role="radio"
      aria-checked={selected}
      onClick={onSelect}
      className={cn(
        "flex items-start gap-3 rounded-md border p-3 text-left transition-colors",
        selected ? "border-primary bg-primary/5 ring-primary ring-1" : "hover:bg-muted/40",
      )}
    >
      <span className={cn("mt-0.5 flex size-4 shrink-0 items-center justify-center rounded-full border", selected ? "border-primary" : "border-input")}>
        {selected && <span className="bg-primary size-2 rounded-full" />}
      </span>
      <span className="flex flex-col gap-1">
        <span className="text-sm font-medium">{title}</span>
        <ul className="text-muted-foreground list-disc pl-4 text-xs">
          {points.map((p) => (
            <li key={p}>{p}</li>
          ))}
        </ul>
      </span>
    </button>
  )
}

function SummaryItem({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div>
      <dt className="text-muted-foreground text-xs font-medium">{label}</dt>
      <dd className="min-w-0 break-words">{children}</dd>
    </div>
  )
}
