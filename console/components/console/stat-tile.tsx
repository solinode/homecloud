import Link from "next/link"
import type { ReactNode } from "react"

import { Skeleton } from "@/components/ui/skeleton"
import { StatusDot, type Tone } from "@/components/console/status-badge"
import { cn } from "@/lib/utils"

/**
 * StatTile is a KPI card: small mono label, large tight number, a muted
 * caption. `tone` adds a status dot to the label; `href` makes it a link.
 */
export function StatTile({
  label,
  value,
  unit,
  caption,
  tone,
  href,
  loading,
  icon,
  className,
}: {
  label: ReactNode
  value: ReactNode
  unit?: ReactNode
  caption?: ReactNode
  tone?: Tone
  href?: string
  loading?: boolean
  icon?: ReactNode
  className?: string
}) {
  const body = (
    <>
      <div className="flex items-center justify-between gap-2">
        <span className="hc-eyebrow flex items-center gap-2">
          {tone && <StatusDot tone={tone} />}
          {label}
        </span>
        {icon && <span className="text-faint [&_svg]:size-4">{icon}</span>}
      </div>
      {loading ? (
        <Skeleton className="mt-4 h-8 w-20" />
      ) : (
        <p className="mt-3 flex items-baseline gap-1.5 text-[30px] leading-none font-semibold tracking-[-0.045em] tabular-nums">
          {value}
          {unit && <span className="text-faint text-base font-medium tracking-[-0.02em]">{unit}</span>}
        </p>
      )}
      {caption && <p className="text-muted-foreground mt-2.5 truncate text-[13px]">{caption}</p>}
    </>
  )
  const cls = cn("bg-card relative block rounded-xl border p-5 shadow-xs", href && "hover:border-border-strong transition-colors", className)
  return href ? (
    <Link href={href} className={cls}>
      {body}
    </Link>
  ) : (
    <div className={cls}>{body}</div>
  )
}
