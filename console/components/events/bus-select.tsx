"use client"

import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select"
import { useApi } from "@/lib/hooks"
import type { EventBus } from "@/lib/types"
import { cn } from "@/lib/utils"

export const BUSES_PATH = "/api/v1/events/buses"

/** BusSelect picks an event bus ("default" is always present). */
export function BusSelect({
  value,
  onChange,
  id,
  className,
  size,
  disabled,
}: {
  value: string
  onChange: (bus: string) => void
  id?: string
  className?: string
  size?: "sm" | "default"
  disabled?: boolean
}) {
  const { data } = useApi<EventBus[]>(BUSES_PATH, { revalidateOnFocus: false })
  const names = Array.from(new Set(["default", ...(data ?? []).map((b) => b.name), value || "default"]))
  return (
    <Select value={value || "default"} onValueChange={onChange} disabled={disabled}>
      <SelectTrigger id={id} size={size} className={cn("w-full", className)} aria-label="Event bus">
        <SelectValue />
      </SelectTrigger>
      <SelectContent>
        {names.map((n) => (
          <SelectItem key={n} value={n}>
            {n}
          </SelectItem>
        ))}
      </SelectContent>
    </Select>
  )
}
