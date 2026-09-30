// A small CloudWatch Logs Insights engine over the demo's generated log events:
// fields, filter, parse, stats (count/sum/avg/min/max/pct ... by ..., bin()),
// sort, limit, dedup and display.

import type { InsightsResults, LogEvent } from "@/lib/types"
import { badRequest, notFound, type Router } from "../engine"
import { ACCOUNT, uid } from "../util"
import { eventsFor, type LogsState } from "./cloudwatch-logs"

type Rec = Record<string, unknown>

const SCAN_CAP = 20_000

function flatten(o: unknown, prefix: string, out: Rec) {
  if (o && typeof o === "object" && !Array.isArray(o)) {
    for (const [k, v] of Object.entries(o as Rec)) flatten(v, prefix ? `${prefix}.${k}` : k, out)
  } else out[prefix] = o
}

function toRecord(e: LogEvent, group: string): Rec {
  const t = Date.parse(e.timestamp)
  const rec: Rec = { "@timestamp": t, "@message": e.message, "@logStream": e.stream, "@log": `${ACCOUNT}:${group}`, "@ingestionTime": t + 400 }
  if (e.message.startsWith("{")) {
    try {
      flatten(JSON.parse(e.message), "", rec)
    } catch {
      // not JSON: only the @ fields
    }
  }
  return rec
}

const fmtTs = (ms: number) => new Date(ms).toISOString().replace("T", " ").replace("Z", "")
const fmtVal = (k: string, v: unknown): string => {
  if (v === undefined || v === null) return ""
  if (k === "@timestamp" || k.startsWith("bin(")) return typeof v === "number" ? fmtTs(v) : String(v)
  if (typeof v === "number") return Number.isInteger(v) ? String(v) : String(Math.round(v * 100) / 100)
  return String(v)
}

// ---- tokenizing helpers ----

