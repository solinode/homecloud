import { badRequest, conflict, err, getState, type DemoService, type Router } from "../engine"
import { DAY, HOUR, MIN, ago, arn, clone, nowIso, uuid } from "../util"
import { NAMES } from "../ids"
import type { AslMachine, AslState, ExecutionCounts, ExecutionStatus, SfnExecution, SfnHistoryEvent, StateMachine, StateMachineSummary } from "@/lib/types"

// ---- state ----

/** The pre-computed outcome of a running execution; events are revealed as their timestamps pass. */
interface Plan {
  history: SfnHistoryEvent[]
  status: ExecutionStatus
  output?: unknown
  error?: string
  cause?: string
  stopAt: string
}
type StoredExecution = SfnExecution & { plan?: Plan }

interface SfnState {
  machines: StateMachine[]
  executions: StoredExecution[]
}
const S = (): SfnState => getState().sfn as SfnState

const machineArn = (name: string) => arn("states", `stateMachine:${name}`)
const execArn = (machine: string, name: string) => arn("states", `execution:${machine}:${name}`)
const lambdaArn = (fn: string) => arn("lambda", `function:${fn}`)
const SFN_ROLE = arn("iam", "role/shop-sfn-role", { region: null })
const NAME_RE = /^[A-Za-z0-9_-]{1,80}$/

// ---- definitions ----

const task = (fn: string, extra: Partial<AslState> = {}): AslState => ({ Type: "Task", Resource: lambdaArn(fn), ...extra })

const ORDER_DEF: AslMachine = {
  Comment: "Order fulfillment: validate, charge the customer, reserve stock and send confirmation",
  StartAt: "ValidateOrder",
  TimeoutSeconds: 900,
  States: {
    ValidateOrder: task("shop-order-processor", {
      ResultPath: "$.validation",
      Retry: [{ ErrorEquals: ["Lambda.ServiceException", "Lambda.TooManyRequestsException"], IntervalSeconds: 2, MaxAttempts: 3, BackoffRate: 2 }],
      Catch: [{ ErrorEquals: ["States.ALL"], Next: "OrderFailed", ResultPath: "$.error" }],
      Next: "ChargePayment",
    }),
    ChargePayment: task(NAMES.paymentsFn, {
      ResultPath: "$.payment",
      Catch: [{ ErrorEquals: ["PaymentDeclined", "States.TaskFailed"], Next: "OrderFailed", ResultPath: "$.error" }],
      Next: "PaymentSucceeded",
    }),
    PaymentSucceeded: { Type: "Choice", Choices: [{ Variable: "$.payment.status", StringEquals: "succeeded", Next: "Fulfill" }], Default: "OrderFailed" },
    Fulfill: {
      Type: "Parallel",
      ResultPath: "$.fulfillment",
      Next: "WaitForShipment",
      Branches: [
        { StartAt: "ReserveInventory", States: { ReserveInventory: task("shop-inventory-sync", { End: true }) } },
        { StartAt: "SendConfirmation", States: { SendConfirmation: task("shop-notifier", { End: true }) } },
      ],
    },
    WaitForShipment: { Type: "Wait", Seconds: 5, Next: "OrderComplete" },
    OrderComplete: { Type: "Succeed" },
    OrderFailed: { Type: "Fail", Error: "OrderFailed", Cause: "The order could not be completed" },
  },
}

const CLEANUP_DEF: AslMachine = {
  Comment: "Nightly report and cleanup of expired carts",
  StartAt: "BuildReport",
  States: {
    BuildReport: task("shop-nightly-report", { ResultPath: "$.report", Next: "FindExpiredCarts" }),
    FindExpiredCarts: { Type: "Pass", Result: { carts: ["cart_881", "cart_902", "cart_917"] }, ResultPath: "$.expired", Next: "DeleteCarts" },
    DeleteCarts: {
      Type: "Map",
      ItemsPath: "$.expired.carts",
      MaxConcurrency: 2,
      ItemProcessor: { StartAt: "DeleteCart", States: { DeleteCart: { Type: "Pass", End: true } } },
      ResultPath: "$.deleted",
      Next: "Done",
    },
    Done: { Type: "Succeed" },
  },
}

