"use client"

import { useEffect, useState } from "react"
import Link from "next/link"
import { Boxes, Copy, Plus, Terminal, Trash2 } from "lucide-react"
import useSWR from "swr"
import { toast } from "sonner"

import { Button } from "@/components/ui/button"
import { Skeleton } from "@/components/ui/skeleton"
import { ActionsMenu } from "@/components/console/actions-menu"
import { CopyableText, copyText } from "@/components/console/copy-button"
import { DataTable, type Column } from "@/components/console/data-table"
import { EmptyState } from "@/components/console/empty-state"
import { PageHeader } from "@/components/console/page-header"
import { TimeAgo } from "@/components/console/time-ago"
import { api } from "@/lib/api"
import { formatBytes, formatDate } from "@/lib/format"
import { useApi, useQueryParam, useSetQueryParam } from "@/lib/hooks"
import type { EcrRepository, EcrRepositoryDetail } from "@/lib/types"

import { CreateRepositoryDialog, DeleteRepositoryDialog, PushCommandsDialog, REPOS_PATH, RegistryPanel, repoApiPath, repoHref } from "./common"

interface RepoStats {
  images: number
  size: number
  lastPushed?: string
  pushCommands: string[]
}

type Row = EcrRepository & { stats?: RepoStats; statsError?: boolean }

const validTime = (t?: string) => !!t && new Date(t).getFullYear() > 1970

/**
 * useRepoStats describes every repository (the list endpoint has no image
 * counts). The key starts with REPOS_PATH so revalidate("/api/v1/ecr") refreshes it.
 */
function useRepoStats(repos: EcrRepository[] | undefined) {
  const names = (repos ?? []).map((r) => r.name).sort()
  const key = names.length ? `${REPOS_PATH}#stats=${names.join(",")}` : null
  return useSWR<Record<string, RepoStats | null>>(
    key,
    async () => {
      const out: Record<string, RepoStats | null> = {}
      await Promise.all(
        names.map(async (n) => {
          try {
            const d = await api.get<EcrRepositoryDetail>(repoApiPath(n))
            const pushed = d.images.map((i) => i.pushed_at).filter(validTime) as string[]
            out[n] = {
              images: d.images.length,
              size: d.images.reduce((a, i) => a + (i.size_bytes || 0), 0),
              lastPushed: pushed.sort().at(-1),
              pushCommands: d.push_commands,
            }
          } catch {
            out[n] = null
          }
        }),
      )
      return out
    },
    { keepPreviousData: true, refreshInterval: 30_000 },
  )
}

