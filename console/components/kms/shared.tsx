"use client"

import { useState } from "react"
import { Lock } from "lucide-react"
import { toast } from "sonner"

import { Input } from "@/components/ui/input"
import { Select, SelectContent, SelectGroup, SelectItem, SelectLabel, SelectTrigger, SelectValue } from "@/components/ui/select"
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip"
import { ConfirmDialog } from "@/components/console/confirm-dialog"
import { Field } from "@/components/console/form-field"
import { StatusBadge, type Tone } from "@/components/console/status-badge"
import { Tag, type TagAccent } from "@/components/console/tag"
import { api, apiUrl, errorMessage, seg } from "@/lib/api"
import { formatDate } from "@/lib/format"
import { revalidate, useApi } from "@/lib/hooks"
import type { KmsKey } from "@/lib/types"
import { cn } from "@/lib/utils"

/** apiOrigin is the API origin for AWS CLI snippets (--endpoint-url). */
export function apiOrigin(): string {
  if (typeof window === "undefined") return ""
  return new URL(apiUrl("/"), window.location.origin).origin
}

export const KMS_PATH = "/api/v1/kms"
export const KEYS_PATH = `${KMS_PATH}/keys`

export const keyHref = (id: string, tab?: string) => `/kms/key/?id=${encodeURIComponent(id)}${tab ? `&tab=${tab}` : ""}`

/** keyIdFromArn extracts the key id from a key ARN ("arn:aws:kms:...:key/<id>"); other refs pass through. */
export function keyIdFromArn(ref: string): string {
  const i = ref.indexOf(":key/")
  return i >= 0 ? ref.slice(i + 5) : ref
}

export const isServiceManaged = (k: Pick<KmsKey, "managed" | "aliases">) =>
  k.managed || (k.aliases ?? []).some((a) => a.startsWith("alias/hc/") || a.startsWith("alias/aws/"))

// ---- key types ----

export const SYMMETRIC_SPEC = "SYMMETRIC_DEFAULT"

/** isSymmetric: a SYMMETRIC_DEFAULT encryption key (the only kind that encrypts via the native API, seals secrets/parameters and rotates). */
export const isSymmetric = (k: Pick<KmsKey, "key_spec" | "key_usage">) => (k.key_spec || SYMMETRIC_SPEC) === SYMMETRIC_SPEC && (k.key_usage || "ENCRYPT_DECRYPT") === "ENCRYPT_DECRYPT"

export type KeyKind = "symmetric" | "asymmetric" | "hmac"

export function keyKind(spec: string): KeyKind {
  if (!spec || spec === SYMMETRIC_SPEC) return "symmetric"
  if (spec.startsWith("HMAC_")) return "hmac"
  return "asymmetric"
}

export const KIND_LABEL: Record<KeyKind, string> = { symmetric: "Symmetric", asymmetric: "Asymmetric", hmac: "HMAC" }

export const USAGE_LABEL: Record<string, string> = {
  ENCRYPT_DECRYPT: "Encrypt and decrypt",
  SIGN_VERIFY: "Sign and verify",
  GENERATE_VERIFY_MAC: "Generate and verify MAC",
}

/** KIND_ACCENT tints key-type / key-spec tags: symmetric=brand, asymmetric=info, HMAC=violet. */
export const KIND_ACCENT: Record<KeyKind, TagAccent> = { symmetric: "brand", asymmetric: "info", hmac: "violet" }

/** USAGE_ACCENT tints key-usage tags. */
export const USAGE_ACCENT: Record<string, TagAccent> = { ENCRYPT_DECRYPT: "brand", SIGN_VERIFY: "info", GENERATE_VERIFY_MAC: "violet" }

/** KeySpecTag is the key spec chip (SYMMETRIC_DEFAULT, RSA_2048, HMAC_256 ...). */
export function KeySpecTag({ spec, className }: { spec: string; className?: string }) {
  const s = spec || SYMMETRIC_SPEC
  return (
    <Tag accent={KIND_ACCENT[keyKind(s)]} className={className}>
      {s}
    </Tag>
  )
}

