"use client"

import { useEffect, useState } from "react"
import { useRouter } from "next/navigation"
import { Loader2 } from "lucide-react"
import { toast } from "sonner"

import { Button } from "@/components/ui/button"
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { Switch } from "@/components/ui/switch"
import { Field } from "@/components/console/form-field"
import { TagsEditor, rowsToTags, type TagRow } from "@/components/console/tags-editor"
import { api, errorMessage } from "@/lib/api"
import { revalidate } from "@/lib/hooks"
import type { CreateKmsKeyInput, KmsKey } from "@/lib/types"

import { AliasInput, KEYS_PATH, KMS_PATH, aliasError, keyHref, keyLabel, normalizeAlias } from "./shared"

export function CreateKeyDialog({ open, onOpenChange }: { open: boolean; onOpenChange: (o: boolean) => void }) {
  const router = useRouter()
  const [alias, setAlias] = useState("")
  const [description, setDescription] = useState("")
  const [rotation, setRotation] = useState(true)
  const [tags, setTags] = useState<TagRow[]>([])
  const [pending, setPending] = useState(false)
  const [touched, setTouched] = useState(false)

  useEffect(() => {
    if (open) {
      setAlias("")
      setDescription("")
      setRotation(true)
      setTags([])
      setTouched(false)
    }
  }, [open])

  const fullAlias = normalizeAlias(alias)
  const aliasErr = aliasError(fullAlias)

  const submit = async (e: React.FormEvent) => {
    e.preventDefault()
    setTouched(true)
    if (aliasErr) return
    setPending(true)
    try {
      const body: CreateKmsKeyInput = { description: description.trim(), alias: fullAlias || undefined, rotation_enabled: rotation, tags: rowsToTags(tags) }
      const k = await api.post<KmsKey>(KEYS_PATH, body)
      toast.success(`Key ${keyLabel(k)} created`)
      revalidate(KMS_PATH)
      onOpenChange(false)
      router.push(keyHref(k.id))
    } catch (err) {
      toast.error(errorMessage(err))
    } finally {
      setPending(false)
    }
  }

  return (
    <Dialog open={open} onOpenChange={(o) => !pending && onOpenChange(o)}>
      <DialogContent className="max-h-[90vh] overflow-y-auto sm:max-w-xl">
        <form onSubmit={submit} className="flex flex-col gap-5">
          <DialogHeader>
            <DialogTitle>Create key</DialogTitle>
            <DialogDescription>A symmetric AES-256 key (SYMMETRIC_DEFAULT) for encrypting and decrypting data up to 4 KB, or data keys for larger payloads.</DialogDescription>
          </DialogHeader>

          <Field label="Alias" htmlFor="kms-alias" optional error={touched || alias ? aliasErr : undefined} help="A friendly name you can use instead of the key ID.">
            <AliasInput id="kms-alias" autoFocus value={alias} onChange={setAlias} invalid={!!aliasErr && (touched || !!alias)} />
          </Field>

          <Field label="Description" htmlFor="kms-desc" optional>
            <Input id="kms-desc" value={description} onChange={(e) => setDescription(e.target.value)} placeholder="Encrypts the orders database backups" />
          </Field>

          <div className="flex items-start justify-between gap-4 rounded-md border p-3">
            <div>
              <Label htmlFor="kms-rotation" className="font-medium">
                Automatic key rotation
              </Label>
              <p className="text-muted-foreground mt-0.5 text-xs">
                Creates new key material every year. Older versions are kept, so data encrypted before a rotation still decrypts.
              </p>
            </div>
            <Switch id="kms-rotation" checked={rotation} onCheckedChange={setRotation} />
          </div>

          <Field label="Tags" optional>
            <TagsEditor rows={tags} onChange={setTags} />
          </Field>

          <DialogFooter>
            <Button type="button" variant="outline" onClick={() => onOpenChange(false)} disabled={pending}>
              Cancel
            </Button>
            <Button type="submit" disabled={pending || (touched && !!aliasErr)}>
              {pending && <Loader2 className="animate-spin" />}
              Create key
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}
