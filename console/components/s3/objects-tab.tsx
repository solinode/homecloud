"use client"

import { useCallback, useMemo, useRef, useState } from "react"
import { Copy, Download, File, FileText, Folder, FolderPlus, Image as ImageIcon, Info, Link2, Trash2, Upload, UploadCloud } from "lucide-react"
import { toast } from "sonner"

import { Button } from "@/components/ui/button"
import { ActionsMenu } from "@/components/console/actions-menu"
import { copyText } from "@/components/console/copy-button"
import { DataTable, type Column } from "@/components/console/data-table"
import { EmptyState } from "@/components/console/empty-state"
import { seg } from "@/lib/api"
import { baseName, formatBytes, formatDate, pluralize } from "@/lib/format"
import { revalidate, useApi } from "@/lib/hooks"
import type { ObjectListing } from "@/lib/types"
import { cn } from "@/lib/utils"

import { objectExt } from "./common"
import { CreateFolderDialog, DeleteObjectsDialog, ObjectSheet, PresignDialog, triggerDownload } from "./object-dialogs"
import { filesFromDrop, UploadPanel, useUploads } from "./uploads"

interface Row {
  key: string
  name: string
  folder: boolean
  size?: number
  lastModified?: string
  storageClass?: string
}

const IMAGE_EXT = new Set(["png", "jpg", "jpeg", "gif", "webp", "svg", "bmp", "ico", "avif"])
const TEXT_EXT = new Set(["txt", "md", "json", "csv", "log", "yaml", "yml", "xml", "html", "css", "js", "ts", "go", "py", "sh"])

function FileIcon({ row }: { row: Row }) {
  if (row.folder) return <Folder className="size-4 shrink-0 fill-amber-400/30 text-amber-500" />
  const ext = objectExt(row.key)
  if (IMAGE_EXT.has(ext)) return <ImageIcon className="size-4 shrink-0 text-sky-600 dark:text-sky-400" />
  if (TEXT_EXT.has(ext)) return <FileText className="text-muted-foreground size-4 shrink-0" />
  return <File className="text-muted-foreground size-4 shrink-0" />
}

/** PrefixBreadcrumbs renders "bucket / folder / subfolder" for the current prefix. */
function PrefixBreadcrumbs({ bucket, prefix, onNavigate }: { bucket: string; prefix: string; onNavigate: (p: string) => void }) {
  const parts = prefix.split("/").filter(Boolean)
  const crumbs = [{ label: bucket, prefix: "" }, ...parts.map((p, i) => ({ label: p, prefix: parts.slice(0, i + 1).join("/") + "/" }))]
  return (
    <nav aria-label="Folder" className="flex min-w-0 flex-wrap items-center gap-1 text-sm">
      {crumbs.map((c, i) => (
        <span key={c.prefix} className="flex min-w-0 items-center gap-1">
          {i > 0 && <span className="text-muted-foreground">/</span>}
          {i < crumbs.length - 1 ? (
            <button type="button" className="text-primary truncate hover:underline" onClick={() => onNavigate(c.prefix)}>
              {c.label}
            </button>
          ) : (
            <span className="truncate font-medium">{c.label}</span>
          )}
        </span>
      ))}
    </nav>
  )
}

