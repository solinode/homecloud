import { Check } from "lucide-react"
import type { ReactNode } from "react"

import { cn } from "@/lib/utils"

export interface StepDef {
  label: ReactNode
  description?: ReactNode
}

/**
 * Stepper shows progress through a multi-step flow: numbered circles, done
 * steps get a check, the current step is brand-filled. Pass `onStepClick` to
 * let users jump back to completed steps (the flow decides what is allowed).
 * `orientation="vertical"` is the sidebar variant for full-page wizards.
 */
export function Stepper({
  steps,
  current,
  onStepClick,
  orientation = "horizontal",
  className,
}: {
  steps: (StepDef | string)[]
  current: number
  onStepClick?: (index: number) => void
  orientation?: "horizontal" | "vertical"
  className?: string
}) {
  const vertical = orientation === "vertical"
  return (
    <ol
      aria-label="Progress"
      className={cn(vertical ? "flex flex-col gap-1" : "flex flex-wrap items-center gap-x-2 gap-y-2", className)}
    >
      {steps.map((raw, i) => {
        const s: StepDef = typeof raw === "string" ? { label: raw } : raw
        const done = i < current
        const active = i === current
        const clickable = !!onStepClick && i !== current
        const inner = (
          <>
            <span
              className={cn(
                "flex size-6 shrink-0 items-center justify-center rounded-full border font-mono text-[11px] font-medium tabular-nums transition-colors",
                active && "border-brand bg-brand text-brand-foreground shadow-[0_0_0_3px_var(--brand-soft)]",
                done && "border-brand-line bg-brand-soft text-primary",
                !active && !done && "border-border-strong text-faint bg-card",
              )}
            >
              {done ? <Check className="size-3.5" strokeWidth={2.5} /> : i + 1}
            </span>
            <span className="flex min-w-0 flex-col text-left">
              <span className={cn("text-[13px] leading-tight", active ? "text-foreground font-medium" : done ? "text-foreground" : "text-muted-foreground")}>
                {s.label}
              </span>
              {vertical && s.description && <span className="text-muted-foreground mt-0.5 text-xs leading-snug">{s.description}</span>}
            </span>
          </>
        )
        return (
          <li key={i} className={cn("flex min-w-0 items-center", !vertical && "gap-2")} aria-current={active ? "step" : undefined}>
            {clickable ? (
              <button
                type="button"
                onClick={() => onStepClick?.(i)}
                className={cn("hover:bg-muted/70 flex min-w-0 items-center gap-2.5 rounded-md text-left transition-colors", vertical ? "w-full px-2 py-2" : "-mx-1 px-1 py-0.5")}
              >
                {inner}
              </button>
            ) : (
              <div className={cn("flex min-w-0 items-center gap-2.5", vertical ? "w-full px-2 py-2" : "py-0.5")}>{inner}</div>
            )}
            {!vertical && i < steps.length - 1 && <span aria-hidden className={cn("h-px w-6 shrink-0 sm:w-10", done ? "bg-brand-line" : "bg-border-strong")} />}
          </li>
        )
      })}
    </ol>
  )
}