/** KeyUsageTag is the key usage chip (ENCRYPT_DECRYPT, SIGN_VERIFY ...). */
export function KeyUsageTag({ usage, className }: { usage: string; className?: string }) {
  return (
    <Tag accent={USAGE_ACCENT[usage] ?? "neutral"} className={className} title={USAGE_LABEL[usage]}>
      {usage}
    </Tag>
  )
}

/** KeyTypeTag is the short key type chip: "Symmetric", or "Asymmetric · RSA_2048". */
export function KeyTypeTag({ spec }: { spec: string }) {
  const kind = keyKind(spec)
  return (
    <Tag accent={KIND_ACCENT[kind]} mono={kind !== "symmetric"} title={spec || SYMMETRIC_SPEC}>
      {kind === "symmetric" ? KIND_LABEL.symmetric : spec}
    </Tag>
  )
}

/** keyTypeLabel is a short description such as "Symmetric" or "Asymmetric · RSA_2048". */
export function keyTypeLabel(k: Pick<KmsKey, "key_spec">): string {
  const kind = keyKind(k.key_spec)
  return kind === "symmetric" ? KIND_LABEL.symmetric : `${KIND_LABEL[kind]} · ${k.key_spec}`
}

/** keyLabel is the first alias (friendlier) or the key id. */
export const keyLabel = (k: Pick<KmsKey, "id" | "aliases">) => k.aliases?.[0] ?? k.id

export const KEY_POLL = 15_000

export function useKmsKeys() {
  return useApi<KmsKey[]>(KEYS_PATH, { refreshInterval: KEY_POLL })
}

export const STATE_LABEL: Record<string, string> = {
  Enabled: "Enabled",
  Disabled: "Disabled",
  PendingDeletion: "Pending deletion",
  PendingImport: "Pending import",
  Unavailable: "Unavailable",
}

/** STATE_TONE maps KMS key states (which StatusBadge's mapping doesn't know) to tones. */
const STATE_TONE: Record<string, Tone> = {
  Enabled: "success",
  Disabled: "neutral",
  PendingDeletion: "danger",
  PendingImport: "warning",
  Unavailable: "danger",
}

export function KeyStateBadge({ state }: { state: string }) {
  return <StatusBadge status={state.toLowerCase()} label={STATE_LABEL[state] ?? state} tone={STATE_TONE[state] ?? "neutral"} />
}

export function ManagedBadge({ className }: { className?: string }) {
  return (
    <Tooltip>
      <TooltipTrigger asChild>
        <span className={cn("inline-flex", className)}>
          <Tag accent="violet" mono={false}>
            <Lock /> HomeCloud managed
          </Tag>
        </span>
      </TooltipTrigger>
      <TooltipContent className="max-w-64">Created and used by a HomeCloud service. It can be used to encrypt and decrypt but cannot be changed.</TooltipContent>
    </Tooltip>
  )
}

// ---- aliases ----

export const ALIAS_RE = /^alias\/[a-zA-Z0-9/_-]{1,250}$/

/** normalizeAlias trims and adds the alias/ prefix when missing. */
export function normalizeAlias(input: string): string {
  const s = input.trim()
  if (!s) return ""
  return s.startsWith("alias/") ? s : `alias/${s.replace(/^\/+/, "")}`
}

/** aliasError mirrors validAlias in kms.go; returns null when valid. */
export function aliasError(alias: string): string | null {
  if (!alias) return null
  if (alias.startsWith("alias/hc/") || alias.startsWith("alias/aws/")) return "The alias/hc/ and alias/aws/ prefixes are reserved."
  if (!ALIAS_RE.test(alias)) return "Use 1-250 letters, digits, slashes (/), underscores (_) or hyphens (-) after alias/."
  return null
}

