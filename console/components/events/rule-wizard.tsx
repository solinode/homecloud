"use client"

import { useEffect, useMemo, useState } from "react"
import Link from "next/link"
import { useRouter } from "next/navigation"
import { AlertCircle, ArrowLeft, CalendarClock, Check, CheckCircle2, FlaskConical, Info, Loader2, Plus, Workflow, X, XCircle } from "lucide-react"
import { toast } from "sonner"

import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select"
import { Switch } from "@/components/ui/switch"
import { Textarea } from "@/components/ui/textarea"
import { EmptyState } from "@/components/console/empty-state"
import { ErrorState } from "@/components/console/error-state"
import { Field } from "@/components/console/form-field"
import { JsonEditor, jsonError } from "@/components/console/json-editor"
import { DetailSkeleton } from "@/components/console/loading"
import { PageHeader } from "@/components/console/page-header"
import { Section } from "@/components/console/section"
import { ApiError, api, errorMessage, getSession, seg } from "@/lib/api"
import { formatDate } from "@/lib/format"
import { revalidate, useApi, useQueryParam } from "@/lib/hooks"
import type { EventRule, LambdaFunction, PutRuleInput, Queue, RuleTarget, StateMachineSummary, Topic } from "@/lib/types"
import { cn } from "@/lib/utils"
import {
  CRON_FIELDS,
  EVENTS_PATH,
  RULES_PATH,
  busQuery,
  TARGET_KINDS,
  describeSchedule,
  formatUtc,
  hasNextRun,
  nextRuns,
  parseSchedule,
  patternSummary,
  ruleHref,
  ruleNameError,
  scheduleError,
  targetKind,
  targetName,
  type TargetKind,
} from "./common"
import { BusSelect } from "./bus-select"

type RuleType = "schedule" | "pattern"

type InputMode = "event" | "constant" | "path" | "transformer"

interface TargetRow {
  id?: string
  kind: TargetKind
  arn: string
  mode: InputMode
  input: string
  inputPath: string
  pathsMap: string
  template: string
  retries: string
  maxAge: string
  dlq: string
  /** fields this form does not edit, kept on save */
  extra?: Pick<RuleTarget, "role_arn" | "message_group_id">
}

const newTarget = (): TargetRow => ({ kind: "lambda", arn: "", mode: "event", input: "", inputPath: "", pathsMap: "", template: "", retries: "", maxAge: "", dlq: "" })

function rowOf(t: RuleTarget): TargetRow {
  const it = t.input_transformer
  return {
    id: t.id,
    kind: targetKind(t.arn) ?? "lambda",
    arn: t.arn,
    mode: it ? "transformer" : t.input_path ? "path" : t.input ? "constant" : "event",
    input: t.input ?? "",
    inputPath: t.input_path ?? "",
    pathsMap: it?.input_paths_map && Object.keys(it.input_paths_map).length ? JSON.stringify(it.input_paths_map, null, 2) : "",
    template: it?.input_template ?? "",
    retries: t.retry_policy?.maximum_retry_attempts != null ? String(t.retry_policy.maximum_retry_attempts) : "",
    maxAge: t.retry_policy?.maximum_event_age_in_seconds != null ? String(t.retry_policy.maximum_event_age_in_seconds) : "",
    dlq: t.dead_letter_arn ?? "",
    extra: { role_arn: t.role_arn, message_group_id: t.message_group_id },
  }
}

function targetOf(t: TargetRow): NonNullable<PutRuleInput["targets"]>[number] {
  const retry =
    t.retries.trim() || t.maxAge.trim()
      ? {
          maximum_retry_attempts: t.retries.trim() ? Number(t.retries) : undefined,
          maximum_event_age_in_seconds: t.maxAge.trim() ? Number(t.maxAge) : undefined,
        }
      : undefined
  return {
    ...t.extra,
    id: t.id,
    arn: t.arn,
    input: t.mode === "constant" ? t.input.trim() : undefined,
    input_path: t.mode === "path" ? t.inputPath.trim() : undefined,
    input_transformer:
      t.mode === "transformer"
        ? { input_paths_map: t.pathsMap.trim() ? (JSON.parse(t.pathsMap) as Record<string, string>) : undefined, input_template: t.template }
        : undefined,
    retry_policy: retry,
    dead_letter_arn: t.dlq || undefined,
  }
}

const PATH_RE = /^\$(\.[A-Za-z0-9_\-]+|\[\d+\])*$/

