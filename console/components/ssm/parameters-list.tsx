"use client"

import { useEffect, useMemo, useState } from "react"
import Link from "next/link"
import { ChevronRight, Folder, FolderOpen, Plus, SlidersHorizontal, Trash2, X } from "lucide-react"
import { toast } from "sonner"

import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { ConfirmDialog } from "@/components/console/confirm-dialog"
import { DataTable, cellLinkClass, type Column } from "@/components/console/data-table"
import { EmptyState } from "@/components/console/empty-state"
import { PageHeader } from "@/components/console/page-header"
import { TimeAgo } from "@/components/console/time-ago"
import { keyHref, keyIdFromArn, keyLabel, useKmsKeys } from "@/components/kms/shared"
import { api } from "@/lib/api"
import { pluralize } from "@/lib/format"
import { revalidate, useApi, useQueryParam, useSetQueryParam } from "@/lib/hooks"
import type { KmsKey, SsmParameter } from "@/lib/types"
import { cn } from "@/lib/utils"

import { PARAMETERS_PATH, PARAMETER_PATH, SSM_PATH, TypeBadge, parameterHref } from "./shared"

/** folderOf returns the "directory" part of a prefix: "/app/d" -> "/app/", "" -> "". */
const folderOf = (prefix: string) => prefix.slice(0, prefix.lastIndexOf("/") + 1)

/** childFolders lists the immediate sub-folders under folder with how many parameters each holds. */
function childFolders(names: string[], folder: string): [string, number][] {
  const base = folder || "/"
  const out = new Map<string, number>()
  for (const n of names) {
    if (!n.startsWith(base)) continue
    const rest = n.slice(base.length)
    const i = rest.indexOf("/")
    if (i > 0) {
      const f = base + rest.slice(0, i + 1)
      out.set(f, (out.get(f) ?? 0) + 1)
    }
  }
  return [...out.entries()].sort((a, b) => a[0].localeCompare(b[0]))
}

/** crumbsOf splits "/app/db/" into ["/", "/app/", "/app/db/"]. */
function crumbsOf(folder: string): string[] {
  if (!folder) return []
  const parts = folder.split("/").filter(Boolean)
  const out = ["/"]
  let acc = "/"
  for (const p of parts) {
    acc += `${p}/`
    out.push(acc)
  }
  return out
}

function KeyCell({ p, keys }: { p: SsmParameter; keys?: KmsKey[] }) {
  if (p.type !== "SecureString" || !p.key_id) return <span className="text-muted-foreground">-</span>
  const id = keyIdFromArn(p.key_id)
  const k = keys?.find((x) => x.id === id)
  return (
    <Link href={keyHref(id)} onClick={(e) => e.stopPropagation()} className="text-primary font-mono text-[13px] break-all hover:underline" title={p.key_id}>
      {k ? keyLabel(k) : `${id.slice(0, 8)}...`}
    </Link>
  )
}

