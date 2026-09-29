"use client"

import { useState, type ReactNode } from "react"
import { toast } from "sonner"

import { ConfirmDialog } from "@/components/console/confirm-dialog"
import { api, errorMessage, seg } from "@/lib/api"
import { pluralize } from "@/lib/format"
import { revalidate, useQueryParam } from "@/lib/hooks"
import type { EventRule } from "@/lib/types"

export const EVENTS_PATH = "/api/v1/events"
export const RULES_PATH = "/api/v1/events/rules"

const busParam = (bus?: string) => (bus && bus !== "default" ? `&bus=${encodeURIComponent(bus)}` : "")
export const ruleHref = (name: string, bus?: string) => `/events/rule/?name=${encodeURIComponent(name)}${busParam(bus)}`
export const editRuleHref = (name: string, bus?: string) => `/events/create/?name=${encodeURIComponent(name)}${busParam(bus)}`
/** busQuery is the API query selecting a non-default event bus. */
export const busQuery = (bus?: string) => (bus && bus !== "default" ? { event_bus: bus } : undefined)

export const RULE_NAME_RE = /^[\w.-]{1,64}$/

export function ruleNameError(name: string): string | null {
  if (!name) return "Enter a rule name"
  if (name.length > 64) return "Rule names are at most 64 characters"
  if (!RULE_NAME_RE.test(name)) return "Use only letters, digits, dots (.), hyphens (-) and underscores (_)"
  return null
}

export const isSchedule = (r: Pick<EventRule, "schedule_expression">) => !!r.schedule_expression

// ---- targets ----

export type TargetKind = "lambda" | "sqs" | "sns" | "sfn"

export const TARGET_KINDS: { kind: TargetKind; label: string; noun: string }[] = [
  { kind: "lambda", label: "Lambda function", noun: "function" },
  { kind: "sqs", label: "SQS queue", noun: "queue" },
  { kind: "sns", label: "SNS topic", noun: "topic" },
  { kind: "sfn", label: "Step Functions state machine", noun: "state machine" },
]

export function targetKind(arn: string): TargetKind | null {
  if (arn.includes(":function:")) return "lambda"
  if (arn.includes(":sqs:")) return "sqs"
  if (arn.includes(":sns:")) return "sns"
  if (arn.includes(":stateMachine:")) return "sfn"
  return null
}

export const targetKindLabel = (arn: string) => TARGET_KINDS.find((t) => t.kind === targetKind(arn))?.label ?? "Unknown"

/** targetName is the resource name at the end of a target ARN. */
export function targetName(arn: string): string {
  const i = arn.lastIndexOf(":")
  return i >= 0 ? arn.slice(i + 1) : arn
}

export function targetHref(arn: string): string | null {
  const n = encodeURIComponent(targetName(arn))
  switch (targetKind(arn)) {
    case "lambda":
      return `/lambda/function/?name=${n}`
    case "sqs":
      return `/sqs/queue/?name=${n}`
    case "sns":
      return `/sns/topic/?name=${n}`
    case "sfn":
      return `/sfn/state-machine/?name=${n}`
  }
  return null
}

// ---- event patterns ----

/** patternSummary flattens a pattern to "source: [shop] · detail.total: [...]". */
export function patternSummary(p: Record<string, unknown> | null | undefined, max = 90): string {
  if (!p) return "-"
  const parts: string[] = []
  const walk = (o: Record<string, unknown>, prefix: string) => {
    for (const [k, v] of Object.entries(o)) {
      const path = prefix ? `${prefix}.${k}` : k
      if (v && typeof v === "object" && !Array.isArray(v)) walk(v as Record<string, unknown>, path)
      else parts.push(`${path}: ${JSON.stringify(v)}`)
    }
  }
  walk(p, "")
  const s = parts.join(" · ") || "{}"
  return s.length > max ? `${s.slice(0, max - 1)}…` : s
}

// ---- schedules (a port of cli/internal/svc/events/cron.go) ----

const MONTH_NAMES: Record<string, number> = { JAN: 1, FEB: 2, MAR: 3, APR: 4, MAY: 5, JUN: 6, JUL: 7, AUG: 8, SEP: 9, OCT: 10, NOV: 11, DEC: 12 }
const DAY_NAMES: Record<string, number> = { SUN: 1, MON: 2, TUE: 3, WED: 4, THU: 5, FRI: 6, SAT: 7 }

export const CRON_FIELDS = [
  { label: "Minutes", hint: "0-59", placeholder: "0" },
  { label: "Hours", hint: "0-23", placeholder: "12" },
  { label: "Day of month", hint: "1-31 or ?", placeholder: "*" },
  { label: "Month", hint: "1-12 or JAN-DEC", placeholder: "*" },
  { label: "Day of week", hint: "1-7 or SUN-SAT, or ?", placeholder: "?" },
  { label: "Year", hint: "1970-2199", placeholder: "*" },
] as const

