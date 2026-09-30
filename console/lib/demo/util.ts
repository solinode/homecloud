// Small helpers shared by the demo fixtures.

export const ACCOUNT = "123456789012"
export const REGION = "us-east-1"
export const AZ_A = `${REGION}a`
export const AZ_B = `${REGION}b`

const SEC = 1000
export const MIN = 60 * SEC
export const HOUR = 60 * MIN
export const DAY = 24 * HOUR

/** ago returns an ISO timestamp `ms` milliseconds in the past. */
export const ago = (ms: number) => new Date(Date.now() - ms).toISOString()
export const nowIso = () => new Date().toISOString()
/** ahead returns an ISO timestamp `ms` in the future. */
export const ahead = (ms: number) => new Date(Date.now() + ms).toISOString()
export const epoch = () => Math.floor(Date.now() / 1000)

/** Deterministic PRNG (mulberry32) so fixtures and generated series are stable. */
export function rng(seed: number | string) {
  let a = typeof seed === "number" ? seed : hashStr(seed)
  return () => {
    a |= 0
    a = (a + 0x6d2b79f5) | 0
    let t = Math.imul(a ^ (a >>> 15), 1 | a)
    t = (t + Math.imul(t ^ (t >>> 7), 61 | t)) ^ t
    return ((t ^ (t >>> 14)) >>> 0) / 4294967296
  }
}

export function hashStr(s: string): number {
  let h = 2166136261
  for (let i = 0; i < s.length; i++) {
    h ^= s.charCodeAt(i)
    h = Math.imul(h, 16777619)
  }
  return h >>> 0
}

const HEX = "0123456789abcdef"
/** hex returns n random lowercase hex chars (for ids created at runtime). */
export function hex(n: number): string {
  let s = ""
  for (let i = 0; i < n; i++) s += HEX[Math.floor(Math.random() * 16)]
  return s
}
/** uid("i-", 17) -> "i-0a1b2c..." (AWS-style resource id). */
export const uid = (prefix: string, n = 17) => prefix + hex(n)
/** uuid returns a random RFC-4122-looking id. */
export const uuid = () => `${hex(8)}-${hex(4)}-4${hex(3)}-a${hex(3)}-${hex(12)}`

/** stableId derives a fixed, fake-looking hex id from a name (for fixtures). */
export function stableId(prefix: string, name: string, n = 17): string {
  const r = rng(name)
  let s = ""
  for (let i = 0; i < n; i++) s += HEX[Math.floor(r() * 16)]
  return prefix + s
}

export const arn = (service: string, resource: string, opts: { region?: string | null; account?: string | null } = {}) =>
  `arn:aws:${service}:${opts.region === null ? "" : (opts.region ?? REGION)}:${opts.account === null ? "" : (opts.account ?? ACCOUNT)}:${resource}`

/** pick returns `x` unless it is null/undefined, in which case `d`. */
export const dflt = <T,>(x: T | undefined | null, d: T): T => (x === undefined || x === null ? d : x)

/** clone deep-copies JSON data. */
export const clone = <T,>(x: T): T => JSON.parse(JSON.stringify(x)) as T

/** byName / byId find helpers */
export const findBy = <T, K extends keyof T>(list: T[], key: K, value: unknown) => list.find((x) => x[key] === value)

/** removeWhere deletes matching items in place and returns how many were removed. */
export function removeWhere<T>(list: T[], pred: (x: T) => boolean): number {
  let n = 0
  for (let i = list.length - 1; i >= 0; i--) {
    if (pred(list[i])) {
      list.splice(i, 1)
      n++
    }
  }
  return n
}

/**
 * timeSeries generates `points` values ending now, spaced `periodMs` apart, that look
 * like real metric data: a base level, daily seasonality, noise and optional spikes.
 * Deterministic for a given key, aligned to the period so refreshes stay stable.
 */
export function timeSeries(
  key: string,
  opts: { start: number; end: number; periodMs: number; base: number; amp?: number; noise?: number; min?: number; max?: number; spikes?: number; integer?: boolean },
): { t: number; v: number }[] {
  const { start, end, periodMs, base, amp = base * 0.3, noise = base * 0.08, min = 0, max = Infinity, spikes = 0, integer } = opts
  const out: { t: number; v: number }[] = []
  const first = Math.floor(start / periodMs) * periodMs
  for (let t = first; t <= end; t += periodMs) {
    const r = rng(`${key}:${t}`)
    const hourOfDay = new Date(t).getUTCHours() + new Date(t).getUTCMinutes() / 60
    // business-hours hump peaking mid-afternoon UTC
    const season = Math.sin(((hourOfDay - 9) / 24) * Math.PI * 2)
    let v = base + amp * season + (r() - 0.5) * 2 * noise
    if (spikes > 0 && r() < spikes) v += base * (0.6 + r())
    v = Math.min(max, Math.max(min, v))
    out.push({ t, v: integer ? Math.round(v) : Math.round(v * 1000) / 1000 })
  }
  return out
}

/** fmtBytes for descriptions in fixtures */
export function fmtBytes(n: number): string {
  const u = ["B", "KB", "MB", "GB", "TB"]
  let i = 0
  while (n >= 1024 && i < u.length - 1) {
    n /= 1024
    i++
  }
  return `${n.toFixed(i ? 1 : 0)} ${u[i]}`
}

/** Fake, obviously-not-real credentials for fixtures. */
export const FAKE_SECRET = "demo-not-a-real-secret"
export const FAKE_ACCESS_KEY = "AKIADEMOEXAMPLE00000"
