"use client"

import { useCallback, useEffect, useRef, useState } from "react"
import { CheckCircle2, ChevronDown, ChevronUp, Loader2, Upload, X, XCircle } from "lucide-react"

import { Button } from "@/components/ui/button"
import { Progress } from "@/components/ui/progress"
import { StatusBadge } from "@/components/console/status-badge"
import { errorMessage, seg, upload, type UploadHandle } from "@/lib/api"
import { formatBytes, pluralize } from "@/lib/format"
import { cn } from "@/lib/utils"

export type UploadStatus = "queued" | "uploading" | "done" | "error" | "cancelled"

export interface UploadItem {
  id: number
  bucket: string
  key: string
  size: number
  loaded: number
  status: UploadStatus
  error?: string
  startedAt?: number
  finishedAt?: number
}

const MAX_PARALLEL = 3

/**
 * useUploads is a small upload queue: files are PUT to the object endpoint
 * (at most MAX_PARALLEL at a time) with progress, cancel and error states.
 * onSettled fires whenever the queue drains so the listing can be refreshed.
 */
export function useUploads(onSettled?: () => void) {
  const [items, setItems] = useState<UploadItem[]>([])
  const files = useRef(new Map<number, File>())
  const handles = useRef(new Map<number, UploadHandle>())
  const nextId = useRef(1)
  const settledRef = useRef(onSettled)
  settledRef.current = onSettled

  const patch = useCallback((id: number, p: Partial<UploadItem>) => {
    setItems((list) => list.map((it) => (it.id === id ? { ...it, ...p } : it)))
  }, [])

  const add = useCallback((bucket: string, entries: { file: File; key: string }[]) => {
    const created: UploadItem[] = entries.map(({ file, key }) => {
      const id = nextId.current++
      files.current.set(id, file)
      return { id, bucket, key, size: file.size, loaded: 0, status: "queued" }
    })
    setItems((list) => [...list, ...created])
  }, [])

  // Start queued uploads while fewer than MAX_PARALLEL are running.
  useEffect(() => {
    const running = items.filter((i) => i.status === "uploading").length
    const queued = items.filter((i) => i.status === "queued")
    if (running === 0 && queued.length === 0) return
    const slots = MAX_PARALLEL - running
    for (const it of queued.slice(0, Math.max(0, slots))) {
      const file = files.current.get(it.id)
      if (!file) continue
      const h = upload(`/api/v1/s3/buckets/${seg(it.bucket)}/object`, { key: it.key }, file, (loaded) => patch(it.id, { loaded }))
      handles.current.set(it.id, h)
      patch(it.id, { status: "uploading", startedAt: Date.now() })
      h.promise
        .then(() => patch(it.id, { status: "done", loaded: it.size, finishedAt: Date.now() }))
        .catch((e) => {
          const cancelled = e && typeof e === "object" && "code" in e && (e as { code: string }).code === "Aborted"
          patch(it.id, { status: cancelled ? "cancelled" : "error", error: cancelled ? undefined : errorMessage(e), finishedAt: Date.now() })
        })
        .finally(() => {
          handles.current.delete(it.id)
          files.current.delete(it.id)
        })
    }
  }, [items, patch])

  // Notify when all uploads have finished.
  const active = items.some((i) => i.status === "queued" || i.status === "uploading")
  const wasActive = useRef(false)
  useEffect(() => {
    if (wasActive.current && !active) settledRef.current?.()
    wasActive.current = active
  }, [active])

  // A finished upload also refreshes the list as soon as it lands (batches of many files).
  const doneCount = items.filter((i) => i.status === "done").length
  useEffect(() => {
    if (doneCount > 0 && active) settledRef.current?.()
  }, [doneCount, active])

  const cancel = useCallback(
    (id: number) => {
      const h = handles.current.get(id)
      if (h) h.abort()
      else {
        files.current.delete(id)
        patch(id, { status: "cancelled" })
      }
    },
    [patch],
  )

  const cancelAll = useCallback(() => {
    for (const it of items) if (it.status === "queued" || it.status === "uploading") cancel(it.id)
  }, [items, cancel])

  const clearCompleted = useCallback(() => {
    setItems((list) => list.filter((i) => i.status === "queued" || i.status === "uploading"))
  }, [])

  // Abort in-flight uploads when leaving the page.
  useEffect(() => {
    const hs = handles.current
    return () => hs.forEach((h) => h.abort())
  }, [])

  return { items, add, cancel, cancelAll, clearCompleted, active }
}

function speed(it: UploadItem): string {
  if (!it.startedAt || it.loaded === 0) return ""
  const secs = ((it.finishedAt ?? Date.now()) - it.startedAt) / 1000
  if (secs <= 0) return ""
  return `${formatBytes(it.loaded / secs)}/s`
}