const PRESETS: [string, string][] = [
  ["Every minute", "rate(1 minute)"],
  ["Every 5 minutes", "rate(5 minutes)"],
  ["Every 15 minutes", "cron(0/15 * * * ? *)"],
  ["Hourly", "rate(1 hour)"],
  ["Daily", "rate(1 day)"],
  ["Daily at 12:00 UTC", "cron(0 12 * * ? *)"],
  ["Weekdays at 09:00 UTC", "cron(0 9 ? * MON-FRI *)"],
  ["1st of the month, 00:00 UTC", "cron(0 0 1 * ? *)"],
]

const DEFAULT_CRON = ["0", "12", "*", "*", "?", "*"]
const DEFAULT_PATTERN = JSON.stringify({ source: ["my.app"], "detail-type": ["order.paid"] }, null, 2)

function sampleEvent(): string {
  return JSON.stringify(
    {
      version: "0",
      id: "6a7e8feb-b491-4cf7-a9f1-bf3703467718",
      "detail-type": "order.paid",
      source: "my.app",
      account: getSession()?.account_id ?? "000000000000",
      time: new Date().toISOString().replace(/\.\d+Z$/, "Z"),
      region: "us-east-1",
      resources: [],
      detail: { order_id: "o-1001", total: 42 },
    },
    null,
    2,
  )
}

const objectOnly = (v: unknown) => (!v || typeof v !== "object" || Array.isArray(v) ? "Must be a JSON object" : null)

/** cronFieldsOf splits "cron(a b c d e f)" into its six fields. */
function cronFieldsOf(expr: string): string[] | null {
  const e = expr.trim()
  if (!e.startsWith("cron(") || !e.endsWith(")")) return null
  const f = e.slice(5, -1).trim().split(/\s+/)
  return f.length === 6 ? f : null
}

interface TargetOption {
  name: string
  arn: string
}

/** useTargetOptions lists the functions, queues, topics and state machines a rule can target. */
export function useTargetOptions() {
  const fns = useApi<LambdaFunction[]>("/api/v1/lambda/functions", { revalidateOnFocus: false })
  const queues = useApi<Queue[]>("/api/v1/sqs/queues", { revalidateOnFocus: false })
  const topics = useApi<Topic[]>("/api/v1/sns/topics", { revalidateOnFocus: false })
  const machines = useApi<StateMachineSummary[]>("/api/v1/sfn/state-machines", { revalidateOnFocus: false })
  const opts: Record<TargetKind, { data?: TargetOption[]; error?: unknown }> = {
    lambda: { data: fns.data?.map((f) => ({ name: f.name, arn: f.arn })), error: fns.error },
    sqs: { data: queues.data?.map((q) => ({ name: q.name, arn: q.arn })), error: queues.error },
    sns: { data: topics.data?.map((t) => ({ name: t.name, arn: t.arn })), error: topics.error },
    sfn: { data: machines.data?.map((m) => ({ name: m.name, arn: m.arn })), error: machines.error },
  }
  return opts
}

