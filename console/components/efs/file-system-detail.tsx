"use client"

import { useState } from "react"
import Link from "next/link"
import { useRouter } from "next/navigation"
import { AlertCircle, ArrowLeft, Copy, FileLock, FilePen, HardDrive, Loader2, RefreshCw, Rocket, Server, Trash2 } from "lucide-react"
import { toast } from "sonner"

import { Button } from "@/components/ui/button"
import { ActionsMenu } from "@/components/console/actions-menu"
import { CodeBlock } from "@/components/console/code-block"
import { CopyableText, copyText } from "@/components/console/copy-button"
import { CellLink, CellText, DataTable, type Column } from "@/components/console/data-table"
import { EmptyState } from "@/components/console/empty-state"
import { ErrorState } from "@/components/console/error-state"
import { KeyValueGrid } from "@/components/console/key-value"
import { DetailSkeleton } from "@/components/console/loading"
import { PageHeader } from "@/components/console/page-header"
import { Section } from "@/components/console/section"
import { StatTile } from "@/components/console/stat-tile"
import { StatusBadge } from "@/components/console/status-badge"
import { Tag } from "@/components/console/tag"
import { TagList } from "@/components/console/tags-editor"
import { TimeAgo } from "@/components/console/time-ago"
import { ApiError, seg } from "@/lib/api"
import { formatBytes, formatDate } from "@/lib/format"
import { useApi, useQueryParam } from "@/lib/hooks"
import type { FileSystem, Instance } from "@/lib/types"

import { DeleteFileSystemDialog, FILE_SYSTEMS_PATH, fsLabel } from "./common"

const validTime = (t?: string) => !!t && new Date(t).getFullYear() > 1970

export function FileSystemDetail() {
  const id = useQueryParam("id")
  const router = useRouter()
  // Each GET re-measures the size when the last measurement is over a minute old.
  const { data: fs, error, isLoading, isValidating, mutate } = useApi<FileSystem>(id ? `${FILE_SYSTEMS_PATH}/${seg(id)}` : null, { refreshInterval: 30_000 })
  const [deleting, setDeleting] = useState(false)

  const crumbs = [{ label: "Amazon EFS", href: "/efs/" }, { label: "File systems", href: "/efs/" }, { label: fs ? fsLabel(fs) : id || "File system" }]

  if (!id) {
    return (
      <>
        <PageHeader title="File system" breadcrumbs={crumbs} />
        <EmptyState title="No file system selected" description="Open a file system from the file systems list." action={<BackButton />} />
      </>
    )
  }
  if (error && !fs) {
    const notFound = error instanceof ApiError && error.status === 404
    return (
      <div className="flex flex-col gap-4">
        <PageHeader title={id} breadcrumbs={crumbs} />
        {notFound ? (
          <Section>
            <EmptyState icon={AlertCircle} title="File system not found" description={`File system ${id} does not exist.`} action={<BackButton />} />
          </Section>
        ) : (
          <ErrorState error={error} onRetry={() => mutate()} />
        )}
      </div>
    )
  }
  if (isLoading || !fs) return <DetailSkeleton />

  return (
    <div className="flex flex-col gap-4">
      <PageHeader
        title={fsLabel(fs)}
        badge={<StatusBadge status={fs.state} />}
        description={fs.name ? <span className="font-mono text-[13px]">{fs.id}</span> : undefined}
        breadcrumbs={crumbs}
        actions={
          <>
            <Button variant="outline" size="sm" onClick={() => mutate()} disabled={isValidating}>
              {isValidating ? <Loader2 className="animate-spin" /> : <RefreshCw />}
              Refresh
            </Button>
            <ActionsMenu
              items={[
                {
                  label: "Copy ARN",
                  icon: <Copy />,
                  onSelect: async () => {
                    if (await copyText(fs.arn)) toast.success("ARN copied")
                  },
                },
                { separator: true },
                { label: "Delete file system", icon: <Trash2 />, destructive: true, onSelect: () => setDeleting(true) },
              ]}
            />
            <Button size="sm" asChild>
              <Link href="/ec2/launch/">
                <Rocket /> Launch instance
              </Link>
            </Button>
          </>
        }
      />

      <div className="grid grid-cols-1 gap-3 sm:grid-cols-3">
        <StatTile
          label="Metered size"
          value={validTime(fs.size_updated) ? formatBytes(fs.size_bytes) : "-"}
          caption={validTime(fs.size_updated) ? <>Measured <TimeAgo value={fs.size_updated} /></> : "Not measured yet"}
          icon={<HardDrive />}
        />
        <StatTile label="Mounted by" value={fs.mounted_by.length} unit={fs.mounted_by.length === 1 ? "instance" : "instances"} tone={fs.mounted_by.length ? "success" : "neutral"} icon={<Server />} />
        <StatTile label="Access" value={fs.read_only ? "Read-only" : "Read/write"} caption={fs.read_only ? "Instances mount it read-only" : "Instances can write"} icon={fs.read_only ? <FileLock /> : <FilePen />} />
      </div>

      <Section title="General">
        <KeyValueGrid
          columns={3}
          items={[
            { label: "File system ID", value: <CopyableText value={fs.id} /> },
            { label: "Name", value: fs.name },
            { label: "State", value: <StatusBadge status={fs.state} /> },
            {
              label: "Metered size",
              value: validTime(fs.size_updated) ? (
                <span className="flex flex-col">
                  <span className="tabular-nums">{formatBytes(fs.size_bytes)}</span>
                  <span className="text-muted-foreground text-xs">
                    Measured <TimeAgo value={fs.size_updated} />
                  </span>
                </span>
              ) : (
                <span className="text-muted-foreground">Not measured yet</span>
              ),
            },
            {
              label: "Access",
              value: fs.read_only ? (
                <Tag accent="info" mono={false}>
                  Read-only
                </Tag>
              ) : (
                <Tag mono={false}>Read/write</Tag>
              ),
            },
            { label: "Created", value: <span>{formatDate(fs.created_at)} (<TimeAgo value={fs.created_at} />)</span> },
            { label: "ARN", value: <CopyableText value={fs.arn} />, wide: true },
          ]}
        />
      </Section>

      <MountsSection fs={fs} />

      <Section title="Tags">
        <TagList tags={fs.tags ?? undefined} />
      </Section>

      <DeleteFileSystemDialog fs={fs} open={deleting} onOpenChange={setDeleting} onDeleted={() => router.push("/efs/")} />
    </div>
  )
}

