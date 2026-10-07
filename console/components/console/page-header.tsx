import Link from "next/link"
import { ChevronRight } from "lucide-react"
import type { ReactNode } from "react"

import { cn } from "@/lib/utils"

export interface Crumb {
  label: string
  href?: string
}

/** Breadcrumbs renders "EC2 › Instances › i-123". */
export function Breadcrumbs({ items, className }: { items: Crumb[]; className?: string }) {
  return (
    <nav aria-label="Breadcrumb" className={cn("text-muted-foreground flex min-w-0 flex-wrap items-center gap-1 text-[13px]", className)}>
      {items.map((c, i) => {
        const last = i === items.length - 1
        return (
          <span key={`${c.label}-${i}`} className="flex min-w-0 items-center gap-1">
            {i > 0 && <ChevronRight className="text-faint size-3.5 shrink-0" />}
            {c.href && !last ? (
              <Link href={c.href} className="hover:text-foreground truncate transition-colors">
                {c.label}
              </Link>
            ) : (
              <span className={cn("truncate", last && "text-foreground font-medium")} aria-current={last ? "page" : undefined}>
                {c.label}
              </span>
            )}
          </span>
        )
      })}
    </nav>
  )
}

/**
 * PageHeader is the title row of every page: breadcrumbs, title (+ badge),
 * description, and actions (primary action last, rightmost).
 */
export function PageHeader({
  title,
  description,
  breadcrumbs,
  actions,
  badge,
  className,
}: {
  title: ReactNode
  description?: ReactNode
  breadcrumbs?: Crumb[]
  actions?: ReactNode
  badge?: ReactNode
  className?: string
}) {
  return (
    <div className={cn("mb-6 flex flex-col gap-3 lg:mb-8", className)}>
      {breadcrumbs && breadcrumbs.length > 0 && <Breadcrumbs items={breadcrumbs} />}
      <div className="flex flex-col gap-4 sm:flex-row sm:items-end sm:justify-between">
        <div className="min-w-0">
          <div className="flex flex-wrap items-center gap-x-3 gap-y-2">
            <h1 className="min-w-0 truncate text-[22px] leading-tight font-semibold tracking-[-0.03em] sm:text-[26px]">{title}</h1>
            {badge}
          </div>
          {description && <p className="text-muted-foreground mt-1.5 max-w-3xl text-sm leading-relaxed text-pretty">{description}</p>}
        </div>
        {actions && <div className="flex shrink-0 flex-wrap items-center gap-2">{actions}</div>}
      </div>
    </div>
  )
}
