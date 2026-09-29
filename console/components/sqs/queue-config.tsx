"use client"

import Link from "next/link"

import { Checkbox } from "@/components/ui/checkbox"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select"
import { Switch } from "@/components/ui/switch"
import { Field } from "@/components/console/form-field"
import { JsonEditor, jsonError } from "@/components/console/json-editor"
import { useApi } from "@/lib/hooks"
import type {
  DeduplicationScope,
  FifoThroughputLimit,
  KmsAlias,
  KmsKey,
  Queue,
  QueueAttributesInput,
  RedriveAllowPolicy,
  RedrivePermission,
} from "@/lib/types"
import { cn } from "@/lib/utils"

import { queueHref } from "./common"

export type RetentionUnit = "s" | "min" | "h" | "days"

const UNIT_SECONDS: Record<RetentionUnit, number> = { s: 1, min: 60, h: 3600, days: 86400 }

export type SseMode = "none" | "sqs" | "kms"

export const DEFAULT_KMS_KEY = "alias/aws/sqs"

/** Form state for the queue attributes shared by the create page and the settings editor. */
export interface QueueConfig {
  visibility: string
  retention: string
  retentionUnit: RetentionUnit
  delay: string
  wait: string
  maxSizeKB: string
  dedup: boolean
  dedupScope: DeduplicationScope
  throughputLimit: FifoThroughputLimit
  dlqEnabled: boolean
  dlq: string
  maxReceives: string
  /** none | SSE-SQS | SSE-KMS */
  sse: SseMode
  kmsKey: string
  /** data key reuse period, seconds */
  kmsReuse: string
  /** redrive allow policy (who may use this queue as a DLQ); "" = not set (all queues allowed) */
  allow: RedrivePermission | ""
  /** source queue ARNs for byQueue */
  allowSources: string[]
  policyEnabled: boolean
  policy: string
}

export const DEFAULT_CONFIG: QueueConfig = {
  visibility: "30",
  retention: "4",
  retentionUnit: "days",
  delay: "0",
  wait: "0",
  maxSizeKB: "256",
  dedup: false,
  dedupScope: "queue",
  throughputLimit: "perQueue",
  dlqEnabled: false,
  dlq: "",
  maxReceives: "3",
  sse: "sqs",
  kmsKey: DEFAULT_KMS_KEY,
  kmsReuse: "300",
  allow: "",
  allowSources: [],
  policyEnabled: false,
  policy: "",
}

/** parseRedriveAllow reads a queue's redrive allow policy JSON (null when unset or unreadable). */
export function parseRedriveAllow(s?: string): RedriveAllowPolicy | null {
  if (!s) return null
  try {
    const p = JSON.parse(s) as RedriveAllowPolicy
    return p && typeof p.redrivePermission === "string" ? p : null
  } catch {
    return null
  }
}

export function sseMode(q: Pick<Queue, "kms_master_key_id" | "sqs_managed_sse_enabled">): SseMode {
  if (q.kms_master_key_id) return "kms"
  return q.sqs_managed_sse_enabled ? "sqs" : "none"
}

export function prettyJson(s: string) {
  try {
    return JSON.stringify(JSON.parse(s), null, 2)
  } catch {
    return s
  }
}

function examplePolicy(arn: string) {
  const account = arn.split(":")[4] || "*"
  return JSON.stringify(
    {
      Version: "2012-10-17",
      Id: "__default_policy_ID",
      Statement: [{ Sid: "__owner_statement", Effect: "Allow", Principal: { AWS: account }, Action: ["SQS:*"], Resource: arn || "*" }],
    },
    null,
    2,
  )
}

function retentionParts(seconds: number): [string, RetentionUnit] {
  for (const u of ["days", "h", "min"] as RetentionUnit[]) if (seconds % UNIT_SECONDS[u] === 0) return [String(seconds / UNIT_SECONDS[u]), u]
  return [String(seconds), "s"]
}

