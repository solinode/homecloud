// In-browser mock backend engine for the static demo build.
//
// Services register routes on a Router (pattern like "/api/v1/ec2/instances/:id/start")
// and keep their data under state[<service>]. State is plain JSON so it can be
// persisted in sessionStorage; "Reset demo" clears it and re-seeds.

export class DemoError extends Error {
  readonly status: number
  readonly code: string
  constructor(status: number, code: string, message: string) {
    super(message)
    this.name = "DemoError"
    this.status = status
    this.code = code
  }
}

/** err builds an API-style error (`{"error":{"code","message"}}` equivalent). */
export const err = (status: number, code: string, message: string) => new DemoError(status, code, message)
export const notFound = (what: string, id?: string) => err(404, "NotFound", `${what}${id ? ` ${id}` : ""} not found`)
export const badRequest = (msg: string) => err(400, "InvalidRequest", msg)
export const conflict = (msg: string) => err(409, "Conflict", msg)
/** unavailable is thrown for features that cannot run without a real server. */
export const unavailable = (what = "This feature") =>
  err(501, "NotAvailableInDemo", `${what} is not available in the demo. Install HomeCloud to use it on your own machine.`)

export interface Req {
  method: string
  path: string
  params: Record<string, string>
  query: Record<string, string>
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  body: any
  /** the raw request body (Blob/string) when the body was not JSON */
  raw?: unknown
  headers: Record<string, string>
}

/** Handlers return the JSON response body; undefined becomes {"ok":true}. */
export type Handler = (req: Req) => unknown | Promise<unknown>

interface Route {
  method: string
  segs: string[]
  score: number
  handler: Handler
  order: number
}

export class Router {
  private routes: Route[] = []

  add(method: string, pattern: string, handler: Handler) {
    const segs = pattern.split("/").filter(Boolean)
    const score = segs.filter((s) => !s.startsWith(":") && !s.startsWith("*")).length
    this.routes.push({ method, segs, score, handler, order: this.routes.length })
  }
  get(p: string, h: Handler) {
    this.add("GET", p, h)
  }
  post(p: string, h: Handler) {
    this.add("POST", p, h)
  }
  put(p: string, h: Handler) {
    this.add("PUT", p, h)
  }
  patch(p: string, h: Handler) {
    this.add("PATCH", p, h)
  }
  del(p: string, h: Handler) {
    this.add("DELETE", p, h)
  }

  match(method: string, path: string): { route: Route; params: Record<string, string> } | null {
    const parts = path.split("/").filter(Boolean)
    let best: { route: Route; params: Record<string, string> } | null = null
    for (const r of this.routes) {
      if (r.method !== method) continue
      const params: Record<string, string> = {}
      let ok = true
      for (let i = 0; i < r.segs.length && ok; i++) {
        const s = r.segs[i]
        if (s.startsWith("*")) {
          params[s.slice(1)] = parts.slice(i).map(safeDecode).join("/")
          if (parts.length < i + 1) ok = false
          break
        }
        if (i >= parts.length) ok = false
        else if (s.startsWith(":")) params[s.slice(1)] = safeDecode(parts[i])
        else if (s !== parts[i]) ok = false
      }
      const wildcard = r.segs.length > 0 && r.segs[r.segs.length - 1].startsWith("*")
      if (ok && !wildcard && parts.length !== r.segs.length) ok = false
      if (!ok) continue
      if (!best || r.score > best.route.score) best = { route: r, params }
    }
    return best
  }
}

function safeDecode(s: string): string {
  try {
    return decodeURIComponent(s)
  } catch {
    return s
  }
}

// ---- state ----

// eslint-disable-next-line @typescript-eslint/no-explicit-any
export type State = Record<string, any>

export interface DemoService {
  name: string
  /** initial state for state[name] (JSON-serializable, timestamps relative to now) */
  seed: () => unknown
  /** register the routes; read/write state via getState()[name] */
  routes: (r: Router) => void
  /** called after state is (re)loaded, e.g. to resume pending transitions */
  onLoad?: (s: State) => void
}

const STORAGE_KEY = "homecloud.demo.state.v1"

let state: State = {}
let services: DemoService[] = []

/** getState returns the live state object; mutate it and call save() (done automatically per request). */
export function getState(): State {
  return state
}

export function seedState(): State {
  const s: State = {}
  for (const svc of services) s[svc.name] = svc.seed()
  return s
}

export function load(svcs: DemoService[]) {
  services = svcs
  let restored: State | null = null
  try {
    const raw = window.sessionStorage.getItem(STORAGE_KEY)
    if (raw) restored = JSON.parse(raw) as State
  } catch {
    restored = null
  }
  state = restored ?? seedState()
  // Any service added since the snapshot (or a corrupt snapshot) is re-seeded.
  for (const svc of services) if (state[svc.name] === undefined) state[svc.name] = svc.seed()
  for (const svc of services) svc.onLoad?.(state)
}

let saveTimer: ReturnType<typeof setTimeout> | undefined
export function save() {
  if (saveTimer) clearTimeout(saveTimer)
  saveTimer = setTimeout(() => {
    try {
      window.sessionStorage.setItem(STORAGE_KEY, JSON.stringify(state))
    } catch {
      // storage full or unavailable: the demo keeps working in memory
    }
  }, 50)
}

/** resetDemo drops every change made in this session and re-seeds. */
export function resetDemo() {
  try {
    window.sessionStorage.removeItem(STORAGE_KEY)
  } catch {
    // ignore
  }
  state = seedState()
  for (const svc of services) svc.onLoad?.(state)
}

/**
 * later runs fn after ms and persists the result. Use it for state transitions
 * (pending -> running). Pair it with an onLoad hook that settles anything still
 * transitional after a page reload.
 */
export function later(ms: number, fn: () => void) {
  setTimeout(() => {
    try {
      fn()
    } catch {
      // the object may have been deleted meanwhile
    }
    save()
    notifyChanged()
  }, ms)
}

// Lets the UI re-fetch after a delayed transition (the console polls anyway).
const changeListeners = new Set<() => void>()
export function onDemoChange(l: () => void): () => void {
  changeListeners.add(l)
  return () => changeListeners.delete(l)
}
export function notifyChanged() {
  changeListeners.forEach((l) => l())
}

// ---- dispatch ----

const router = new Router()
let registered = false

export function register(svcs: DemoService[]) {
  if (registered) return
  registered = true
  for (const svc of svcs) svc.routes(router)
}

export interface DemoResult {
  status: number
  data: unknown
}

export async function dispatch(
  method: string,
  fullPath: string,
  query: Record<string, string>,
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  body: any,
  headers: Record<string, string> = {},
  raw?: unknown,
): Promise<DemoResult> {
  // a tiny latency so spinners and skeletons flash like a real (fast) server
  await new Promise((r) => setTimeout(r, 25 + Math.random() * 60))
  const path = fullPath.split("?")[0]
  const m = router.match(method, path)
  if (!m) throw err(404, "NotFound", `No such API route in the demo: ${method} ${path}`)
  const out = await m.route.handler({ method, path, params: m.params, query, body, raw, headers })
  if (method !== "GET") {
    save()
    notifyChanged()
  }
  const data = out === undefined ? { ok: true } : out
  return { status: 200, data: data === null ? null : JSON.parse(JSON.stringify(data)) }
}
