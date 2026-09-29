"use client"

import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip"
import { formatDate, timeAgo } from "@/lib/format"
import { useNow } from "@/lib/hooks"

/** TimeAgo renders a relative time with the absolute timestamp in a tooltip. */
export function TimeAgo({ value, className }: { value: string | number | null | undefined; className?: string }) {
  const now = useNow(30_000)
  if (!value) return <span className="text-muted-foreground">-</span>
  return (
    <Tooltip>
      <TooltipTrigger asChild>
        <span className={className ?? "whitespace-nowrap"}>{timeAgo(value, now)}</span>
      </TooltipTrigger>
      <TooltipContent>{formatDate(value)}</TooltipContent>
    </Tooltip>
  )
}