export function RepositoryList() {
  const { data, error, isLoading, isValidating, mutate } = useApi<EcrRepository[]>(REPOS_PATH, { refreshInterval: 30_000 })
  const stats = useRepoStats(data)
  const [selected, setSelected] = useState<string[]>([])
  const [createOpen, setCreateOpen] = useState(false)
  const [deleteTarget, setDeleteTarget] = useState<Row | null>(null)
  const [pushTarget, setPushTarget] = useState<Row | null>(null)
  const createParam = useQueryParam("create")
  const setParam = useSetQueryParam()

  useEffect(() => {
    if (createParam === "1") setCreateOpen(true)
  }, [createParam])

  const onCreateOpenChange = (o: boolean) => {
    setCreateOpen(o)
    if (!o && createParam) setParam("create", null)
  }

  const rows: Row[] | undefined = data?.map((r) => {
    const s = stats.data?.[r.name]
    return { ...r, stats: s ?? undefined, statsError: s === null }
  })
  const sel = rows?.find((r) => r.name === selected[0])
  const statsLoading = !stats.data

  const statCell = (r: Row, render: (s: RepoStats) => React.ReactNode) =>
    r.stats ? render(r.stats) : r.statsError ? <span className="text-muted-foreground">-</span> : statsLoading ? <Skeleton className="h-4 w-12" /> : "-"

  const columns: Column<Row>[] = [
    {
      id: "name",
      header: "Repository name",
      value: (r) => r.name,
      cell: (r) => (
        <Link href={repoHref(r.name)} className="text-primary font-medium break-all hover:underline" onClick={(e) => e.stopPropagation()}>
          {r.name}
        </Link>
      ),
    },
    { id: "uri", header: "URI", value: (r) => r.uri, cell: (r) => <CopyableText value={r.uri} className="max-w-72" />, hideBelow: "md" },
    {
      id: "images",
      header: "Images",
      value: (r) => r.stats?.images ?? -1,
      cell: (r) => statCell(r, (s) => <span className="tabular-nums">{s.images}</span>),
      className: "text-right",
      headerClassName: "text-right",
    },
    {
      id: "size",
      header: "Size",
      value: (r) => r.stats?.size ?? -1,
      cell: (r) => statCell(r, (s) => <span className="whitespace-nowrap tabular-nums">{formatBytes(s.size)}</span>),
      className: "text-right",
      headerClassName: "text-right",
      hideBelow: "sm",
    },
    {
      id: "pushed",
      header: "Last pushed",
      value: (r) => r.stats?.lastPushed ?? "",
      cell: (r) => statCell(r, (s) => (s.lastPushed ? <TimeAgo value={s.lastPushed} /> : <span className="text-muted-foreground">Never</span>)),
      hideBelow: "lg",
    },
    { id: "created", header: "Created", value: (r) => r.created_at, cell: (r) => <span className="whitespace-nowrap">{formatDate(r.created_at)}</span>, hideBelow: "lg" },
  ]

  return (
    <div className="flex flex-col gap-6">
      <PageHeader
        title="Repositories"
        description="Amazon ECR-compatible private container registry. Push images with the standard docker CLI and run them on EC2, ECS or Lambda."
        breadcrumbs={[{ label: "Amazon ECR", href: "/ecr/" }, { label: "Repositories" }]}
      />

      <DataTable
        title="Private repositories"
        data={rows}
        columns={columns}
        rowId={(r) => r.name}
        loading={isLoading}
        error={error}
        onRefresh={() => {
          mutate()
          stats.mutate()
        }}
        refreshing={isValidating || stats.isValidating}
        selection="single"
        selected={selected}
        onSelectedChange={setSelected}
        searchPlaceholder="Find repositories"
        defaultSort={{ id: "name" }}
        actions={
          <>
            <ActionsMenu
              disabled={!sel}
              items={[
                { label: "View push commands", icon: <Terminal />, onSelect: () => sel && setPushTarget(sel), disabled: !sel?.stats },
                {
                  label: "Copy URI",
                  icon: <Copy />,
                  onSelect: async () => {
                    if (sel && (await copyText(sel.uri))) toast.success("URI copied")
                  },
                },
                { separator: true },
                { label: "Delete", icon: <Trash2 />, destructive: true, onSelect: () => sel && setDeleteTarget(sel) },
              ]}
            />
            <Button size="sm" onClick={() => setCreateOpen(true)}>
              <Plus /> Create repository
            </Button>
          </>
        }
        empty={
          <EmptyState
            icon={Boxes}
            title="No repositories"
            description="Push an image to the registry (see below) or create a repository to get its push commands."
            action={
              <Button size="sm" onClick={() => setCreateOpen(true)}>
                <Plus /> Create repository
              </Button>
            }
          />
        }
      />

      <RegistryPanel />

      <CreateRepositoryDialog open={createOpen} onOpenChange={onCreateOpenChange} />
      <DeleteRepositoryDialog
        name={deleteTarget?.name ?? ""}
        imageCount={deleteTarget?.stats?.images}
        open={!!deleteTarget}
        onOpenChange={(o) => !o && setDeleteTarget(null)}
        onDeleted={() => setSelected([])}
      />
      {pushTarget?.stats && (
        <PushCommandsDialog repo={pushTarget} commands={pushTarget.stats.pushCommands} open onOpenChange={(o) => !o && setPushTarget(null)} />
      )}
    </div>
  )
}
