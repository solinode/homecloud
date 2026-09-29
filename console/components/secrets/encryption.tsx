"use client"

import { useEffect, useState } from "react"
import Link from "next/link"
import { Loader2, Pencil } from "lucide-react"
import { toast } from "sonner"

import { Button } from "@/components/ui/button"
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog"
import { CopyButton } from "@/components/console/copy-button"
import { Field } from "@/components/console/form-field"
import { KeyPicker, keyHref, keyIdFromArn, keyLabel, useKmsKeys } from "@/components/kms/shared"
import { api, errorMessage, seg } from "@/lib/api"
import type { KmsKey, Secret } from "@/lib/types"

/** The Secrets Manager default key; the API stores it as "no customer key". */
export const SECRETS_DEFAULT_KEY = "alias/aws/secretsmanager"
export const DEFAULT_SENTINEL = "default"

/** SecretKeyPicker chooses the default key or an enabled symmetric customer key. */
export function SecretKeyPicker({ id, keys, value, onChange, disabled }: { id?: string; keys: KmsKey[] | undefined; value: string; onChange: (v: string) => void; disabled?: boolean }) {
  return (
    <KeyPicker
      id={id}
      keys={keys}
      value={value}
      onChange={onChange}
      onlyEnabled
      symmetricOnly
      disabled={disabled}
      extra={[{ value: DEFAULT_SENTINEL, label: SECRETS_DEFAULT_KEY, hint: "default" }]}
    />
  )
}

/** KeyHelp explains the chosen key under the picker. */
export function KeyHelp({ value }: { value: string }) {
  return value === DEFAULT_SENTINEL ? (
    <>Encrypted with the installation&apos;s master key ({SECRETS_DEFAULT_KEY}). No KMS permissions are needed to read it.</>
  ) : (
    <>Each version is sealed with a data key from this KMS key; reading the secret also requires kms:Decrypt on the key. Only symmetric keys can be used.</>
  )
}

/** SecretKeyRef shows the secret's (or a version's) encryption key as a link to KMS. */
export function SecretKeyRef({ arn, keys }: { arn?: string; keys?: KmsKey[] }) {
  if (!arn) return <span className="font-mono text-[13px]">{SECRETS_DEFAULT_KEY}</span>
  const id = keyIdFromArn(arn)
  const k = keys?.find((x) => x.id === id)
  return (
    <span className="inline-flex items-center gap-1">
      <Link href={keyHref(id)} className="text-primary font-mono text-[13px] break-all hover:underline" title={arn}>
        {k ? keyLabel(k) : id}
      </Link>
      <CopyButton value={arn} label="Copy key ARN" />
    </span>
  )
}

/** EncryptionField is the "Encryption key" detail value with an edit button. */
export function EncryptionField({ secret, editable, onSaved }: { secret: Secret; editable: boolean; onSaved: () => void }) {
  const keys = useKmsKeys()
  const [open, setOpen] = useState(false)
  return (
    <span className="inline-flex items-start gap-1">
      <SecretKeyRef arn={secret.kms_key_id} keys={keys.data} />
      {editable && (
        <button
          type="button"
          onClick={() => setOpen(true)}
          aria-label="Change encryption key"
          className="text-muted-foreground hover:text-foreground hover:bg-accent inline-flex size-6 shrink-0 items-center justify-center rounded"
        >
          <Pencil className="size-3.5" />
        </button>
      )}
      <ChangeKeyDialog secret={open ? secret : null} keys={keys.data} onClose={() => setOpen(false)} onSaved={onSaved} />
    </span>
  )
}

function ChangeKeyDialog({ secret, keys, onClose, onSaved }: { secret: Secret | null; keys: KmsKey[] | undefined; onClose: () => void; onSaved: () => void }) {
  const [value, setValue] = useState(DEFAULT_SENTINEL)
  const [pending, setPending] = useState(false)
  const current = secret?.kms_key_id ? keyIdFromArn(secret.kms_key_id) : DEFAULT_SENTINEL

  useEffect(() => {
    if (secret) setValue(current)
  }, [secret, current])

  const submit = async (e: React.FormEvent) => {
    e.preventDefault()
    if (!secret || value === current) return
    setPending(true)
    try {
      await api.patch(`/api/v1/secrets/${seg(secret.name)}`, { kms_key_id: value === DEFAULT_SENTINEL ? SECRETS_DEFAULT_KEY : value })
      toast.success("Encryption key changed; existing versions were re-encrypted")
      onSaved()
      onClose()
    } catch (err) {
      toast.error(errorMessage(err))
    } finally {
      setPending(false)
    }
  }

  return (
    <Dialog open={!!secret} onOpenChange={(o) => !o && !pending && onClose()}>
      <DialogContent className="sm:max-w-md">
        <form onSubmit={submit} className="flex flex-col gap-4">
          <DialogHeader>
            <DialogTitle>Change encryption key</DialogTitle>
            <DialogDescription>
              All stored versions are re-encrypted under the new key. This requires kms:Decrypt on the current key and kms:GenerateDataKey on the new one.
            </DialogDescription>
          </DialogHeader>
          <Field label="Encryption key" htmlFor="secret-change-key" help={<KeyHelp value={value} />}>
            <SecretKeyPicker id="secret-change-key" keys={keys} value={value} onChange={setValue} />
          </Field>
          <DialogFooter>
            <Button type="button" variant="outline" onClick={onClose} disabled={pending}>
              Cancel
            </Button>
            <Button type="submit" disabled={pending || value === current}>
              {pending && <Loader2 className="animate-spin" />}
              Save
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}
