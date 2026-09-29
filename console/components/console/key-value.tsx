import type { ReactNode } from "react"

import { cn } from "@/lib/utils"

export interface KV {
  label: string
  value: ReactNode
  /** span the full row */
  wide?: boolean
}

/** KeyValueGrid is the AWS-style "Details" block: labelled values in columns. */
export function KeyValueGrid({ items, columns = 3, className }: { items: KV[]; columns?: 2 | 3 | 4; className?: string }) {
  const cols = { 2: "sm:grid-cols-2", 3: "sm:grid-cols-2 lg:grid-cols-3", 4: "sm:grid-cols-2 lg:grid-cols-4" }[columns]
  return (
    <dl className={cn("grid grid-cols-1 gap-x-8 gap-y-4", cols, className)}>
      {items.map((it) => (
        <div key={it.label} className={cn("min-w-0", it.wide && "sm:col-span-full")}>
          <dt className="text-muted-foreground mb-0.5 text-xs font-medium">{it.label}</dt>
          <dd className="min-w-0 text-sm break-words">{it.value === "" || it.value === null || it.value === undefined ? <span className="text-muted-foreground">-</span> : it.value}</dd>
        </div>
      ))}
    </dl>
  )
}