/** UploadPanel is the floating bottom-right card listing uploads and their progress. */
export function UploadPanel({ uploads }: { uploads: ReturnType<typeof useUploads> }) {
  const { items, cancel, cancelAll, clearCompleted, active } = uploads
  const [collapsed, setCollapsed] = useState(false)
  if (items.length === 0) return null

  const done = items.filter((i) => i.status === "done").length
  const failed = items.filter((i) => i.status === "error").length
  const inFlight = items.filter((i) => i.status === "queued" || i.status === "uploading").length
  const totalBytes = items.reduce((a, i) => a + i.size, 0)
  const loadedBytes = items.reduce((a, i) => a + (i.status === "done" ? i.size : i.loaded), 0)
  const pct = totalBytes ? Math.round((loadedBytes / totalBytes) * 100) : active ? 0 : 100

  return (
    <div className="bg-popover text-popover-foreground fixed right-4 bottom-4 left-4 z-40 flex max-h-[60vh] flex-col overflow-hidden rounded-xl border shadow-lg sm:left-auto sm:w-[26rem]">
      <div className="flex items-center gap-2 border-b px-3 py-2">
        {active ? <Loader2 className="text-primary size-4 animate-spin" /> : failed ? <XCircle className="text-destructive size-4" /> : <CheckCircle2 className="text-success size-4" />}
        <div className="min-w-0 flex-1">
          <p className="truncate text-sm font-medium">
            {active ? `Uploading ${pluralize(inFlight, "file")}` : `Uploaded ${done} of ${pluralize(items.length, "file")}`}
          </p>
          <p className="text-muted-foreground text-xs">
            {formatBytes(loadedBytes)} of {formatBytes(totalBytes)}
            {failed > 0 && <span className="text-destructive"> · {failed} failed</span>}
          </p>
        </div>
        {active ? (
          <Button variant="ghost" size="sm" onClick={cancelAll}>
            Cancel all
          </Button>
        ) : (
          <Button variant="ghost" size="sm" onClick={clearCompleted}>
            Clear completed
          </Button>
        )}
        <Button variant="ghost" size="icon" className="size-7" onClick={() => setCollapsed(!collapsed)} aria-label={collapsed ? "Expand" : "Collapse"}>
          {collapsed ? <ChevronUp /> : <ChevronDown />}
        </Button>
      </div>
      {active && <Progress value={pct} className="h-1 rounded-none" />}
      {!collapsed && (
        <ul className="divide-y overflow-y-auto">
          {items.map((it) => {
            const p = it.size ? Math.round(((it.status === "done" ? it.size : it.loaded) / it.size) * 100) : it.status === "done" ? 100 : 0
            return (
              <li key={it.id} className="flex flex-col gap-1.5 px-3 py-2">
                <div className="flex items-center gap-2">
                  <Upload className="text-muted-foreground size-3.5 shrink-0" />
                  <span className="min-w-0 flex-1 truncate text-sm" title={it.key}>
                    {it.key}
                  </span>
                  {it.status === "done" && <StatusBadge status="success" label="Done" />}
                  {it.status === "error" && <StatusBadge status="failed" label="Failed" />}
                  {it.status === "cancelled" && <StatusBadge status="cancelled" label="Cancelled" tone="neutral" />}
                  {(it.status === "queued" || it.status === "uploading") && (
                    <button
                      type="button"
                      onClick={() => cancel(it.id)}
                      aria-label={`Cancel ${it.key}`}
                      className="text-muted-foreground hover:text-foreground hover:bg-accent inline-flex size-6 shrink-0 items-center justify-center rounded"
                    >
                      <X className="size-3.5" />
                    </button>
                  )}
                </div>
                {(it.status === "uploading" || it.status === "queued") && <Progress value={p} className="h-1.5" />}
                <div className={cn("text-muted-foreground flex justify-between gap-2 text-xs", it.status === "error" && "text-destructive")}>
                  <span className="truncate">
                    {it.status === "queued" && "Waiting"}
                    {it.status === "uploading" && `${p}% · ${formatBytes(it.loaded)} of ${formatBytes(it.size)}`}
                    {it.status === "done" && `Succeeded · ${formatBytes(it.size)}`}
                    {it.status === "cancelled" && "Cancelled"}
                    {it.status === "error" && (it.error ?? "Failed")}
                  </span>
                  {(it.status === "uploading" || it.status === "done") && <span className="shrink-0 tabular-nums">{speed(it)}</span>}
                </div>
              </li>
            )
          })}
        </ul>
      )}
    </div>
  )
}

// ---- drag and drop helpers ----

interface FsEntry {
  isFile: boolean
  isDirectory: boolean
  name: string
  fullPath: string
}
interface FsFileEntry extends FsEntry {
  file: (ok: (f: File) => void, err: (e: unknown) => void) => void
}
interface FsDirEntry extends FsEntry {
  createReader: () => { readEntries: (ok: (e: FsEntry[]) => void, err: (e: unknown) => void) => void }
}

async function walk(entry: FsEntry, path: string, out: { file: File; path: string }[]) {
  if (entry.isFile) {
    const f = await new Promise<File>((res, rej) => (entry as FsFileEntry).file(res, rej))
    out.push({ file: f, path: path + f.name })
  } else if (entry.isDirectory) {
    const reader = (entry as FsDirEntry).createReader()
    // readEntries returns results in batches; call until empty.
    for (;;) {
      const batch = await new Promise<FsEntry[]>((res, rej) => reader.readEntries(res, rej))
      if (!batch.length) break
      for (const e of batch) await walk(e, `${path}${entry.name}/`, out)
    }
  }
}

/** filesFromDrop returns the dropped files with relative paths (folders are walked recursively). */
export async function filesFromDrop(dt: DataTransfer): Promise<{ file: File; path: string }[]> {
  const out: { file: File; path: string }[] = []
  const items = Array.from(dt.items ?? [])
  const entries = items
    .filter((i) => i.kind === "file")
    .map((i) => (i as DataTransferItem & { webkitGetAsEntry?: () => FsEntry | null }).webkitGetAsEntry?.() ?? null)
  if (entries.length && entries.every(Boolean)) {
    for (const e of entries) await walk(e as FsEntry, "", out)
    return out
  }
  return Array.from(dt.files).map((f) => ({ file: f, path: f.name }))
}