const ROUTER_DEF: AslMachine = {
  Comment: "Routes inbound webhooks to the right handler (express)",
  StartAt: "RouteEvent",
  States: {
    RouteEvent: {
      Type: "Choice",
      Choices: [
        { Variable: "$.source", StringEquals: "payments", Next: "HandlePayment" },
        { Variable: "$.source", StringEquals: "warehouse", Next: "HandleInventory" },
      ],
      Default: "Ignore",
    },
    HandlePayment: task(NAMES.paymentsFn, { End: true }),
    HandleInventory: task("shop-inventory-sync", { End: true }),
    Ignore: { Type: "Succeed" },
  },
}

// ---- JSONPath helpers ----

function getPath(data: unknown, path?: string): unknown {
  if (!path || path === "$") return data
  let cur: unknown = data
  for (const k of path.replace(/^\$\.?/, "").split(".").filter(Boolean)) {
    if (cur && typeof cur === "object" && k in (cur as Record<string, unknown>)) cur = (cur as Record<string, unknown>)[k]
    else return undefined
  }
  return cur
}

function applyResult(data: unknown, result: unknown, resultPath?: string | null): unknown {
  if (resultPath === null) return data
  if (resultPath === undefined || resultPath === "$") return result
  const out: Record<string, unknown> = data && typeof data === "object" && !Array.isArray(data) ? clone(data as Record<string, unknown>) : {}
  const keys = resultPath.replace(/^\$\.?/, "").split(".").filter(Boolean)
  let cur = out
  keys.slice(0, -1).forEach((k) => {
    if (!cur[k] || typeof cur[k] !== "object") cur[k] = {}
    cur = cur[k] as Record<string, unknown>
  })
  cur[keys[keys.length - 1]] = result
  return out
}

function evalChoice(rule: Record<string, unknown>, data: unknown): boolean {
  if (Array.isArray(rule.And)) return (rule.And as Record<string, unknown>[]).every((r) => evalChoice(r, data))
  if (Array.isArray(rule.Or)) return (rule.Or as Record<string, unknown>[]).some((r) => evalChoice(r, data))
  if (rule.Not) return !evalChoice(rule.Not as Record<string, unknown>, data)
  const v = getPath(data, rule.Variable as string) as unknown
  if ("IsPresent" in rule) return (v !== undefined) === rule.IsPresent
  for (const [k, want] of Object.entries(rule)) {
    if (k === "Variable" || k === "Next") continue
    if (v === undefined) return false
    const n = v as number
    const w = want as number
    if (k.endsWith("Equals") && !k.endsWith("GreaterThanEquals") && !k.endsWith("LessThanEquals")) return v === want
    if (k.endsWith("GreaterThan")) return n > w
    if (k.endsWith("GreaterThanEquals")) return n >= w
    if (k.endsWith("LessThan")) return n < w
    if (k.endsWith("LessThanEquals")) return n <= w
  }
  return false
}

// ---- canned task results ----

function taskOutput(fn: string, input: unknown): unknown {
  const inp = (input && typeof input === "object" ? input : {}) as Record<string, unknown>
  switch (fn) {
    case "shop-order-processor":
      return { order_id: inp.order_id ?? "ord_10432", valid: true, item_count: Array.isArray(inp.items) ? inp.items.length : 2 }
    case NAMES.paymentsFn:
      return { status: "succeeded", payment_id: `pay_${Math.random().toString(16).slice(2, 10)}`, amount: inp.total ?? 4999, currency: inp.currency ?? "usd" }
    case "shop-inventory-sync":
      return { reserved: true, warehouse: "wh-east-1" }
    case "shop-notifier":
      return { sent: true, channel: "email" }
    case "shop-nightly-report":
      return { report: `reports/daily/${new Date(Date.now() - DAY).toISOString().slice(0, 10)}.csv`, rows: 318 }
    default:
      return { ok: true }
  }
}

const fnOf = (resource: string) => {
  const m = /:function:([^:]+)/.exec(resource)
  return m ? m[1] : (resource.split(":").pop() ?? resource)
}
const rtype = (resource: string) => (resource.includes(":lambda:") || resource.includes(":function:") ? "lambda" : (/:::([a-z]+):/.exec(resource)?.[1] ?? "resource"))

