"use client"

import { useEffect, useState } from "react"
import { Download, ExternalLink, File, Folder, Link2, Loader2 } from "lucide-react"
import { toast } from "sonner"

import { Button } from "@/components/ui/button"
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog"
import { Input } from "@/components/ui/input"
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select"
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from "@/components/ui/sheet"
import { ConfirmDialog } from "@/components/console/confirm-dialog"
import { CopyButton, CopyableText } from "@/components/console/copy-button"
import { ErrorState } from "@/components/console/error-state"
import { Field } from "@/components/console/form-field"
import { KeyValueGrid } from "@/components/console/key-value"
import { DetailSkeleton } from "@/components/console/loading"
import { TimeAgo } from "@/components/console/time-ago"
import { api, authUrl, errorMessage, seg } from "@/lib/api"
import { baseName, formatBytes, formatDate, pluralize } from "@/lib/format"
import { useApi } from "@/lib/hooks"
import type { ObjectMeta, PresignResult } from "@/lib/types"

export const objectPath = (bucket: string) => `/api/v1/s3/buckets/${seg(bucket)}/object`

export function downloadUrl(bucket: string, key: string) {
  return authUrl(objectPath(bucket), { key })
}

export function openUrl(bucket: string, key: string) {
  return authUrl(objectPath(bucket), { key, inline: "true" })
}

/** triggerDownload starts a browser download of an object. */
export function triggerDownload(bucket: string, key: string) {
  const a = document.createElement("a")
  a.href = downloadUrl(bucket, key)
  a.download = baseName(key)
  document.body.appendChild(a)
  a.click()
  a.remove()
}

const EXPIRIES = [
  { label: "1 hour", value: 3600 },
  { label: "24 hours", value: 86400 },
  { label: "7 days", value: 7 * 86400 },
]

