import { badRequest, conflict, err, getState, type DemoService, type Router } from "../engine"
import { DAY, HOUR, MIN, ago, ahead, arn, nowIso, uuid } from "../util"
import { NAMES } from "../ids"
import type { EventBus, EventRule, RuleTarget, Schedule, ScheduleGroup } from "@/lib/types"

interface EventsState {
  buses: EventBus[]
  rules: EventRule[]
  groups: ScheduleGroup[]
  schedules: Schedule[]
}
const S = (): EventsState => getState().events as EventsState

const DEFAULT_BUS = "default"
const busArn = (name: string) => arn("events", `event-bus/${name}`)
const ruleArn = (bus: string, name: string) => arn("events", bus === DEFAULT_BUS ? `rule/${name}` : `rule/${bus}/${name}`)
const groupArn = (g: string) => arn("scheduler", `schedule-group/${g}`)
const schedArn = (g: string, n: string) => arn("scheduler", `schedule/${g}/${n}`)
const fnArn = (n: string) => arn("lambda", `function:${n}`)
const SCHED_ROLE = arn("iam", "role/shop-scheduler-role", { region: null })

// ---- pattern matching (EventBridge content filtering) ----

type Json = any // eslint-disable-line @typescript-eslint/no-explicit-any

function matchValue(spec: Json, v: Json): boolean {
  if (spec === null) return v === null
  if (typeof spec !== "object") return v === spec
  if ("exists" in spec) return (v !== undefined) === !!spec.exists
  if (v === undefined) return false
  const str = typeof v === "string" ? v : undefined
  if ("prefix" in spec) return str !== undefined && (typeof spec.prefix === "string" ? str.startsWith(spec.prefix) : matchValue({ "equals-ignore-case": spec.prefix["equals-ignore-case"] }, v))
  if ("suffix" in spec) return str !== undefined && str.endsWith(spec.suffix)
  if ("equals-ignore-case" in spec) return str !== undefined && str.toLowerCase() === String(spec["equals-ignore-case"]).toLowerCase()
  if ("wildcard" in spec) return str !== undefined && new RegExp("^" + String(spec.wildcard).split("*").map((p) => p.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")).join(".*") + "$").test(str)
  if ("anything-but" in spec) {
    const ab = spec["anything-but"]
    if (Array.isArray(ab)) return !ab.includes(v)
    if (ab && typeof ab === "object") return !matchValue(ab, v)
    return v !== ab
  }
  if ("numeric" in spec) {
    if (typeof v !== "number") return false
    const n = spec.numeric as (string | number)[]
    for (let i = 0; i + 1 < n.length; i += 2) {
      const op = n[i]
      const x = n[i + 1] as number
      if (op === "=" && !(v === x)) return false
      if (op === ">" && !(v > x)) return false
      if (op === ">=" && !(v >= x)) return false
      if (op === "<" && !(v < x)) return false
      if (op === "<=" && !(v <= x)) return false
    }
    return true
  }
  return false
}

function matchPattern(pattern: Json, event: Json): boolean {
  if (Array.isArray(pattern)) {
    const vals = Array.isArray(event) ? event : [event]
    if (pattern.some((s) => typeof s === "object" && s && "exists" in s)) return pattern.some((s) => matchValue(s, event))
    return pattern.some((s) => vals.some((v) => matchValue(s, v)))
  }
  if (pattern && typeof pattern === "object") {
    if (Array.isArray(pattern.$or)) return pattern.$or.some((p: Json) => matchPattern(p, event))
    return Object.entries(pattern).every(([k, sub]) => {
      if (k === "$or") return true
      const child = event && typeof event === "object" ? event[k] : undefined
      if (Array.isArray(child) && !Array.isArray(sub) && typeof sub === "object") return child.some((c) => matchPattern(sub, c))
      return matchPattern(sub, child)
    })
  }
  return false
}

function validatePattern(p: Json, path = ""): string | null {
  if (!p || typeof p !== "object" || Array.isArray(p)) return "Event pattern must be a JSON object"
  for (const [k, v] of Object.entries(p)) {
    if (k === "$or") continue
    if (Array.isArray(v)) continue
    if (v && typeof v === "object") {
      const e = validatePattern(v, `${path}${k}.`)
      if (e) return e
    } else return `Invalid event pattern: ${path}${k} must be an array of values or a nested object`
  }
  return null
}

// ---- schedule expressions ----

function nextRun(expr: string): string | undefined {
  const rate = /^rate\((\d+)\s+(minute|minutes|hour|hours|day|days)\)$/.exec(expr)
  if (rate) {
    const n = Number(rate[1])
    const unit = rate[2].startsWith("minute") ? MIN : rate[2].startsWith("hour") ? HOUR : DAY
    return ahead(n * unit)
  }
  const cron = /^cron\(([^)]+)\)$/.exec(expr)
  if (cron) {
    const f = cron[1].trim().split(/\s+/)
    const d = new Date()
    const min = /^\d+$/.test(f[0]) ? Number(f[0]) : d.getUTCMinutes() + 1
    const hr = /^\d+$/.test(f[1]) ? Number(f[1]) : d.getUTCHours()
    const t = new Date(Date.UTC(d.getUTCFullYear(), d.getUTCMonth(), d.getUTCDate(), hr, min, 0))
    if (t.getTime() <= Date.now()) t.setUTCDate(t.getUTCDate() + (/^\d+$/.test(f[1]) ? 1 : 0))
    if (t.getTime() <= Date.now()) t.setTime(Date.now() + HOUR)
    return t.toISOString()
  }
  return undefined
}

function validSchedule(expr: string): boolean {
  return /^rate\(\d+\s+(minute|minutes|hour|hours|day|days)\)$/.test(expr) || /^cron\((\S+\s+){5}\S+\)$/.test(expr)
}

// ---- seed ----

const target = (id: string, tArn: string, extra: Partial<RuleTarget> = {}): RuleTarget => ({ id, arn: tArn, ...extra })

function seed(): EventsState {
  const sm = arn("states", `stateMachine:${NAMES.stateMachine}`)
  const alerts = arn("sns", NAMES.alertsTopic)
  const orders = arn("sqs", NAMES.ordersQueue)
  const dlq = arn("sqs", NAMES.ordersDlq)
  const rule = (name: string, bus: string, o: Partial<EventRule>, created: number): EventRule => ({
    name,
    arn: ruleArn(bus, name),
    description: "",
    event_bus: bus,
    state: "ENABLED",
    targets: [],
    invocations: 0,
    failed_invocations: 0,
    created_at: ago(created),
    ...o,
  })
  const rules: EventRule[] = [
    rule("shop-nightly-report", DEFAULT_BUS, { description: "Build the daily sales report at 02:00 UTC", schedule_expression: "cron(0 2 * * ? *)", next_run: nextRun("cron(0 2 * * ? *)"), targets: [target("report-fn", fnArn("shop-nightly-report"), { input: '{"trigger":"schedule"}' })], invocations: 141, last_triggered: ago(9 * HOUR) }, 140 * DAY),
    rule("shop-inventory-poll", DEFAULT_BUS, { description: "Poll the warehouse for stock changes", schedule_expression: "rate(15 minutes)", next_run: nextRun("rate(15 minutes)"), targets: [target("sync-fn", fnArn("shop-inventory-sync"))], invocations: 9220, failed_invocations: 4, last_error: "Lambda function shop-inventory-sync timed out after 120s", last_triggered: ago(6 * MIN) }, 96 * DAY),
    rule("shop-order-created", DEFAULT_BUS, {
      description: "Start the fulfillment workflow for every new order",
      event_pattern: { source: ["shop.orders"], "detail-type": ["Order Created"] },
      targets: [
        target("start-fulfillment", sm, { input_path: "$.detail", retry_policy: { maximum_retry_attempts: 3, maximum_event_age_in_seconds: 3600 } }),
        target("audit-queue", orders, { input_transformer: { input_paths_map: { id: "$.detail.order_id", total: "$.detail.total" }, input_template: '{"order":"<id>","total":<total>,"source":"events"}' }, dead_letter_arn: dlq }),
      ],
      invocations: 4213,
      failed_invocations: 2,
      last_triggered: ago(11 * MIN),
    }, 88 * DAY),
    rule("shop-order-failed-alert", DEFAULT_BUS, {
      description: "Notify on-call when an order fails",
      event_pattern: { source: ["shop.orders"], "detail-type": ["Order Failed"], detail: { reason: [{ "anything-but": "customer_cancelled" }] } },
      targets: [target("alerts", alerts)],
      invocations: 87,
      last_triggered: ago(5 * HOUR),
    }, 88 * DAY),
    rule("shop-image-uploaded", DEFAULT_BUS, {
      description: "Resize product photos uploaded to S3",
      event_pattern: { source: ["aws.s3"], "detail-type": ["Object Created"], detail: { bucket: { name: [NAMES.bucketUploads] }, object: { key: [{ prefix: "products/" }] } } },
      targets: [target("resizer", fnArn("shop-image-resizer"))],
      invocations: 1532,
      last_triggered: ago(2 * HOUR),
    }, 61 * DAY),
    rule("shop-instance-stopped", DEFAULT_BUS, {
      description: "Alert when a production instance stops (disabled during maintenance)",
      state: "DISABLED",
      event_pattern: { source: ["aws.ec2"], "detail-type": ["EC2 Instance State-change Notification"], detail: { state: ["stopped", "terminated"] } },
      targets: [target("alerts", alerts)],
      invocations: 12,
      last_triggered: ago(23 * DAY),
    }, 120 * DAY),
    rule("shop-payment-settled", "shop-events", {
      description: "Payment provider notifications",
      event_pattern: { source: ["shop.payments"], "detail-type": ["Payment Settled", "Payment Refunded"] },
      targets: [target("payments-fn", fnArn(NAMES.paymentsFn))],
      invocations: 3390,
      last_triggered: ago(26 * MIN),
    }, 50 * DAY),
    rule("shop-stock-low", "shop-events", {
      description: "Low stock reports from the warehouse",
      event_pattern: { source: ["shop.inventory"], detail: { quantity: [{ numeric: ["<", 10] }] } },
      targets: [target("alerts", alerts)],
      invocations: 41,
      last_triggered: ago(2 * DAY),
    }, 30 * DAY),
  ]
  const buses: EventBus[] = [
    { name: DEFAULT_BUS, arn: busArn(DEFAULT_BUS), description: "Default event bus for the account" },
    { name: "shop-events", arn: busArn("shop-events"), description: "Application events published by the shop services", created_at: ago(90 * DAY) },
    { name: "partner-integrations", arn: busArn("partner-integrations"), description: "Events from external partners", dead_letter_arn: dlq, created_at: ago(35 * DAY) },
  ]
  const sched = (group: string, name: string, o: Partial<Schedule> & { Target: Schedule["Target"] }): Schedule => ({
    Name: name,
    GroupName: group,
    Arn: schedArn(group, name),
    ScheduleExpression: "rate(1 hour)",
    State: "ENABLED",
    FlexibleTimeWindow: { Mode: "OFF" },
    ...o,
  })
  const tgt = (fn: string, input?: unknown): Schedule["Target"] => ({ Arn: fnArn(fn), RoleArn: SCHED_ROLE, ...(input ? { Input: JSON.stringify(input) } : {}) })
  const schedules: Schedule[] = [
    sched("default", "shop-cart-reminders", { Description: "Email customers who left items in their cart", ScheduleExpression: "rate(1 hour)", Target: tgt("shop-notifier", { template: "cart-reminder" }) }),
    sched("default", "shop-weekly-digest", { Description: "Weekly newsletter", ScheduleExpression: "cron(0 9 ? * MON *)", ScheduleExpressionTimezone: "Europe/Berlin", Target: tgt("shop-notifier", { template: "weekly-digest" }) }),
    sched("shop-maintenance", "db-snapshot-cleanup", { Description: "Delete manual snapshots older than 35 days", ScheduleExpression: "cron(0 4 ? * SUN *)", FlexibleTimeWindow: { Mode: "FLEXIBLE", MaximumWindowInMinutes: 30 }, Target: tgt("shop-inventory-sync", { task: "cleanup" }) }),
    sched("shop-maintenance", "autumn-promo-launch", { Description: "Turn on the autumn promotion banner", ScheduleExpression: "at(2026-10-15T09:00:00)", ScheduleExpressionTimezone: "America/New_York", ActionAfterCompletion: "DELETE", Target: tgt("shop-api-handler", { promo: "autumn-2026" }) }),
    sched("shop-maintenance", "stock-full-resync", { Description: "Full stock resync (paused after the warehouse migration)", ScheduleExpression: "rate(6 hours)", State: "DISABLED", Target: tgt("shop-inventory-sync", { mode: "full" }) }),
  ]
  const groups: ScheduleGroup[] = [
    { Name: "default", Arn: groupArn("default"), State: "ACTIVE" },
    { Name: "shop-maintenance", Arn: groupArn("shop-maintenance"), State: "ACTIVE" },
  ]
  return { buses, rules, groups, schedules }
}

// ---- helpers ----

const busOf = (q: Record<string, string>) => q.event_bus || DEFAULT_BUS

function needBus(name: string) {
  if (!S().buses.some((b) => b.name === name)) throw err(404, "ResourceNotFound", `Event bus ${name} does not exist.`)
}
function needRule(bus: string, name: string): EventRule {
  needBus(bus)
  const r = S().rules.find((x) => x.event_bus === bus && x.name === name)
  if (!r) throw err(404, "ResourceNotFound", `Rule ${name} does not exist on EventBus ${bus}.`)
  return r
}

function targetExists(a: string): boolean {
  const st = getState()
  const name = a.split(/[:/]/).pop() ?? ""
  if (a.includes(":lambda:")) return !!st.lambda?.functions?.some((f: { name: string }) => f.name === name)
  if (a.includes(":states:")) return !!st.sfn?.machines?.some((m: { name: string }) => m.name === name)
  return true
}

function checkTarget(t: RuleTarget) {
  if (!/^[.\-_A-Za-z0-9]{1,64}$/.test(t.id)) throw badRequest("target IDs are 1-64 letters, digits, . - or _")
  if (!/^arn:aws:(lambda|sqs|sns|states):/.test(t.arn)) throw badRequest(`target ${t.arn} is not a Lambda function, SQS queue, SNS topic or state machine ARN`)
  const n = [t.input, t.input_path, t.input_transformer].filter(Boolean).length
  if (n > 1) throw badRequest("Input, InputPath and InputTransformer are mutually exclusive")
  if (t.input) {
    try {
      JSON.parse(t.input)
    } catch {
      throw badRequest("target input must be JSON")
    }
  }
  if (t.input_transformer && !t.input_transformer.input_template) throw badRequest("InputTransformer.InputTemplate is required")
  if (!targetExists(t.arn)) throw badRequest(`target ${t.arn} does not exist (use a Lambda function, SQS queue, SNS topic or state machine ARN)`)
}

function fire(rule: EventRule) {
  rule.invocations += 1
  rule.last_triggered = nowIso()
  for (const t of rule.targets ?? []) {
    if (t.arn.includes(":lambda:")) {
      const inv = getState().lambda?.invoked as Record<string, string> | undefined
      if (inv) inv[t.arn.split(":").pop() ?? ""] = nowIso()
    }
  }
}

function routes(r: Router) {
  // ---- buses
  r.get("/api/v1/events/buses", () => [...S().buses].sort((a, b) => a.name.localeCompare(b.name)))
  r.post("/api/v1/events/buses", ({ body: b }) => {
    b = b ?? {}
    const name = String(b.name ?? "")
    if (!/^[\w.\-/]{1,256}$/.test(name) || name === DEFAULT_BUS) throw badRequest("event bus names are 1-256 letters, digits, . - _ or /")
    if (S().buses.some((x) => x.name === name)) throw err(409, "ResourceAlreadyExists", `Event bus ${name} already exists.`)
    const bus: EventBus = { name, arn: busArn(name), description: b.description || undefined, dead_letter_arn: b.dead_letter_arn || undefined, created_at: nowIso() }
    S().buses.push(bus)
    return bus
  })
  r.del("/api/v1/events/buses/:name", ({ params }) => {
    if (params.name === DEFAULT_BUS) throw badRequest("The default event bus cannot be deleted")
    needBus(params.name)
    S().buses = S().buses.filter((b) => b.name !== params.name)
    S().rules = S().rules.filter((x) => x.event_bus !== params.name)
  })

  // ---- rules
  r.get("/api/v1/events/rules", ({ query }) => {
    const bus = busOf(query)
    needBus(bus)
    return S()
      .rules.filter((x) => x.event_bus === bus)
      .sort((a, b) => a.name.localeCompare(b.name))
  })
  r.get("/api/v1/events/rules/:name", ({ params, query }) => needRule(busOf(query), params.name))
  r.put("/api/v1/events/rules/:name", ({ params, query, body: b }) => {
    const bus = busOf(query)
    needBus(bus)
    b = b ?? {}
    const name = params.name
    if (!/^[\w.-]{1,64}$/.test(name)) throw badRequest("rule names are 1-64 letters, digits, dots, hyphens or underscores")
    const hasSched = !!b.schedule_expression
    const hasPattern = !!b.event_pattern && Object.keys(b.event_pattern).length > 0
    if (!hasSched && !hasPattern) throw badRequest("a rule needs a schedule expression or an event pattern")
    if (hasSched && hasPattern) throw badRequest("HomeCloud rules have either a schedule expression or an event pattern, not both")
    if (b.state && b.state !== "ENABLED" && b.state !== "DISABLED") throw badRequest("State must be ENABLED or DISABLED")
    if (hasSched) {
      if (bus !== DEFAULT_BUS) throw badRequest("ScheduleExpression is supported only on the default event bus.")
      if (!validSchedule(b.schedule_expression)) throw badRequest("Parameter ScheduleExpression is not valid: use rate(<n> minutes|hours|days) or cron(<6 fields>)")
    } else {
      const e = validatePattern(b.event_pattern)
      if (e) throw err(400, "InvalidEventPattern", e)
    }
    const targets: RuleTarget[] = (b.targets ?? []).map((t: RuleTarget, i: number) => ({ ...t, id: t.id || `target-${i + 1}` }))
    if (targets.length > 5) throw badRequest("a rule has at most 5 targets")
    targets.forEach(checkTarget)
    const old = S().rules.find((x) => x.event_bus === bus && x.name === name)
    const rule: EventRule = {
      name,
      arn: ruleArn(bus, name),
      description: b.description ?? "",
      event_bus: bus,
      schedule_expression: hasSched ? b.schedule_expression : undefined,
      event_pattern: hasPattern ? b.event_pattern : undefined,
      state: b.state ?? "ENABLED",
      targets,
      last_triggered: old?.last_triggered,
      next_run: hasSched ? nextRun(b.schedule_expression) : undefined,
      invocations: old?.invocations ?? 0,
      failed_invocations: old?.failed_invocations ?? 0,
      last_error: old?.last_error,
      created_at: old?.created_at ?? nowIso(),
    }
    S().rules = [...S().rules.filter((x) => x !== old), rule]
    return rule
  })
  r.del("/api/v1/events/rules/:name", ({ params, query }) => {
    const rule = needRule(busOf(query), params.name)
    S().rules = S().rules.filter((x) => x !== rule)
  })
  r.post("/api/v1/events/rules/:name/enable", ({ params, query }) => {
    const rule = needRule(busOf(query), params.name)
    rule.state = "ENABLED"
    if (rule.schedule_expression) rule.next_run = nextRun(rule.schedule_expression)
    return rule
  })
  r.post("/api/v1/events/rules/:name/disable", ({ params, query }) => {
    const rule = needRule(busOf(query), params.name)
    rule.state = "DISABLED"
    return rule
  })
  r.post("/api/v1/events/rules/:name/run", ({ params, query }) => {
    const rule = needRule(busOf(query), params.name)
    fire(rule)
    return rule
  })

  // ---- events
  r.post("/api/v1/events/events", ({ body: b }) => {
    const entries: Json[] = b?.entries ?? []
    if (!entries.length) throw badRequest("entries is required")
    if (entries.length > 10) throw badRequest("PutEvents accepts at most 10 entries")
    let failed = 0
    const out = entries.map((e) => {
      if (!e.source || !e.detail_type) {
        failed++
        return { event_id: "", matched_rules: 0 }
      }
      const bus = e.event_bus || DEFAULT_BUS
      const envelope = { version: "0", id: uuid(), "detail-type": e.detail_type, source: e.source, account: "123456789012", time: nowIso(), region: "us-east-1", resources: e.resources ?? [], detail: e.detail ?? {} }
      let matched = 0
      for (const rule of S().rules) {
        if (rule.event_bus !== bus || rule.state !== "ENABLED" || !rule.event_pattern) continue
        if (matchPattern(rule.event_pattern, envelope)) {
          matched++
          fire(rule)
        }
      }
      return { event_id: envelope.id, matched_rules: matched }
    })
    return { entries: out, failed_entry_count: failed }
  })
  r.post("/api/v1/events/test-pattern", ({ body: b }) => {
    let pattern = b?.pattern
    let event = b?.event
    try {
      if (typeof pattern === "string") pattern = JSON.parse(pattern)
      if (typeof event === "string") event = JSON.parse(event)
    } catch {
      throw err(400, "InvalidEventPattern", "pattern and event must be valid JSON")
    }
    const e = validatePattern(pattern)
    if (e) throw err(400, "InvalidEventPattern", e)
    if (!event || typeof event !== "object") throw badRequest("event must be a JSON object")
    return { result: matchPattern(pattern, event) }
  })

  // ---- scheduler
  const findGroup = (name: string) => {
    const g = S().groups.find((x) => x.Name === name)
    if (!g) throw err(404, "ResourceNotFoundException", `Schedule group ${name} does not exist.`)
    return g
  }
  r.get("/api/v1/scheduler/schedule-groups", () => [...S().groups].sort((a, b) => a.Name.localeCompare(b.Name)))
  r.post("/api/v1/scheduler/schedule-groups", ({ body: b }) => {
    const name = String(b?.name ?? "")
    if (!/^[0-9a-zA-Z-_.]{1,64}$/.test(name)) throw badRequest("schedule group names are 1-64 letters, digits, . - or _")
    if (S().groups.some((g) => g.Name === name)) throw conflict(`Schedule group ${name} already exists.`)
    const g: ScheduleGroup = { Name: name, Arn: groupArn(name), State: "ACTIVE" }
    S().groups.push(g)
    return g
  })
  r.del("/api/v1/scheduler/schedule-groups/:group", ({ params }) => {
    if (params.group === "default") throw badRequest("The default schedule group cannot be deleted")
    findGroup(params.group)
    S().groups = S().groups.filter((g) => g.Name !== params.group)
    S().schedules = S().schedules.filter((s) => s.GroupName !== params.group)
  })
  r.get("/api/v1/scheduler/schedules", ({ query }) =>
    S()
      .schedules.filter((s) => !query.group || s.GroupName === query.group)
      .sort((a, b) => a.GroupName.localeCompare(b.GroupName) || a.Name.localeCompare(b.Name)),
  )
  const findSchedule = (group: string, name: string) => {
    findGroup(group)
    const s = S().schedules.find((x) => x.GroupName === group && x.Name === name)
    if (!s) throw err(404, "ResourceNotFoundException", `Schedule ${name} does not exist.`)
    return s
  }
  r.get("/api/v1/scheduler/schedule-groups/:group/schedules/:name", ({ params }) => findSchedule(params.group, params.name))
  r.put("/api/v1/scheduler/schedule-groups/:group/schedules/:name", ({ params, body: b }) => {
    findGroup(params.group)
    b = b ?? {}
    if (!/^[0-9a-zA-Z-_.]{1,64}$/.test(params.name)) throw badRequest("schedule names are 1-64 letters, digits, . - or _")
    const expr = String(b.ScheduleExpression ?? "")
    if (!/^at\(\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\)$/.test(expr) && !validSchedule(expr)) throw badRequest("ScheduleExpression must be at(yyyy-mm-ddThh:mm:ss), rate(<n> <unit>) or cron(<6 fields>)")
    if (!b.Target?.Arn || !b.Target?.RoleArn) throw badRequest("Target.Arn and Target.RoleArn are required")
    if (b.Target.Input) {
      try {
        JSON.parse(b.Target.Input)
      } catch {
        throw badRequest("Target.Input must be JSON")
      }
    }
    const old = S().schedules.find((x) => x.GroupName === params.group && x.Name === params.name)
    const s: Schedule = {
      Name: params.name,
      GroupName: params.group,
      Arn: schedArn(params.group, params.name),
      Description: b.Description ?? old?.Description,
      ScheduleExpression: expr,
      ScheduleExpressionTimezone: b.ScheduleExpressionTimezone,
      StartDate: b.StartDate,
      EndDate: b.EndDate,
      State: b.State === "DISABLED" ? "DISABLED" : "ENABLED",
      FlexibleTimeWindow: b.FlexibleTimeWindow ?? { Mode: "OFF" },
      Target: b.Target,
      ActionAfterCompletion: b.ActionAfterCompletion ?? "NONE",
    }
    S().schedules = [...S().schedules.filter((x) => x !== old), s]
    return s
  })
  r.del("/api/v1/scheduler/schedule-groups/:group/schedules/:name", ({ params }) => {
    const s = findSchedule(params.group, params.name)
    S().schedules = S().schedules.filter((x) => x !== s)
  })
}

const service: DemoService = { name: "events", seed, routes }

export default service

