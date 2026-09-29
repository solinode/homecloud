"use client"

import Link from "next/link"

import { queueHref } from "@/components/sqs/common"
import { seg } from "@/lib/api"
import { useApi } from "@/lib/hooks"
import type { Subscription, SubscriptionProtocol, Topic } from "@/lib/types"
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

const PROTOCOL_TONE: Record<string, string> = {
  sqs: "bg-amber-50 text-amber-800 ring-amber-600/20 dark:bg-amber-500/10 dark:text-amber-300 dark:ring-amber-400/20",
  lambda: "bg-orange-50 text-orange-800 ring-orange-600/20 dark:bg-orange-500/10 dark:text-orange-300 dark:ring-orange-400/20",
  http: "bg-sky-50 text-sky-800 ring-sky-600/20 dark:bg-sky-500/10 dark:text-sky-300 dark:ring-sky-400/20",
  https: "bg-sky-50 text-sky-800 ring-sky-600/20 dark:bg-sky-500/10 dark:text-sky-300 dark:ring-sky-400/20",
}

export const PROTOCOL_LABEL: Record<SubscriptionProtocol, string> = { sqs: "Amazon SQS", lambda: "AWS Lambda", http: "HTTP", https: "HTTPS" }

export function ProtocolBadge({ protocol }: { protocol: string }) {
  return (
    <span
      className={cn(
        "inline-flex items-center rounded-md px-1.5 py-0.5 font-mono text-xs font-medium whitespace-nowrap uppercase ring-1 ring-inset",
        PROTOCOL_TONE[protocol] ?? "bg-muted text-muted-foreground ring-border",
      )}
    >
      {protocol}
    </span>
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

/** filterPolicyError checks the shape SNS accepts: an object whose values are arrays of strings. */
export function filterPolicyError(parsed: unknown): string | null {
  if (!parsed || typeof parsed !== "object" || Array.isArray(parsed)) return "The filter policy must be a JSON object"
  const entries = Object.entries(parsed as Record<string, unknown>)
  if (!entries.length) return "Add at least one attribute, or turn the filter policy off"
  for (const [k, v] of entries) {
    if (!Array.isArray(v) || !v.length) return `"${k}" must be a non-empty array of allowed values`
    if (!v.every((x) => typeof x === "string")) return `"${k}" must list string values (e.g. ["high", "critical"])`
  }
  return null
}

export function compactPolicy(p?: Record<string, string[]> | null) {
  return p && Object.keys(p).length ? JSON.stringify(p) : ""
}
