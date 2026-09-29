"use client"

import { useEffect, useState } from "react"
import { useRouter } from "next/navigation"
import { AlertTriangle, CheckCircle2, Circle, Loader2 } from "lucide-react"
import { toast } from "sonner"

import { Button } from "@/components/ui/button"
import { Checkbox } from "@/components/ui/checkbox"
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { Switch } from "@/components/ui/switch"
import { ConfirmDialog } from "@/components/console/confirm-dialog"
import { Field } from "@/components/console/form-field"
import { TagsEditor, rowsToTags, type TagRow } from "@/components/console/tags-editor"
import { api, ApiError, errorMessage, seg } from "@/lib/api"
import { revalidate } from "@/lib/hooks"
import { cn } from "@/lib/utils"

import { bucketHref, bucketNameChecks, validBucketName } from "./common"

export function CreateBucketDialog({ open, onOpenChange }: { open: boolean; onOpenChange: (o: boolean) => void }) {
  const router = useRouter()
  const [name, setName] = useState("")
  const [versioning, setVersioning] = useState(false)
  const [blockPublic, setBlockPublic] = useState(true)
  const [objectLock, setObjectLock] = useState(false)
  const [tags, setTags] = useState<TagRow[]>([])
  const [pending, setPending] = useState(false)
  const [touched, setTouched] = useState(false)

  useEffect(() => {
    if (open) {
      setName("")
      setVersioning(false)
      setBlockPublic(true)
      setObjectLock(false)
      setTags([])
      setTouched(false)
    }
  }, [open])

  const checks = bucketNameChecks(name)
  const valid = validBucketName(name)

  const submit = async (e: React.FormEvent) => {
    e.preventDefault()
    setTouched(true)
    if (!valid) return
    setPending(true)
    try {
      await api.post("/api/v1/s3/buckets", { name, versioning, public: !blockPublic, object_lock: objectLock, tags: rowsToTags(tags) })
      toast.success(`Bucket ${name} created`)
      revalidate("/api/v1/s3")
      onOpenChange(false)
      router.push(bucketHref(name))
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
            <DialogTitle>Create bucket</DialogTitle>
            <DialogDescription>Buckets are containers for objects stored in HomeCloud S3.</DialogDescription>
          </DialogHeader>

          <Field label="Bucket name" htmlFor="bucket-name">
            <Input
              id="bucket-name"
              autoFocus
              autoComplete="off"
              spellCheck={false}
              value={name}
              onChange={(e) => setName(e.target.value)}
              onBlur={() => setTouched(true)}
              placeholder="my-bucket"
              aria-invalid={touched && !valid}
            />
            <ul className="mt-1 flex flex-col gap-1 text-xs">
              {checks.map((c) => (
                <li
                  key={c.label}
                  className={cn(
                    "flex items-center gap-1.5",
                    c.ok ? "text-emerald-600 dark:text-emerald-400" : touched || name ? "text-destructive" : "text-muted-foreground",
                  )}
                >
                  {c.ok ? <CheckCircle2 className="size-3.5" /> : <Circle className="size-3.5" />}
                  {c.label}
                </li>
              ))}
            </ul>
          </Field>

          <div className="flex items-start justify-between gap-4 rounded-md border p-3">
            <div>
              <Label htmlFor="bucket-versioning" className="font-medium">
                Bucket versioning
              </Label>
              <p className="text-muted-foreground mt-0.5 text-xs">Keep multiple variants of an object to recover from overwrites and deletes.</p>
            </div>
            <Switch id="bucket-versioning" checked={versioning} onCheckedChange={setVersioning} />
          </div>

          <div className="flex flex-col gap-2 rounded-md border p-3">
            <div className="flex items-start justify-between gap-4">
              <div>
                <Label htmlFor="bucket-block" className="font-medium">
                  Block all public access
                </Label>
                <p className="text-muted-foreground mt-0.5 text-xs">When off, anyone can read objects in this bucket without credentials.</p>
              </div>
              <Switch id="bucket-block" checked={blockPublic} onCheckedChange={setBlockPublic} />
            </div>
            {!blockPublic && (
              <div className="flex items-start gap-2 rounded-md border border-amber-300 bg-amber-50 p-2 text-xs text-amber-800 dark:border-amber-500/30 dark:bg-amber-500/10 dark:text-amber-300">
                <AlertTriangle className="mt-0.5 size-3.5 shrink-0" />
                Objects in this bucket will be publicly readable by anyone who can reach the S3 endpoint.
              </div>
            )}
          </div>

          <div className="flex items-start gap-3 rounded-md border p-3">
            <Checkbox id="bucket-lock" checked={objectLock} onCheckedChange={(v) => setObjectLock(v === true)} className="mt-0.5" />
            <div>
              <Label htmlFor="bucket-lock" className="font-medium">
                Enable Object Lock
              </Label>
              <p className="text-muted-foreground mt-0.5 text-xs">Store objects using a write-once-read-many model. Can only be set at creation.</p>
            </div>
          </div>

          <Field label="Tags" optional>
            <TagsEditor rows={tags} onChange={setTags} />
          </Field>

          <DialogFooter>
            <Button type="button" variant="outline" onClick={() => onOpenChange(false)} disabled={pending}>
              Cancel
            </Button>
            <Button type="submit" disabled={pending || (touched && !valid)}>
              {pending && <Loader2 className="animate-spin" />}
              Create bucket
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}

/** DeleteBucketDialog deletes a bucket, optionally emptying it first (force=true). */
export function DeleteBucketDialog({
  bucket,
  open,
  onOpenChange,
  onDeleted,
}: {
  bucket: string
  open: boolean
  onOpenChange: (o: boolean) => void
  onDeleted?: () => void
}) {
  const [force, setForce] = useState(false)
  const [apiError, setApiError] = useState<string | null>(null)

  useEffect(() => {
    if (open) {
      setForce(false)
      setApiError(null)
    }
  }, [open])

  return (
    <ConfirmDialog
      open={open}
      onOpenChange={onOpenChange}
      title={`Delete bucket ${bucket}?`}
      description="Deleting a bucket cannot be undone. Bucket names are unique; another bucket with this name can be created afterwards."
      confirmText={bucket}
      actionLabel="Delete bucket"
      onConfirm={async () => {
        setApiError(null)
        try {
          await api.del(`/api/v1/s3/buckets/${seg(bucket)}`, force ? { force: "true" } : undefined)
        } catch (e) {
          if (e instanceof ApiError && e.code === "BucketNotEmpty") {
            setApiError("The bucket is not empty. Select “Empty the bucket first” to delete all objects together with the bucket.")
          } else {
            setApiError(errorMessage(e))
          }
          throw e
        }
        toast.success(`Bucket ${bucket} deleted`)
        revalidate("/api/v1/s3")
        onDeleted?.()
      }}
    >
      <div className="flex items-start gap-3 rounded-md border p-3">
        <Checkbox id="bucket-force" checked={force} onCheckedChange={(v) => setForce(v === true)} className="mt-0.5" />
        <div>
          <Label htmlFor="bucket-force" className="font-medium">
            Empty the bucket first (delete all objects)
          </Label>
          <p className="text-muted-foreground mt-0.5 text-xs">Every object and object version in the bucket is permanently deleted.</p>
        </div>
      </div>
      {apiError && (
        <div className="border-destructive/30 bg-destructive/5 text-destructive flex items-start gap-2 rounded-md border p-3 text-sm">
          <AlertTriangle className="mt-0.5 size-4 shrink-0" />
          <span>{apiError}</span>
        </div>
      )}
    </ConfirmDialog>
  )
}