/** PresignDialog creates a time-limited share link for an object. */
export function PresignDialog({ bucket, objectKey, open, onOpenChange }: { bucket: string; objectKey: string; open: boolean; onOpenChange: (o: boolean) => void }) {
  const [expires, setExpires] = useState("3600")
  const [result, setResult] = useState<PresignResult | null>(null)
  const [pending, setPending] = useState(false)

  useEffect(() => {
    if (open) setResult(null)
  }, [open, objectKey])

  const generate = async () => {
    setPending(true)
    try {
      const r = await api.post<PresignResult>(`/api/v1/s3/buckets/${seg(bucket)}/presign`, { key: objectKey, expires_seconds: Number(expires) })
      setResult(r)
    } catch (e) {
      toast.error(errorMessage(e))
    } finally {
      setPending(false)
    }
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-lg">
        <DialogHeader>
          <DialogTitle>Share object with a presigned URL</DialogTitle>
          <DialogDescription>
            Anyone with the URL can download <span className="text-foreground font-mono break-all">{baseName(objectKey)}</span> until it expires.
          </DialogDescription>
        </DialogHeader>
        <div className="flex flex-col gap-4">
          <Field label="Expires after" htmlFor="presign-expiry" help="Changing the expiry invalidates the URL shown below; create a new one.">
            <Select
              value={expires}
              onValueChange={(v) => {
                setExpires(v)
                setResult(null)
              }}
            >
              <SelectTrigger id="presign-expiry" className="w-full sm:w-48">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {EXPIRIES.map((e) => (
                  <SelectItem key={e.value} value={String(e.value)}>
                    {e.label}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </Field>
          {result && (
            <Field label="Presigned URL" htmlFor="presign-url" help={`Expires ${formatDate(result.expires_at)}`}>
              <div className="flex gap-2">
                <Input id="presign-url" readOnly value={result.url} className="font-mono text-xs" onFocus={(e) => e.currentTarget.select()} />
                <CopyButton value={result.url} size="sm" toastMessage="Presigned URL copied" />
              </div>
            </Field>
          )}
        </div>
        <DialogFooter>
          <Button variant="outline" onClick={() => onOpenChange(false)}>
            Close
          </Button>
          <Button onClick={generate} disabled={pending}>
            {pending ? <Loader2 className="animate-spin" /> : <Link2 />}
            {result ? "Regenerate URL" : "Create presigned URL"}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

/** DeleteObjectsDialog deletes files (by key) and folders (recursively, by prefix). */
export function DeleteObjectsDialog({
  bucket,
  keys,
  open,
  onOpenChange,
  onDeleted,
}: {
  bucket: string
  keys: string[]
  open: boolean
  onOpenChange: (o: boolean) => void
  onDeleted: () => void
}) {
  const folders = keys.filter((k) => k.endsWith("/"))
  const files = keys.filter((k) => !k.endsWith("/"))
  return (
    <ConfirmDialog
      open={open}
      onOpenChange={onOpenChange}
      title={`Delete ${pluralize(keys.length, "item")}?`}
      description={
        folders.length
          ? "Deleting a folder permanently deletes every object inside it. This cannot be undone."
          : "The selected objects will be permanently deleted. This cannot be undone."
      }
      confirmText={folders.length ? "delete" : undefined}
      actionLabel="Delete objects"
      onConfirm={async () => {
        let done = 0
        try {
          for (const k of keys) {
            await api.del(objectPath(bucket), k.endsWith("/") ? { key: k, recursive: "true" } : { key: k })
            done++
          }
        } finally {
          if (done) onDeleted()
        }
        toast.success(`Deleted ${pluralize(done, "item")}`)
      }}
    >
      <ul className="bg-muted/50 max-h-48 overflow-y-auto rounded-md border text-sm">
        {[...folders, ...files].map((k) => (
          <li key={k} className="flex items-center gap-2 border-b px-3 py-1.5 last:border-0">
            {k.endsWith("/") ? <Folder className="text-warning size-4 shrink-0" /> : <File className="text-muted-foreground size-4 shrink-0" />}
            <span className="min-w-0 truncate font-mono text-[13px]" title={k}>
              {k}
            </span>
            {k.endsWith("/") && <span className="text-muted-foreground ml-auto shrink-0 text-xs">and all contents</span>}
          </li>
        ))}
      </ul>
    </ConfirmDialog>
  )
}

/** CreateFolderDialog creates a zero-byte "prefix/name/" object. */
export function CreateFolderDialog({
  bucket,
  prefix,
  open,
  onOpenChange,
  onCreated,
}: {
  bucket: string
  prefix: string
  open: boolean
  onOpenChange: (o: boolean) => void
  onCreated: () => void
}) {
  const [name, setName] = useState("")
  const [pending, setPending] = useState(false)
  useEffect(() => {
    if (open) setName("")
  }, [open])

  const trimmed = name.trim().replace(/^\/+|\/+$/g, "")
  const err = !trimmed ? null : trimmed.includes("//") ? "Folder names cannot contain consecutive slashes" : null

  const submit = async (e: React.FormEvent) => {
    e.preventDefault()
    if (!trimmed || err) return
    setPending(true)
    try {
      await api.post(`/api/v1/s3/buckets/${seg(bucket)}/folders`, { key: `${prefix}${trimmed}/` })
      toast.success(`Folder ${trimmed} created`)
      onCreated()
      onOpenChange(false)
    } catch (e) {
      toast.error(errorMessage(e))
    } finally {
      setPending(false)
    }
  }

  return (
    <Dialog open={open} onOpenChange={(o) => !pending && onOpenChange(o)}>
      <DialogContent className="sm:max-w-md">
        <form onSubmit={submit} className="flex flex-col gap-4">
          <DialogHeader>
            <DialogTitle>Create folder</DialogTitle>
            <DialogDescription>
              Folders group objects under a shared key prefix
              {prefix && (
                <>
                  {" "}
                  inside <span className="text-foreground font-mono">{prefix}</span>
                </>
              )}
              .
            </DialogDescription>
          </DialogHeader>
          <Field label="Folder name" htmlFor="folder-name" error={err} help="Use / to create nested folders, e.g. photos/2026">
            <Input id="folder-name" autoFocus autoComplete="off" value={name} onChange={(e) => setName(e.target.value)} placeholder="photos" />
          </Field>
          <DialogFooter>
            <Button type="button" variant="outline" onClick={() => onOpenChange(false)} disabled={pending}>
              Cancel
            </Button>
            <Button type="submit" disabled={pending || !trimmed || !!err}>
              {pending && <Loader2 className="animate-spin" />}
              Create folder
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}

/** ObjectSheet is the object details side panel. */
export function ObjectSheet({
  bucket,
  objectKey,
  onOpenChange,
  onShare,
}: {
  bucket: string
  objectKey: string | null
  onOpenChange: (o: boolean) => void
  onShare: (key: string) => void
}) {
  const { data, error, isLoading, mutate } = useApi<ObjectMeta>(objectKey ? `${objectPath(bucket)}/meta` : null, {
    query: { key: objectKey ?? "" },
    revalidateOnFocus: false,
  })
  const key = objectKey ?? ""
  const uri = `s3://${bucket}/${key}`
  const meta = Object.entries(data?.metadata ?? {})

  return (
    <Sheet open={!!objectKey} onOpenChange={onOpenChange}>
      <SheetContent className="w-full gap-0 overflow-y-auto sm:max-w-lg">
        <SheetHeader className="border-b">
          <SheetTitle className="pr-6 break-all">{baseName(key)}</SheetTitle>
          <SheetDescription className="font-mono text-xs break-all">{uri}</SheetDescription>
          <div className="flex flex-wrap gap-2 pt-2">
            <Button size="sm" asChild>
              <a href={objectKey ? downloadUrl(bucket, key) : undefined} download={baseName(key)}>
                <Download /> Download
              </a>
            </Button>
            <Button size="sm" variant="outline" asChild>
              <a href={objectKey ? openUrl(bucket, key) : undefined} target="_blank" rel="noreferrer">
                <ExternalLink /> Open
              </a>
            </Button>
            <Button size="sm" variant="outline" onClick={() => onShare(key)}>
              <Link2 /> Copy link
            </Button>
          </div>
        </SheetHeader>
        <div className="flex flex-col gap-6 p-4">
          {error ? (
            <ErrorState error={error} onRetry={() => mutate()} />
          ) : isLoading || !data ? (
            <DetailSkeleton />
          ) : (
            <>
              <KeyValueGrid
                columns={2}
                items={[
                  { label: "Key", value: <CopyableText value={data.key} />, wide: true },
                  { label: "Size", value: `${formatBytes(data.size)} (${data.size.toLocaleString()} bytes)` },
                  { label: "Type", value: data.content_type },
                  { label: "Last modified", value: <TimeAgo value={data.last_modified} /> },
                  { label: "Version ID", value: data.version_id ? <CopyableText value={data.version_id} /> : "null" },
                  { label: "ETag", value: <CopyableText value={data.etag.replace(/"/g, "")} />, wide: true },
                  { label: "S3 URI", value: <CopyableText value={uri} />, wide: true },
                  { label: "Amazon Resource Name (ARN)", value: <CopyableText value={data.arn} />, wide: true },
                  { label: "Object URL", value: <CopyableText value={data.url} />, wide: true },
                ]}
              />
              <div className="flex flex-col gap-2">
                <h3 className="hc-eyebrow">Metadata</h3>
                {meta.length === 0 ? (
                  <p className="text-muted-foreground text-sm">No user-defined metadata.</p>
                ) : (
                  <div className="overflow-x-auto rounded-md border">
                  <table className="w-full text-sm">
                    <thead>
                      <tr className="bg-muted text-muted-foreground border-b text-left text-xs">
                        <th className="px-3 py-1.5 font-semibold">Key</th>
                        <th className="px-3 py-1.5 font-semibold">Value</th>
                      </tr>
                    </thead>
                    <tbody>
                      {meta.map(([k, v]) => (
                        <tr key={k} className="border-b last:border-0">
                          <td className="px-3 py-1.5 font-mono text-[13px]">{k}</td>
                          <td className="px-3 py-1.5 break-all">{v}</td>
                        </tr>
                      ))}
                    </tbody>
                  </table>
                  </div>
                )}
              </div>
            </>
          )}
        </div>
      </SheetContent>
    </Sheet>
  )
}
