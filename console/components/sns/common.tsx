"use client"

import Link from "next/link"

import { StatusBadge } from "@/components/console/status-badge"
import { Tag, protocolAccent } from "@/components/console/tag"
import { queueHref } from "@/components/sqs/common"
import { seg } from "@/lib/api"
import { useApi } from "@/lib/hooks"
import type { FilterPolicyScope, MessageAttribute, Subscription, SubscriptionProtocol, Topic } from "@/lib/types"
import { cn } from "@/lib/utils"

export const SNS_PATH = "/api/v1/sns"
export const TOPICS_PATH = `${SNS_PATH}/topics`
export const SUBSCRIPTIONS_PATH = `${SNS_PATH}/subscriptions`

/** Delivery stats update asynchronously after a publish; refresh moderately often. */
export const SNS_POLL = 10_000

export const topicPath = (name: string) => `${TOPICS_PATH}/${seg(name)}`
export const subscriptionPath = (arn: string) => `${SUBSCRIPTIONS_PATH}/${seg(arn)}`

export const topicHref = (name: string, tab?: string) => `/sns/topic/?name=${encodeURIComponent(name)}${tab ? `&tab=${tab}` : ""}`

const functionHref = (name: string) => `/lambda/function/?name=${encodeURIComponent(name)}`

export const lastSegment = (arn: string) => arn.slice(arn.lastIndexOf(":") + 1)

/** Topic names: 1-256 letters, digits, hyphens, underscores (FIFO topics end in .fifo). */
export const TOPIC_NAME_RE = /^[A-Za-z0-9_-]{1,256}(\.fifo)?$/

export function useTopics(refreshInterval = SNS_POLL) {
  return useApi<Topic[]>(TOPICS_PATH, { refreshInterval })
}

export const PROTOCOL_LABEL: Record<SubscriptionProtocol, string> = {
  sqs: "Amazon SQS",
  lambda: "AWS Lambda",
  http: "HTTP",
  https: "HTTPS",
  email: "Email",
  "email-json": "Email-JSON",
  sms: "SMS",
}

/** Protocols that support raw message delivery. */
export const RAW_PROTOCOLS: string[] = ["sqs", "http", "https"]

/** Protocols whose subscriptions must be confirmed by the endpoint owner. */
export const CONFIRM_PROTOCOLS: string[] = ["http", "https", "email", "email-json"]

export const isPending = (s: Pick<Subscription, "status">) => s.status === "PendingConfirmation"

/** ProtocolBadge is the subscription protocol chip (SQS, LAMBDA, HTTPS ...). */
export function ProtocolBadge({ protocol }: { protocol: string }) {
  return (
    <Tag accent={protocolAccent(protocol)} className="uppercase">
      {protocol}
    </Tag>
  )
}

/** SubscriptionStatusBadge shows Confirmed / Pending confirmation. */
export function SubscriptionStatusBadge({ status }: { status: string }) {
  const pending = status === "PendingConfirmation"
  return pending ? (
    <StatusBadge status="pending" label="Pending confirmation" />
  ) : (
    <StatusBadge status={status || "Confirmed"} tone="success" />
  )
}

/** SubscriptionEndpoint links queue and function endpoints to their consoles. */
export function SubscriptionEndpoint({ sub, className }: { sub: Pick<Subscription, "protocol" | "endpoint">; className?: string }) {
  const cls = cn("font-mono text-[13px] break-all", className)
  if (sub.protocol === "sqs") {
    const q = lastSegment(sub.endpoint)
    return (
      <Link href={queueHref(q)} onClick={(e) => e.stopPropagation()} className={cn(cls, "text-primary hover:underline")} title={sub.endpoint}>
        {q}
      </Link>
    )
  }
  if (sub.protocol === "lambda") {
    const f = lastSegment(sub.endpoint)
    return (
      <Link href={functionHref(f)} onClick={(e) => e.stopPropagation()} className={cn(cls, "text-primary hover:underline")} title={sub.endpoint}>
        {f}
      </Link>
    )
  }
  return (
    <span className={cls} title={sub.endpoint}>
      {sub.endpoint}
    </span>
  )
}

/** The dead-letter queue ARN of a subscription's redrive policy, if any. */
export function subscriptionDlq(s: Pick<Subscription, "redrive_policy">): string | null {
  if (!s.redrive_policy) return null
  try {
    const p = JSON.parse(s.redrive_policy) as { deadLetterTargetArn?: string }
    return p.deadLetterTargetArn || null
  } catch {
    return null
  }
}

