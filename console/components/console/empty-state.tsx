import type { LucideIcon } from "lucide-react"
import { Inbox } from "lucide-react"
import type { ReactNode } from "react"

import { cn } from "@/lib/utils"

/** EmptyState is shown when a list has no resources, with a call to action. */
export function EmptyState({
  icon: Icon = Inbox,
  title,
  description,
  action,
  className,
}: {
  icon?: LucideIcon
  title: string
  description?: ReactNode
  action?: ReactNode
  className?: string
}) {
  return (
    <div className={cn("flex flex-col items-center justify-center gap-1.5 px-6 py-14 text-center", className)}>
      <div className="bg-muted text-muted-foreground relative mb-3 flex size-11 items-center justify-center rounded-xl border shadow-xs">
        <Icon className="size-5" strokeWidth={1.75} />
      </div>
      <p className="text-[15px] font-medium tracking-[-0.01em]">{title}</p>
      {description && <p className="text-muted-foreground max-w-md text-sm text-pretty">{description}</p>}
      {action && <div className="mt-4 flex flex-wrap items-center justify-center gap-2">{action}</div>}
    </div>
  )
}
