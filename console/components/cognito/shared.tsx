"use client"

import { Checkbox } from "@/components/ui/checkbox"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { Switch } from "@/components/ui/switch"
import { Field } from "@/components/console/form-field"
import { StatusBadge } from "@/components/console/status-badge"
import { seg } from "@/lib/api"
import { useApi } from "@/lib/hooks"
import type { PasswordPolicy, UserPool } from "@/lib/types"

export const COGNITO_PATH = "/api/v1/cognito"
export const POOLS_PATH = `${COGNITO_PATH}/user-pools`
export const poolPath = (id: string) => `${POOLS_PATH}/${seg(id)}`

export const poolHref = (id: string, tab?: string) => `/cognito/pool/?id=${encodeURIComponent(id)}${tab ? `&tab=${tab}` : ""}`

export function usePools() {
  return useApi<UserPool[]>(POOLS_PATH, { refreshInterval: 30_000 })
}

/** Mirrors poolName in cognito.go (also used for group names). */
export const NAME_RE = /^[\w\s+=,.@-]{1,128}$/

export const DEFAULT_POLICY: PasswordPolicy = { min_length: 8, require_uppercase: false, require_lowercase: true, require_numbers: true, require_symbols: false }

export function policySummary(p: PasswordPolicy): string {
  const req = [p.require_uppercase && "uppercase", p.require_lowercase && "lowercase", p.require_numbers && "numbers", p.require_symbols && "symbols"].filter(Boolean)
  return `At least ${p.min_length} characters${req.length ? `, with ${req.join(", ")}` : ""}`
}

/** passwordProblems mirrors PasswordPolicy.check in cognito.go (symbols = anything but letters and digits). */
export function passwordProblems(pw: string, p: PasswordPolicy): string | null {
  const missing: string[] = []
  if ([...pw].length < p.min_length) missing.push(`at least ${p.min_length} characters`)
  if (p.require_uppercase && !/\p{Lu}/u.test(pw)) missing.push("an uppercase letter")
  if (p.require_lowercase && !/\p{Ll}/u.test(pw)) missing.push("a lowercase letter")
  if (p.require_numbers && !/\p{Nd}/u.test(pw)) missing.push("a number")
  if (p.require_symbols && !/[^\p{L}\p{Nd}]/u.test(pw)) missing.push("a symbol")
  return missing.length ? `Needs ${missing.join(", ")}` : null
}

const STATUS: Record<string, { label: string; tone: "success" | "warning" | "neutral" }> = {
  CONFIRMED: { label: "Confirmed", tone: "success" },
  UNCONFIRMED: { label: "Unconfirmed", tone: "warning" },
  FORCE_CHANGE_PASSWORD: { label: "Force change password", tone: "warning" },
}

export function UserStatusBadge({ status }: { status: string }) {
  const s = STATUS[status] ?? { label: status, tone: "neutral" as const }
  return <StatusBadge status={status.toLowerCase()} label={s.label} tone={s.tone} />
}

export function EnabledBadge({ enabled }: { enabled: boolean }) {
  return enabled ? <StatusBadge status="enabled" label="Enabled" /> : <StatusBadge status="disabled" label="Disabled" />
}

/** SignUpBadge shows a pool's self sign-up mode: auto-confirmed, needs confirmation, or disabled. */
export function SignUpBadge({ pool }: { pool: Pick<UserPool, "self_sign_up" | "auto_confirm"> }) {
  if (!pool.self_sign_up) return <StatusBadge status="disabled" label="Disabled" tone="neutral" />
  return pool.auto_confirm ? (
    <StatusBadge status="auto-confirmed" label="Auto-confirmed" tone="success" />
  ) : (
    <StatusBadge status="needs-confirmation" label="Needs confirmation" tone="warning" />
  )
}

/** PasswordPolicyFields edits a password policy (min length 6-128 and the four character classes). */
export function PasswordPolicyFields({ value, onChange, idPrefix, error }: { value: PasswordPolicy; onChange: (p: PasswordPolicy) => void; idPrefix: string; error?: string }) {
  const boxes: [keyof PasswordPolicy, string][] = [
    ["require_uppercase", "Uppercase letters"],
    ["require_lowercase", "Lowercase letters"],
    ["require_numbers", "Numbers"],
    ["require_symbols", "Symbols"],
  ]
  return (
    <div className="flex flex-col gap-3">
      <Field label="Minimum length" htmlFor={`${idPrefix}-min`} error={error} help="6-128 characters.">
        <Input
          id={`${idPrefix}-min`}
          type="number"
          min={6}
          max={128}
          value={Number.isNaN(value.min_length) ? "" : value.min_length}
          onChange={(e) => onChange({ ...value, min_length: e.target.value === "" ? NaN : Number(e.target.value) })}
          className="w-28"
        />
      </Field>
      <Field label="Require" help="Character classes every new password must contain.">
        <div className="grid grid-cols-1 gap-2 min-[400px]:grid-cols-2">
          {boxes.map(([k, label]) => (
            <label key={k} className="flex items-center gap-2 text-sm">
              <Checkbox checked={!!value[k]} onCheckedChange={(c) => onChange({ ...value, [k]: !!c })} />
              {label}
            </label>
          ))}
        </div>
      </Field>
    </div>
  )
}

export const minLengthError = (n: number) => (!Number.isInteger(n) || n < 6 || n > 99 ? "Enter a whole number from 6 to 99" : undefined)

/** SwitchRow is a labelled switch with a description (settings toggles). */
export function SwitchRow({ id, label, description, checked, onChange, disabled }: { id: string; label: string; description: string; checked: boolean; onChange: (v: boolean) => void; disabled?: boolean }) {
  return (
    <div className="flex items-start justify-between gap-4 rounded-md border p-3">
      <div>
        <Label htmlFor={id} className="font-medium">
          {label}
        </Label>
        <p className="text-muted-foreground mt-0.5 text-xs">{description}</p>
      </div>
      <Switch id={id} checked={checked} onCheckedChange={onChange} disabled={disabled} />
    </div>
  )
}