// ---- simulation ----

interface SimOpts {
  waitMs?: number
  failAt?: string
  /** stop generating events after the point where the wall clock would be (for kinds other than ok) */
  cutAfter?: number
}
interface Sim {
  history: SfnHistoryEvent[]
  status: ExecutionStatus
  output?: unknown
  error?: string
  cause?: string
  endMs: number
}

function simulate(def: AslMachine, input: unknown, startMs: number, opts: SimOpts = {}): Sim {
  const history: SfnHistoryEvent[] = []
  let t = startMs
  let id = 0
  const tick = (min = 8, max = 60) => {
    t += min + Math.floor(Math.random() * (max - min))
  }
  const ev = (type: string, state?: string, details?: unknown) => {
    tick()
    history.push({ id: ++id, timestamp: new Date(t).toISOString(), type, ...(state ? { state } : {}), ...(details !== undefined ? { details } : {}) })
  }
  ev("ExecutionStarted", undefined, { input, roleArn: SFN_ROLE })

  type Failure = { error: string; cause: string; state: string }
  const run = (states: Record<string, AslState>, startAt: string, data0: unknown): { data: unknown; failed?: Failure } => {
    let data = data0
    let cur = startAt
    for (let guard = 0; guard < 60; guard++) {
      const st = states[cur]
      if (!st) return { data, failed: { error: "States.Runtime", cause: `State ${cur} does not exist`, state: cur } }
      const type = st.Type ?? "Pass"
      ev(`${type}StateEntered`, cur, { name: cur, input: data })
      if (type === "Succeed") {
        ev("SucceedStateExited", cur, { output: data })
        return { data }
      }
      if (type === "Fail") return { data, failed: { error: String(st.Error ?? "States.Fail"), cause: String(st.Cause ?? ""), state: cur } }

      // failure handling shared by every state type: Catch -> next state, otherwise the execution fails
      const handleFailure = (error: string, cause: string): string | Failure => {
        const c = (st.Catch ?? []).find((x) => (x.ErrorEquals ?? []).some((e) => e === error || e === "States.ALL" || (e === "States.TaskFailed" && !error.startsWith("States."))))
        if (!c || !c.Next) {
          ev(`${type}StateFailed`, cur, { error, cause })
          return { error, cause, state: cur }
        }
        data = applyResult(data, { Error: error, Cause: cause }, c.ResultPath)
        ev(`${type}StateExited`, cur, { caught: true, error, cause, output: data, next: c.Next })
        return c.Next
      }

      let failure: { error: string; cause: string } | null = null
      let result: unknown = data
      if (type === "Task") {
        const resource = String(st.Resource ?? "")
        const fn = fnOf(resource)
        const rt = rtype(resource)
        ev("TaskScheduled", cur, { resourceType: rt, resourceName: resource, parameters: data })
        tick(60, 320)
        if (opts.failAt === cur) {
          failure = { error: "PaymentDeclined", cause: "The card was declined by the issuer (simulated failure)" }
          ev("TaskFailed", cur, { resourceType: rt, resourceName: resource, ...failure })
        } else {
          result = taskOutput(fn, data)
          ev("TaskSucceeded", cur, { resourceType: rt, resourceName: resource, output: result })
        }
      } else if (type === "Wait") {
        const ms = opts.waitMs ?? Number(st.Seconds ?? 1) * 1000
        ev("WaitStateWaiting", cur, { seconds: Number(st.Seconds ?? 1) })
        t += ms
      } else if (type === "Parallel") {
        const outs: unknown[] = []
        for (const b of (st.Branches ?? []) as AslMachine[]) {
          const r = run((b.States ?? {}) as Record<string, AslState>, String(b.StartAt), data)
          if (r.failed) {
            failure = { error: r.failed.error, cause: r.failed.cause }
            break
          }
          outs.push(r.data)
        }
        result = outs
      } else if (type === "Map") {
        const items = getPath(data, st.ItemsPath as string | undefined)
        const list = Array.isArray(items) ? items : []
        ev("MapStateStarted", cur, { length: list.length })
        const proc = (st.ItemProcessor ?? st.Iterator) as AslMachine | undefined
        const outs: unknown[] = []
        for (const item of list.slice(0, 5)) {
          if (!proc) break
          const r = run((proc.States ?? {}) as Record<string, AslState>, String(proc.StartAt), item)
          if (r.failed) {
            failure = { error: r.failed.error, cause: r.failed.cause }
            break
          }
          outs.push(r.data)
        }
        result = outs
      } else if (type === "Choice") {
        const hit = (st.Choices ?? []).find((c) => evalChoice(c, data))
        const next = (hit?.Next as string | undefined) ?? st.Default
        if (!next) {
          const f = handleFailure("States.NoChoiceMatched", "No Choices matched and no Default is defined")
          if (typeof f !== "string") return { data, failed: f }
          cur = f
          continue
        }
        ev("ChoiceStateExited", cur, { output: data, next })
        cur = next
        continue
      } else if (type === "Pass") {
        if (st.Result !== undefined) result = st.Result
      }

      if (failure) {
        const f = handleFailure(failure.error, failure.cause)
        if (typeof f !== "string") return { data, failed: f }
        cur = f
        continue
      }
      if (type === "Task" || type === "Parallel" || type === "Map" || (type === "Pass" && st.Result !== undefined)) data = applyResult(data, result, st.ResultPath as string | null | undefined)
      ev(`${type}StateExited`, cur, { output: data })
      if (st.End || !st.Next) return { data }
      cur = st.Next
    }
    return { data, failed: { error: "States.Runtime", cause: "Too many state transitions", state: cur } }
  }

  const r = run((def.States ?? {}) as Record<string, AslState>, String(def.StartAt), input)
  if (r.failed) {
    ev("ExecutionFailed", undefined, { error: r.failed.error, cause: r.failed.cause })
    return { history, status: "FAILED", error: r.failed.error, cause: r.failed.cause, endMs: t }
  }
  ev("ExecutionSucceeded", undefined, { output: r.data })
  return { history, status: "SUCCEEDED", output: r.data, endMs: t }
}