function MountsSection({ fs }: { fs: FileSystem }) {
  const instances = useApi<Instance[]>("/api/v1/ec2/instances", { refreshInterval: 15_000 })
  const mounts: MountRow[] = (instances.data ?? [])
    .filter((i) => i.state !== "terminated")
    .flatMap((i) => (i.file_systems ?? []).filter((m) => m.file_system_id === fs.id).map((m) => ({ inst: i, path: m.mount_path, readOnly: !!m.read_only })))

  const columns: Column<MountRow>[] = [
    {
      id: "instance",
      header: "Instance",
      cell: ({ inst }) => (
        <span className="flex min-w-0 items-center gap-2">
          <CellLink href={`/ec2/instance/?id=${encodeURIComponent(inst.id)}`} mono={!inst.name} title={inst.id}>
            {inst.name || inst.id}
          </CellLink>
          {inst.name && <span className="text-muted-foreground hidden font-mono text-xs whitespace-nowrap sm:inline">{inst.id}</span>}
        </span>
      ),
      value: ({ inst }) => inst.name || inst.id,
    },
    { id: "state", header: "State", cell: ({ inst }) => <StatusBadge status={inst.state} />, value: ({ inst }) => inst.state },
    { id: "path", header: "Mount path", cell: (r) => <CellText mono>{r.path}</CellText>, value: (r) => r.path },
    {
      id: "access",
      header: "Access",
      cell: (r) =>
        r.readOnly ? (
          <Tag accent="info" mono={false}>
            Read-only
          </Tag>
        ) : (
          <Tag mono={false}>Read/write</Tag>
        ),
      value: (r) => (r.readOnly ? "Read-only" : "Read/write"),
      hideBelow: "sm",
    },
  ]

  const apiSnippet = `"file_systems": [{"file_system_id": "${fs.id}", "mount_path": "/mnt/efs"${fs.read_only ? ', "read_only": true' : ""}}]`

  return (
    <>
      <DataTable
        title="Mounted by"
        description="Instances that mount this file system. Every instance sees the same files."
        data={instances.data || !fs.mounted_by.length ? mounts : undefined}
        columns={columns}
        rowId={(r) => `${r.inst.id}:${r.path}`}
        loading={!instances.data && !instances.error && fs.mounted_by.length > 0}
        error={instances.error}
        onRetry={() => instances.mutate()}
        noSearch={mounts.length < 6}
        empty={
          <EmptyState
            icon={Server}
            title="No instance mounts this file system"
            description="File systems are attached when an instance launches."
            action={
              <Button size="sm" variant="outline" asChild>
                <Link href="/ec2/launch/">
                  <Rocket /> Launch instance
                </Link>
              </Button>
            }
          />
        }
      />
      <Section title="How to mount" description="File systems are attached when an instance launches.">
        <div className="flex flex-col gap-3 text-sm">
          <p className="text-muted-foreground">
            In the{" "}
            <Link href="/ec2/launch/" className="text-primary hover:underline">
              launch wizard
            </Link>
            , add this file system under <span className="text-foreground">File systems</span> with a mount path such as{" "}
            <span className="text-foreground font-mono text-[13px]">/mnt/efs</span>. Via the API, pass this to RunInstances:
          </p>
          <CodeBlock code={apiSnippet} title="RunInstances" wrap />
        </div>
      </Section>
    </>
  )
}

interface MountRow {
  inst: Instance
  path: string
  readOnly: boolean
}

function BackButton() {
  return (
    <Button variant="outline" size="sm" asChild>
      <Link href="/efs/">
        <ArrowLeft /> Back to file systems
      </Link>
    </Button>
  )
}
