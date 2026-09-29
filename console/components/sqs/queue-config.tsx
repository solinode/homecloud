"use client"

import Link from "next/link"

import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select"
import { Switch } from "@/components/ui/switch"
import { Field } from "@/components/console/form-field"
import type { Queue, QueueAttributesInput } from "@/lib/types"

import { queueHref } from "./common"

export type RetentionUnit = "s" | "min" | "h" | "days"

const UNIT_SECONDS: Record<RetentionUnit, number> = { s: 1, min: 60, h: 3600, days: 86400 }

/** Form state for the queue attributes shared by the create page and the settings editor. */
export interface QueueConfig {
  visibility: string
  retention: string
  retentionUnit: RetentionUnit
  delay: string
  wait: string
  maxSizeKB: string
  dedup: boolean
  dlqEnabled: boolean
  dlq: string
  maxReceives: string
}

export const DEFAULT_CONFIG: QueueConfig = {
  visibility: "30",
  retention: "4",
  retentionUnit: "days",
  delay: "0",
  wait: "0",
  maxSizeKB: "256",
  dedup: false,
  dlqEnabled: false,
  dlq: "",
  maxReceives: "3",
}

function retentionParts(seconds: number): [string, RetentionUnit] {
  for (const u of ["days", "h", "min"] as RetentionUnit[]) if (seconds % UNIT_SECONDS[u] === 0) return [String(seconds / UNIT_SECONDS[u]), u]
  return [String(seconds), "s"]
}

export function configFromQueue(q: Queue): QueueConfig {
  const [retention, retentionUnit] = retentionParts(q.message_retention_seconds)
  return {
    visibility: String(q.visibility_timeout),
    retention,
    retentionUnit,
    delay: String(q.delay_seconds),
    wait: String(q.receive_wait_time_seconds),
    maxSizeKB: String(Math.round(q.max_message_size / 1024)),
    dedup: q.content_based_deduplication,
    dlqEnabled: !!q.redrive_policy,
    dlq: q.redrive_policy?.dead_letter_queue ?? "",
    maxReceives: String(q.redrive_policy?.max_receive_count ?? 3),
  }
}

const int = (s: string) => (/^\s*\d+\s*$/.test(s) ? Number(s) : NaN)

export const retentionSeconds = (c: QueueConfig) => int(c.retention) * UNIT_SECONDS[c.retentionUnit]

/** validateConfig applies the API's ranges; keys match QueueConfig fields. */
export function validateConfig(c: QueueConfig): Partial<Record<keyof QueueConfig, string>> {
  const e: Partial<Record<keyof QueueConfig, string>> = {}
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
  return e
}

/** configToInput builds the attributes body; `had` is the queue's current redrive policy (for removal on edit). */
export function configToInput(c: QueueConfig, fifo: boolean, hadRedrive = false): QueueAttributesInput {
  const out: QueueAttributesInput = {
    visibility_timeout: int(c.visibility),
    message_retention_seconds: retentionSeconds(c),
    delay_seconds: int(c.delay),
    receive_wait_time_seconds: int(c.wait),
    max_message_size: int(c.maxSizeKB) * 1024,
  }
  if (fifo) out.content_based_deduplication = c.dedup
  if (c.dlqEnabled) out.redrive_policy = { dead_letter_queue: c.dlq, max_receive_count: int(c.maxReceives) }
  else if (hadRedrive) out.redrive_policy = { dead_letter_queue: "", max_receive_count: 0 }
  return out
}

type Errors = Partial<Record<keyof QueueConfig, string>>

function NumberInput({ id, value, onChange, suffix, invalid }: { id: string; value: string; onChange: (v: string) => void; suffix: string; invalid?: boolean }) {
  return (
    <div className="flex items-center gap-2">
      <Input id={id} inputMode="numeric" value={value} onChange={(e) => onChange(e.target.value)} className="h-9 w-32" aria-invalid={invalid} />
      <span className="text-muted-foreground text-sm">{suffix}</span>
    </div>
  )
}

/** ConfigurationFields: visibility timeout, retention, delay, long polling, max size, dedup. */
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
            </p>
          )}
        </div>
      )}
    </div>
  )
}