const LIMITS: [number, number][] = [
  [0, 59],
  [0, 23],
  [1, 31],
  [1, 12],
  [1, 7],
  [1970, 2199],
]

function atoi(s: string): number {
  if (!/^[+-]?\d+$/.test(s)) throw new Error(`bad value "${s}"`)
  return parseInt(s, 10)
}

function parseField(f: string, lo: number, hi: number, names?: Record<string, number>): Set<number> {
  const out = new Set<number>()
  if (f === "*" || f === "?") {
    for (let i = lo; i <= hi; i++) out.add(i)
    return out
  }
  const val = (s: string) => names?.[s.toUpperCase()] ?? atoi(s)
  for (let part of f.split(",")) {
    let step = 1
    const slash = part.indexOf("/")
    if (slash >= 0) {
      const s = part.slice(slash + 1)
      const n = /^\d+$/.test(s) ? parseInt(s, 10) : NaN
      if (!(n >= 1)) throw new Error(`bad step "${part}"`)
      part = part.slice(0, slash)
      step = n
    }
    let start = lo
    let end = hi
    if (part === "*") {
      // full range
    } else if (part.includes("-")) {
      const dash = part.indexOf("-")
      start = val(part.slice(0, dash))
      end = val(part.slice(dash + 1))
    } else {
      start = val(part)
      if (step === 1) end = start
    }
    if (start < lo || end > hi || start > end) throw new Error(`value out of range in "${f}"`)
    for (let i = start; i <= end; i += step) out.add(i)
  }
  return out
}

export type Schedule = { kind: "rate"; everyMs: number; value: number; unit: string } | { kind: "cron"; fields: Set<number>[]; anyDOM: boolean; anyDOW: boolean }

/** parseSchedule mirrors the server's ParseSchedule; it throws with the same messages. */
export function parseSchedule(expr: string): Schedule {
  const e = expr.trim()
  if (e.startsWith("rate(") && e.endsWith(")")) {
    const parts = e.slice(5, -1).trim().split(/\s+/).filter(Boolean)
    if (parts.length !== 2) throw new Error("rate expressions look like rate(5 minutes)")
    const n = /^[+-]?\d+$/.test(parts[0]) ? parseInt(parts[0], 10) : NaN
    if (!(n >= 1)) throw new Error("rate value must be a positive integer")
    const unit = { minute: 60_000, minutes: 60_000, hour: 3_600_000, hours: 3_600_000, day: 86_400_000, days: 86_400_000 }[parts[1]]
    if (!unit) throw new Error("rate unit must be minute(s), hour(s) or day(s)")
    return { kind: "rate", everyMs: n * unit, value: n, unit: parts[1] }
  }
  if (e.startsWith("cron(") && e.endsWith(")")) {
    const f = e.slice(5, -1).trim().split(/\s+/).filter(Boolean)
    if (f.length !== 6) throw new Error("cron expressions have 6 fields: minutes hours day-of-month month day-of-week year")
    if ((f[2] === "?") === (f[4] === "?")) throw new Error("exactly one of day-of-month or day-of-week must be '?'")
    const fields = f.map((fld, i) => {
      try {
        return parseField(fld, LIMITS[i][0], LIMITS[i][1], i === 3 ? MONTH_NAMES : i === 4 ? DAY_NAMES : undefined)
      } catch (err) {
        throw new Error(`field ${i + 1} (${CRON_FIELDS[i].label.toLowerCase()}): ${err instanceof Error ? err.message : String(err)}`)
      }
    })
    return { kind: "cron", fields, anyDOM: f[2] === "?", anyDOW: f[4] === "?" }
  }
  throw new Error("schedule must be rate(...) or cron(...)")
}

export function scheduleError(expr: string): string | null {
  if (!expr.trim()) return "Enter a schedule expression"
  try {
    parseSchedule(expr)
    return null
  } catch (e) {
    return errorMessage(e)
  }
}

function cronMatches(c: Extract<Schedule, { kind: "cron" }>, t: Date): boolean {
  const [min, hour, dom, month, dow, year] = c.fields
  if (!min.has(t.getUTCMinutes()) || !hour.has(t.getUTCHours()) || !month.has(t.getUTCMonth() + 1) || !year.has(t.getUTCFullYear())) return false
  const d = dom.has(t.getUTCDate())
  const w = dow.has(t.getUTCDay() + 1)
  if (c.anyDOM) return w
  if (c.anyDOW) return d
  return d && w
}

/**
 * nextRuns previews upcoming run times. Rates count from `from` (the server
 * counts from creation or the last run); cron times are UTC minutes, searched
 * up to a year ahead like the server.
 */
export function nextRuns(expr: string, count = 5, from = new Date()): Date[] {
  let s: Schedule
  try {
    s = parseSchedule(expr)
  } catch {
    return []
  }
  if (s.kind === "rate") return Array.from({ length: count }, (_, i) => new Date(from.getTime() + (i + 1) * s.everyMs))
  const out: Date[] = []
  let t = Math.floor(from.getTime() / 60_000) * 60_000 + 60_000
  const limit = t + 366 * 86_400_000
  while (out.length < count && t < limit) {
    const d = new Date(t)
    if (cronMatches(s, d)) out.push(d)
    t += 60_000
  }
  return out
}

