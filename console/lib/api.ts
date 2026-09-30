// Typed client for the HomeCloud REST API (/api/v1).
//
// Every request carries the console session token as a Bearer token. Browser
// navigations that cannot set headers (downloads, the WebSocket terminal) use
// the ?access_token= query parameter instead, which the API accepts on GET.

export const API_BASE = (process.env.NEXT_PUBLIC_API_URL ?? "").replace(/\/+$/, "")

const SESSION_KEY = "homecloud.session"

/**
 * DEMO is true only in the static demo build (NEXT_PUBLIC_DEMO=1). Next inlines
 * the env var, so in normal builds every `if (DEMO)` branch and the dynamic
 * import of the mock backend are dead code and never reach the bundle.
 */
/** BASE_PATH is the sub-path the console is served from ("/demo" in the demo build, "" otherwise). */
export const BASE_PATH = process.env.NEXT_PUBLIC_BASE_PATH ?? ""

export const DEMO = process.env.NEXT_PUBLIC_DEMO === "1"

/** Marker href for links that need a real server (downloads); the demo shell intercepts clicks on it. */
export const DEMO_UNAVAILABLE_HREF = "#demo-unavailable"

const DEMO_SESSION: Session = {
  token: "demo-token",
  expires: "2099-01-01T00:00:00Z",
  account_id: "123456789012",
  user: { name: "demo-admin", id: "AIDADEMOADMIN000000", arn: "arn:aws:iam::123456789012:user/demo-admin", root: false, console_access: true },
}

export interface SessionUser {
  name: string
  id: string
  arn: string
  root: boolean
  console_access?: boolean
}

export interface Session {
  token: string
  expires: string
  user: SessionUser
  account_id: string
}

/** ApiError mirrors the API's `{"error":{"code","message"}}` body. */
export class ApiError extends Error {
  readonly status: number
  readonly code: string

  constructor(status: number, code: string, message: string) {
    super(message)
    this.name = "ApiError"
    this.status = status
    this.code = code
  }
}

/** errorMessage turns anything thrown by a request into a user-facing string. */
export function errorMessage(err: unknown): string {
  if (err instanceof ApiError) return err.message || err.code
  if (err instanceof Error) return err.message
  return String(err)
}

// ---- session storage ----

type Listener = (s: Session | null) => void
const listeners = new Set<Listener>()

export function getSession(): Session | null {
  if (DEMO) return DEMO_SESSION
  if (typeof window === "undefined") return null
  try {
    const raw = window.localStorage.getItem(SESSION_KEY)
    if (!raw) return null
    const s = JSON.parse(raw) as Session
    if (!s.token || (s.expires && new Date(s.expires).getTime() < Date.now())) {
      window.localStorage.removeItem(SESSION_KEY)
      return null
    }
    return s
  } catch {
    return null
  }
}

export function setSession(s: Session | null) {
  try {
    if (s) window.localStorage.setItem(SESSION_KEY, JSON.stringify(s))
    else window.localStorage.removeItem(SESSION_KEY)
  } catch {
    // storage unavailable (private mode); the session lives for this page only
  }
  listeners.forEach((l) => l(s))
}

export function onSessionChange(l: Listener): () => void {
  listeners.add(l)
  return () => listeners.delete(l)
}

export function getToken(): string | null {
  return getSession()?.token ?? null
}

let redirecting = false

/** Called on 401: drop the session and send the user to the sign-in page. */
export function handleUnauthorized() {
  setSession(null)
  if (typeof window === "undefined" || redirecting) return
  if (window.location.pathname.startsWith("/login")) return
  redirecting = true
  const next = window.location.pathname + window.location.search
  window.location.href = `/login/?next=${encodeURIComponent(next)}`
}

// ---- URLs ----

export type Query = Record<string, string | number | boolean | undefined | null>

export function buildQuery(q?: Query): string {
  if (!q) return ""
  const p = new URLSearchParams()
  for (const [k, v] of Object.entries(q)) {
    if (v === undefined || v === null || v === "") continue
    p.set(k, String(v))
  }
  const s = p.toString()
  return s ? `?${s}` : ""
}

/** apiUrl resolves an API path ("/api/v1/...") against the configured base URL. */
export function apiUrl(path: string, q?: Query): string {
  return `${API_BASE}${path}${buildQuery(q)}`
}

/** authUrl is an API URL that authenticates via ?access_token= (GET only). */
export function authUrl(path: string, q?: Query): string {
  if (DEMO) return DEMO_UNAVAILABLE_HREF
  return apiUrl(path, { ...q, access_token: getToken() ?? "" })
}

/** wsUrl is the WebSocket URL for an API path, authenticated via ?access_token=. */
export function wsUrl(path: string, q?: Query): string {
  if (DEMO) throw new ApiError(501, "NotAvailableInDemo", "Live connections are not available in the demo.")
  const base = API_BASE || window.location.origin
  const u = new URL(authUrl(path, q), base)
  u.protocol = u.protocol === "https:" ? "wss:" : "ws:"
  return u.toString()
}

/** Path segment encoding for names that may contain "/" (secrets, log groups). */
export const seg = (s: string) => encodeURIComponent(s)

// ---- requests ----

export interface RequestOptions {
  query?: Query
  body?: unknown
  headers?: Record<string, string>
  signal?: AbortSignal
  /** Skip the redirect to /login on 401 (used by the login call). */
  noAuthRedirect?: boolean
}