export function configFromQueue(q: Queue): QueueConfig {
  const allow = parseRedriveAllow(q.redrive_allow_policy)
  const [retention, retentionUnit] = retentionParts(q.message_retention_seconds)
  return {
    visibility: String(q.visibility_timeout),
    retention,
    retentionUnit,
    delay: String(q.delay_seconds),
    wait: String(q.receive_wait_time_seconds),
    maxSizeKB: String(Math.round(q.max_message_size / 1024)),
    dedup: q.content_based_deduplication,
    dedupScope: q.deduplication_scope ?? "queue",
    throughputLimit: q.fifo_throughput_limit ?? "perQueue",
    dlqEnabled: !!q.redrive_policy,
    dlq: q.redrive_policy?.dead_letter_queue ?? "",
    maxReceives: String(q.redrive_policy?.max_receive_count ?? 3),
    sse: sseMode(q),
    kmsKey: q.kms_master_key_id || DEFAULT_KMS_KEY,
    kmsReuse: String(q.kms_data_key_reuse_period_seconds || 300),
    allow: allow?.redrivePermission === "allowAll" ? "" : (allow?.redrivePermission ?? ""),
    allowSources: allow?.sourceQueueArns ?? [],
    policyEnabled: !!q.policy,
    policy: q.policy ? prettyJson(q.policy) : "",
  }
}

const int = (s: string) => (/^\s*\d+\s*$/.test(s) ? Number(s) : NaN)

export const retentionSeconds = (c: QueueConfig) => int(c.retention) * UNIT_SECONDS[c.retentionUnit]

type Errors = Partial<Record<keyof QueueConfig, string>>

/** validateConfig applies the API's ranges; keys match QueueConfig fields. */
export function validateConfig(c: QueueConfig): Errors {
  const e: Errors = {}
  const range = (k: keyof QueueConfig, v: number, lo: number, hi: number, msg: string) => {
    if (!Number.isInteger(v) || v < lo || v > hi) e[k] = msg
  }
  range("visibility", int(c.visibility), 0, 43200, "Enter 0-43200 seconds (12 hours)")
  range("retention", retentionSeconds(c), 60, 1209600, "Enter between 1 minute and 14 days")
  range("delay", int(c.delay), 0, 900, "Enter 0-900 seconds (15 minutes)")
  range("wait", int(c.wait), 0, 20, "Enter 0-20 seconds")
  range("maxSizeKB", int(c.maxSizeKB), 1, 256, "Enter 1-256 KB")
  if (c.dlqEnabled) {
    if (!c.dlq) e.dlq = "Choose a dead-letter queue"
    range("maxReceives", int(c.maxReceives), 1, 1000, "Enter 1-1000")
  }
  if (c.sse === "kms") {
    if (!c.kmsKey.trim()) e.kmsKey = "Enter a KMS key ID, ARN or alias"
    range("kmsReuse", int(c.kmsReuse), 60, 86400, "Enter 60-86400 seconds (1 minute to 24 hours)")
  }
  if (c.allow === "byQueue" && (c.allowSources.length < 1 || c.allowSources.length > 10)) e.allowSources = "Choose 1-10 source queues"
  if (c.policyEnabled) {
    const je = jsonError(c.policy)
    if (je) e.policy = je
    else if (!c.policy.trim().startsWith("{")) e.policy = "The policy must be a JSON object"
  }
  return e
}

/** configToInput builds the attributes body; `hadRedrive` is whether the queue has a redrive policy now (for removal on edit). */
export function configToInput(c: QueueConfig, fifo: boolean, hadRedrive = false): QueueAttributesInput {
  const out: QueueAttributesInput = {
    visibility_timeout: int(c.visibility),
    message_retention_seconds: retentionSeconds(c),
    delay_seconds: int(c.delay),
    receive_wait_time_seconds: int(c.wait),
    max_message_size: int(c.maxSizeKB) * 1024,
  }
  if (fifo) {
    out.content_based_deduplication = c.dedup
    out.deduplication_scope = c.dedupScope
    out.fifo_throughput_limit = c.throughputLimit
  }
  if (c.sse === "kms") {
    out.kms_master_key_id = c.kmsKey.trim()
    out.kms_data_key_reuse_period_seconds = int(c.kmsReuse)
    out.sqs_managed_sse_enabled = false
  } else {
    out.kms_master_key_id = ""
    out.sqs_managed_sse_enabled = c.sse === "sqs"
  }
  // "" removes an attribute.
  out.redrive_allow_policy = c.allow
    ? JSON.stringify(c.allow === "byQueue" ? { redrivePermission: c.allow, sourceQueueArns: c.allowSources } : { redrivePermission: c.allow })
    : ""
  out.policy = c.policyEnabled ? JSON.stringify(JSON.parse(c.policy)) : ""
  if (c.dlqEnabled) out.redrive_policy = { dead_letter_queue: c.dlq, max_receive_count: int(c.maxReceives) }
  else if (hadRedrive) out.redrive_policy = { dead_letter_queue: "", max_receive_count: 0 }
  return out
}

