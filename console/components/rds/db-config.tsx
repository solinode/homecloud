"use client"

import { useEffect, useState } from "react"
import { Eye, EyeOff, Info, KeyRound, Loader2 } from "lucide-react"
import { toast } from "sonner"

import { Button } from "@/components/ui/button"
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { Switch } from "@/components/ui/switch"
import { Field } from "@/components/console/form-field"
import { Section } from "@/components/console/section"
import { TagsEditor, rowsToTags, tagsToRows, type TagRow } from "@/components/console/tags-editor"
import { api, errorMessage, seg } from "@/lib/api"
import { revalidate } from "@/lib/hooks"
import type { DbInstance } from "@/lib/types"
import { ClassSelect, DB_INSTANCES_PATH, RDS_PATH, classSummary, passwordError, supportsPasswordReset, supportsSnapshots, type FamilyConfig } from "./shared"

/** DbConfiguration is the Configuration tab: class, backups, deletion protection, tags and password rotation. */
export function DbConfiguration({ cfg, inst, onResetPassword }: { cfg: FamilyConfig; inst: DbInstance; onResetPassword: () => void }) {
  const [cls, setCls] = useState(inst.class)
  const [retention, setRetention] = useState(String(inst.backup_retention_days))
  const [protect, setProtect] = useState(inst.deletion_protection)
  const [saving, setSaving] = useState(false)
  const [tagRows, setTagRows] = useState<TagRow[]>(() => tagsToRows(inst.tags))
  const [tagErr, setTagErr] = useState<string | null>(null)
  const [savingTags, setSavingTags] = useState(false)

  // Pick up changes made elsewhere (other tab, CLI) while the form is untouched.
  useEffect(() => setCls(inst.class), [inst.class])
  useEffect(() => setRetention(String(inst.backup_retention_days)), [inst.backup_retention_days])
  useEffect(() => setProtect(inst.deletion_protection), [inst.deletion_protection])
  const tagsKey = JSON.stringify(inst.tags ?? {})
  useEffect(() => setTagRows(tagsToRows(JSON.parse(tagsKey))), [tagsKey])

  const backups = supportsSnapshots(inst.engine)
  const r = Number(retention)
  const retErr = backups && (!Number.isInteger(r) || r < 0 || r > 35) ? "0-35 days" : undefined
  const classChanged = cls !== inst.class
  const canResize = inst.status === "available" || inst.status === "stopped"
  const dirty = classChanged || (backups && r !== inst.backup_retention_days) || protect !== inst.deletion_protection

  const save = async (e: React.FormEvent) => {
    e.preventDefault()
    if (retErr || !dirty) return
    const body: Record<string, unknown> = {}
    if (classChanged) body.class = cls
    if (backups && r !== inst.backup_retention_days) body.backup_retention_days = r
    if (protect !== inst.deletion_protection) body.deletion_protection = protect
    setSaving(true)
    try {
      await api.patch(`${DB_INSTANCES_PATH}/${seg(inst.id)}`, body)
      toast.success(`Modified ${inst.id}`)
      await revalidate(RDS_PATH)
    } catch (err) {
      toast.error(errorMessage(err))
    } finally {
      setSaving(false)
    }
  }

  const saveTags = async () => {
    const keys = tagRows.map((t) => t.key.trim()).filter(Boolean)
    if (new Set(keys).size !== keys.length) {
      setTagErr("Tag keys must be unique")
      return
    }
    setSavingTags(true)
    try {
      await api.patch(`${DB_INSTANCES_PATH}/${seg(inst.id)}`, { tags: rowsToTags(tagRows) ?? {} })
      toast.success(`Saved tags for ${inst.id}`)
      await revalidate(RDS_PATH)
    } catch (err) {
      toast.error(errorMessage(err))
    } finally {
      setSavingTags(false)
    }
  }

  return (
    <div className="flex flex-col gap-4">
      <Section title={`${cfg.Noun} settings`} description="Changes apply immediately.">
        <form onSubmit={save} className="flex max-w-2xl flex-col gap-5">
          <Field
            label={cfg.family === "rds" ? "DB instance class" : "Node type"}
            htmlFor="cfg-class"
            help={
              canResize
                ? `Currently ${inst.class} (${classSummary(inst)}). The new CPU and memory limits apply to the running container right away.`
                : `The ${cfg.noun} must be available or stopped to change its class.`
            }
          >
            <ClassSelect id="cfg-class" kind={cfg.classKind} value={cls} onChange={setCls} className="max-w-md" />
          </Field>
          {backups ? (
            <Field
              label="Backup retention period (days)"
              htmlFor="cfg-retention"
              error={retErr}
              help={`Daily automated ${cfg.snaps} are kept this many days. 0 turns them off.`}
            >
              <Input id="cfg-retention" type="number" min={0} max={35} value={retention} onChange={(e) => setRetention(e.target.value)} className="w-28" />
            </Field>
          ) : (
            <p className="text-muted-foreground flex gap-1.5 text-sm">
              <Info className="mt-0.5 size-4 shrink-0" /> Memcached keeps data only in memory, so {cfg.snaps} are not available.
            </p>
          )}
          <div className="flex items-start gap-3">
            <Switch id="cfg-protect" checked={protect} onCheckedChange={setProtect} className="mt-0.5" />
            <Label htmlFor="cfg-protect" className="flex flex-col items-start gap-0.5">
              <span>Deletion protection</span>
              <span className="text-muted-foreground text-xs font-normal">While on, the {cfg.noun} cannot be deleted.</span>
            </Label>
          </div>
          <div className="flex gap-2">
            <Button type="submit" size="sm" disabled={saving || !dirty || !!retErr || (classChanged && !canResize)}>
              {saving && <Loader2 className="animate-spin" />}
              Save changes
            </Button>
            <Button
              type="button"
              variant="outline"
              size="sm"
              disabled={saving || !dirty}
              onClick={() => (setCls(inst.class), setRetention(String(inst.backup_retention_days)), setProtect(inst.deletion_protection))}
            >
              Reset
            </Button>
          </div>
        </form>
      </Section>

      <Section title="Tags" description="Key/value labels for organizing and finding resources.">
        <div className="flex max-w-2xl flex-col gap-3">
          <TagsEditor rows={tagRows} onChange={(r) => (setTagRows(r), setTagErr(null))} />
          {tagErr && <p className="text-destructive text-xs">{tagErr}</p>}
          <div>
            <Button size="sm" onClick={saveTags} disabled={savingTags}>
              {savingTags && <Loader2 className="animate-spin" />}
              Save tags
            </Button>
          </div>
        </div>
      </Section>

      {inst.secret_name && (
        <Section title={cfg.family === "rds" ? "Master password" : "Auth token"}>
          {supportsPasswordReset(inst.engine) ? (
            <div className="flex flex-col items-start gap-3">
              <p className="text-muted-foreground text-sm">
                Changes the master user&apos;s password in the engine and updates the secret <span className="font-mono">{inst.secret_name}</span>. Existing
                connections stay open; new ones need the new password.
              </p>
              <Button variant="outline" size="sm" onClick={onResetPassword} disabled={inst.status !== "available"}>
                <KeyRound /> Reset master password
              </Button>
            </div>
          ) : (
            <p className="text-muted-foreground text-sm">
              The auth token of a {inst.engine} {cfg.noun} cannot be rotated in place. To change it, take a {cfg.snap} and restore it into a new {cfg.noun}.
            </p>
          )}
        </Section>
      )}
    </div>
  )
}

