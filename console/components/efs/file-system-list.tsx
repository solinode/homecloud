"use client"

import { useEffect, useState } from "react"
import Link from "next/link"
import { Copy, FolderTree, Plus, Rocket, Trash2 } from "lucide-react"
import { toast } from "sonner"

import { Button } from "@/components/ui/button"
import { ActionsMenu } from "@/components/console/actions-menu"
import { copyText } from "@/components/console/copy-button"
import { CellLink, DataTable, type Column } from "@/components/console/data-table"
import { EmptyState } from "@/components/console/empty-state"
import { PageHeader } from "@/components/console/page-header"
import { StatusBadge } from "@/components/console/status-badge"
import { Tag } from "@/components/console/tag"
import { TimeAgo } from "@/components/console/time-ago"
import { formatBytes } from "@/lib/format"
import { useApi, useQueryParam, useSetQueryParam } from "@/lib/hooks"
import type { FileSystem } from "@/lib/types"

import { CreateFileSystemDialog, DeleteFileSystemDialog, FILE_SYSTEMS_PATH, fileSystemHref } from "./common"

const TRANSITIONAL = ["creating", "deleting"]

export function FileSystemList() {
  const { data, error, isLoading, isValidating, mutate } = useApi<FileSystem[]>(FILE_SYSTEMS_PATH, {
    refreshInterval: (d) => (d?.some((f) => TRANSITIONAL.includes(f.state)) ? 2_000 : 15_000),
  })
  const [selected, setSelected] = useState<string[]>([])
  const [createOpen, setCreateOpen] = useState(false)
  const [deleteTarget, setDeleteTarget] = useState<FileSystem | null>(null)
  const createParam = useQueryParam("create")
  const setParam = useSetQueryParam()

  useEffect(() => {
    if (createParam === "1") setCreateOpen(true)
  }, [createParam])

  const onCreateOpenChange = (o: boolean) => {
    setCreateOpen(o)
    if (!o && createParam) setParam("create", null)
  }

  const sel = data?.find((f) => f.id === selected[0])

  const columns: Column<FileSystem>[] = [
    {
      id: "name",
      header: "Name",
      value: (f) => f.name,
      cell: (f) =>
        f.name ? (
          <CellLink href={fileSystemHref(f.id)}>{f.name}</CellLink>
        ) : (
          <span className="text-muted-foreground">-</span>
        ),
    },
    {
      id: "id",
      header: "File system ID",
      value: (f) => f.id,
      cell: (f) => (
        <CellLink href={fileSystemHref(f.id)} mono>
          {f.id}
        </CellLink>
      ),
    },
    { id: "state", header: "State", value: (f) => f.state, cell: (f) => <StatusBadge status={f.state} /> },
    {
      id: "size",
      header: "Metered size",
      value: (f) => f.size_bytes,
      cell: (f) =>
        f.size_updated && new Date(f.size_updated).getFullYear() > 1970 ? (
          <span className="whitespace-nowrap tabular-nums">{formatBytes(f.size_bytes)}</span>
        ) : (
          <span className="text-muted-foreground" title="Open the file system to measure it">
            Not measured
          </span>
        ),
      className: "text-right",
      headerClassName: "text-right",
      hideBelow: "sm",
    },
    {
      id: "mounted",
      header: "Mounted by",
      value: (f) => f.mounted_by.join(" "),
      cell: (f) =>
        f.mounted_by.length ? (
          <span className="flex flex-wrap gap-x-3 gap-y-1">
            {f.mounted_by.map((id) => (
              <Link
                key={id}
                href={`/ec2/instance/?id=${encodeURIComponent(id)}`}
                className="text-primary font-mono text-[13px] whitespace-nowrap hover:underline"
                onClick={(e) => e.stopPropagation()}
              >
                {id}
              </Link>
            ))}
          </span>
        ) : (
          <span className="text-muted-foreground">None</span>
        ),
      hideBelow: "md",
    },
    {
      id: "access",
      header: "Access",
      value: (f) => (f.read_only ? "Read-only" : "Read/write"),
      cell: (f) =>
        f.read_only ? (
          <Tag accent="info" mono={false}>
            Read-only
          </Tag>
        ) : (
          <Tag mono={false}>Read/write</Tag>
        ),
      hideBelow: "lg",
    },
    { id: "created", header: "Creation time", value: (f) => f.created_at, cell: (f) => <TimeAgo value={f.created_at} />, hideBelow: "lg" },
  ]

  return (
    <div className="flex flex-col gap-6">
      <PageHeader
        title="File systems"
        description="Amazon EFS-compatible shared file systems. Mount one file system on many EC2 instances at once; data outlives the instances."
        breadcrumbs={[{ label: "Amazon EFS", href: "/efs/" }, { label: "File systems" }]}
      />

      <DataTable
        title="File systems"
        data={data}
        columns={columns}
        rowId={(f) => f.id}
        loading={isLoading}
        error={error}
        onRefresh={() => mutate()}
        refreshing={isValidating}
        selection="single"
        selected={selected}
        onSelectedChange={setSelected}
        searchPlaceholder="Find file systems by name, ID or instance"
        defaultSort={{ id: "created", desc: true }}
        actions={
          <>
            <ActionsMenu
              disabled={!sel}
              items={[
                {
                  label: "Copy ARN",
                  icon: <Copy />,
                  onSelect: async () => {
                    if (sel && (await copyText(sel.arn))) toast.success("ARN copied")
                  },
                },
                { separator: true },
                { label: "Delete", icon: <Trash2 />, destructive: true, onSelect: () => sel && setDeleteTarget(sel) },
              ]}
            />
            <Button size="sm" onClick={() => setCreateOpen(true)}>
              <Plus /> Create file system
            </Button>
          </>
        }
        empty={
          <EmptyState
            icon={FolderTree}
            title="No file systems"
            description="Create a file system, then attach it to instances in the EC2 launch wizard to share files between them."
            action={
              <Button size="sm" onClick={() => setCreateOpen(true)}>
                <Plus /> Create file system
              </Button>
            }
          />
        }
      />

      <p className="text-muted-foreground flex flex-wrap items-center gap-1.5 text-sm">
        <Rocket className="size-4 shrink-0" />
        File systems are mounted when an instance launches: choose them under <span className="text-foreground font-medium">File systems</span> in the{" "}
        <Link href="/ec2/launch/" className="text-primary hover:underline">
          EC2 launch wizard
        </Link>
        .
      </p>

      <CreateFileSystemDialog open={createOpen} onOpenChange={onCreateOpenChange} />
      <DeleteFileSystemDialog fs={deleteTarget} open={!!deleteTarget} onOpenChange={(o) => !o && setDeleteTarget(null)} onDeleted={() => setSelected([])} />
    </div>
  )
}
