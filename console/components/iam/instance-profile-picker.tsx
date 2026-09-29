"use client"

import Link from "next/link"
import { ExternalLink, RefreshCw } from "lucide-react"

import { Button } from "@/components/ui/button"
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select"
import { useApi } from "@/lib/hooks"
import type { InstanceProfile } from "@/lib/types"
import { cn } from "@/lib/utils"

import { IAM, instanceProfileHref } from "./common"

const NONE = "__none__"

/** InstanceProfilePicker selects an IAM instance profile by name ("" = none). */
export function InstanceProfilePicker({ value, onChange, id }: { value: string; onChange: (name: string) => void; id?: string }) {
  const { data, isLoading, isValidating, error, mutate } = useApi<InstanceProfile[]>(`${IAM}/instance-profiles`)
  const profiles = [...(data ?? [])].sort((a, b) => a.name.localeCompare(b.name))
  const selected = profiles.find((p) => p.name === value)
  return (
    <div className="flex min-w-0 flex-col gap-1.5">
      <div className="flex min-w-0 items-center gap-1">
        <Select value={value || NONE} onValueChange={(v) => v && onChange(v === NONE ? "" : v)} disabled={isLoading}>
          <SelectTrigger id={id} className="w-full min-w-0" aria-label="IAM instance profile">
            <SelectValue placeholder={isLoading ? "Loading instance profiles..." : "No instance profile"}>{value || "No instance profile"}</SelectValue>
          </SelectTrigger>
          <SelectContent>
            <SelectItem value={NONE}>No instance profile</SelectItem>
            {value && !selected && <SelectItem value={value}>{value}</SelectItem>}
            {profiles.map((p) => (
              <SelectItem key={p.name} value={p.name}>
                <span className="flex min-w-0 flex-col">
                  <span className="truncate">{p.name}</span>
                  <span className="text-muted-foreground truncate text-xs">{p.roles.length ? `Role: ${p.roles.join(", ")}` : "No role"}</span>
                </span>
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
        <Button type="button" size="icon" variant="ghost" className="size-9 shrink-0" onClick={() => mutate()} disabled={isValidating} aria-label="Refresh instance profiles">
          <RefreshCw className={cn(isValidating && "animate-spin")} />
        </Button>
      </div>
      <div className="text-muted-foreground flex flex-wrap items-center gap-x-3 gap-y-1 text-xs">
        <Link href="/iam/instance-profiles/" target="_blank" className="text-primary inline-flex items-center gap-1 hover:underline">
          Create instance profile <ExternalLink className="size-3" />
        </Link>
        {selected && (
          <Link href={instanceProfileHref(selected.name)} target="_blank" className="text-primary inline-flex items-center gap-1 hover:underline">
            View {selected.name} <ExternalLink className="size-3" />
          </Link>
        )}
      </div>
      {error && <p className="text-destructive text-xs">Cannot list instance profiles: {error.message}</p>}
      {selected && !selected.roles.length && <p className="text-xs text-amber-700 dark:text-amber-300">{selected.name} has no role, so the instance gets no credentials.</p>}
    </div>
  )
}