function NumberInput({ id, value, onChange, suffix, invalid }: { id: string; value: string; onChange: (v: string) => void; suffix: string; invalid?: boolean }) {
  return (
    <div className="flex items-center gap-2">
      <Input id={id} inputMode="numeric" value={value} onChange={(e) => onChange(e.target.value)} className="h-9 w-32" aria-invalid={invalid} />
      <span className="text-muted-foreground text-sm">{suffix}</span>
    </div>
  )
}

/** ChoiceCards is a compact card-style radio group. */
function ChoiceCards<T extends string>({
  label,
  value,
  onChange,
  options,
}: {
  label: string
  value: T
  onChange: (v: T) => void
  options: { value: T; title: string; text: string }[]
}) {
  return (
    <div role="radiogroup" aria-label={label} className="grid grid-cols-1 gap-2 sm:grid-cols-3">
      {options.map((o) => (
        <button
          key={o.value || "_"}
          type="button"
          role="radio"
          aria-checked={value === o.value}
          onClick={() => onChange(o.value)}
          className={cn(
            "flex items-start gap-3 rounded-md border p-3 text-left transition-colors",
            value === o.value ? "border-primary bg-primary/5 ring-primary ring-1" : "hover:bg-muted/40",
          )}
        >
          <span className={cn("mt-0.5 flex size-4 shrink-0 items-center justify-center rounded-full border", value === o.value ? "border-primary" : "border-input")}>
            {value === o.value && <span className="bg-primary size-2 rounded-full" />}
          </span>
          <span className="flex flex-col gap-0.5">
            <span className="text-sm font-medium">{o.title}</span>
            <span className="text-muted-foreground text-xs">{o.text}</span>
          </span>
        </button>
      ))}
    </div>
  )
}