export function RuleWizard() {
  const router = useRouter()
  const editName = useQueryParam("name")
  const editing = !!editName
  const busParam = useQueryParam("bus") || "default"
  const [bus, setBus] = useState(busParam)
  useEffect(() => setBus(busParam), [busParam])

  const existing = useApi<EventRule>(editing ? `${RULES_PATH}/${seg(editName)}` : null, { revalidateOnFocus: false, keepPreviousData: false, query: busQuery(bus) })
  const allRules = useApi<EventRule[]>(editing ? null : RULES_PATH, { revalidateOnFocus: false, query: busQuery(bus) })
  const targetOpts = useTargetOptions()

  const [name, setName] = useState("")
  const [description, setDescription] = useState("")
  const [type, setType] = useState<RuleType>("schedule")
  const [schedule, setSchedule] = useState("rate(5 minutes)")
  const [cron, setCron] = useState<string[]>(DEFAULT_CRON)
  const [showCron, setShowCron] = useState(false)
  const [pattern, setPattern] = useState(DEFAULT_PATTERN)
  const [sample, setSample] = useState("")
  const [testResult, setTestResult] = useState<boolean | null>(null)
  const [testing, setTesting] = useState(false)
  const [targets, setTargets] = useState<TargetRow[]>([newTarget()])
  const [enabled, setEnabled] = useState(true)
  const [loaded, setLoaded] = useState(false)
  const [submitted, setSubmitted] = useState(false)
  const [pending, setPending] = useState(false)

  useEffect(() => setSample(sampleEvent()), [])

  // Prefill from the rule being edited, once.
  useEffect(() => {
    const r = existing.data
    if (!r || loaded) return
    setName(r.name)
    setDescription(r.description ?? "")
    if (r.schedule_expression) {
      setType("schedule")
      setSchedule(r.schedule_expression)
      const f = cronFieldsOf(r.schedule_expression)
      if (f) setCron(f)
    } else {
      setType("pattern")
      setPattern(JSON.stringify(r.event_pattern ?? {}, null, 2))
    }
    setTargets((r.targets ?? []).map(rowOf))
    setEnabled(r.state !== "DISABLED")
    setLoaded(true)
  }, [existing.data, loaded])

  const setScheduleExpr = (v: string) => {
    setSchedule(v)
    const f = cronFieldsOf(v)
    if (f) setCron(f)
  }
  const setCronField = (i: number, v: string) => {
    const f = cron.map((x, j) => (j === i ? v : x))
    setCron(f)
    setSchedule(`cron(${f.map((x) => x.trim() || "*").join(" ")})`)
  }

  const setTarget = (i: number, patch: Partial<TargetRow>) => setTargets(targets.map((t, j) => (j === i ? { ...t, ...patch } : t)))

  // ---- validation ----
  const errors = useMemo(() => {
    const e: Record<string, string> = {}
    if (!editing) {
      const ne = ruleNameError(name)
      if (ne) e.name = ne
      else if (allRules.data?.some((r) => r.name === name)) e.name = `A rule named ${name} already exists`
    }
    if (description.length > 512) e.description = "Descriptions are at most 512 characters"
    if (type === "schedule") {
      const se = scheduleError(schedule)
      if (se) e.schedule = se
    } else {
      const pe = jsonError(pattern) ?? objectOnly(JSON.parse(pattern))
      if (pe) e.pattern = pe
    }
    const seen = new Set<string>()
    targets.forEach((t, i) => {
      if (!t.arn) e[`t${i}arn`] = `Choose a ${TARGET_KINDS.find((k) => k.kind === t.kind)?.noun ?? "target"}`
      else if (seen.has(t.arn)) e[`t${i}arn`] = "This target is already added"
      seen.add(t.arn)
      if (t.mode === "constant") {
        const je = t.input.trim() ? jsonError(t.input) : "Enter the JSON to send"
        if (je) e[`t${i}input`] = t.input.trim() ? `Invalid JSON: ${je}` : je
      } else if (t.mode === "path") {
        if (!PATH_RE.test(t.inputPath.trim())) e[`t${i}input`] = "Enter a JSONPath such as $.detail"
      } else if (t.mode === "transformer") {
        if (t.pathsMap.trim()) {
          const je = jsonError(t.pathsMap)
          const m = je ? null : (JSON.parse(t.pathsMap) as unknown)
          if (je) e[`t${i}paths`] = `Invalid JSON: ${je}`
          else if (!m || typeof m !== "object" || Array.isArray(m) || Object.values(m).some((v) => typeof v !== "string" || !PATH_RE.test(v)))
            e[`t${i}paths`] = 'Must be an object of JSONPaths, e.g. {"id": "$.detail.id"}'
        }
        if (!t.template.trim()) e[`t${i}template`] = "The input template is required"
      }
      const r = Number(t.retries)
      if (t.retries.trim() && (!Number.isInteger(r) || r < 0 || r > 185)) e[`t${i}retries`] = "0 to 185"
      const a = Number(t.maxAge)
      if (t.maxAge.trim() && (!Number.isInteger(a) || a < 60 || a > 86400)) e[`t${i}age`] = "60 to 86400 seconds"
    })
    return e
  }, [editing, name, allRules.data, description, type, schedule, pattern, targets])
  const err = (k: string) => (submitted ? errors[k] : undefined)
  const valid = Object.keys(errors).length === 0

  const preview = useMemo(() => (type === "schedule" && !scheduleError(schedule) ? nextRuns(schedule, 5) : []), [type, schedule])
  const isRate = useMemo(() => {
    try {
      return parseSchedule(schedule).kind === "rate"
    } catch {
      return false
    }
  }, [schedule])

  const testPattern = async () => {
    const pe = jsonError(pattern) ?? objectOnly(JSON.parse(pattern))
    const se = jsonError(sample) ?? objectOnly(JSON.parse(sample))
    if (pe || se) {
      toast.error(pe ? `Event pattern: ${pe}` : `Sample event: ${se}`)
      return
    }
    setTesting(true)
    try {
      const r = await api.post<{ result: boolean }>(`${EVENTS_PATH}/test-pattern`, { pattern: JSON.parse(pattern), event: JSON.parse(sample) })
      setTestResult(r.result)
    } catch (e) {
      toast.error(errorMessage(e))
    } finally {
      setTesting(false)
    }
  }

  const save = async () => {
    setSubmitted(true)
    if (!valid) {
      toast.error("Fix the highlighted fields before saving")
      return
    }
    const body: PutRuleInput = {
      description: description.trim(),
      schedule_expression: type === "schedule" ? schedule.trim() : undefined,
      event_pattern: type === "pattern" ? (JSON.parse(pattern) as Record<string, unknown>) : undefined,
      state: enabled ? "ENABLED" : "DISABLED",
      targets: targets.map(targetOf),
    }
    setPending(true)
    try {
      const r = await api.put<EventRule>(`${RULES_PATH}/${seg(name)}`, body, busQuery(bus))
      const when = r.schedule_expression && hasNextRun(r) ? ` Next run ${formatDate(r.next_run)}${r.state === "DISABLED" ? " once enabled" : ""}.` : ""
      toast.success(`${editing ? "Saved" : "Created"} rule ${r.name}.${when}`)
      await revalidate(RULES_PATH)
      router.push(ruleHref(r.name, bus))
    } catch (e) {
      toast.error(errorMessage(e))
      setPending(false)
    }
  }

  const title = editing ? `Edit rule ${editName}` : "Create rule"
  const crumbs = [
    { label: "EventBridge", href: "/events/" },
    { label: "Rules", href: "/events/" },
    ...(editing ? [{ label: editName, href: ruleHref(editName, bus) }, { label: "Edit" }] : [{ label: "Create rule" }]),
  ]

  if (editing && existing.error && !existing.data) {
    const notFound = existing.error instanceof ApiError && existing.error.status === 404
    return (
      <div className="flex flex-col gap-4">
        <PageHeader title={title} breadcrumbs={crumbs} />
        {notFound ? (
          <Section>
            <EmptyState
              icon={AlertCircle}
              title="Rule not found"
              description={`Rule ${editName} does not exist.`}
              action={
                <Button variant="outline" size="sm" asChild>
                  <Link href="/events/">
                    <ArrowLeft /> Back to rules
                  </Link>
                </Button>
              }
            />
          </Section>
        ) : (
          <ErrorState error={existing.error} onRetry={() => existing.mutate()} />
        )}
      </div>
    )
  }
  if (editing && !loaded) return <DetailSkeleton />

  return (
    <div className="flex flex-col gap-4">
      <PageHeader
        title={title}
        description="A rule runs on a schedule or matches published events, then delivers to up to five targets."
        breadcrumbs={crumbs}
      />
      <form
        onSubmit={(e) => {
          e.preventDefault()
          save()
        }}
        className="grid grid-cols-1 items-start gap-4 xl:grid-cols-[minmax(0,1fr)_320px]"
      >
        <div className="flex min-w-0 flex-col gap-4">
          {/* ---- Details ---- */}
          <Section title="Rule details">
            <div className="flex flex-col gap-4">
              <Field label="Event bus" htmlFor="rule-bus" help={editing ? "A rule's bus cannot be changed." : "Schedules run on the default bus only; event patterns work on any bus."}>
                <BusSelect id="rule-bus" value={bus} onChange={setBus} disabled={editing} className="max-w-md" />
              </Field>
              <Field
                label="Name"
                htmlFor="rule-name"
                error={err("name")}
                help={editing ? "Rule names cannot be changed." : "Up to 64 letters, digits, dots, hyphens and underscores."}
              >
                <Input
                  id="rule-name"
                  value={name}
                  onChange={(e) => setName(e.target.value)}
                  disabled={editing}
                  placeholder="e.g. nightly-report"
                  autoFocus={!editing}
                  autoComplete="off"
                  spellCheck={false}
                  className="max-w-md"
                />
              </Field>
              <Field label="Description" htmlFor="rule-desc" optional error={err("description")}>
                <Textarea id="rule-desc" rows={2} value={description} onChange={(e) => setDescription(e.target.value)} className="max-w-xl" />
              </Field>
              <div className="flex flex-col gap-2">
                <span className="text-sm font-medium">Rule type</span>
                <div className="grid grid-cols-1 gap-3 sm:grid-cols-2" role="radiogroup" aria-label="Rule type">
                  {(
                    [
                      ["schedule", CalendarClock, "Schedule", "Run the targets at a fixed rate or on a cron schedule (UTC)."],
                      ["pattern", Workflow, "Event pattern", "Run the targets when a published event matches a pattern."],
                    ] as const
                  ).map(([k, Icon, label, desc]) => {
                    const active = type === k
                    return (
                      <button
                        key={k}
                        type="button"
                        role="radio"
                        aria-checked={active}
                        onClick={() => setType(k)}
                        className={cn(
                          "relative flex gap-3 rounded-lg border p-3 text-left transition-colors",
                          active ? "border-primary bg-primary/5 ring-primary ring-1 dark:bg-primary/10" : "hover:bg-muted/50",
                        )}
                      >
                        <Icon className={cn("mt-0.5 size-5 shrink-0", active ? "text-primary" : "text-muted-foreground")} />
                        <span className="flex flex-col gap-0.5 pr-6">
                          <span className="text-sm font-medium">{label}</span>
                          <span className="text-muted-foreground text-xs">{desc}</span>
                        </span>
                        {active && (
                          <span className="bg-primary text-primary-foreground absolute top-2 right-2 flex size-5 items-center justify-center rounded-full">
                            <Check className="size-3.5" />
                          </span>
                        )}
                      </button>
                    )
                  })}
                </div>
              </div>
            </div>
          </Section>

          {/* ---- Schedule ---- */}
          {type === "schedule" && (
            <Section title="Schedule" description="Times are UTC. The scheduler checks rules at the top of every minute.">
              <div className="flex flex-col gap-4">
                <div className="flex flex-wrap gap-1.5">
                  {PRESETS.map(([label, expr]) => (
                    <Button
                      key={expr}
                      type="button"
                      size="sm"
                      variant={schedule.trim() === expr ? "secondary" : "outline"}
                      className="h-7 text-xs"
                      onClick={() => setScheduleExpr(expr)}
                      title={expr}
                    >
                      {label}
                    </Button>
                  ))}
                </div>
                <Field
                  label="Schedule expression"
                  htmlFor="rule-schedule"
                  error={err("schedule") ?? (schedule.trim() && errors.schedule ? errors.schedule : undefined)}
                  help={
                    <>
                      <span className="font-mono">rate(N minute|minutes|hour|hours|day|days)</span> or{" "}
                      <span className="font-mono">cron(minutes hours day-of-month month day-of-week year)</span>
                    </>
                  }
                >
                  <Input
                    id="rule-schedule"
                    value={schedule}
                    onChange={(e) => setScheduleExpr(e.target.value)}
                    placeholder="rate(5 minutes)"
                    spellCheck={false}
                    className="max-w-md font-mono"
                  />
                </Field>

                <div className="flex flex-col gap-3">
                  <button type="button" onClick={() => setShowCron(!showCron)} className="text-primary w-fit text-sm hover:underline">
                    {showCron ? "Hide cron builder" : "Build a cron expression"}
                  </button>
                  {showCron && (
                    <div className="flex flex-col gap-2 rounded-md border p-3">
                      <div className="grid grid-cols-2 gap-3 sm:grid-cols-3 lg:grid-cols-6">
                        {CRON_FIELDS.map((f, i) => (
                          <Field key={f.label} label={<span className="text-xs">{f.label}</span>} htmlFor={`cron-${i}`} help={f.hint}>
                            <Input
                              id={`cron-${i}`}
                              value={cron[i]}
                              onChange={(e) => setCronField(i, e.target.value)}
                              placeholder={f.placeholder}
                              className="h-8 font-mono"
                              spellCheck={false}
                            />
                          </Field>
                        ))}
                      </div>
                      <p className="text-muted-foreground text-xs">
                        Use <span className="font-mono">*</span> for any value, <span className="font-mono">?</span> in exactly one of day-of-month and
                        day-of-week, ranges <span className="font-mono">MON-FRI</span>, lists <span className="font-mono">1,15</span> and steps{" "}
                        <span className="font-mono">0/15</span>.
                      </p>
                    </div>
                  )}
                </div>

                <div className="bg-muted/30 flex flex-col gap-2 rounded-md border p-3">
                  <span className="text-sm font-medium">
                    Next runs{" "}
                    {!errors.schedule && <span className="text-muted-foreground font-normal">· {describeSchedule(schedule)}</span>}
                  </span>
                  {preview.length ? (
                    <ol className="flex flex-col gap-0.5 text-sm">
                      {preview.map((d) => (
                        <li key={d.getTime()} className="flex flex-wrap gap-x-3">
                          <span className="font-mono text-[13px]">{formatUtc(d)}</span>
                          <span className="text-muted-foreground">{formatDate(d, false)} local</span>
                        </li>
                      ))}
                    </ol>
                  ) : (
                    <p className="text-muted-foreground text-sm">{errors.schedule ? "Enter a valid expression to preview run times." : "No run in the next year."}</p>
                  )}
                  <p className="text-muted-foreground flex gap-1.5 text-xs">
                    <Info className="mt-px size-3.5 shrink-0" />
                    <span>
                      {isRate ? "Rates are counted from when the rule is saved (then from each run). " : ""}
                      This is an estimate: the server computes the exact next run, shown on the rule after saving.
                    </span>
                  </p>
                </div>
              </div>
            </Section>
          )}

          {/* ---- Event pattern ---- */}
          {type === "pattern" && (
            <Section
              title="Event pattern"
              description="Every key in the pattern must match the event. Arrays list allowed values; nested objects match nested fields."
            >
              <div className="flex flex-col gap-4">
                <Field label="Pattern" error={err("pattern")}>
                  <JsonEditor value={pattern} onChange={(v) => (setPattern(v), setTestResult(null))} rows={8} validate={objectOnly} />
                </Field>
                <p className="text-muted-foreground text-xs">
                  Supported matchers: exact values <span className="font-mono">{`["a","b"]`}</span>, <span className="font-mono">{`{"prefix":"ord"}`}</span>,{" "}
                  <span className="font-mono">{`{"exists":true}`}</span>, <span className="font-mono">{`{"anything-but":["x"]}`}</span> and{" "}
                  <span className="font-mono">{`{"numeric":[">",0,"<=",100]}`}</span>.
                </p>
                <div className="flex flex-col gap-2 border-t pt-4">
                  <span className="text-sm font-medium">Test with a sample event</span>
                  <JsonEditor value={sample} onChange={(v) => (setSample(v), setTestResult(null))} rows={12} validate={objectOnly} />
                  <div className="flex flex-wrap items-center gap-3">
                    <Button type="button" variant="outline" size="sm" onClick={testPattern} disabled={testing}>
                      {testing ? <Loader2 className="animate-spin" /> : <FlaskConical />}
                      Test pattern
                    </Button>
                    {testResult === true && (
                      <span className="flex items-center gap-1.5 text-sm font-medium text-emerald-700 dark:text-emerald-400">
                        <CheckCircle2 className="size-4" /> The sample event matches the pattern
                      </span>
                    )}
                    {testResult === false && (
                      <span className="text-destructive flex items-center gap-1.5 text-sm font-medium">
                        <XCircle className="size-4" /> The sample event does not match
                      </span>
                    )}
                  </div>
                </div>
              </div>
            </Section>
          )}

          {/* ---- Targets ---- */}
          <Section title="Targets" description="Each target receives the event JSON, part of it, a constant, or a transformed template. Failed deliveries are retried and can go to a dead-letter queue.">
            <div className="flex flex-col gap-3">
              {targets.length === 0 && (
                <p className="text-muted-foreground text-sm">No targets: the rule will fire and count invocations but deliver nothing.</p>
              )}
              {targets.map((t, i) => (
                <TargetEditor
                  key={i}
                  index={i}
                  row={t}
                  options={targetOpts[t.kind]}
                  onChange={(p) => setTarget(i, p)}
                  onRemove={() => setTargets(targets.filter((_, j) => j !== i))}
                  queues={targetOpts.sqs}
                  errors={{
                    arn: err(`t${i}arn`),
                    input: err(`t${i}input`),
                    paths: err(`t${i}paths`) ?? errors[`t${i}paths`],
                    template: err(`t${i}template`),
                    retries: err(`t${i}retries`) ?? errors[`t${i}retries`],
                    age: err(`t${i}age`) ?? errors[`t${i}age`],
                  }}
                />
              ))}
              <div className="flex flex-wrap items-center gap-3">
                <Button
                  type="button"
                  variant="outline"
                  size="sm"
                  disabled={targets.length >= 5}
                  onClick={() => setTargets([...targets, newTarget()])}
                >
                  <Plus /> Add target
                </Button>
                <span className="text-muted-foreground text-xs">{targets.length} of 5</span>
              </div>
            </div>
          </Section>
        </div>

        {/* ---- Summary ---- */}
        <aside className="xl:sticky xl:top-4">
          <Section title="Summary">
            <div className="flex flex-col gap-4">
              <div className="flex items-start justify-between gap-3">
                <div>
                  <Label htmlFor="rule-enabled" className="font-medium">
                    Enabled
                  </Label>
                  <p className="text-muted-foreground mt-0.5 text-xs">Disabled rules keep their configuration but never fire.</p>
                </div>
                <Switch id="rule-enabled" checked={enabled} onCheckedChange={setEnabled} />
              </div>
              <dl className="flex flex-col gap-3 border-t pt-3 text-sm">
                <SummaryItem label="Name">
                  <span className="font-mono text-[13px] break-all">{name || "-"}</span>
                </SummaryItem>
                <SummaryItem label="Type">{type === "schedule" ? "Schedule" : "Event pattern"}</SummaryItem>
                {type === "schedule" ? (
                  <SummaryItem label="Schedule">
                    <span className="font-mono text-[13px] break-all">{schedule || "-"}</span>
                  </SummaryItem>
                ) : (
                  <SummaryItem label="Pattern">
                    <span className="font-mono text-[12.5px] break-all">{jsonError(pattern) ? "Invalid JSON" : patternSummary(JSON.parse(pattern), 140)}</span>
                  </SummaryItem>
                )}
                <SummaryItem label="Targets">
                  {targets.filter((t) => t.arn).length ? (
                    <ul className="flex flex-col gap-0.5">
                      {targets
                        .filter((t) => t.arn)
                        .map((t) => (
                          <li key={t.arn} className="truncate">
                            <span className="text-muted-foreground text-xs">{TARGET_KINDS.find((k) => k.kind === t.kind)?.label}:</span> {targetName(t.arn)}
                          </li>
                        ))}
                    </ul>
                  ) : (
                    "None"
                  )}
                </SummaryItem>
              </dl>
              {submitted && !valid && <p className="text-destructive text-xs">Some settings need attention. Check the highlighted fields.</p>}
              <div className="flex flex-col gap-2 border-t pt-4">
                <Button type="submit" disabled={pending}>
                  {pending && <Loader2 className="animate-spin" />}
                  {editing ? "Save changes" : "Create rule"}
                </Button>
                <Button type="button" variant="outline" asChild>
                  <Link href={editing ? ruleHref(editName, bus) : "/events/"}>Cancel</Link>
                </Button>
              </div>
            </div>
          </Section>
        </aside>
      </form>
    </div>
  )
}