// ---- seed ----

const orderInput = (n: number, total: number) => ({
  order_id: `ord_${10000 + n}`,
  customer_id: `cus_${2000 + (n % 37)}`,
  items: [{ sku: "SKU-TOTE-01", qty: 1 }, { sku: "SKU-MUG-03", qty: 1 + (n % 3) }],
  total,
  currency: "usd",
})

function makeExecution(machine: StateMachine, name: string, input: unknown, startedAgoMs: number, kind: "ok" | "fail" | "timeout" | "abort" | "running", opts: SimOpts = {}): StoredExecution {
  const startMs = Date.now() - startedAgoMs
  const failAt = kind === "fail" ? (opts.failAt ?? Object.keys(machine.definition.States ?? {}).find((k) => machine.definition.States?.[k]?.Type === "Task" && k !== "ValidateOrder")) : undefined
  const sim = simulate(machine.definition, input, startMs, { ...opts, failAt })
  const base: SfnExecution = {
    id: uuid(),
    arn: execArn(machine.name, name),
    name,
    status: sim.status,
    start_date: new Date(startMs).toISOString(),
    stop_date: new Date(sim.endMs + 30).toISOString(),
    state_machine: machine.name,
    input,
    output: sim.output,
    error: sim.error,
    cause: sim.cause,
    history: sim.history,
    role_arn: machine.role_arn,
  }
  if (kind === "ok" || kind === "fail") return base
  // cut the history at roughly 60% and end (or keep going) in another way
  const cut = Math.max(3, Math.floor(sim.history.length * 0.6))
  const kept = sim.history.slice(0, cut)
  const lastTs = new Date(kept[kept.length - 1].timestamp).getTime()
  if (kind === "running") {
    return { ...base, status: "RUNNING", stop_date: null, output: undefined, error: undefined, cause: undefined, history: kept, plan: { history: sim.history, status: sim.status, output: sim.output, error: sim.error, cause: sim.cause, stopAt: new Date(Date.now() + 40 * 60 * 1000).toISOString() } }
  }
  const type = kind === "timeout" ? "ExecutionTimedOut" : "ExecutionAborted"
  const error = kind === "timeout" ? "States.Timeout" : undefined
  const cause = kind === "timeout" ? "State machine timed out after 900 seconds" : "Stopped by user"
  kept.push({ id: kept.length + 1, timestamp: new Date(lastTs + 2000).toISOString(), type, details: { error, cause } })
  return { ...base, status: kind === "timeout" ? "TIMED_OUT" : "ABORTED", stop_date: new Date(lastTs + 2000).toISOString(), output: undefined, error, cause, history: kept }
}

