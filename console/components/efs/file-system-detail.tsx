"use client"

import { useState } from "react"
import Link from "next/link"
import { useRouter } from "next/navigation"
import { AlertCircle, ArrowLeft, Info, Loader2, Rocket, Trash2 } from "lucide-react"

import { Button } from "@/components/ui/button"
import { Skeleton } from "@/components/ui/skeleton"
import { CopyableText } from "@/components/console/copy-button"
import { EmptyState } from "@/components/console/empty-state"
import { ErrorState } from "@/components/console/error-state"
import { KeyValueGrid } from "@/components/console/key-value"
import { DetailSkeleton } from "@/components/console/loading"
import { PageHeader } from "@/components/console/page-header"
import { Section } from "@/components/console/section"
import { StatusBadge } from "@/components/console/status-badge"
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
              {isValidating && <Loader2 className="animate-spin" />}
              Refresh
            </Button>
            <Button variant="destructive" size="sm" onClick={() => setDeleting(true)}>
              <Trash2 /> Delete
            </Button>
          </>
        }
      />

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
            { label: "Access", value: fs.read_only ? "Read-only (instances mount it read-only)" : "Read/write" },
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
  const mounts = (instances.data ?? [])
    .filter((i) => i.state !== "terminated")
    .flatMap((i) => (i.file_systems ?? []).filter((m) => m.file_system_id === fs.id).map((m) => ({ inst: i, m })))

  return (
    <Section
      title={`Mounted by (${fs.mounted_by.length})`}
      description="Instances that mount this file system. Every instance sees the same files."
      flush
      actions={
        <Button variant="outline" size="sm" asChild>
          <Link href="/ec2/launch/">
            <Rocket /> Launch instance
          </Link>
        </Button>
      }
    >
      {instances.error ? (
        <div className="p-4">
          <ErrorState error={instances.error} onRetry={() => instances.mutate()} />
        </div>
      ) : !instances.data && fs.mounted_by.length > 0 ? (
        <div className="flex flex-col gap-2 p-4">
          <Skeleton className="h-6 w-full" />
          <Skeleton className="h-6 w-full" />
        </div>
      ) : mounts.length ? (
        <div className="overflow-x-auto">
          <table className="w-full text-sm">
            <thead>
              <tr className="bg-muted/40 border-b">
                <th className="text-muted-foreground px-4 py-2 text-left text-xs font-semibold">Instance</th>
                <th className="text-muted-foreground px-3 py-2 text-left text-xs font-semibold">State</th>
                <th className="text-muted-foreground px-3 py-2 text-left text-xs font-semibold">Mount path</th>
                <th className="text-muted-foreground px-4 py-2 text-left text-xs font-semibold">Access</th>
              </tr>
            </thead>
            <tbody>
              {mounts.map(({ inst, m }) => (
                <tr key={`${inst.id}:${m.mount_path}`} className="border-b last:border-0">
                  <td className="px-4 py-2">
                    <Link href={`/ec2/instance/?id=${encodeURIComponent(inst.id)}`} className="text-primary hover:underline">
                      {inst.name ? (
                        <>
                          {inst.name} <span className="text-muted-foreground font-mono text-xs">{inst.id}</span>
                        </>
                      ) : (
                        <span className="font-mono text-[13px]">{inst.id}</span>
                      )}
                    </Link>
                  </td>
                  <td className="px-3 py-2">
                    <StatusBadge status={inst.state} />
                  </td>
                  <td className="px-3 py-2 font-mono text-[13px]">{m.mount_path}</td>
                  <td className="px-4 py-2 whitespace-nowrap">{m.read_only ? "Read-only" : "Read/write"}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      ) : (
        <p className="text-muted-foreground p-4 text-sm">No instance mounts this file system.</p>
      )}
      <div className="text-muted-foreground flex gap-2 border-t p-4 text-sm">
        <Info className="mt-0.5 size-4 shrink-0" />
        <span>
          <span className="text-foreground font-medium">How to mount:</span> file systems are attached when an instance launches. In the{" "}
          <Link href="/ec2/launch/" className="text-primary hover:underline">
            launch wizard
          </Link>
          , add this file system under <span className="text-foreground">File systems</span> with a mount path such as{" "}
          <span className="text-foreground font-mono text-[13px]">/mnt/efs</span>. Via the API, pass{" "}
          <span className="text-foreground font-mono text-[13px] break-all">
            {`"file_systems": [{"file_system_id": "${fs.id}", "mount_path": "/mnt/efs"${fs.read_only ? ', "read_only": true' : ""}}]`}
          </span>{" "}
          to RunInstances.
        </span>
      </div>
    </Section>
  )
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