const OPERATORS = new Set(["prefix", "suffix", "equals-ignore-case", "anything-but", "numeric", "exists", "cidr", "wildcard"])

/**
 * filterPolicyError does a quick shape check of an SNS filter policy (the
 * server validates it fully): a non-empty object whose keys map to arrays of
 * values/operators, "$or" to an array of policies, and (MessageBody scope)
 * nested objects.
 */
export function filterPolicyError(parsed: unknown, scope: FilterPolicyScope = "MessageAttributes", depth = 0): string | null {
  if (!parsed || typeof parsed !== "object" || Array.isArray(parsed)) return "The filter policy must be a JSON object"
  const entries = Object.entries(parsed as Record<string, unknown>)
  if (!entries.length) return depth ? "Nested objects must not be empty" : "Add at least one condition, or turn the filter policy off"
  for (const [k, v] of entries) {
    if (k === "$or") {
      if (!Array.isArray(v) || v.length < 2) return `"$or" must be an array of at least two policies`
      for (const p of v) {
        const e = filterPolicyError(p, scope, depth + 1)
        if (e) return e
      }
      continue
    }
    if (Array.isArray(v)) {
      if (!v.length) return `"${k}" must list at least one value`
      for (const x of v) {
        if (x && typeof x === "object") {
          if (Array.isArray(x)) return `"${k}" values must be strings, numbers or operator objects`
          const ops = Object.keys(x as object)
          if (ops.length !== 1 || !OPERATORS.has(ops[0])) return `"${k}": unknown operator ${JSON.stringify(ops.join(", "))}`
        }
      }
      continue
    }
    if (v && typeof v === "object") {
      if (scope !== "MessageBody") return `"${k}": nested objects are only allowed with the MessageBody scope`
      const e = filterPolicyError(v, scope, depth + 1)
      if (e) return e.startsWith('"') ? e : `"${k}": ${e}`
      continue
    }
    return `"${k}" must be an array of allowed values (e.g. ["high", "critical"])`
  }
  return null
}

export function compactPolicy(p?: Record<string, unknown> | null) {
  return p && Object.keys(p).length ? JSON.stringify(p) : ""
}

export const EXAMPLE_POLICY: Record<FilterPolicyScope, string> = {
  MessageAttributes: '{\n  "severity": ["high", "critical"],\n  "price": [{ "numeric": [">=", 100] }]\n}',
  MessageBody: '{\n  "order": {\n    "status": ["shipped"],\n    "region": [{ "prefix": "eu-" }]\n  }\n}',
}

// ---- client-side filter policy evaluation (publish preview; mirrors the server) ----

type Lookup = (path: string[]) => [unknown[], boolean]

const isObj = (v: unknown): v is Record<string, unknown> => !!v && typeof v === "object" && !Array.isArray(v)

/** resolve collects the leaf values at path in a JSON document, descending into arrays. */
function resolve(v: unknown, path: string[]): unknown[] {
  if (!path.length) return Array.isArray(v) ? v.filter((x) => !isObj(x)) : [v]
  if (isObj(v)) return Object.prototype.hasOwnProperty.call(v, path[0]) ? resolve(v[path[0]], path.slice(1)) : []
  if (Array.isArray(v)) return v.flatMap((x) => resolve(x, path))
  return []
}

const num = (v: unknown) => (typeof v === "number" ? v : NaN)

function excluded(arg: unknown, v: unknown): boolean {
  const eq = (x: unknown) => (typeof x === "string" ? v === x : typeof x === "number" ? num(v) === x : false)
  if (typeof arg === "string" || typeof arg === "number") return eq(arg)
  if (Array.isArray(arg)) return arg.some(eq)
  if (isObj(arg) && typeof v === "string") {
    const [op, p] = Object.entries(arg)[0] ?? []
    if (op === "prefix") return v.startsWith(String(p))
    if (op === "suffix") return v.endsWith(String(p))
    if (op === "equals-ignore-case") return (Array.isArray(p) ? p : [p]).some((x) => typeof x === "string" && x.toLowerCase() === v.toLowerCase())
  }
  return false
}

function wildcard(p: string, s: string) {
  const re = new RegExp(`^${p.split("*").map((x) => x.replace(/[.+?^${}()|[\]\\]/g, "\\$&")).join(".*")}$`, "s")
  return re.test(s)
}