/** AliasInput shows a fixed "alias/" prefix in front of the name. */
export function AliasInput({ id, value, onChange, autoFocus, invalid }: { id: string; value: string; onChange: (v: string) => void; autoFocus?: boolean; invalid?: boolean }) {
  return (
    <div className="flex">
      <span className="bg-muted text-muted-foreground inline-flex items-center rounded-l-md border border-r-0 px-2.5 font-mono text-[13px]">alias/</span>
      <Input
        id={id}
        autoFocus={autoFocus}
        autoComplete="off"
        spellCheck={false}
        value={value}
        onChange={(e) => onChange(e.target.value.replace(/^alias\//, ""))}
        placeholder="my-app-key"
        className="rounded-l-none font-mono text-[13px]"
        aria-invalid={invalid}
      />
    </div>
  )
}

// ---- key picker ----

/**
 * KeyPicker selects a KMS key by id. `onlyEnabled` hides keys that cannot encrypt.
 * Customer managed keys are listed before HomeCloud managed ones.
 */
export function KeyPicker({
  id,
  keys,
  value,
  onChange,
  onlyEnabled,
  placeholder = "Choose a key",
  className,
  disabled,
  extra,
  symmetricOnly,
}: {
  id?: string
  keys: KmsKey[] | undefined
  value: string
  onChange: (id: string) => void
  onlyEnabled?: boolean
  placeholder?: string
  className?: string
  disabled?: boolean
  /** Extra items shown first (e.g. a "default key" sentinel). */
  extra?: { value: string; label: string; hint?: string }[]
  /** Only symmetric encryption keys (what Encrypt, Secrets Manager and SecureString parameters accept). */
  symmetricOnly?: boolean
}) {
  const list = (keys ?? []).filter((k) => (!onlyEnabled || k.state === "Enabled" || k.id === value) && (!symmetricOnly || isSymmetric(k) || k.id === value))
  const customer = list.filter((k) => !isServiceManaged(k))
  const managed = list.filter((k) => isServiceManaged(k))
  const item = (k: KmsKey) => (
    <SelectItem key={k.id} value={k.id} disabled={onlyEnabled && k.state !== "Enabled"}>
      <span className="font-mono text-[13px]">{keyLabel(k)}</span>
      {k.aliases?.length ? <span className="text-muted-foreground hidden font-mono text-xs sm:inline">{k.id.slice(0, 8)}</span> : null}
      {k.state !== "Enabled" && <span className="text-muted-foreground text-xs">({STATE_LABEL[k.state] ?? k.state})</span>}
    </SelectItem>
  )
  return (
    <Select value={value} onValueChange={(v) => v && onChange(v)} disabled={disabled || !keys}>
      <SelectTrigger id={id} className={cn("w-full", className)}>
        <SelectValue placeholder={keys ? placeholder : "Loading keys..."} />
      </SelectTrigger>
      <SelectContent>
        {extra?.map((x) => (
          <SelectItem key={x.value} value={x.value}>
            <span className="font-mono text-[13px]">{x.label}</span>
            {x.hint && <span className="text-muted-foreground text-xs">{x.hint}</span>}
          </SelectItem>
        ))}
        {customer.length > 0 && (
          <SelectGroup>
            <SelectLabel>Customer managed keys</SelectLabel>
            {customer.map(item)}
          </SelectGroup>
        )}
        {managed.length > 0 && (
          <SelectGroup>
            <SelectLabel>HomeCloud managed keys</SelectLabel>
            {managed.map(item)}
          </SelectGroup>
        )}
        {!extra?.length && list.length === 0 && <div className="text-muted-foreground px-2 py-1.5 text-sm">{symmetricOnly ? "No enabled symmetric keys" : onlyEnabled ? "No enabled keys" : "No keys"}</div>}
      </SelectContent>
    </Select>
  )
}

// ---- actions ----

/**
 * useKeyActions runs the key state changes shared by the list and detail pages.
 * Render `dialogs` once in the page.
 */
export function useKeyActions(onChanged?: () => void) {
  const [busy, setBusy] = useState(false)
  const [deleting, setDeleting] = useState<KmsKey | null>(null)
  const [days, setDays] = useState("30")

  const call = async (fn: () => Promise<unknown>, success: string) => {
    setBusy(true)
    try {
      await fn()
      toast.success(success)
      await revalidate(KMS_PATH)
      onChanged?.()
      return true
    } catch (e) {
      toast.error(errorMessage(e))
      return false
    } finally {
      setBusy(false)
    }
  }

  const enable = (k: KmsKey) => call(() => api.post(`${KEYS_PATH}/${seg(k.id)}/enable`), `Key ${keyLabel(k)} enabled`)
  const disable = (k: KmsKey) => call(() => api.post(`${KEYS_PATH}/${seg(k.id)}/disable`), `Key ${keyLabel(k)} disabled`)
  const rotate = (k: KmsKey) => call(() => api.post(`${KEYS_PATH}/${seg(k.id)}/rotate`), `Key ${keyLabel(k)} rotated to a new key version`)
  const setRotation = (k: KmsKey, on: boolean, periodDays?: number) =>
    call(
      () => api.patch(`${KEYS_PATH}/${seg(k.id)}`, { rotation_enabled: on, ...(on && periodDays ? { rotation_period_days: periodDays } : {}) }),
      on ? (periodDays && k.rotation_enabled ? `Rotation period set to ${periodDays} days` : "Automatic rotation enabled") : "Automatic rotation disabled",
    )
  const cancelDeletion = (k: KmsKey) => call(() => api.post(`${KEYS_PATH}/${seg(k.id)}/cancel-deletion`), `Deletion of ${keyLabel(k)} cancelled; the key is disabled`)
  const scheduleDeletion = (k: KmsKey) => {
    setDays("30")
    setDeleting(k)
  }

  const n = Number(days)
  const daysValid = Number.isInteger(n) && n >= 7 && n <= 30
  const deletionDate = daysValid ? new Date(Date.now() + n * 86_400_000) : null

  const dialogs = (
    <ConfirmDialog
      open={!!deleting}
      onOpenChange={(o) => !o && setDeleting(null)}
      title="Schedule key deletion"
      actionLabel="Schedule deletion"
      confirmText={deleting ? keyLabel(deleting) : undefined}
      description={
        <div className="flex flex-col gap-2">
          <p>
            The key <span className="text-foreground font-mono">{deleting ? keyLabel(deleting) : ""}</span> is disabled immediately and deleted with all of its key
            material at the end of the waiting period.
          </p>
          <p className="text-destructive">Data encrypted under this key, including SecureString parameters, becomes unrecoverable once the key is deleted.</p>
        </div>
      }
      onConfirm={async () => {
        if (!deleting) return
        if (!daysValid) throw new Error("The waiting period must be 7-30 days")
        setBusy(true)
        try {
          await api.post(`${KEYS_PATH}/${seg(deleting.id)}/schedule-deletion`, { pending_window_days: n })
          toast.success(`Key ${keyLabel(deleting)} scheduled for deletion`)
          await revalidate(KMS_PATH)
          onChanged?.()
        } finally {
          setBusy(false)
        }
      }}
    >
      <Field
        label="Waiting period (days)"
        htmlFor="kms-pending-days"
        error={!daysValid ? "Enter a whole number of days between 7 and 30." : undefined}
        help={deletionDate ? `Deletion date: ${formatDate(deletionDate, false)}. You can cancel the deletion until then.` : undefined}
      >
        <Input id="kms-pending-days" type="number" min={7} max={30} value={days} onChange={(e) => setDays(e.target.value)} className="w-32" aria-invalid={!daysValid} />
      </Field>
    </ConfirmDialog>
  )

  return { busy, enable, disable, rotate, setRotation, cancelDeletion, scheduleDeletion, dialogs }
}
