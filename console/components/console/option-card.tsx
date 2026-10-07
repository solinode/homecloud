import { Check } from "lucide-react"
import type { LucideIcon } from "lucide-react"
import type { ReactNode } from "react"

import { cn } from "@/lib/utils"

/**
 * OptionCard is a large radio choice (Container vs VM, trusted entity type,
 * engine ...). Put several in an OptionGroup. Selected = brand ring + check.
 */
export function OptionCard({
  selected,
  onSelect,
  title,
  description,
  icon: Icon,
  badge,
  disabled,
  className,
  children,
}: {
  selected: boolean
  onSelect: () => void
  title: ReactNode
  description?: ReactNode
  icon?: LucideIcon
  /** small adornment next to the title (a Tag, "Recommended" ...) */
  badge?: ReactNode
  disabled?: boolean
  className?: string
  children?: ReactNode
}) {
  return (
    <button
      type="button"
      role="radio"
      aria-checked={selected}
      disabled={disabled}
      onClick={onSelect}
      className={cn(
        "bg-card relative flex w-full items-start gap-3 rounded-lg border p-3.5 text-left shadow-xs transition-[border-color,background-color,box-shadow] disabled:cursor-not-allowed disabled:opacity-50",
        selected ? "border-brand bg-brand-soft ring-brand/40 ring-1" : "hover:border-border-strong hover:bg-muted/50",
        className,
      )}
    >
      {Icon && (
        <span
          className={cn(
            "flex size-8 shrink-0 items-center justify-center rounded-md border",
            selected ? "border-brand-line bg-card text-primary" : "bg-muted text-muted-foreground",
          )}
        >
          <Icon className="size-4" strokeWidth={1.75} />
        </span>
      )}
      <span className="flex min-w-0 flex-1 flex-col gap-0.5 pr-6">
        <span className="flex flex-wrap items-center gap-2 text-sm font-medium">
          {title}
          {badge}
        </span>
        {description && <span className="text-muted-foreground text-xs leading-relaxed">{description}</span>}
        {children}
      </span>
      <span
        aria-hidden
        className={cn(
          "absolute top-3 right-3 flex size-4 items-center justify-center rounded-full border transition-colors",
          selected ? "border-brand bg-brand text-brand-foreground" : "border-border-strong bg-card",
        )}
      >
        {selected && <Check className="size-3" strokeWidth={3} />}
      </span>
    </button>
  )
}

/** OptionGroup lays out OptionCards as an accessible radiogroup grid. */
export function OptionGroup({
  label,
  columns = 2,
  className,
  children,
}: {
  label: string
  columns?: 1 | 2 | 3 | 4
  className?: string
  children: ReactNode
}) {
  const cols = { 1: "", 2: "sm:grid-cols-2", 3: "sm:grid-cols-2 lg:grid-cols-3", 4: "sm:grid-cols-2 lg:grid-cols-4" }[columns]
  return (
    <div role="radiogroup" aria-label={label} className={cn("grid grid-cols-1 gap-3", cols, className)}>
      {children}
    </div>
  )
}