function inCidr(cidr: string, ip: string): boolean | null {
  const v4 = (s: string) => {
    const m = s.split(".")
    if (m.length !== 4 || m.some((x) => !/^\d{1,3}$/.test(x) || Number(x) > 255)) return null
    return m.reduce((a, x) => a * 256 + Number(x), 0)
  }
  const [net, bits] = cidr.split("/")
  const n = v4(net)
  const a = v4(ip)
  if (n === null || bits === undefined) return null // IPv6: leave it to the server
  if (a === null) return false
  const size = 2 ** (32 - Number(bits))
  return Math.floor(a / size) === Math.floor(n / size)
}

/** matchCondition returns null only for conditions the preview can't decide. */
function matchCondition(c: unknown, vals: unknown[], present: boolean): boolean | null {
  const any = (f: (v: unknown) => boolean) => vals.some(f)
  if (typeof c === "string") return any((v) => v === c)
  if (typeof c === "number") return any((v) => num(v) === c)
  if (typeof c === "boolean") return any((v) => v === c)
  if (c === null) return any((v) => v === null)
  if (!isObj(c)) return false
  const [op, arg] = Object.entries(c)[0] ?? []
  switch (op) {
    case "exists":
      return present === arg
    case "prefix":
      return any((v) => typeof v === "string" && v.startsWith(String(arg)))
    case "suffix":
      return any((v) => typeof v === "string" && v.endsWith(String(arg)))
    case "equals-ignore-case":
      return any((v) => typeof v === "string" && v.toLowerCase() === String(arg).toLowerCase())
    case "wildcard":
      return any((v) => typeof v === "string" && wildcard(String(arg), v))
    case "cidr": {
      const r = vals.map((v) => (typeof v === "string" ? inCidr(String(arg), v) : false))
      return r.includes(true) ? true : r.includes(null) ? null : false
    }
    case "numeric": {
      if (!Array.isArray(arg)) return null
      return any((v) => {
        const f = num(v)
        if (Number.isNaN(f)) return false
        for (let i = 0; i + 1 < arg.length; i += 2) {
          const b = Number(arg[i + 1])
          const ok = ({ "=": f === b, "<": f < b, "<=": f <= b, ">": f > b, ">=": f >= b } as Record<string, boolean>)[String(arg[i])]
          if (!ok) return false
        }
        return true
      })
    }
    case "anything-but":
      return present && any((v) => !excluded(arg, v))
  }
  return null
}

function matchObject(policy: Record<string, unknown>, get: Lookup, path: string[]): boolean | null {
  let unknown = false
  for (const [k, v] of Object.entries(policy)) {
    let r: boolean | null
    if (k === "$or") {
      const alts = (Array.isArray(v) ? v : []).map((alt) => (isObj(alt) ? matchObject(alt, get, path) : false))
      r = alts.includes(true) ? true : alts.includes(null) ? null : false
    } else if (isObj(v)) {
      r = matchObject(v, get, [...path, k])
    } else if (Array.isArray(v)) {
      const [vals, present] = get([...path, k])
      const rs = v.map((c) => matchCondition(c, vals, present))
      r = rs.includes(true) ? true : rs.includes(null) ? null : false
    } else r = false
    if (r === false) return false
    if (r === null) unknown = true
  }
  return unknown ? null : true
}

/**
 * policyMatches evaluates a subscription's filter policy against a message the
 * way SNS does. It returns null when the preview cannot decide (the server does).
 */
export function policyMatches(
  s: Pick<Subscription, "filter_policy" | "filter_policy_scope">,
  attrs: Record<string, MessageAttribute> | undefined,
  message: string,
): boolean | null {
  const p = s.filter_policy
  if (!p || !Object.keys(p).length) return true
  let get: Lookup
  if (s.filter_policy_scope === "MessageBody") {
    let doc: unknown
    try {
      doc = JSON.parse(message)
    } catch {
      return false // non-JSON bodies never match a body policy
    }
    if (!isObj(doc)) return false
    get = (path) => {
      const vals = resolve(doc, path)
      return [vals, vals.length > 0]
    }
  } else {
    get = (path) => {
      const a = attrs?.[path[0]]
      if (!a || path.length !== 1) return [[], false]
      if (a.data_type === "String.Array") {
        try {
          const arr = JSON.parse(a.string_value)
          return [Array.isArray(arr) ? arr : [], true]
        } catch {
          return [[], true]
        }
      }
      const base = a.data_type.split(".")[0]
      if (base === "String") return [[a.string_value], true]
      if (base === "Number") return [[Number(a.string_value.trim())], true]
      return [[], false]
    }
  }
  return matchObject(p, get, [])
}
