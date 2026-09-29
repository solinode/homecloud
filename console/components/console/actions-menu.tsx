"use client"

import type { ReactNode } from "react"
import { ChevronDown } from "lucide-react"

import { Button } from "@/components/ui/button"
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu"

export type ActionItem =
  | { label: string; onSelect: () => void; disabled?: boolean; destructive?: boolean; icon?: ReactNode; hint?: string }
  | { separator: true }
  | { heading: string }

/** ActionsMenu is the "Actions ▾" dropdown of a resource toolbar. */
export function ActionsMenu({ items, label = "Actions", disabled, size = "sm" }: { items: ActionItem[]; label?: string; disabled?: boolean; size?: "sm" | "default" }) {
  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <Button variant="outline" size={size} disabled={disabled}>
          {label} <ChevronDown className="opacity-60" />
        </Button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end" className="min-w-52">
        {items.map((it, i) => {
          if ("separator" in it) return <DropdownMenuSeparator key={`sep-${i}`} />
          if ("heading" in it)
            return (
              <DropdownMenuLabel key={`h-${i}`} className="text-muted-foreground text-xs">
                {it.heading}
              </DropdownMenuLabel>
            )
          return (
            <DropdownMenuItem
              key={it.label}
              disabled={it.disabled}
              variant={it.destructive ? "destructive" : "default"}
              onSelect={() => it.onSelect()}
              title={it.hint}
            >
              {it.icon}
              {it.label}
            </DropdownMenuItem>
          )
        })}
      </DropdownMenuContent>
    </DropdownMenu>
  )
}