export function ObjectsTab({ bucket, prefix, onPrefixChange }: { bucket: string; prefix: string; onPrefixChange: (p: string) => void }) {
  const listPath = `/api/v1/s3/buckets/${seg(bucket)}/objects`
  const { data, error, isLoading, isValidating, mutate } = useApi<ObjectListing>(listPath, { query: { prefix } })
  const [selected, setSelected] = useState<string[]>([])
  const [sheetKey, setSheetKey] = useState<string | null>(null)
  const [shareKey, setShareKey] = useState<string | null>(null)
  const [deleteKeys, setDeleteKeys] = useState<string[] | null>(null)
  const [folderOpen, setFolderOpen] = useState(false)
  const [dragging, setDragging] = useState(false)
  const dragDepth = useRef(0)
  const fileInput = useRef<HTMLInputElement>(null)

  const refresh = useCallback(() => {
    revalidate(`/api/v1/s3/buckets/${seg(bucket)}`)
  }, [bucket])
  const uploads = useUploads(refresh)

  // Only rows of the currently listed prefix count (the listing may be stale while navigating).
  const rows = useMemo<Row[] | undefined>(() => {
    if (!data || data.prefix !== prefix) return undefined
    const folders: Row[] = data.prefixes.map((p) => ({ key: p, name: baseName(p), folder: true }))
    const files: Row[] = data.objects
      .filter((o) => o.key !== prefix && !o.delete_marker)
      .map((o) => ({ key: o.key, name: o.key.slice(prefix.length), folder: false, size: o.size, lastModified: o.last_modified, storageClass: o.storage_class }))
    return [...folders, ...files]
  }, [data, prefix])

  const navigate = (p: string) => {
    setSelected([])
    onPrefixChange(p)
  }

  const addFiles = (entries: { file: File; path: string }[]) => {
    if (!entries.length) return
    uploads.add(
      bucket,
      entries.map((e) => ({ file: e.file, key: prefix + e.path })),
    )
  }

  const selRows = (rows ?? []).filter((r) => selected.includes(r.key))
  const single = selRows.length === 1 ? selRows[0] : undefined
  const singleFile = single && !single.folder ? single : undefined

  const columns: Column<Row>[] = [
    {
      id: "name",
      header: "Name",
      value: (r) => (r.folder ? `0${r.name}` : `1${r.name}`),
      cell: (r) => (
        <button
          type="button"
          className="text-primary inline-flex max-w-[28rem] items-center gap-2 text-left font-medium hover:underline"
          onClick={(e) => {
            e.stopPropagation()
            if (r.folder) navigate(r.key)
            else setSheetKey(r.key)
          }}
          title={r.key}
        >
          <FileIcon row={r} />
          <span className="truncate">
            {r.name}
            {r.folder && "/"}
          </span>
        </button>
      ),
    },
    { id: "type", header: "Type", value: (r) => (r.folder ? "Folder" : objectExt(r.key)), cell: (r) => (r.folder ? "Folder" : objectExt(r.key) || "-"), hideBelow: "sm" },
    {
      id: "size",
      header: "Size",
      value: (r) => r.size ?? -1,
      cell: (r) => <span className="whitespace-nowrap tabular-nums">{r.folder ? "-" : formatBytes(r.size)}</span>,
    },
    {
      id: "modified",
      header: "Last modified",
      value: (r) => r.lastModified ?? "",
      cell: (r) => <span className="whitespace-nowrap">{r.folder ? "-" : formatDate(r.lastModified)}</span>,
      hideBelow: "md",
    },
    { id: "class", header: "Storage class", value: (r) => r.storageClass ?? "", cell: (r) => (r.folder ? "-" : r.storageClass), hideBelow: "lg" },
  ]

  const onDragEnter = (e: React.DragEvent) => {
    if (!Array.from(e.dataTransfer.types).includes("Files")) return
    e.preventDefault()
    dragDepth.current++
    setDragging(true)
  }
  const onDragLeave = (e: React.DragEvent) => {
    e.preventDefault()
    dragDepth.current = Math.max(0, dragDepth.current - 1)
    if (dragDepth.current === 0) setDragging(false)
  }
  const onDragOver = (e: React.DragEvent) => {
    if (!Array.from(e.dataTransfer.types).includes("Files")) return
    e.preventDefault()
    e.dataTransfer.dropEffect = "copy"
  }
  const onDrop = async (e: React.DragEvent) => {
    e.preventDefault()
    dragDepth.current = 0
    setDragging(false)
    try {
      addFiles(await filesFromDrop(e.dataTransfer))
    } catch {
      toast.error("Could not read the dropped files")
    }
  }

  const uploadButton = (
    <Button size="sm" onClick={() => fileInput.current?.click()}>
      <Upload /> Upload
    </Button>
  )

  return (
    <div className="relative flex flex-col gap-3" onDragEnter={onDragEnter} onDragLeave={onDragLeave} onDragOver={onDragOver} onDrop={onDrop}>
      <input
        ref={fileInput}
        type="file"
        multiple
        className="hidden"
        onChange={(e) => {
          addFiles(Array.from(e.target.files ?? []).map((f) => ({ file: f, path: f.name })))
          e.target.value = ""
        }}
      />

      <div className="flex flex-wrap items-center justify-between gap-2">
        <PrefixBreadcrumbs bucket={bucket} prefix={prefix} onNavigate={navigate} />
        <p className="text-muted-foreground hidden text-xs sm:block">Drag and drop files or folders here to upload them.</p>
      </div>

      <DataTable
        title="Objects"
        description={
          data?.truncated && data.prefix === prefix ? (
            <span className="text-amber-700 dark:text-amber-400">Showing the first 1,000 items of this folder. Use the AWS CLI or an SDK to list everything.</span>
          ) : (
            "Objects are the files stored in this bucket."
          )
        }
        data={rows}
        columns={columns}
        rowId={(r) => r.key}
        loading={isLoading || (!!data && data.prefix !== prefix)}
        error={error}
        onRefresh={() => mutate()}
        refreshing={isValidating}
        selection="multi"
        selected={selected}
        onSelectedChange={setSelected}
        searchPlaceholder="Find objects by name in this folder"
        defaultSort={{ id: "name" }}
        pageSize={50}
        actions={
          <>
            <ActionsMenu
              disabled={selRows.length === 0}
              items={[
                { label: "Download", icon: <Download />, disabled: !singleFile, onSelect: () => singleFile && triggerDownload(bucket, singleFile.key) },
                { label: "Copy share link", icon: <Link2 />, disabled: !singleFile, onSelect: () => singleFile && setShareKey(singleFile.key) },
                {
                  label: "Copy S3 URI",
                  icon: <Copy />,
                  disabled: !single,
                  onSelect: async () => {
                    if (single && (await copyText(`s3://${bucket}/${single.key}`))) toast.success("S3 URI copied")
                  },
                },
                { label: "Object details", icon: <Info />, disabled: !singleFile, onSelect: () => singleFile && setSheetKey(singleFile.key) },
                { separator: true },
                { label: "Delete", icon: <Trash2 />, destructive: true, onSelect: () => setDeleteKeys(selRows.map((r) => r.key)) },
              ]}
            />
            <Button size="sm" variant="outline" onClick={() => setFolderOpen(true)}>
              <FolderPlus /> Create folder
            </Button>
            {uploadButton}
          </>
        }
        empty={
          <EmptyState
            icon={UploadCloud}
            title="No objects"
            description={
              prefix ? "This folder is empty. Upload files or drag and drop them here." : "This bucket is empty. Upload files or drag and drop them anywhere on this panel."
            }
            action={uploadButton}
          />
        }
      />

      {dragging && (
        <div className="border-primary bg-primary/5 pointer-events-none absolute inset-0 z-20 flex items-center justify-center rounded-lg border-2 border-dashed backdrop-blur-[1px]">
          <div className="bg-card flex flex-col items-center gap-2 rounded-lg border px-6 py-4 shadow-md">
            <UploadCloud className="text-primary size-8" />
            <p className="text-sm font-medium">Drop files to upload</p>
            <p className="text-muted-foreground font-mono text-xs">
              s3://{bucket}/{prefix}
            </p>
          </div>
        </div>
      )}

      <UploadPanel uploads={uploads} />

      <ObjectSheet bucket={bucket} objectKey={sheetKey} onOpenChange={(o) => !o && setSheetKey(null)} onShare={(k) => setShareKey(k)} />
      <PresignDialog bucket={bucket} objectKey={shareKey ?? ""} open={!!shareKey} onOpenChange={(o) => !o && setShareKey(null)} />
      <DeleteObjectsDialog
        bucket={bucket}
        keys={deleteKeys ?? []}
        open={!!deleteKeys}
        onOpenChange={(o) => !o && setDeleteKeys(null)}
        onDeleted={() => {
          setSelected([])
          refresh()
        }}
      />
      <CreateFolderDialog bucket={bucket} prefix={prefix} open={folderOpen} onOpenChange={setFolderOpen} onCreated={() => mutate()} />
      <span className={cn("sr-only")} aria-live="polite">
        {uploads.active ? `Uploading ${pluralize(uploads.items.length, "file")}` : ""}
      </span>
    </div>
  )
}