const INPUT_MODES: { value: InputMode; label: string; help: string }[] = [
  { value: "event", label: "Matched event", help: "The whole event JSON." },
  { value: "path", label: "Part of the matched event", help: "The value at a JSONPath of the event." },
  { value: "constant", label: "Constant (JSON text)", help: "Fixed JSON instead of the event." },
  { value: "transformer", label: "Input transformer", help: "Extract values into variables, then fill a template." },
]

const NO_DLQ = "__none__"

function TargetEditor({
  index,
  row,
  options,
  queues,
  onChange,
  onRemove,
  errors,
}: {
  index: number
  row: TargetRow
  options: { data?: TargetOption[]; error?: unknown }
  queues: { data?: TargetOption[]; error?: unknown }
  onChange: (p: Partial<TargetRow>) => void
  onRemove: () => void
  errors: Partial<Record<"arn" | "input" | "paths" | "template" | "retries" | "age", string>>
}) {
  const [advanced, setAdvanced] = useState(false)
  // Rows are keyed by position, so also show the settings whenever the row has some.
  const showAdvanced = advanced || !!(row.retries || row.maxAge || row.dlq)
  const kind = TARGET_KINDS.find((k) => k.kind === row.kind)!
  const list = options.data ?? []
  const missing = row.arn && options.data && !list.some((o) => o.arn === row.arn)
  const dlqs = queues.data ?? []
  const mode = INPUT_MODES.find((m) => m.value === row.mode)!

  return (
    <div className="flex flex-col gap-3 rounded-md border p-3">
      <div className="flex items-center justify-between gap-2">
        <span className="text-sm font-medium">Target {index + 1}</span>
        <Button type="button" variant="ghost" size="icon" className="size-7" onClick={onRemove} aria-label="Remove target">
          <X />
        </Button>
      </div>
      <div className="grid grid-cols-1 gap-3 sm:grid-cols-[12rem_minmax(0,1fr)]">
        <Field label="Target type">
          <Select value={row.kind} onValueChange={(v) => onChange({ kind: v as TargetKind, arn: "" })}>
            <SelectTrigger size="sm" className="w-full" aria-label="Target type">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {TARGET_KINDS.map((k) => (
                <SelectItem key={k.kind} value={k.kind}>
                  {k.label}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </Field>
        <Field label={kind.label} error={errors.arn ?? (options.error ? errorMessage(options.error) : undefined)} help={row.arn ? <span className="font-mono break-all">{row.arn}</span> : undefined}>
          <Select value={row.arn} onValueChange={(v) => onChange({ arn: v })} disabled={!options.data}>
            <SelectTrigger size="sm" className="w-full" aria-label={kind.label}>
              <SelectValue placeholder={!options.data ? "Loading..." : list.length ? `Choose a ${kind.noun}` : `No ${kind.noun}s found`} />
            </SelectTrigger>
            <SelectContent>
              {missing && (
                <SelectItem value={row.arn}>
                  {targetName(row.arn)} <span className="text-destructive text-xs">(not found)</span>
                </SelectItem>
              )}
              {list.map((o) => (
                <SelectItem key={o.arn} value={o.arn}>
                  {o.name}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </Field>
      </div>
      <Field label="Configure target input" help={mode.help}>
        <Select value={row.mode} onValueChange={(v) => onChange({ mode: v as InputMode })}>
          <SelectTrigger size="sm" className="w-full sm:w-72" aria-label="Target input">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            {INPUT_MODES.map((m) => (
              <SelectItem key={m.value} value={m.value}>
                {m.label}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
      </Field>
      {row.mode === "constant" && (
        <Field label="Constant input (JSON)" error={errors.input}>
          <Textarea rows={3} value={row.input} onChange={(e) => onChange({ input: e.target.value })} placeholder='{"task": "cleanup"}' className="font-mono text-[13px]" spellCheck={false} />
        </Field>
      )}
      {row.mode === "path" && (
        <Field label="Input path" error={errors.input} help="For example $.detail sends only the event's detail object.">
          <Input value={row.inputPath} onChange={(e) => onChange({ inputPath: e.target.value })} placeholder="$.detail" className="h-8 font-mono text-[13px]" spellCheck={false} />
        </Field>
      )}
      {row.mode === "transformer" && (
        <>
          <Field label="Input path" optional error={errors.paths} help="A JSON object mapping variable names to JSONPaths into the event.">
            <Textarea
              rows={3}
              value={row.pathsMap}
              onChange={(e) => onChange({ pathsMap: e.target.value })}
              placeholder={'{\n  "order": "$.detail.order_id",\n  "source": "$.source"\n}'}
              className="font-mono text-[13px]"
              spellCheck={false}
            />
          </Field>
          <Field label="Template" error={errors.template} help="Use <variable> placeholders. <aws.events.event> inserts the whole event, <aws.events.rule-name> the rule name.">
            <Textarea
              rows={3}
              value={row.template}
              onChange={(e) => onChange({ template: e.target.value })}
              placeholder={'{"text": "Order <order> from <source>"}'}
              className="font-mono text-[13px]"
              spellCheck={false}
            />
          </Field>
        </>
      )}
      <label className="flex w-fit items-center gap-2 text-sm">
        <Switch checked={showAdvanced} onCheckedChange={(v) => { setAdvanced(v); if (!v) onChange({ retries: "", maxAge: "", dlq: "" }) }} aria-label="Retry policy and dead-letter queue" />
        Retry policy and dead-letter queue
      </label>
      {showAdvanced && (
        <div className="grid grid-cols-1 gap-3 sm:grid-cols-3">
          <Field label="Maximum age of event" error={errors.age} help="Seconds, 60-86400 (default 86400).">
            <Input inputMode="numeric" value={row.maxAge} onChange={(e) => onChange({ maxAge: e.target.value.replace(/[^\d]/g, "") })} placeholder="86400" className="h-8" />
          </Field>
          <Field label="Retry attempts" error={errors.retries} help="0-185 (default 185).">
            <Input inputMode="numeric" value={row.retries} onChange={(e) => onChange({ retries: e.target.value.replace(/[^\d]/g, "") })} placeholder="185" className="h-8" />
          </Field>
          <Field label="Dead-letter queue" help="Undeliverable events go to this SQS queue." error={queues.error ? errorMessage(queues.error) : undefined}>
            <Select value={row.dlq || NO_DLQ} onValueChange={(v) => onChange({ dlq: v === NO_DLQ ? "" : v })} disabled={!queues.data}>
              <SelectTrigger size="sm" className="w-full" aria-label="Dead-letter queue">
                <SelectValue placeholder="None" />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value={NO_DLQ}>None</SelectItem>
                {row.dlq && !dlqs.some((q) => q.arn === row.dlq) && <SelectItem value={row.dlq}>{targetName(row.dlq)}</SelectItem>}
                {dlqs.map((q) => (
                  <SelectItem key={q.arn} value={q.arn}>
                    {q.name}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </Field>
        </div>
      )}
    </div>
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