/** splits on a separator outside of quotes, regex literals and parentheses/brackets. */
function splitTop(s: string, sep: string | RegExp, keepEmpty = false): string[] {
  const out: string[] = []
  let cur = ""
  let depth = 0
  let q = ""
  let inRe = false
  for (let i = 0; i < s.length; i++) {
    const c = s[i]
    if (q) {
      cur += c
      if (c === "\\") cur += s[++i] ?? ""
      else if (c === q) q = ""
      continue
    }
    if (inRe) {
      cur += c
      if (c === "\\") cur += s[++i] ?? ""
      else if (c === "/") inRe = false
      continue
    }
    if (c === '"' || c === "'" || c === "`") {
      q = c
      cur += c
      continue
    }
    if (c === "/" && /(like|=~|\s|\()\s*$/.test(cur)) {
      inRe = true
      cur += c
      continue
    }
    if (c === "(" || c === "[") depth++
    if (c === ")" || c === "]") depth--
    if (depth === 0) {
      if (typeof sep === "string" ? c === sep : sep.test(c)) {
        if (cur.trim() || keepEmpty) out.push(cur.trim())
        cur = ""
        continue
      }
    }
    cur += c
  }
  if (cur.trim() || keepEmpty) out.push(cur.trim())
  return out
}

const stripQ = (s: string) => {
  s = s.trim()
  const m = /^(["'`])([\s\S]*)\1$/.exec(s)
  return m ? m[2] : s
}
const toRegex = (s: string): RegExp => {
  s = s.trim()
  const m = /^\/([\s\S]*)\/([a-z]*)$/.exec(s)
  try {
    return m ? new RegExp(m[1], m[2].includes("i") ? "i" : "") : new RegExp(escapeRe(stripQ(s)))
  } catch {
    throw badRequest(`MalformedQueryException: invalid regular expression ${s}`)
  }
}
const escapeRe = (s: string) => s.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")

// ---- expressions (filter) ----

type Pred = (r: Rec) => boolean

const val = (r: Rec, k: string) => r[k.trim()]
const litOrField = (tok: string, r: Rec): unknown => {
  const t = tok.trim()
  if (/^(["'`])/.test(t)) return stripQ(t)
  if (/^-?\d+(\.\d+)?$/.test(t)) return Number(t)
  return r[t]
}

function parseCond(src: string): Pred {
  src = src.trim()
  while (src.startsWith("(") && src.endsWith(")") && balanced(src.slice(1, -1))) src = src.slice(1, -1).trim()
  const ors = splitKeyword(src, "or")
  if (ors.length > 1) {
    const ps = ors.map(parseCond)
    return (r) => ps.some((p) => p(r))
  }
  const ands = splitKeyword(src, "and")
  if (ands.length > 1) {
    const ps = ands.map(parseCond)
    return (r) => ps.every((p) => p(r))
  }
  if (/^not\s+/i.test(src)) {
    const p = parseCond(src.replace(/^not\s+/i, ""))
    return (r) => !p(r)
  }
  let m = /^ispresent\((.+)\)$/i.exec(src)
  if (m) return (r) => r[m![1].trim()] !== undefined
  m = /^(\S+)\s+(not\s+)?like\s+(.+)$/i.exec(src)
  if (m) {
    const re = toRegex(m[3])
    const neg = !!m[2]
    return (r) => {
      const v = val(r, m![1])
      const hit = v !== undefined && re.test(String(v))
      return neg ? !hit : hit
    }
  }
  m = /^(\S+)\s*=~\s*(.+)$/.exec(src)
  if (m) {
    const re = toRegex(m[2])
    return (r) => re.test(String(val(r, m![1]) ?? ""))
  }
  m = /^(\S+)\s+(not\s+)?in\s*\[(.*)\]$/i.exec(src)
  if (m) {
    const set = splitTop(m[3], ",").map(stripQ)
    const neg = !!m[2]
    return (r) => set.includes(String(val(r, m![1]) ?? "")) !== neg
  }
  m = /^(.+?)\s*(!=|<=|>=|=|<|>)\s*(.+)$/.exec(src)
  if (m) {
    const [, l, op, rr] = m
    return (r) => {
      const a = litOrField(l, r)
      const b = litOrField(rr, r)
      if (a === undefined || a === null) return op === "!="
      const numeric = typeof a === "number" || typeof b === "number"
      const na = (numeric ? Number(a) : String(a)) as number
      const nb = (numeric ? Number(b) : String(b)) as number
      switch (op) {
        case "=":
          return String(a) === String(b)
        case "!=":
          return String(a) !== String(b)
        case "<":
          return na < nb
        case ">":
          return na > nb
        case "<=":
          return na <= nb
        default:
          return na >= nb
      }
    }
  }
  throw badRequest(`MalformedQueryException: unexpected filter expression "${src}"`)
}

function balanced(s: string): boolean {
  let d = 0
  for (const c of s) {
    if (c === "(") d++
    if (c === ")") d--
    if (d < 0) return false
  }
  return d === 0
}

function splitKeyword(s: string, kw: string): string[] {
  const parts: string[] = []
  let cur = ""
  let depth = 0
  let q = ""
  let inRe = false
  const re = new RegExp(`^\\s${kw}\\s`, "i")
  for (let i = 0; i < s.length; i++) {
    const c = s[i]
    if (q) {
      cur += c
      if (c === q) q = ""
      continue
    }
    if (inRe) {
      cur += c
      if (c === "/") inRe = false
      continue
    }
    if (c === '"' || c === "'") q = c
    else if (c === "/" && /(like|=~)\s*$/i.test(cur)) inRe = true
    else if (c === "(" || c === "[") depth++
    else if (c === ")" || c === "]") depth--
    if (depth === 0 && q === "" && !inRe && re.test(s.slice(i - 0, i + kw.length + 2)) && i > 0) {
      parts.push(cur)
      cur = ""
      i += kw.length + 1
      continue
    }
    cur += c
  }
  parts.push(cur)
  return parts.map((p) => p.trim()).filter(Boolean)
}

// ---- commands ----

interface Ctx {
  cols: string[]
  display?: string[]
  /** true once stats ran (rows are aggregate rows) */
  agg: boolean
}

const addCol = (c: Ctx, k: string) => {
  if (!c.cols.includes(k)) c.cols.push(k)
}

function evalExpr(expr: string, r: Rec): unknown {
  const e = expr.trim()
  let m = /^(strlen|toupper|tolower|abs|ceil|floor|round)\((.+)\)$/i.exec(e)
  if (m) {
    const v = evalExpr(m[2], r)
    switch (m[1].toLowerCase()) {
      case "strlen":
        return String(v ?? "").length
      case "toupper":
        return String(v ?? "").toUpperCase()
      case "tolower":
        return String(v ?? "").toLowerCase()
      case "abs":
        return Math.abs(Number(v))
      case "ceil":
        return Math.ceil(Number(v))
      case "floor":
        return Math.floor(Number(v))
      default:
        return Math.round(Number(v))
    }
  }
  m = /^bin\((.+?)\)$/i.exec(e)
  if (m) return bin(r["@timestamp"] as number, m[1])
  m = /^(.+?)\s*([*/+-])\s*(.+)$/.exec(e)
  if (m && !/^["'`]/.test(e)) {
    const a = Number(evalExpr(m[1], r))
    const b = Number(evalExpr(m[3], r))
    return m[2] === "*" ? a * b : m[2] === "/" ? a / b : m[2] === "+" ? a + b : a - b
  }
  if (/^["'`]/.test(e)) return stripQ(e)
  if (/^-?\d+(\.\d+)?$/.test(e)) return Number(e)
  return r[e]
}

function bin(t: number, spec: string): number {
  const m = /^(\d+)\s*(s|m|h|d|sec|min|hr|second|minute|hour|day)?s?$/i.exec(spec.trim())
  const n = m ? Number(m[1]) : 5
  const u = (m?.[2] ?? "m").toLowerCase()[0]
  const ms = n * (u === "s" ? 1000 : u === "h" ? 3600_000 : u === "d" ? 86400_000 : 60_000)
  return Math.floor(t / ms) * ms
}

function runCommand(cmd: string, rest: string, rows: Rec[], c: Ctx): Rec[] {
  switch (cmd) {
    case "fields":
    case "display": {
      const items = splitTop(rest, ",").map((it) => {
        const m = /^(.+?)\s+as\s+(\S+)$/i.exec(it)
        return { expr: (m ? m[1] : it).trim(), name: (m ? m[2] : it).trim() }
      })
      if (cmd === "display") {
        c.display = items.map((i) => i.name)
        return rows
      }
      for (const it of items) addCol(c, it.name)
      if (items.some((i) => i.expr !== i.name)) for (const r of rows) for (const it of items) if (it.expr !== it.name) r[it.name] = evalExpr(it.expr, r)
      return rows
    }
    case "filter": {
      const p = parseCond(rest)
      return rows.filter(p)
    }
    case "parse": {
      const m = /^(\S+)\s+("[^"]*"|'[^']*'|\/.*\/)\s+as\s+(.+)$/i.exec(rest)
      if (!m) throw badRequest("MalformedQueryException: parse expects: parse <field> <pattern> as <name>, ...")
      const names = m[3].split(",").map((x) => x.trim())
      let re: RegExp
      if (m[2].startsWith("/")) re = toRegex(m[2])
      else re = new RegExp(escapeRe(stripQ(m[2])).replace(/\\\*/g, "(.*?)"))
      const namedRe = m[2].startsWith("/")
      for (const n of names) addCol(c, n)
      return rows.filter((r) => {
        const x = re.exec(String(r[m[1]] ?? ""))
        if (!x) return false
        if (namedRe && x.groups) for (const [k, v] of Object.entries(x.groups)) r[k] = v
        else names.forEach((n, i) => (r[n] = x[i + 1]))
        return true
      })
    }
    case "sort": {
      const keys = splitTop(rest, ",").map((k) => {
        const m = /^(.+?)(?:\s+(asc|desc))?$/i.exec(k)
        return { k: m![1].trim(), dir: (m![2] ?? "asc").toLowerCase() === "desc" ? -1 : 1 }
      })
      return [...rows].sort((a, b) => {
        for (const { k, dir } of keys) {
          const x = a[k]
          const y = b[k]
          if (x === y) continue
          if (x === undefined) return 1
          if (y === undefined) return -1
          const cmp = typeof x === "number" && typeof y === "number" ? x - y : String(x).localeCompare(String(y))
          if (cmp) return cmp * dir
        }
        return 0
      })
    }
    case "limit": {
      const n = Number(rest)
      if (!Number.isInteger(n) || n < 1) throw badRequest("MalformedQueryException: limit needs a positive number")
      return rows.slice(0, n)
    }
    case "dedup": {
      const keys = splitTop(rest, ",")
      const seen = new Set<string>()
      return rows.filter((r) => {
        const k = keys.map((x) => String(r[x] ?? "")).join("\u0000")
        if (seen.has(k)) return false
        seen.add(k)
        return true
      })
    }
    case "stats":
      return runStats(rest, rows, c)
    default:
      throw badRequest(`MalformedQueryException: unknown command "${cmd}"`)
  }
}

function runStats(rest: string, rows: Rec[], c: Ctx): Rec[] {
  const byM = /^([\s\S]*?)\s+by\s+([\s\S]+)$/i.exec(rest)
  const aggSrc = byM ? byM[1] : rest
  const groupBy = byM ? splitTop(byM[2], ",") : []
  const aggs = splitTop(aggSrc, ",").map((a) => {
    const m = /^(count|count_distinct|sum|avg|min|max|pct|stddev|earliest|latest)\((.*)\)(?:\s+as\s+(\S+))?$/i.exec(a)
    if (!m) throw badRequest(`MalformedQueryException: unsupported stats expression "${a}"`)
    const fn = m[1].toLowerCase()
    const args = splitTop(m[2], ",")
    return { fn, args, name: m[3] ?? `${fn}(${m[2]})` }
  })
  c.cols = []
  c.agg = true
  for (const a of aggs) addCol(c, a.name)
  const groupNames = groupBy.map((g) => g.replace(/\s+as\s+\S+$/i, "").trim())
  for (const g of groupNames) addCol(c, g)
  const groups = new Map<string, { key: Rec; rows: Rec[] }>()
  for (const r of rows) {
    const key: Rec = {}
    for (const g of groupNames) key[g] = evalExpr(g, r)
    const k = groupNames.map((g) => String(key[g])).join("\u0000")
    let e = groups.get(k)
    if (!e) groups.set(k, (e = { key, rows: [] }))
    e.rows.push(r)
  }
  if (!groupNames.length && !groups.size) groups.set("", { key: {}, rows: [] })
  const out: Rec[] = []
  for (const { key, rows: rs } of groups.values()) {
    const rec: Rec = { ...key }
    for (const a of aggs) {
      const field = a.args[0]?.trim()
      const nums = field && field !== "*" ? rs.map((r) => evalExpr(field, r)).filter((v) => v !== undefined && v !== null && v !== "").map(Number).filter((v) => !isNaN(v)) : []
      switch (a.fn) {
        case "count":
          rec[a.name] = field === "*" || !field ? rs.length : rs.filter((r) => r[field] !== undefined).length
          break
        case "count_distinct":
          rec[a.name] = new Set(rs.map((r) => String(r[field] ?? ""))).size
          break
        case "sum":
          rec[a.name] = nums.reduce((x, y) => x + y, 0)
          break
        case "avg":
          rec[a.name] = nums.length ? nums.reduce((x, y) => x + y, 0) / nums.length : undefined
          break
        case "min":
          rec[a.name] = nums.length ? Math.min(...nums) : undefined
          break
        case "max":
          rec[a.name] = nums.length ? Math.max(...nums) : undefined
          break
        case "stddev": {
          const mean = nums.reduce((x, y) => x + y, 0) / (nums.length || 1)
          rec[a.name] = Math.sqrt(nums.reduce((x, y) => x + (y - mean) ** 2, 0) / (nums.length || 1))
          break
        }
        case "pct": {
          const p = Number(a.args[1] ?? 50) / 100
          const s = [...nums].sort((x, y) => x - y)
          rec[a.name] = s.length ? s[Math.min(s.length - 1, Math.ceil(p * s.length) - 1)] : undefined
          break
        }
        case "earliest":
        case "latest": {
          const s = [...rs].sort((x, y) => (x["@timestamp"] as number) - (y["@timestamp"] as number))
          const r = a.fn === "earliest" ? s[0] : s[s.length - 1]
          rec[a.name] = r ? r[field] : undefined
          break
        }
      }
    }
    out.push(rec)
  }
  return out
}

// ---- query execution ----

export interface InsightsStore {
  results: Map<string, InsightsResults>
}
export const insightsStore: InsightsStore = { results: new Map() }

export function runInsights(st: LogsState, groups: string[], startSec: number, endSec: number, query: string): InsightsResults {
  if (!groups.length) throw badRequest("MalformedQueryException: at least one log group is required")
  if (!query.trim()) throw badRequest("MalformedQueryException: query string is required")
  const start = startSec * 1000
  const end = endSec * 1000
  let recs: Rec[] = []
  let scanned = 0
  let bytes = 0
  for (const g of groups) {
    if (!st.groups.some((x) => x.name === g) && !g.startsWith("/aws/lambda/")) throw notFound("log group", g)
    const evs = eventsFor(st, g, start, end, undefined, "", SCAN_CAP)
    scanned += evs.length
    for (const e of evs) {
      bytes += e.message.length + 26
      recs.push(toRecord(e, g))
    }
  }
  recs.sort((a, b) => (b["@timestamp"] as number) - (a["@timestamp"] as number))
  if (recs.length > SCAN_CAP) recs = recs.slice(0, SCAN_CAP)
  const ctx: Ctx = { cols: [], agg: false }
  let rows = recs
  let explicitFields = false
  let limited = false
  for (const part of splitTop(query, "|")) {
    const m = /^(\w+)\s*([\s\S]*)$/.exec(part.trim())
    if (!m) continue
    const cmd = m[1].toLowerCase()
    if (cmd === "fields") explicitFields = true
    if (cmd === "limit") limited = true
    rows = runCommand(cmd, m[2].trim(), rows, ctx)
  }
  const matched = rows.length
  if (!ctx.agg) {
    if (!explicitFields) ctx.cols = ["@timestamp", "@message"]
    else if (!ctx.cols.includes("@ptr")) ctx.cols = ctx.cols.filter((c) => c !== "@ptr")
    if (!limited) rows = rows.slice(0, 1000)
  }
  const cols = ctx.display ?? ctx.cols
  const results = rows.map((r, i) => {
    const row = cols.map((k) => ({ field: k, value: fmtVal(k, r[k]) }))
    if (!ctx.agg) row.push({ field: "@ptr", value: `CmoKKjEyMzQ1Njc4OTAxMjovYXdzL2xvZ3MvZGVtbxABGgUImLmPvgEgASoMCJqL${i.toString(36).padStart(4, "0")}` })
    return row
  })
  return { status: "Complete", results, statistics: { recordsMatched: matched, recordsScanned: scanned, bytesScanned: bytes } }
}

export function insightsRoutes(r: Router, st: () => LogsState) {
  r.post("/api/v1/logs/insights/queries", ({ body }) => {
    const groups = (body?.logGroupNames ?? []) as string[]
    const now = Math.floor(Date.now() / 1000)
    const res = runInsights(st(), groups, Number(body?.startTime ?? now - 3600), Number(body?.endTime ?? now), String(body?.queryString ?? ""))
    const id = uid("", 32).replace(/^(.{8})(.{4})(.{4})(.{4})(.{12}).*$/, "$1-$2-$3-$4-$5")
    insightsStore.results.set(id, res)
    if (insightsStore.results.size > 50) insightsStore.results.delete(insightsStore.results.keys().next().value as string)
    return { queryId: id }
  })
  r.get("/api/v1/logs/insights/queries/:id", ({ params }) => {
    const res = insightsStore.results.get(params.id)
    if (!res) throw notFound("query", params.id)
    return res
  })
}