/** demoRequest answers the call from the in-browser mock backend (demo build only). */
async function demoRequest<T>(method: string, path: string, opts: RequestOptions): Promise<T> {
  const demo = await import("@/lib/demo")
  let body: unknown = opts.body
  let raw: unknown
  if (opts.body instanceof Blob) {
    raw = opts.body.size <= 512 * 1024 ? await opts.body.text() : undefined
    body = raw
  } else if (typeof opts.body === "string") {
    raw = opts.body
    try {
      body = JSON.parse(opts.body)
    } catch {
      body = opts.body
    }
  }
  const query: Record<string, string> = {}
  for (const [k, v] of Object.entries(opts.query ?? {})) if (v !== undefined && v !== null && v !== "") query[k] = String(v)
  try {
    return await demo.demoRequest<T>({ method, path, query, body, raw, headers: opts.headers })
  } catch (e) {
    if (e instanceof demo.DemoError) throw new ApiError(e.status, e.code, e.message)
    throw e
  }
}

export async function request<T = unknown>(method: string, path: string, opts: RequestOptions = {}): Promise<T> {
  if (DEMO) return demoRequest<T>(method, path, opts)
  const headers: Record<string, string> = { Accept: "application/json", ...opts.headers }
  const token = getToken()
  if (token) headers.Authorization = `Bearer ${token}`
  let body: BodyInit | undefined
  if (opts.body !== undefined) {
    if (opts.body instanceof Blob || opts.body instanceof FormData || typeof opts.body === "string") {
      body = opts.body as BodyInit
    } else {
      headers["Content-Type"] = "application/json"
      body = JSON.stringify(opts.body)
    }
  }
  let res: Response
  try {
    res = await fetch(apiUrl(path, opts.query), { method, headers, body, signal: opts.signal })
  } catch (e) {
    if (e instanceof DOMException && e.name === "AbortError") throw e
    throw new ApiError(0, "NetworkError", "Cannot reach the HomeCloud API. Is the server running?")
  }
  const text = await res.text()
  let data: unknown = undefined
  if (text) {
    try {
      data = JSON.parse(text)
    } catch {
      data = text
    }
  }
  if (!res.ok) {
    const e = (data as { error?: { code?: string; message?: string } } | undefined)?.error
    const err = new ApiError(res.status, e?.code ?? `HTTP${res.status}`, e?.message ?? (typeof data === "string" && data ? data : res.statusText))
    if (res.status === 401 && !opts.noAuthRedirect) handleUnauthorized()
    throw err
  }
  return data as T
}

export const api = {
  get: <T = unknown>(path: string, query?: Query, signal?: AbortSignal) => request<T>("GET", path, { query, signal }),
  post: <T = unknown>(path: string, body?: unknown, query?: Query) => request<T>("POST", path, { body, query }),
  put: <T = unknown>(path: string, body?: unknown, query?: Query) => request<T>("PUT", path, { body, query }),
  patch: <T = unknown>(path: string, body?: unknown, query?: Query) => request<T>("PATCH", path, { body, query }),
  del: <T = unknown>(path: string, query?: Query) => request<T>("DELETE", path, { query }),
}

// ---- uploads ----

export interface UploadHandle {
  promise: Promise<unknown>
  abort: () => void
}

/**
 * upload PUTs a file with XMLHttpRequest so progress can be reported
 * (fetch has no upload progress events).
 */
export function upload(
  path: string,
  query: Query,
  file: Blob,
  onProgress?: (loaded: number, total: number) => void,
  contentType?: string,
): UploadHandle {
  if (DEMO) {
    // Small files are stored in the in-memory demo bucket; anything bigger is refused.
    const promise = (async () => {
      if (file.size > 256 * 1024) throw new ApiError(501, "NotAvailableInDemo", "Uploads larger than 256 KB are not available in the demo.")
      await new Promise((r) => setTimeout(r, 300))
      onProgress?.(file.size, file.size)
      return demoRequest("PUT", path, { query, body: file, headers: { "Content-Type": contentType || file.type || "application/octet-stream" } })
    })()
    return { promise, abort: () => {} }
  }
  const xhr = new XMLHttpRequest()
  const promise = new Promise<unknown>((resolve, reject) => {
    xhr.open("PUT", apiUrl(path, query))
    const token = getToken()
    if (token) xhr.setRequestHeader("Authorization", `Bearer ${token}`)
    xhr.setRequestHeader("Content-Type", contentType || file.type || "application/octet-stream")
    xhr.upload.onprogress = (e) => {
      if (e.lengthComputable) onProgress?.(e.loaded, e.total)
    }
    xhr.onload = () => {
      let data: unknown = undefined
      try {
        data = xhr.responseText ? JSON.parse(xhr.responseText) : undefined
      } catch {
        data = xhr.responseText
      }
      if (xhr.status >= 200 && xhr.status < 300) {
        resolve(data)
        return
      }
      const e = (data as { error?: { code?: string; message?: string } } | undefined)?.error
      if (xhr.status === 401) handleUnauthorized()
      reject(new ApiError(xhr.status, e?.code ?? `HTTP${xhr.status}`, e?.message ?? xhr.statusText))
    }
    xhr.onerror = () => reject(new ApiError(0, "NetworkError", "Upload failed: cannot reach the HomeCloud API"))
    xhr.onabort = () => reject(new ApiError(0, "Aborted", "Upload cancelled"))
    xhr.send(file)
  })
  return { promise, abort: () => xhr.abort() }
}

// ---- auth ----

export async function login(username: string, password: string): Promise<Session> {
  if (DEMO) return DEMO_SESSION
  const s = await request<Session>("POST", "/api/v1/auth/login", { body: { username, password }, noAuthRedirect: true })
  setSession(s)
  return s
}

export async function logout() {
  if (DEMO) return
  try {
    await request("POST", "/api/v1/auth/logout", { noAuthRedirect: true })
  } catch {
    // the session may already be gone
  }
  setSession(null)
}
