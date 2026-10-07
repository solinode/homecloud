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
    <section className={cn("bg-card text-card-foreground rounded-xl border shadow-xs", className)}>
      {(title || actions) && (
        <header className="flex flex-col gap-2 border-b px-5 py-3.5 sm:flex-row sm:items-center sm:justify-between">
          <div className="min-w-0">
            {title && <h2 className="text-[15px] font-semibold tracking-[-0.015em]">{title}</h2>}
            {description && <p className="text-muted-foreground mt-0.5 text-[13px]">{description}</p>}
          </div>
          {actions && <div className="flex shrink-0 flex-wrap items-center gap-2">{actions}</div>}
        </header>
      )}
      <div className={cn(!flush && "p-5", bodyClassName)}>{children}</div>
    </section>
  )
}
