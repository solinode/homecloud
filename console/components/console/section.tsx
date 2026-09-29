import type { ReactNode } from "react"

import { cn } from "@/lib/utils"

/** Section is a titled card, the building block of detail pages. */
export function Section({
  title,
  description,
  actions,
  children,
  className,
  bodyClassName,
  flush,
}: {
  title?: ReactNode
  description?: ReactNode
  actions?: ReactNode
  children: ReactNode
  className?: string
  bodyClassName?: string
  /** no body padding (for tables) */
  flush?: boolean
}) {
  return (
    <section className={cn("bg-card text-card-foreground rounded-lg border shadow-xs", className)}>
      {(title || actions) && (
        <header className="flex flex-col gap-2 border-b px-4 py-3 sm:flex-row sm:items-center sm:justify-between">
          <div className="min-w-0">
            {title && <h2 className="text-base font-semibold">{title}</h2>}
            {description && <p className="text-muted-foreground mt-0.5 text-sm">{description}</p>}
          </div>
          {actions && <div className="flex shrink-0 flex-wrap items-center gap-2">{actions}</div>}
        </header>
      )}
      <div className={cn(!flush && "p-4", bodyClassName)}>{children}</div>
    </section>
  )
}
