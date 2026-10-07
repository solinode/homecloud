import type { ReactNode } from "react"

import type { Tone } from "@/components/console/status-badge"
import { cn } from "@/lib/utils"

/**
 * Accent of a Tag. Tags share one neutral chip style; the accent only tints
 * the text (and a hairline of the border), so a row of mixed tags stays calm.
 */
export type TagAccent = "neutral" | "brand" | "info" | "success" | "warning" | "danger" | "violet"

const ACCENT: Record<TagAccent, string> = {
  neutral: "text-muted-foreground",
  brand: "text-primary border-brand-line",
  info: "text-info border-info/25",
  success: "text-success border-success/25",
  warning: "text-warning border-warning/30",
  danger: "text-danger border-danger/25",
  violet: "text-violet border-violet/30",
}

/**
 * Tag is the console's one "kind" chip: HTTP methods, protocols, record
 * types, engines, runtimes, key specs. Mono, uppercase-friendly, neutral fill.
 * Use StatusBadge (not Tag) for lifecycle states.
 */
export function Tag({
  children,
  accent = "neutral",
  mono = true,
  className,
  title,
}: {
  children: ReactNode
  accent?: TagAccent
  /** monospace (default) for machine identifiers; false for prose labels */
  mono?: boolean
  className?: string
  title?: string
}) {
  return (
    <span
      title={title}
      className={cn(
        "bg-muted inline-flex h-5 shrink-0 items-center gap-1 rounded-md border px-1.5 text-[11px] leading-none font-medium whitespace-nowrap [&>svg]:size-3",
        mono && "font-mono tracking-[0.02em]",
        ACCENT[accent],
        className,
      )}
    >
      {children}
    </span>
  )
}

/** Accent for an HTTP method (GET, POST ...). */
export function methodAccent(method: string): TagAccent {
  switch (method.toUpperCase()) {
    case "GET":
      return "success"
    case "POST":
      return "info"
    case "PUT":
      return "warning"
    case "PATCH":
      return "violet"
    case "DELETE":
      return "danger"
    case "ANY":
      return "brand"
    default:
      return "neutral"
  }
}

/** Tone for an HTTP status code (2xx success ... 5xx danger). */
export function httpStatusTone(code: number): Tone {
  if (code >= 500) return "danger"
  if (code >= 400) return "warning"
  if (code >= 300) return "info"
  if (code >= 200) return "success"
  return "neutral"
}

/** Accent for a messaging/subscription protocol (SNS, EventBridge targets ...). */
export function protocolAccent(protocol: string): TagAccent {
  switch (protocol.toLowerCase()) {
    case "http":
    case "https":
      return "info"
    case "sqs":
      return "warning"
    case "lambda":
      return "brand"
    case "email":
    case "email-json":
      return "success"
    case "sms":
    case "application":
      return "violet"
    default:
      return "neutral"
  }
}

/** Accent for a DNS record type. */
export function recordTypeAccent(type: string): TagAccent {
  switch (type.toUpperCase()) {
    case "A":
    case "AAAA":
      return "info"
    case "CNAME":
      return "violet"
    case "ALIAS":
      return "brand"
    case "MX":
      return "warning"
    case "TXT":
    case "SPF":
      return "success"
    case "SRV":
    case "CAA":
      return "danger"
    default:
      return "neutral"
  }
}

/** Accent for a database engine. */
export function engineAccent(engine: string): TagAccent {
  const e = engine.toLowerCase()
  if (e.includes("postgres")) return "info"
  if (e.includes("mariadb")) return "violet"
  if (e.includes("mysql")) return "warning"
  if (e.includes("redis") || e.includes("valkey")) return "danger"
  if (e.includes("memcached")) return "success"
  return "neutral"
}
