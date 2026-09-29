"use client"

import { useEffect, useState } from "react"
import Link from "next/link"
import { useRouter } from "next/navigation"
import { AlertTriangle, Loader2 } from "lucide-react"
import { toast } from "sonner"

import { Button } from "@/components/ui/button"
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { Switch } from "@/components/ui/switch"
import { ConfirmDialog } from "@/components/console/confirm-dialog"
import { Field } from "@/components/console/form-field"
import { TagsEditor, rowsToTags, type TagRow } from "@/components/console/tags-editor"
import { api, ApiError, errorMessage, seg } from "@/lib/api"
import { revalidate } from "@/lib/hooks"
import type { CreateFileSystemInput, FileSystem } from "@/lib/types"

export const EFS_PATH = "/api/v1/efs"
export const FILE_SYSTEMS_PATH = `${EFS_PATH}/file-systems`
export const fileSystemHref = (id: string) => `/efs/file-system/?id=${encodeURIComponent(id)}`
export const fsLabel = (fs: Pick<FileSystem, "id" | "name">) => fs.name || fs.id

const NAME_RE = /^[\w .+=@/-]{0,128}$/

export function CreateFileSystemDialog({ open, onOpenChange }: { open: boolean; onOpenChange: (o: boolean) => void }) {
  const router = useRouter()
  const [name, setName] = useState("")
  const [readOnly, setReadOnly] = useState(false)
  const [tags, setTags] = useState<TagRow[]>([])
  const [pending, setPending] = useState(false)
  const [err, setErr] = useState<string | null>(null)

  useEffect(() => {
    if (open) {
      setName("")
      setReadOnly(false)
      setTags([])
      setErr(null)
    }
  }, [open])

  const submit = async (e: React.FormEvent) => {
    e.preventDefault()
    const n = name.trim()
    if (!NAME_RE.test(n)) {
      setErr("Up to 128 letters, digits, spaces and _ . + = @ / - characters")
      return
    }
    const keys = tags.map((r) => r.key.trim()).filter(Boolean)
    if (new Set(keys).size !== keys.length) {
      setErr("Tag keys must be unique")
      return
    }
    setPending(true)
    try {
      const body: CreateFileSystemInput = { name: n || undefined, read_only: readOnly, tags: rowsToTags(tags) }
      const fs = await api.post<FileSystem>(FILE_SYSTEMS_PATH, body)
      toast.success(`File system ${fs.id} created`)
      await revalidate(EFS_PATH)
      onOpenChange(false)
      router.push(fileSystemHref(fs.id))
    } catch (e) {
      toast.error(errorMessage(e))
    } finally {
      setPending(false)
    }
  }

  return (
    <Dialog open={open} onOpenChange={(o) => !pending && onOpenChange(o)}>
      <DialogContent className="max-h-[90vh] overflow-y-auto sm:max-w-xl">
        <form onSubmit={submit} className="flex flex-col gap-5">
          <DialogHeader>
            <DialogTitle>Create file system</DialogTitle>
            <DialogDescription>A shared, elastic file system (a Docker volume) that any number of instances can mount at the same time.</DialogDescription>
          </DialogHeader>

          <Field label="Name" htmlFor="fs-name" optional error={err} help="A display name, e.g. web-content. Up to 128 characters.">
            <Input id="fs-name" autoFocus autoComplete="off" value={name} onChange={(e) => (setName(e.target.value), setErr(null))} placeholder="web-content" />
          </Field>

          <div className="flex items-start justify-between gap-4 rounded-md border p-3">
            <div>
              <Label htmlFor="fs-ro" className="font-medium">
                Read-only
              </Label>
              <p className="text-muted-foreground mt-0.5 text-xs">
                Instances can only mount it read-only. Use it for content that is populated once and then shared, like static assets.
              </p>
            </div>
            <Switch id="fs-ro" checked={readOnly} onCheckedChange={setReadOnly} />
          </div>

          <Field label="Tags" optional>
            <TagsEditor rows={tags} onChange={setTags} />
          </Field>

          <DialogFooter>
            <Button type="button" variant="outline" onClick={() => onOpenChange(false)} disabled={pending}>
              Cancel
            </Button>
            <Button type="submit" disabled={pending}>
              {pending && <Loader2 className="animate-spin" />}
              Create file system
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}

/** DeleteFileSystemDialog deletes a file system; the API refuses while instances mount it. */
export function DeleteFileSystemDialog({
  fs,
  open,
  onOpenChange,
  onDeleted,
}: {
  fs: FileSystem | null
  open: boolean
  onOpenChange: (o: boolean) => void
  onDeleted?: () => void
}) {
  const [apiError, setApiError] = useState<string | null>(null)

  useEffect(() => {
    if (open) setApiError(null)
  }, [open])

  const mounted = fs?.mounted_by ?? []

  return (
    <ConfirmDialog
      open={open}
      onOpenChange={onOpenChange}
      title={`Delete file system ${fs ? fsLabel(fs) : ""}?`}
      description="All files stored in the file system are permanently deleted. This cannot be undone."
      confirmText={fs?.id}
      actionLabel="Delete file system"
      onConfirm={async () => {
        if (!fs) return
        setApiError(null)
        try {
          await api.del(`${FILE_SYSTEMS_PATH}/${seg(fs.id)}`)
        } catch (e) {
          if (e instanceof ApiError && e.code === "FileSystemInUse") {
            setApiError(`${errorMessage(e)}. Terminate those instances first: a file system can only be deleted when no instance mounts it (stopped instances count).`)
          } else {
            setApiError(errorMessage(e))
          }
          throw e
        }
        toast.success(`File system ${fs.id} deleted`)
        await revalidate(EFS_PATH)
        onDeleted?.()
      }}
    >
      {mounted.length > 0 && !apiError && (
        <div className="flex items-start gap-2 rounded-md border border-amber-300 bg-amber-50 p-3 text-sm text-amber-800 dark:border-amber-500/30 dark:bg-amber-500/10 dark:text-amber-300">
          <AlertTriangle className="mt-0.5 size-4 shrink-0" />
          <span>
            Mounted by{" "}
            {mounted.map((id, i) => (
              <span key={id}>
                {i > 0 && ", "}
                <Link href={`/ec2/instance/?id=${encodeURIComponent(id)}`} className="font-mono text-[13px] underline">
                  {id}
                </Link>
              </span>
            ))}
            . The deletion will be refused until those instances are terminated.
          </span>
        </div>
      )}
      {apiError && (
        <div className="border-destructive/30 bg-destructive/5 text-destructive flex items-start gap-2 rounded-md border p-3 text-sm">
          <AlertTriangle className="mt-0.5 size-4 shrink-0" />
          <span>{apiError}</span>
        </div>
      )}
    </ConfirmDialog>
  )
}