function seed(): SfnState {
  const mk = (name: string, def: AslMachine, o: Partial<StateMachine>, created: number, updated: number): StateMachine => ({
    name,
    arn: machineArn(name),
    definition: def,
    status: "ACTIVE",
    type: "STANDARD",
    role_arn: SFN_ROLE,
    description: def.Comment as string | undefined,
    created_at: ago(created),
    updated_at: ago(updated),
    tags: { app: "shop" },
    ...o,
  })
  const order = mk(NAMES.stateMachine, ORDER_DEF, { tags: { app: "shop", team: "orders" } }, 88 * DAY, 12 * DAY)
  const cleanup = mk("shop-nightly-cleanup", CLEANUP_DEF, {}, 60 * DAY, 60 * DAY)
  const router = mk("shop-webhook-router", ROUTER_DEF, { type: "EXPRESS" }, 30 * DAY, 30 * DAY)
  const execs: StoredExecution[] = []
  const plan: [number, "ok" | "fail" | "timeout" | "abort" | "running", number][] = [
    [3 * MIN, "running", 0],
    [14 * MIN, "ok", 4999],
    [52 * MIN, "ok", 12800],
    [2 * HOUR + 10 * MIN, "ok", 2400],
    [5 * HOUR, "fail", 7999],
    [9 * HOUR, "ok", 1800],
    [1 * DAY + 2 * HOUR, "ok", 6400],
    [1 * DAY + 9 * HOUR, "abort", 3300],
    [2 * DAY + 3 * HOUR, "ok", 9900],
    [3 * DAY, "timeout", 5100],
    [4 * DAY + 5 * HOUR, "ok", 2200],
    [6 * DAY, "fail", 15000],
    [8 * DAY, "ok", 4200],
  ]
  plan.forEach(([ago_, kind, total], i) => {
    execs.push(makeExecution(order, `order-${10500 - i * 7}`, orderInput(500 - i * 7, total || 2999), ago_, kind, kind === "running" ? { waitMs: 40 * 60 * 1000 } : {}))
  })
  execs.push(makeExecution(cleanup, "nightly-2026-09-29", { date: "2026-09-29" }, 17 * HOUR, "ok"))
  execs.push(makeExecution(cleanup, "nightly-2026-09-28", { date: "2026-09-28" }, 41 * HOUR, "ok"))
  execs.push(makeExecution(cleanup, "nightly-2026-09-27", { date: "2026-09-27" }, 65 * HOUR, "ok"))
  execs.push(makeExecution(router, uuid(), { source: "payments", id: "evt_301" }, 20 * MIN, "ok"))
  execs.push(makeExecution(router, uuid(), { source: "warehouse", id: "evt_302" }, 33 * MIN, "ok"))
  return { machines: [order, cleanup, router], executions: execs }
}

// ---- execution lifecycle ----

/** resolve reveals a planned execution's events up to now, finalizing it once it is due. */
function resolve(x: StoredExecution): StoredExecution {
  const p = x.plan
  if (!p) return x
  const now = Date.now()
  if (now >= new Date(p.stopAt).getTime()) {
    x.history = p.history
    x.status = p.status
    x.output = p.output
    x.error = p.error
    x.cause = p.cause
    x.stop_date = new Date(Math.min(new Date(p.stopAt).getTime(), now)).toISOString()
    delete x.plan
    return x
  }
  x.history = p.history.filter((e) => new Date(e.timestamp).getTime() <= now)
  return x
}

const counts = (name: string): ExecutionCounts => {
  const c: ExecutionCounts = {}
  for (const x of S().executions) if (x.state_machine === name) c[resolve(x).status] = (c[x.status] ?? 0) + 1
  return c
}

function getMachine(name: string): StateMachine {
  const m = S().machines.find((x) => x.name === name)
  if (!m) throw err(404, "StateMachineDoesNotExist", `state machine "${name}" does not exist`)
  return m
}