/** ResetPasswordDialog rotates the master password (generated or explicit). */
export function ResetPasswordDialog({ inst, onClose }: { inst: DbInstance | null; onClose: () => void }) {
  const [auto, setAuto] = useState(true)
  const [pass, setPass] = useState("")
  const [pass2, setPass2] = useState("")
  const [show, setShow] = useState(false)
  const [err, setErr] = useState<{ pass?: string; pass2?: string }>({})
  const [pending, setPending] = useState(false)
  useEffect(() => {
    if (inst) {
      setAuto(true)
      setPass("")
      setPass2("")
      setErr({})
    }
  }, [inst])

  const submit = async (e: React.FormEvent) => {
    e.preventDefault()
    if (!inst) return
    if (!auto) {
      const pe = passwordError(pass)
      if (pe || pass !== pass2) {
        setErr({ pass: pe, pass2: pe ? undefined : "Passwords do not match" })
        return
      }
    }
    setPending(true)
    try {
      await api.post(`${DB_INSTANCES_PATH}/${seg(inst.id)}/password`, auto ? {} : { password: pass })
      toast.success(`Reset the master password of ${inst.id}`, { description: `The new password is in the secret ${inst.secret_name}.` })
      await revalidate("/api/v1/secrets")
      onClose()
    } catch (e) {
      toast.error(errorMessage(e))
    } finally {
      setPending(false)
    }
  }

  return (
    <Dialog open={!!inst} onOpenChange={(o) => !o && !pending && onClose()}>
      <DialogContent>
        <form onSubmit={submit} className="flex flex-col gap-4">
          <DialogHeader>
            <DialogTitle>Reset master password</DialogTitle>
            <DialogDescription>
              Sets a new password for <span className="font-mono">{inst?.master_username}</span> and stores it in{" "}
              <span className="font-mono">{inst?.secret_name}</span>. Applications using the old password must be updated.
            </DialogDescription>
          </DialogHeader>
          <div className="flex items-start gap-3">
            <Switch id="rp-auto" checked={auto} onCheckedChange={setAuto} className="mt-0.5" />
            <Label htmlFor="rp-auto" className="flex flex-col items-start gap-0.5">
              <span>Auto-generate a password</span>
              <span className="text-muted-foreground text-xs font-normal">A strong 24-character password. Reveal it with Show credentials.</span>
            </Label>
          </div>
          {!auto && (
            <>
              <Field label="New password" htmlFor="rp-pass" error={err.pass} help="At least 8 characters. No quotes, slashes, @ or spaces.">
                <div className="relative">
                  <Input
                    id="rp-pass"
                    type={show ? "text" : "password"}
                    value={pass}
                    onChange={(e) => (setPass(e.target.value), setErr({}))}
                    className="pr-9 font-mono"
                    autoComplete="new-password"
                    autoFocus
                  />
                  <button
                    type="button"
                    onClick={() => setShow(!show)}
                    className="text-muted-foreground hover:text-foreground absolute top-1/2 right-2 -translate-y-1/2"
                    aria-label={show ? "Hide password" : "Show password"}
                  >
                    {show ? <EyeOff className="size-4" /> : <Eye className="size-4" />}
                  </button>
                </div>
              </Field>
              <Field label="Confirm password" htmlFor="rp-pass2" error={err.pass2}>
                <Input
                  id="rp-pass2"
                  type={show ? "text" : "password"}
                  value={pass2}
                  onChange={(e) => (setPass2(e.target.value), setErr({}))}
                  className="font-mono"
                  autoComplete="new-password"
                />
              </Field>
            </>
          )}
          <DialogFooter>
            <Button type="button" variant="outline" onClick={onClose} disabled={pending}>
              Cancel
            </Button>
            <Button type="submit" disabled={pending}>
              {pending && <Loader2 className="animate-spin" />}
              Reset password
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}
