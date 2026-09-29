"use client"

import Link from "next/link"
import { useMemo, useState } from "react"
import { ExternalLink, RefreshCw } from "lucide-react"

import { Button } from "@/components/ui/button"
import { Checkbox } from "@/components/ui/checkbox"
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select"
import { useApi } from "@/lib/hooks"
import type { IamRole } from "@/lib/types"
import { cn } from "@/lib/utils"

import { IAM } from "./common"
import { createRoleHref, roleHref, serviceName } from "./role-common"

const NONE = "__none__"

export interface RolePickerProps {
  /** The selected role: its ARN (default) or name, per `valueType`. "" = none. */
  value: string
  onChange: (value: string, role: IamRole | null) => void
  /** Only offer roles whose trust policy allows this service principal, e.g. "lambda.amazonaws.com". */
  service?: string
  valueType?: "arn" | "name"
  /** Offer a "No role" choice. */
  allowNone?: boolean
  id?: string
  placeholder?: string
  disabled?: boolean
  invalid?: boolean
  className?: string
}

/**
 * RolePicker selects an existing IAM role, optionally only roles that trust a
 * service principal, with a link to create one (opens in a new tab; the
 * refresh button picks it up). For service forms: Lambda execution role, ECS
 * task role, EC2 instance profile, Step Functions, EventBridge targets...
 */
export function RolePicker({ value, onChange, service, valueType = "arn", allowNone, id, placeholder = "Choose a role", disabled, invalid, className }: RolePickerProps) {
  const { data, isLoading, isValidating, error, mutate } = useApi<IamRole[]>(`${IAM}/roles`)
  const [showAll, setShowAll] = useState(false)
  const key = (r: IamRole) => (valueType === "arn" ? r.arn : r.name)

  const roles = useMemo(() => {
    const all = [...(data ?? [])].sort((a, b) => a.name.localeCompare(b.name))
    if (!service || showAll) return all
    return all.filter((r) => r.trusted_services?.includes(service) || (valueType === "arn" ? r.arn : r.name) === value)
  }, [data, service, showAll, value, valueType])

  const selected = (data ?? []).find((r) => key(r) === value) ?? null
  const unknown = !!value && !!data && !selected
  const selectedDoesNotTrust = !!selected && !!service && !selected.trusted_services?.includes(service)

  return (
    <div className={cn("flex min-w-0 flex-col gap-1.5", className)}>
      <div className="flex min-w-0 items-center gap-1">
        <Select
          value={value || (allowNone ? NONE : "")}
          onValueChange={(v) => {
            if (v === NONE) return onChange("", null)
            const r = (data ?? []).find((x) => key(x) === v) ?? null
            onChange(v, r)
          }}
          disabled={disabled || isLoading}
        >
          <SelectTrigger id={id} className="w-full min-w-0" aria-invalid={invalid} aria-label="IAM role">
            <SelectValue placeholder={isLoading ? "Loading roles..." : placeholder}>{selected ? selected.name : value || (allowNone ? "No role" : "")}</SelectValue>
          </SelectTrigger>
          <SelectContent>
            {allowNone && <SelectItem value={NONE}>No role</SelectItem>}
            {unknown && <SelectItem value={value}>{value}</SelectItem>}
            {roles.map((r) => (
              <SelectItem key={r.name} value={key(r)}>
                <span className="flex min-w-0 flex-col">
                  <span className="truncate">{r.name}</span>
                  {r.description && <span className="text-muted-foreground truncate text-xs">{r.description}</span>}
                </span>
              </SelectItem>
            ))}
            {!roles.length && !allowNone && !unknown && (
              <div className="text-muted-foreground px-2 py-3 text-center text-sm">{service && !showAll ? `No roles trust ${serviceName(service)}.` : "No roles yet."}</div>
            )}
          </SelectContent>
        </Select>
        <Button type="button" size="icon" variant="ghost" className="size-9 shrink-0" onClick={() => mutate()} disabled={isValidating} aria-label="Refresh roles">
          <RefreshCw className={cn(isValidating && "animate-spin")} />
        </Button>
      </div>
      <div className="text-muted-foreground flex flex-wrap items-center gap-x-3 gap-y-1 text-xs">
        {service && (
          <label className="flex items-center gap-1.5">
            <Checkbox checked={showAll} onCheckedChange={(v) => setShowAll(v === true)} className="size-3.5" />
            Show roles that do not trust {serviceName(service)}
          </label>
        )}
        <Link href={createRoleHref(service)} target="_blank" className="text-primary inline-flex items-center gap-1 hover:underline">
          Create role <ExternalLink className="size-3" />
        </Link>
        {selected && (
          <Link href={roleHref(selected.name)} target="_blank" className="text-primary inline-flex items-center gap-1 hover:underline">
            View {selected.name} <ExternalLink className="size-3" />
          </Link>
        )}
      </div>
      {error && <p className="text-destructive text-xs">Cannot list roles: {error.message}</p>}
      {selectedDoesNotTrust && (
        <p className="text-xs text-amber-700 dark:text-amber-300">
          {selected!.name} does not trust {service}. Add the service to its trust policy or the service cannot assume it.
        </p>
      )}
    </div>
  )
}