const VALID_TYPES = ["Task", "Pass", "Choice", "Wait", "Succeed", "Fail", "Parallel", "Map"]

function validateDef(def: unknown): string[] {
  const errors: string[] = []
  if (!def || typeof def !== "object" || Array.isArray(def)) return ["definition must be a JSON object"]
  const check = (m: AslMachine, path: string) => {
    if (!m.StartAt) errors.push(`${path}StartAt is required`)
    const states = m.States
    if (!states || typeof states !== "object" || !Object.keys(states).length) {
      errors.push(`${path}States must contain at least one state`)
      return
    }
    if (m.StartAt && !states[m.StartAt]) errors.push(`${path}StartAt refers to a non-existent state: ${m.StartAt}`)
    for (const [n, s] of Object.entries(states)) {
      if (!s.Type || !VALID_TYPES.includes(s.Type)) errors.push(`${path}State ${n}: Type must be one of ${VALID_TYPES.join(", ")}`)
      const targets = [s.Next, s.Default, ...(s.Choices ?? []).map((c) => c.Next as string | undefined), ...(s.Catch ?? []).map((c) => c.Next)]
      for (const t of targets) if (t && !states[t]) errors.push(`${path}State ${n}: Next refers to a non-existent state: ${t}`)
      if (["Task", "Pass", "Wait", "Parallel", "Map"].includes(s.Type ?? "") && !s.Next && !s.End) errors.push(`${path}State ${n}: must have Next or End`)
      if (s.Type === "Task" && !s.Resource) errors.push(`${path}State ${n}: Task requires Resource`)
      for (const b of (s.Branches ?? []) as AslMachine[]) check(b, `${path}${n}/`)
      const inner = (s.ItemProcessor ?? s.Iterator) as AslMachine | undefined
      if (inner) check(inner, `${path}${n}/`)
    }
  }
  check(def as AslMachine, "")
  return errors
}

function parseDef(input: unknown): AslMachine {
  let def = input
  if (typeof def === "string") {
    try {
      def = JSON.parse(def)
    } catch (e) {
      throw err(400, "InvalidDefinition", `Invalid State Machine Definition: definition is not valid JSON: ${(e as Error).message}`)
    }
  }
  if (def === undefined || def === null || def === "") throw badRequest("definition is required")
  const errors = validateDef(def)
  if (errors.length) throw err(400, "InvalidDefinition", `Invalid State Machine Definition: ${errors.join("; ")}`)
  return def as AslMachine
}

// ---- routes ----

