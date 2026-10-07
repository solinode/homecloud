import { cn } from "@/lib/utils"

export type Tone = "success" | "warning" | "neutral" | "danger" | "info"

/** Badge fills and text per tone, from the semantic tokens in globals.css. */
export const TONES: Record<Tone, string> = {
  success: "text-success bg-success-soft border-success/20",
  warning: "text-warning bg-warning-soft border-warning/25",
  neutral: "text-muted-foreground bg-muted border-border-strong",
  danger: "text-danger bg-danger-soft border-danger/25",
  info: "text-info bg-info-soft border-info/20",
}

/** Dot colors per tone (for inline dots without the pill). */
export const DOTS: Record<Tone, string> = {
  success: "bg-success",
  warning: "bg-warning",
  neutral: "bg-faint",
  danger: "bg-danger",
  info: "bg-info",
}


const STATUS_TONE: Record<string, Tone> = {
  running: "success",
  available: "success",
  active: "success",
  enabled: "success",
  ok: "success",
  success: "success",
  allowed: "success",
  "in-use": "info",
  pending: "warning",
  stopping: "warning",
  "shutting-down": "warning",
  starting: "warning",
  rebooting: "warning",
  insufficient_data: "neutral",
  stopped: "neutral",
  inactive: "neutral",
  suspended: "neutral",
  disabled: "neutral",
  implicitdeny: "neutral",
  terminated: "danger",
  failed: "danger",
  alarm: "danger",
  error: "danger",
  explicitdeny: "danger",
  "scheduled for deletion": "danger",
  creating: "warning",
  deleting: "warning",
  modifying: "warning",
  restoring: "warning",
  "backing-up": "warning",
  updating: "warning",
  confirmed: "success",
  "in-flight": "info",
  delayed: "neutral",
}

const TRANSITIONAL = new Set([
  "pending",
  "stopping",
  "shutting-down",
  "starting",
  "rebooting",
  "creating",
  "deleting",
  "modifying",
  "restoring",
  "backing-up",
  "updating",
])

/** Human labels for machine states that don't read well when just capitalized. */
const STATUS_LABEL: Record<string, string> = {
  implicitdeny: "Implicitly denied",
  explicitdeny: "Explicitly denied",
  insufficient_data: "Insufficient data",
  "in-use": "In use",
  "shutting-down": "Shutting down",
  "backing-up": "Backing up",
  "in-flight": "In flight",
}

/**
 * statusLabel turns a raw state into display text: known machine states get a
 * fixed label; lower-case/snake/kebab words become sentence case ("shutting
 * down" -> "Shutting down"); anything already human (mixed case, e.g.
 * "HomeCloud managed") is left untouched.
 */
export function statusLabel(status: string): string {
  const known = STATUS_LABEL[status.toLowerCase()]
  if (known) return known
  if (status !== status.toLowerCase() && status !== status.toUpperCase()) return status
  const words = status.toLowerCase().replace(/[_]+/g, " ").trim()
  return words.charAt(0).toUpperCase() + words.slice(1)
}

export function statusTone(status: string): Tone {
  return STATUS_TONE[status.toLowerCase()] ?? "neutral"
}


/** StatusDot is the colored state dot; transitional states pulse. */
export function StatusDot({ tone, pulse, className }: { tone: Tone; pulse?: boolean; className?: string }) {
  return (
    <span className={cn("relative inline-flex size-1.5 shrink-0", className)} aria-hidden>
      {pulse && <span className={cn("absolute inset-0 animate-ping rounded-full opacity-60", DOTS[tone])} />}
      <span className={cn("relative inline-flex size-1.5 rounded-full", DOTS[tone], tone !== "neutral" && "shadow-[0_0_6px_currentColor]")} />
    </span>
  )
}

/**
 * StatusBadge renders a state pill with a dot: running=green, pending/stopping=amber
 * (pulsing), stopped=gray, terminated/failed=red. `tone` overrides the mapping.
 */
export function StatusBadge({ status, label, tone, className }: { status: string; label?: string; tone?: Tone; className?: string }) {
  const t = tone ?? statusTone(status)
  const text = label ?? statusLabel(status)
  return (
    <span
      className={cn(
        "inline-flex h-[22px] items-center gap-1.5 rounded-full border px-2 text-xs font-medium whitespace-nowrap",
        TONES[t],
        className,
      )}
    >
      <StatusDot tone={t} pulse={TRANSITIONAL.has(status.toLowerCase())} />
      <span>{text}</span>
    </span>
  )
}
