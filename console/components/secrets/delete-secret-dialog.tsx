"use client"

import { useEffect, useState } from "react"
import { AlertTriangle } from "lucide-react"
import { toast } from "sonner"

import { Checkbox } from "@/components/ui/checkbox"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { ConfirmDialog } from "@/components/console/confirm-dialog"
import { Field } from "@/components/console/form-field"
import { api, seg } from "@/lib/api"
import { formatDate } from "@/lib/format"
import { revalidate } from "@/lib/hooks"
import type { Secret } from "@/lib/types"

/** ManagedWarning is shown before editing or deleting a secret owned by another service. */
export function ManagedWarning({ by, action }: { by?: string; action: string }) {
  if (!by) return null
  return (
    <div className="flex items-start gap-2 rounded-md border border-amber-300 bg-amber-50 p-3 text-sm text-amber-800 dark:border-amber-500/30 dark:bg-amber-500/10 dark:text-amber-300">
      <AlertTriangle className="mt-0.5 size-4 shrink-0" />
      <span>
        This secret is managed by <strong>{by}</strong>. {action} it can break the {by} service, which reads it on startup.
      </span>
    </div>
  )
}

/** DeleteSecretDialog schedules deletion after a recovery window, or deletes immediately (force). */
export function DeleteSecretDialog({ secret, onOpenChange, onDeleted }: { secret: Secret | null; onOpenChange: (o: boolean) => void; onDeleted?: (force: boolean) => void }) {
  const [days, setDays] = useState("7")
  const [force, setForce] = useState(false)
  const [last, setLast] = useState<Secret | null>(secret)

  useEffect(() => {
    if (secret) {
      setLast(secret)
      setDays("7")
      setForce(false)
    }
  }, [secret])

  const s = secret ?? last
  const n = Number(days)
  const daysValid = Number.isInteger(n) && n >= 1 && n <= 30
  const deletionDate = new Date(Date.now() + (daysValid ? n : 7) * 86400_000)

  return (
    <ConfirmDialog
      open={!!secret}
      onOpenChange={onOpenChange}
      title={`Delete secret ${s?.name ?? ""}?`}
      description={
        force
          ? "The secret and all of its versions are deleted immediately and can't be recovered."
          : "The secret is disabled now and permanently deleted after the recovery window. Until then you can cancel the deletion."
      }
      confirmText={s?.name}
      actionLabel={force ? "Delete immediately" : "Schedule deletion"}
      onConfirm={async () => {
        if (!s) return
        if (!force && !daysValid) throw new Error("The waiting period must be between 1 and 30 days")
        await api.del(`/api/v1/secrets/${seg(s.name)}`, force ? { force: "true" } : { recovery_days: n })
        toast.success(force ? `Secret ${s.name} deleted` : `Secret ${s.name} scheduled for deletion`)
        revalidate("/api/v1/secrets")
        onDeleted?.(force)
      }}
    >
      <ManagedWarning by={s?.managed_by} action="Deleting" />
      {!force && (
        <Field
          label="Waiting period (days)"
          htmlFor="recovery-days"
          error={!daysValid ? "Enter a number of days between 1 and 30" : undefined}
          help={`The secret will be deleted on ${formatDate(deletionDate, false)}.`}
        >
          <Input id="recovery-days" type="number" min={1} max={30} value={days} onChange={(e) => setDays(e.target.value)} className="w-32" />
        </Field>
      )}
      <div className="flex items-start gap-3 rounded-md border p-3">
        <Checkbox id="force-delete" checked={force} onCheckedChange={(v) => setForce(v === true)} className="mt-0.5" />
        <div>
          <Label htmlFor="force-delete" className="font-medium">
            Delete immediately without recovery
          </Label>
          <p className="text-muted-foreground mt-0.5 text-xs">Skip the recovery window. This cannot be undone.</p>
        </div>
      </div>
    </ConfirmDialog>
  )
}