function routes(r: Router) {
  r.get("/api/v1/sfn/state-machines", (): StateMachineSummary[] =>
    [...S().machines]
      .sort((a, b) => a.name.localeCompare(b.name))
      .map((m) => ({ name: m.name, arn: m.arn, status: m.status, created_at: m.created_at, updated_at: m.updated_at, executions: counts(m.name) })),
  )
  r.post("/api/v1/sfn/state-machines", ({ body: b }) => {
    b = b ?? {}
    if (!NAME_RE.test(String(b.name ?? ""))) throw badRequest("names are 1-80 letters, digits, hyphens or underscores")
    if (S().machines.some((m) => m.name === b.name)) throw err(409, "StateMachineAlreadyExists", `state machine "${b.name}" already exists`)
    if (b.type && b.type !== "STANDARD" && b.type !== "EXPRESS") throw badRequest("type must be STANDARD or EXPRESS")
    const def = parseDef(b.definition)
    const now = nowIso()
    const m: StateMachine = {
      name: b.name,
      arn: machineArn(b.name),
      definition: def,
      status: "ACTIVE",
      type: b.type ?? "STANDARD",
      role_arn: b.role_arn || SFN_ROLE,
      description: def.Comment as string | undefined,
      created_at: now,
      updated_at: now,
      tags: b.tags ?? {},
    }
    S().machines.push(m)
    return m
  })
  r.get("/api/v1/sfn/state-machines/:name", ({ params }) => ({ state_machine: getMachine(params.name), executions: counts(params.name) }))
  r.put("/api/v1/sfn/state-machines/:name", ({ params, body: b }) => {
    const m = getMachine(params.name)
    b = b ?? {}
    if (b.definition !== undefined) {
      m.definition = parseDef(b.definition)
      m.description = m.definition.Comment as string | undefined
    }
    if (b.role_arn !== undefined && b.role_arn !== null) m.role_arn = b.role_arn
    m.updated_at = nowIso()
    return m
  })
  r.del("/api/v1/sfn/state-machines/:name", ({ params }) => {
    getMachine(params.name)
    S().machines = S().machines.filter((m) => m.name !== params.name)
    S().executions = S().executions.filter((x) => x.state_machine !== params.name)
  })
  r.post("/api/v1/sfn/state-machines/:name/executions", ({ params, body: b }) => {
    const m = getMachine(params.name)
    b = b ?? {}
    const name = b.name ? String(b.name) : uuid()
    if (!NAME_RE.test(name)) throw badRequest("execution names are 1-80 letters, digits, hyphens or underscores")
    if (S().executions.some((x) => x.state_machine === m.name && x.name === name)) throw err(409, "ExecutionAlreadyExists", `execution "${name}" already exists`)
    const input = b.input ?? {}
    const startMs = Date.now()
    const failing = input && typeof input === "object" && (input.fail === true || input.simulate_failure === true)
    const failAt = failing ? Object.keys(m.definition.States ?? {}).find((k) => m.definition.States?.[k]?.Type === "Task" && k !== "ValidateOrder") : undefined
    // waits are compressed so a simulated run finishes in a few seconds
    const sim = simulate(m.definition, input, startMs, { waitMs: 900, failAt })
    const stopAt = Math.max(sim.endMs, startMs + 3500)
    const x: StoredExecution = {
      id: uuid(),
      arn: execArn(m.name, name),
      name,
      status: "RUNNING",
      start_date: new Date(startMs).toISOString(),
      stop_date: null,
      state_machine: m.name,
      input,
      history: [],
      role_arn: m.role_arn,
      plan: { history: sim.history, status: sim.status, output: sim.output, error: sim.error, cause: sim.cause, stopAt: new Date(stopAt).toISOString() },
    }
    S().executions.push(x)
    return { id: x.id, arn: x.arn, name: x.name, start_date: x.start_date }
  })
  r.get("/api/v1/sfn/state-machines/:name/executions", ({ params, query }) => {
    getMachine(params.name)
    return S()
      .executions.filter((x) => x.state_machine === params.name)
      .map(resolve)
      .filter((x) => !query.status || x.status === query.status)
      .sort((a, b) => b.start_date.localeCompare(a.start_date))
      .map((x) => ({ id: x.id, arn: x.arn, name: x.name, status: x.status, start_date: x.start_date, stop_date: x.stop_date ?? null }))
  })
  const findExec = (id: string) => {
    const x = S().executions.find((e) => e.id === id)
    if (!x) throw err(404, "ExecutionDoesNotExist", `execution "${id}" does not exist`)
    return resolve(x)
  }
  r.get("/api/v1/sfn/executions/:id", ({ params }) => {
    const { plan, ...rest } = findExec(params.id)
    void plan
    return rest
  })
  r.post("/api/v1/sfn/executions/:id/stop", ({ params, body: b }) => {
    const x = findExec(params.id)
    if (x.status !== "RUNNING") throw conflict(`execution is ${x.status}`)
    const now = nowIso()
    delete x.plan
    x.status = "ABORTED"
    x.stop_date = now
    x.error = b?.error || undefined
    x.cause = b?.cause || "Stopped by user"
    x.history = [...(x.history ?? []), { id: (x.history?.length ?? 0) + 1, timestamp: now, type: "ExecutionAborted", details: { error: x.error, cause: x.cause } }]
    const { plan, ...rest } = x
    void plan
    return rest
  })
  r.post("/api/v1/sfn/validate", ({ body: b }) => {
    let def: unknown = b?.definition
    if (typeof def === "string") {
      try {
        def = JSON.parse(def)
      } catch (e) {
        return { valid: false, errors: [`definition is not valid JSON: ${(e as Error).message}`] }
      }
    }
    const errors = validateDef(def)
    return { valid: errors.length === 0, errors }
  })
}

const service: DemoService = { name: "sfn", seed, routes }

export default service