/** ConfigurationFields: visibility timeout, retention, delay, long polling, max size, FIFO options. */
export function ConfigurationFields({
  config: c,
  onChange,
  errors,
  fifo,
}: {
  config: QueueConfig
  onChange: (patch: Partial<QueueConfig>) => void
  errors: Errors
  fifo: boolean
}) {
  return (
    <div className="grid grid-cols-1 gap-5 md:grid-cols-2">
      <Field
        label="Visibility timeout"
        htmlFor="cfg-vis"
        error={errors.visibility}
        help="How long a received message stays hidden from other consumers. 0 seconds to 12 hours."
      >
        <NumberInput id="cfg-vis" value={c.visibility} onChange={(v) => onChange({ visibility: v })} suffix="seconds" invalid={!!errors.visibility} />
      </Field>
      <Field label="Message retention period" htmlFor="cfg-ret" error={errors.retention} help="How long messages are kept. 1 minute to 14 days.">
        <div className="flex items-center gap-2">
          <Input
            id="cfg-ret"
            inputMode="numeric"
            value={c.retention}
            onChange={(e) => onChange({ retention: e.target.value })}
            className="h-9 w-24"
            aria-invalid={!!errors.retention}
          />
          <Select value={c.retentionUnit} onValueChange={(v) => onChange({ retentionUnit: v as RetentionUnit })}>
            <SelectTrigger className="w-28" aria-label="Retention unit">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="s">Seconds</SelectItem>
              <SelectItem value="min">Minutes</SelectItem>
              <SelectItem value="h">Hours</SelectItem>
              <SelectItem value="days">Days</SelectItem>
            </SelectContent>
          </Select>
        </div>
      </Field>
      <Field
        label="Delivery delay"
        htmlFor="cfg-delay"
        error={errors.delay}
        help={fifo ? "Applies to the whole queue; FIFO queues do not support per-message delays. 0-900 seconds." : "Delay before new messages become visible. 0-900 seconds."}
      >
        <NumberInput id="cfg-delay" value={c.delay} onChange={(v) => onChange({ delay: v })} suffix="seconds" invalid={!!errors.delay} />
      </Field>
      <Field
        label="Receive message wait time"
        htmlFor="cfg-wait"
        error={errors.wait}
        help="Default long-polling wait for receive calls. 0 = short polling, up to 20 seconds."
      >
        <NumberInput id="cfg-wait" value={c.wait} onChange={(v) => onChange({ wait: v })} suffix="seconds" invalid={!!errors.wait} />
      </Field>
      <Field label="Maximum message size" htmlFor="cfg-size" error={errors.maxSizeKB} help="1-256 KB.">
        <NumberInput id="cfg-size" value={c.maxSizeKB} onChange={(v) => onChange({ maxSizeKB: v })} suffix="KB" invalid={!!errors.maxSizeKB} />
      </Field>
      {fifo && (
        <>
          <div className="flex items-start justify-between gap-4 rounded-md border p-3 md:col-span-2">
            <div>
              <Label htmlFor="cfg-dedup" className="font-medium">
                Content-based deduplication
              </Label>
              <p className="text-muted-foreground mt-0.5 text-xs">
                Use a hash of the message body as the deduplication ID, so senders don&apos;t have to provide one. Duplicates within 5 minutes are dropped.
              </p>
            </div>
            <Switch id="cfg-dedup" checked={c.dedup} onCheckedChange={(v) => onChange({ dedup: v })} />
          </div>
          <Field label="Deduplication scope" htmlFor="cfg-dedup-scope" help="Whether deduplication IDs are unique across the queue or within each message group.">
            <Select value={c.dedupScope} onValueChange={(v) => onChange({ dedupScope: v as DeduplicationScope })}>
              <SelectTrigger id="cfg-dedup-scope" className="w-full max-w-xs">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="queue">Queue</SelectItem>
                <SelectItem value="messageGroup">Message group</SelectItem>
              </SelectContent>
            </Select>
          </Field>
          <Field
            label="FIFO throughput limit"
            htmlFor="cfg-throughput"
            help={
              c.dedupScope === "messageGroup" && c.throughputLimit === "perMessageGroupId"
                ? "High throughput mode: the quota applies to each message group."
                : "Whether the throughput quota applies to the whole queue or to each message group."
            }
          >
            <Select value={c.throughputLimit} onValueChange={(v) => onChange({ throughputLimit: v as FifoThroughputLimit })}>
              <SelectTrigger id="cfg-throughput" className="w-full max-w-xs">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="perQueue">Per queue</SelectItem>
                <SelectItem value="perMessageGroupId">Per message group ID</SelectItem>
              </SelectContent>
            </Select>
          </Field>
        </>
      )}
    </div>
  )
}

const SSE_OPTIONS: { value: SseMode; title: string; text: string }[] = [
  { value: "sqs", title: "SSE-SQS", text: "Encrypt with SQS-managed keys." },
  { value: "kms", title: "SSE-KMS", text: "Encrypt with a KMS key you choose." },
  { value: "none", title: "Disabled", text: "No server-side encryption." },
]

