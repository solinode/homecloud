import Link from "next/link"
import { ChevronRight } from "lucide-react"
import type { ReactNode } from "react"

import { cn } from "@/lib/utils"

export interface Crumb {
  label: string
  href?: string
}

/** Breadcrumbs renders "EC2 > Instances > i-123". */
export function Breadcrumbs({ items, className }: { items: Crumb[]; className?: string }) {
  return (
    <nav aria-label="Breadcrumb" className={cn("text-muted-foreground flex min-w-0 flex-wrap items-center gap-1 text-sm", className)}>
      {items.map((c, i) => (
        <span key={`${c.label}-${i}`} className="flex min-w-0 items-center gap-1">
          {i > 0 && <ChevronRight className="size-3.5 shrink-0 opacity-60" />}
          {c.href && i < items.length - 1 ? (
            <Link href={c.href} className="text-primary truncate hover:underline">
              {c.label}
            </Link>
          ) : (
            <span className={cn("truncate", i === items.length - 1 && "text-foreground")}>{c.label}</span>
          )}
        </span>
      ))}
    </nav>
  )
}

/** PageHeader is the title row of every page: breadcrumbs, title, description, actions. */
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
    <div className={cn("mb-5 flex flex-col gap-2", className)}>
      {breadcrumbs && breadcrumbs.length > 0 && <Breadcrumbs items={breadcrumbs} />}
      <div className="flex flex-col gap-3 sm:flex-row sm:items-start sm:justify-between">
        <div className="min-w-0">
          <div className="flex flex-wrap items-center gap-2">
            <h1 className="truncate text-xl font-semibold tracking-tight sm:text-2xl">{title}</h1>
            {badge}
          </div>
          {description && <p className="text-muted-foreground mt-1 max-w-3xl text-sm">{description}</p>}
        </div>
        {actions && <div className="flex shrink-0 flex-wrap items-center gap-2">{actions}</div>}
      </div>
    </div>
  )
}
