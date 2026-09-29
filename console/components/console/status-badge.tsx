import { CheckCircle2, CircleDashed, Loader2, MinusCircle, XCircle, AlertTriangle, Circle } from "lucide-react"

import { cn } from "@/lib/utils"

type Tone = "success" | "warning" | "neutral" | "danger" | "info"

const TONES: Record<Tone, string> = {
  success: "text-emerald-700 bg-emerald-50 ring-emerald-600/20 dark:text-emerald-300 dark:bg-emerald-500/10 dark:ring-emerald-400/20",
  warning: "text-amber-700 bg-amber-50 ring-amber-600/20 dark:text-amber-300 dark:bg-amber-500/10 dark:ring-amber-400/20",
  neutral: "text-slate-600 bg-slate-100 ring-slate-500/20 dark:text-slate-300 dark:bg-slate-500/15 dark:ring-slate-400/20",
  danger: "text-red-700 bg-red-50 ring-red-600/20 dark:text-red-300 dark:bg-red-500/10 dark:ring-red-400/20",
  info: "text-blue-700 bg-blue-50 ring-blue-600/20 dark:text-blue-300 dark:bg-blue-500/10 dark:ring-blue-400/20",
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

function StatusIcon({ tone, spinning }: { tone: Tone; spinning: boolean }) {
  const cls = "size-3.5 shrink-0"
  if (spinning) return <Loader2 className={cn(cls, "animate-spin")} />
  switch (tone) {
    case "success":
      return <CheckCircle2 className={cls} />
    case "danger":
      return <XCircle className={cls} />
    case "warning":
      return <AlertTriangle className={cls} />
    case "info":
      return <Circle className={cn(cls, "fill-current")} />
    default:
      return tone === "neutral" ? <MinusCircle className={cls} /> : <CircleDashed className={cls} />
  }
}

/**
 * StatusBadge renders a colored state pill: running=green, pending/stopping=amber,
 * stopped=gray, terminated=red. `tone` overrides the automatic mapping.
 */
export function StatusBadge({ status, label, tone, className }: { status: string; label?: string; tone?: Tone; className?: string }) {
  const t = tone ?? statusTone(status)
  const text = label ?? statusLabel(status)
  return (
    <span
      className={cn(
        "inline-flex items-center gap-1 rounded-full px-2 py-0.5 text-xs font-medium whitespace-nowrap ring-1 ring-inset",
        TONES[t],
        className,
      )}
    >
      <StatusIcon tone={t} spinning={TRANSITIONAL.has(status.toLowerCase())} />
      <span>{text}</span>
    </span>
  )
}