/** EncryptionFields: server-side encryption mode, KMS key and data key reuse period. */
export function EncryptionFields({ config: c, onChange, errors }: { config: QueueConfig; onChange: (patch: Partial<QueueConfig>) => void; errors: Errors }) {
  const keys = useApi<KmsKey[]>(c.sse === "kms" ? "/api/v1/kms/keys" : null, { revalidateOnFocus: false })
  const aliases = useApi<KmsAlias[]>(c.sse === "kms" ? "/api/v1/kms/aliases" : null, { revalidateOnFocus: false })
  const options = Array.from(
    new Set([DEFAULT_KMS_KEY, ...(aliases.data ?? []).map((a) => a.name), ...(keys.data ?? []).filter((k) => k.state === "Enabled").map((k) => k.id)]),
  )
  return (
    <div className="flex flex-col gap-4">
      <ChoiceCards label="Server-side encryption" value={c.sse} onChange={(v) => onChange({ sse: v })} options={SSE_OPTIONS} />
      {c.sse === "kms" && (
        <div className="grid grid-cols-1 gap-5 md:grid-cols-2">
          <Field
            label="KMS key"
            htmlFor="cfg-kms-key"
            error={errors.kmsKey}
            help={
              <>
                Key ID, key ARN or alias.{" "}
                <Link href="/kms/" className="text-primary hover:underline">
                  Manage keys
                </Link>
              </>
            }
          >
            <Input
              id="cfg-kms-key"
              list="cfg-kms-keys"
              value={c.kmsKey}
              onChange={(e) => onChange({ kmsKey: e.target.value })}
              className="h-9 font-mono text-[13px]"
              autoComplete="off"
              spellCheck={false}
              aria-invalid={!!errors.kmsKey}
            />
            <datalist id="cfg-kms-keys">
              {options.map((o) => (
                <option key={o} value={o} />
              ))}
            </datalist>
          </Field>
          <Field label="Data key reuse period" htmlFor="cfg-kms-reuse" error={errors.kmsReuse} help="60 seconds to 24 hours.">
            <NumberInput id="cfg-kms-reuse" value={c.kmsReuse} onChange={(v) => onChange({ kmsReuse: v })} suffix="seconds" invalid={!!errors.kmsReuse} />
          </Field>
        </div>
      )}
    </div>
  )
}

/** DeadLetterFields: enable switch, DLQ picker (same type, not itself), max receives. */
export function DeadLetterFields({
  config: c,
  onChange,
  errors,
  fifo,
  queues,
  self,
}: {
  config: QueueConfig
  onChange: (patch: Partial<QueueConfig>) => void
  errors: Errors
  fifo: boolean
  queues: Queue[] | undefined
  self?: string
}) {
  const candidates = (queues ?? []).filter((q) => q.fifo === fifo && q.name !== self)
  const chosen = candidates.find((q) => q.name === c.dlq)
  const allow = parseRedriveAllow(chosen?.redrive_allow_policy)
  const selfArn = (queues ?? []).find((q) => q.name === self)?.arn
  const denied = allow && (allow.redrivePermission === "denyAll" || (allow.redrivePermission === "byQueue" && !(selfArn && allow.sourceQueueArns?.includes(selfArn))))
  return (
    <div className="flex flex-col gap-4">
      <div className="flex items-start justify-between gap-4">
        <div>
          <Label htmlFor="cfg-dlq-on" className="font-medium">
            Send undeliverable messages to a dead-letter queue
          </Label>
          <p className="text-muted-foreground mt-0.5 text-xs">
            A message received more than the maximum number of times without being deleted is moved to the dead-letter queue.
          </p>
        </div>
        <Switch id="cfg-dlq-on" checked={c.dlqEnabled} onCheckedChange={(v) => onChange({ dlqEnabled: v })} />
      </div>
      {c.dlqEnabled && (
        <div className="grid grid-cols-1 gap-5 md:grid-cols-2">
          <Field
            label="Dead-letter queue"
            htmlFor="cfg-dlq"
            error={errors.dlq}
            help={
              candidates.length ? (
                `Only ${fifo ? "FIFO" : "standard"} queues can be used.`
              ) : (
                <>
                  No other {fifo ? "FIFO" : "standard"} queue exists.{" "}
                  <Link href="/sqs/create/" className="text-primary hover:underline">
                    Create one first
                  </Link>
                  .
                </>
              )
            }
          >
            <Select value={c.dlq} onValueChange={(v) => onChange({ dlq: v })}>
              <SelectTrigger id="cfg-dlq" className="w-full" aria-invalid={!!errors.dlq}>
                <SelectValue placeholder={queues ? "Choose a queue" : "Loading queues..."} />
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
          <Field label="Maximum receives" htmlFor="cfg-maxrecv" error={errors.maxReceives} help="1-1000 receives before a message is moved.">
            <Input
              id="cfg-maxrecv"
              inputMode="numeric"
              value={c.maxReceives}
              onChange={(e) => onChange({ maxReceives: e.target.value })}
              className="h-9 w-32"
              aria-invalid={!!errors.maxReceives}
            />
          </Field>
          {c.dlq && (
            <p className="text-muted-foreground text-xs md:col-span-2">
              Messages will move to{" "}
              <Link href={queueHref(c.dlq)} className="text-primary hover:underline">
                {c.dlq}
              </Link>{" "}
              after {c.maxReceives || "?"} receives.
              {denied && (
                <span className="mt-1 block text-amber-700 dark:text-amber-300">
                  {c.dlq}&apos;s redrive allow policy ({allow.redrivePermission}) may not permit this queue as a source.
                </span>
              )}
            </p>
          )}
        </div>
      )}
    </div>
  )
}