export function ParametersList() {
  const pathParam = useQueryParam("path")
  const setParam = useSetQueryParam()
  const [prefix, setPrefix] = useState(pathParam)
  const { data, error, isLoading, isValidating, mutate } = useApi<SsmParameter[]>(PARAMETERS_PATH, { refreshInterval: 15_000 })
  const keys = useKmsKeys()
  const [selected, setSelected] = useState<string[]>([])
  const [deleting, setDeleting] = useState(false)

  // Follow back/forward navigation.
  useEffect(() => setPrefix(pathParam), [pathParam])

  const applyPrefix = (p: string) => {
    setPrefix(p)
    setParam("path", p || null)
  }

  const names = useMemo(() => (data ?? []).map((p) => p.name), [data])
  const rows = useMemo(() => (data ?? []).filter((p) => !prefix || p.name.startsWith(prefix)), [data, prefix])
  const folder = folderOf(prefix)
  const folders = useMemo(() => childFolders(names, folder), [names, folder])
  const crumbs = crumbsOf(folder)

  const columns: Column<SsmParameter>[] = useMemo(
    () => [
      {
        id: "name",
        header: "Name",
        cell: (p) => {
          const rel = folder && p.name.startsWith(folder) ? p.name.slice(folder.length) : null
          return (
            <Link href={parameterHref(p.name)} onClick={(e) => e.stopPropagation()} className={cn(cellLinkClass(), "font-mono text-[13px] break-all")} title={p.name}>
              {rel !== null ? (
                <>
                  <span className="text-muted-foreground font-normal">{folder}</span>
                  {rel}
                </>
              ) : (
                p.name
              )}
            </Link>
          )
        },
        value: (p) => p.name,
      },
      { id: "type", header: "Type", cell: (p) => <TypeBadge type={p.type} />, value: (p) => p.type },
      { id: "version", header: "Version", cell: (p) => <span className="tabular-nums">{p.version}</span>, value: (p) => p.version, hideBelow: "sm" },
      { id: "modified", header: "Last modified", cell: (p) => <TimeAgo value={p.last_modified} />, value: (p) => p.last_modified, hideBelow: "sm" },
      {
        id: "desc",
        header: "Description",
        cell: (p) => <span className="line-clamp-2 max-w-72">{p.description || <span className="text-muted-foreground">-</span>}</span>,
        value: (p) => p.description,
        hideBelow: "md",
      },
      { id: "key", header: "KMS key", cell: (p) => <KeyCell p={p} keys={keys.data} />, value: (p) => p.key_id, hideBelow: "lg" },
    ],
    [folder, keys.data],
  )

  const sel = rows.filter((p) => selected.includes(p.name))

  const deleteSelected = async () => {
    const failed: string[] = []
    for (const p of sel) {
      try {
        await api.del(PARAMETER_PATH, { name: p.name })
      } catch {
        failed.push(p.name)
      }
    }
    await revalidate(SSM_PATH)
    const ok = sel.length - failed.length
    if (ok) toast.success(`Deleted ${pluralize(ok, "parameter")}`)
    if (failed.length) throw new Error(`Could not delete ${failed.join(", ")}`)
    setSelected([])
  }

  return (
    <div className="flex flex-col gap-4">
      <PageHeader
        title="Parameter Store"
        description="Hierarchical configuration data and secrets for your applications. SecureString values are encrypted with KMS."
        breadcrumbs={[{ label: "Systems Manager", href: "/ssm/" }, { label: "Parameter Store" }]}
      />

      {data && (folders.length > 0 || crumbs.length > 0) && (
        <div className="bg-card flex flex-col gap-2 rounded-lg border px-4 py-3 text-sm sm:flex-row sm:items-center">
          <nav aria-label="Path" className="flex min-w-0 flex-wrap items-center gap-1">
            <span className="text-muted-foreground mr-1 text-xs font-medium">Path</span>
            <button type="button" onClick={() => applyPrefix("")} className={cn("hover:underline", folder ? "text-primary" : "text-foreground font-medium")}>
              All
            </button>
            {crumbs.map((c, i) => (
              <span key={c} className="flex items-center gap-1">
                <ChevronRight className="text-muted-foreground size-3.5" />
                <button
                  type="button"
                  onClick={() => applyPrefix(c)}
                  className={cn("font-mono text-[13px] hover:underline", i === crumbs.length - 1 && c === prefix ? "text-foreground font-medium" : "text-primary")}
                >
                  {i === 0 ? "/" : c.split("/").filter(Boolean).pop()}
                </button>
              </span>
            ))}
          </nav>
          {folders.length > 0 && (
            <div className="flex flex-wrap gap-1.5 sm:ml-auto">
              {folders.map(([f, n]) => (
                <button
                  key={f}
                  type="button"
                  onClick={() => applyPrefix(f)}
                  className="bg-muted/60 hover:bg-muted inline-flex items-center gap-1.5 rounded-md border px-2 py-1 font-mono text-xs transition-colors"
                  title={`${f} (${pluralize(n, "parameter")})`}
                >
                  <Folder className="text-muted-foreground size-3.5" />
                  {f.slice(folder.length || 1)}
                  <span className="text-muted-foreground">{n}</span>
                </button>
              ))}
            </div>
          )}
        </div>
      )}

      <DataTable
        title="My parameters"
        data={data ? rows : undefined}
        count={data ? rows.length : undefined}
        columns={columns}
        rowId={(p) => p.name}
        loading={isLoading}
        error={error}
        onRefresh={() => mutate()}
        refreshing={isValidating}
        selection="multi"
        selected={selected}
        onSelectedChange={setSelected}
        searchPlaceholder="Filter by name, type or description"
        defaultSort={{ id: "name" }}
        actions={
          <>
            <Button variant="outline" size="sm" disabled={!sel.length} onClick={() => setDeleting(true)} className="text-destructive hover:text-destructive">
              <Trash2 /> Delete{sel.length ? ` (${sel.length})` : ""}
            </Button>
            <Button size="sm" asChild>
              <Link href="/ssm/create/">
                <Plus /> Create parameter
              </Link>
            </Button>
          </>
        }
        filters={
          <div className="relative w-full sm:w-56">
            <SlidersHorizontal className="text-muted-foreground pointer-events-none absolute top-1/2 left-2.5 size-4 -translate-y-1/2" />
            <Input
              value={prefix}
              onChange={(e) => applyPrefix(e.target.value)}
              placeholder="Path prefix, e.g. /app/"
              aria-label="Path prefix"
              spellCheck={false}
              className="h-8 pr-8 pl-8 font-mono text-[13px]"
            />
            {prefix && (
              <button
                type="button"
                aria-label="Clear prefix"
                onClick={() => applyPrefix("")}
                className="text-muted-foreground hover:text-foreground absolute top-1/2 right-2 -translate-y-1/2"
              >
                <X className="size-4" />
              </button>
            )}
          </div>
        }
        empty={
          (data?.length ?? 0) > 0 ? (
            <EmptyState
              icon={FolderOpen}
              title="No parameters under this path"
              description={<>No parameter names start with <span className="font-mono">{prefix}</span>.</>}
              action={
                <Button size="sm" variant="outline" onClick={() => applyPrefix("")}>
                  Show all parameters
                </Button>
              }
            />
          ) : (
            <EmptyState
              icon={SlidersHorizontal}
              title="No parameters"
              description="Store configuration values like /app/db/url, and secrets as encrypted SecureString parameters."
              action={
                <Button size="sm" asChild>
                  <Link href="/ssm/create/">
                    <Plus /> Create parameter
                  </Link>
                </Button>
              }
            />
          )
        }
      />

      <ConfirmDialog
        open={deleting}
        onOpenChange={setDeleting}
        title={sel.length === 1 ? "Delete parameter" : `Delete ${pluralize(sel.length, "parameter")}`}
        confirmText={sel.length === 1 ? sel[0].name : "delete"}
        description={
          <div className="flex flex-col gap-2">
            <p>The parameters and their whole version history are deleted permanently:</p>
            <ul className="max-h-40 overflow-y-auto rounded-md border p-2 font-mono text-xs">
              {sel.map((p) => (
                <li key={p.name} className="break-all">
                  {p.name}
                </li>
              ))}
            </ul>
          </div>
        }
        onConfirm={deleteSelected}
      />
    </div>
  )
}