/** describeSchedule renders "Every 5 minutes" for rates and the raw expression otherwise. */
export function describeSchedule(expr: string): string {
  try {
    const s = parseSchedule(expr)
    if (s.kind === "rate") return s.value === 1 ? `Every ${s.unit.replace(/s$/, "")}` : `Every ${s.value} ${s.unit.endsWith("s") ? s.unit : `${s.unit}s`}`
    return "Cron schedule (UTC)"
  } catch {
    return expr
  }
}

export function formatUtc(d: Date): string {
  return `${d.toISOString().slice(0, 16).replace("T", " ")} UTC`
}

/** hasNextRun is false when the server found no run within a year (it then returns the zero time). */
export function hasNextRun(r: Pick<EventRule, "next_run">): r is { next_run: string } {
  return !!r.next_run && new Date(r.next_run).getUTCFullYear() > 1971
}

// ---- shared actions ----

async function bulk(names: string[], call: (n: string) => Promise<unknown>, done: (n: number) => string) {
  const res = await Promise.allSettled(names.map(call))
  await revalidate(RULES_PATH)
  const ok = res.filter((r) => r.status === "fulfilled").length
  const failed = res.filter((r): r is PromiseRejectedResult => r.status === "rejected")
  if (ok) toast.success(done(ok))
  for (const f of failed.slice(0, 3)) toast.error(errorMessage(f.reason))
}

const one = (names: string[], n: number, verb: string) => (names.length === 1 ? `${verb} ${names[0]}` : `${verb} ${pluralize(n, "rule")}`)

export interface RuleActions {
  enable: (names: string[]) => Promise<void>
  disable: (names: string[]) => Promise<void>
  run: (names: string[]) => Promise<void>
  remove: (rules: EventRule[]) => void
  busy: boolean
  dialogs: ReactNode
}

/** useRuleActions provides enable/disable/run/delete for the rules list and detail pages. */
export function useRuleActions(opts: { onDeleted?: () => void } = {}): RuleActions {
  const bus = useQueryParam("bus")
  const [busy, setBusy] = useState(false)
  const [deleting, setDeleting] = useState<EventRule[] | null>(null)

  const wrap = (fn: (names: string[]) => Promise<void>) => async (names: string[]) => {
    if (!names.length) return
    setBusy(true)
    try {
      await fn(names)
    } finally {
      setBusy(false)
    }
  }

  const enable = wrap((names) => bulk(names, (n) => api.post(`${RULES_PATH}/${seg(n)}/enable`, undefined, busQuery(bus)), (k) => one(names, k, "Enabled")))
  const disable = wrap((names) => bulk(names, (n) => api.post(`${RULES_PATH}/${seg(n)}/disable`, undefined, busQuery(bus)), (k) => one(names, k, "Disabled")))
  const run = wrap((names) =>
    bulk(
      names,
      (n) => api.post(`${RULES_PATH}/${seg(n)}/run`, undefined, busQuery(bus)),
      (k) => `${one(names, k, "Ran")}: targets invoked with a Scheduled Event`,
    ),
  )

  const single = deleting?.length === 1 ? deleting[0] : null
  const dialogs = (
    <ConfirmDialog
      open={!!deleting}
      onOpenChange={(o) => !o && setDeleting(null)}
      title={single ? `Delete rule ${single.name}?` : `Delete ${pluralize(deleting?.length ?? 0, "rule")}?`}
      description="The rule stops firing immediately. Its targets (functions, queues, topics, state machines) are not deleted."
      confirmText={single ? single.name : "delete"}
      actionLabel="Delete"
      onConfirm={async () => {
        const list = deleting ?? []
        const res = await Promise.allSettled(list.map((r) => api.del(`${RULES_PATH}/${seg(r.name)}`, busQuery(bus))))
        await revalidate(RULES_PATH)
        const failed = res.filter((r): r is PromiseRejectedResult => r.status === "rejected")
        const ok = list.length - failed.length
        if (ok) toast.success(list.length === 1 ? `Deleted rule ${list[0].name}` : `Deleted ${pluralize(ok, "rule")}`)
        if (failed.length) throw failed[0].reason
        opts.onDeleted?.()
      }}
    >
      {deleting && deleting.length > 1 && (
        <ul className="bg-muted/40 max-h-40 overflow-y-auto rounded-md border p-2 font-mono text-[13px]">
          {deleting.map((r) => (
            <li key={r.name} className="truncate py-0.5">
              {r.name}
            </li>
          ))}
        </ul>
      )}
    </ConfirmDialog>
  )

  return { enable, disable, run, remove: (list) => {
      if (list.length) setDeleting(list)
    },
    busy, dialogs }
}