const ALLOW_OPTIONS: { value: RedrivePermission | ""; title: string; text: string }[] = [
  { value: "", title: "Allow all", text: "Any queue of the same type (default)." },
  { value: "byQueue", title: "By queue", text: "Only the source queues you choose." },
  { value: "denyAll", title: "Deny all", text: "No queue can use this one as its DLQ." },
]

/** RedriveAllowFields: which source queues may use this queue as their dead-letter queue. */
export function RedriveAllowFields({
  config: c,
  onChange,
  errors,
  fifo,
  queues,
  self,
}: {
  config: QueueConfig
  onChange: (patch: Partial<QueueConfig>) => void
  errors: Errors
  fifo: boolean
  queues: Queue[] | undefined
  self?: string
}) {
  const candidates = (queues ?? []).filter((q) => q.fifo === fifo && q.name !== self)
  const known = new Set(candidates.map((q) => q.arn))
  const rows = [...candidates.map((q) => ({ arn: q.arn, name: q.name })), ...c.allowSources.filter((a) => !known.has(a)).map((a) => ({ arn: a, name: a }))]
  const toggle = (arn: string, on: boolean) => onChange({ allowSources: on ? [...c.allowSources.filter((a) => a !== arn), arn] : c.allowSources.filter((a) => a !== arn) })
  return (
    <div className="flex flex-col gap-4">
      <ChoiceCards label="Redrive allow policy" value={c.allow} onChange={(v) => onChange({ allow: v })} options={ALLOW_OPTIONS} />
      {c.allow === "byQueue" && (
        <Field label="Source queues" error={errors.allowSources} help={`Choose up to 10 ${fifo ? "FIFO" : "standard"} queues (${c.allowSources.length} selected).`}>
          {rows.length ? (
            <div className="flex max-h-56 flex-col divide-y overflow-y-auto rounded-md border">
              {rows.map((q) => (
                <label key={q.arn} className="hover:bg-muted/40 flex cursor-pointer items-center gap-3 px-3 py-2 text-sm">
                  <Checkbox checked={c.allowSources.includes(q.arn)} onCheckedChange={(v) => toggle(q.arn, v === true)} aria-label={q.name} />
                  <span className="min-w-0 truncate font-mono text-[13px]" title={q.arn}>
                    {q.name}
                  </span>
                </label>
              ))}
            </div>
          ) : (
            <p className="text-muted-foreground rounded-md border p-3 text-sm">{queues ? `No other ${fifo ? "FIFO" : "standard"} queues exist yet.` : "Loading queues..."}</p>
          )}
        </Field>
      )}
    </div>
  )
}

/** AccessPolicyFields: optional JSON access policy (stored with the queue; HomeCloud does not enforce it). */
export function AccessPolicyFields({
  config: c,
  onChange,
  errors,
  arn,
}: {
  config: QueueConfig
  onChange: (patch: Partial<QueueConfig>) => void
  errors: Errors
  arn: string
}) {
  return (
    <div className="flex flex-col gap-3">
      <div className="flex items-start justify-between gap-4">
        <div>
          <Label htmlFor="cfg-policy-on" className="font-medium">
            Attach an access policy
          </Label>
          <p className="text-muted-foreground mt-0.5 text-xs">
            A resource policy document, as in Amazon SQS. HomeCloud stores and returns it; access is decided by IAM identity policies.
          </p>
        </div>
        <Switch
          id="cfg-policy-on"
          checked={c.policyEnabled}
          onCheckedChange={(v) => onChange({ policyEnabled: v, policy: v && !c.policy.trim() ? examplePolicy(arn) : c.policy })}
        />
      </div>
      {c.policyEnabled && (
        <>
          <JsonEditor value={c.policy} onChange={(v) => onChange({ policy: v })} rows={10} />
          {errors.policy && <p className="text-destructive text-xs">{errors.policy}</p>}
        </>
      )}
    </div>
  )
}
