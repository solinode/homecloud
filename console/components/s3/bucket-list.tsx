"use client"

import { useEffect, useState } from "react"
import Link from "next/link"
import { Copy, HardDrive, Plus, Trash2 } from "lucide-react"

import { Button } from "@/components/ui/button"
import { Skeleton } from "@/components/ui/skeleton"
import { ActionsMenu } from "@/components/console/actions-menu"
import { copyText } from "@/components/console/copy-button"
import { DataTable, type Column } from "@/components/console/data-table"
import { EmptyState } from "@/components/console/empty-state"
import { PageHeader } from "@/components/console/page-header"
import { StatusBadge } from "@/components/console/status-badge"
import { formatDate } from "@/lib/format"
import { useApi, useQueryParam, useSetQueryParam } from "@/lib/hooks"
import type { Bucket, S3Status } from "@/lib/types"
import { toast } from "sonner"

import { CreateBucketDialog, DeleteBucketDialog } from "./bucket-dialogs"
import { bucketHref, S3AccessCard } from "./common"

function StatCard({ label, children, loading }: { label: string; children: React.ReactNode; loading?: boolean }) {
  return (
    <div className="bg-card flex flex-col gap-2 rounded-lg border p-4 shadow-xs">
      <span className="text-muted-foreground text-xs font-medium">{label}</span>
      {loading ? <Skeleton className="h-7 w-24" /> : <div className="min-w-0">{children}</div>}
    </div>
  )
}

export function BucketList() {
  const { data, error, isLoading, isValidating, mutate } = useApi<Bucket[]>("/api/v1/s3/buckets")
  const status = useApi<S3Status>("/api/v1/s3/status", { refreshInterval: (d) => (d?.status === "available" ? 60_000 : 3_000) })
  const [selected, setSelected] = useState<string[]>([])
  const [createOpen, setCreateOpen] = useState(false)
  const [deleteTarget, setDeleteTarget] = useState<string | null>(null)
  const createParam = useQueryParam("create")
  const setParam = useSetQueryParam()

  useEffect(() => {
    if (createParam === "1") setCreateOpen(true)
  }, [createParam])

  const onCreateOpenChange = (o: boolean) => {
    setCreateOpen(o)
    if (!o && createParam) setParam("create", null)
  }

  const sel = data?.find((b) => b.name === selected[0])
  const publicCount = (data ?? []).filter((b) => b.public).length

  const columns: Column<Bucket>[] = [
    {
      id: "name",
      header: "Name",
      value: (b) => b.name,
      cell: (b) => (
        <Link href={bucketHref(b.name)} className="text-primary font-medium hover:underline" onClick={(e) => e.stopPropagation()}>
          {b.name}
        </Link>
      ),
    },
    { id: "region", header: "Region", value: (b) => b.region, cell: (b) => <span className="font-mono text-[13px]">{b.region}</span>, hideBelow: "sm" },
    {
      id: "access",
      header: "Access",
      value: (b) => (b.public ? "Public" : "Private"),
      cell: (b) =>
        b.public ? <StatusBadge status="public" label="Public" tone="warning" /> : <span className="text-muted-foreground">Bucket and objects not public</span>,
    },
    {
      id: "website",
      header: "Static website hosting",
      value: (b) => (b.website ? "Enabled" : "Disabled"),
      cell: (b) => (b.website ? <StatusBadge status="enabled" label="Enabled" /> : <span className="text-muted-foreground">Disabled</span>),
      hideBelow: "md",
    },
    { id: "created", header: "Creation date", value: (b) => b.created_at, cell: (b) => <span className="whitespace-nowrap">{formatDate(b.created_at)}</span>, hideBelow: "sm" },
  ]

  return (
    <div className="flex flex-col gap-6">
      <PageHeader
        title="Buckets"
        description="Amazon S3-compatible object storage. Store any amount of data and access it from the console, SDKs and the AWS CLI."
        breadcrumbs={[{ label: "Amazon S3", href: "/s3/" }, { label: "Buckets" }]}
      />

      <div className="grid grid-cols-1 gap-4 sm:grid-cols-3">
        <StatCard label="Total buckets" loading={isLoading}>
          <span className="text-2xl font-semibold tabular-nums">{data?.length ?? 0}</span>
        </StatCard>
        <StatCard label="Public buckets" loading={isLoading}>
          <span className="text-2xl font-semibold tabular-nums">{publicCount}</span>
        </StatCard>
        <StatCard label="S3 endpoint" loading={status.isLoading}>
          {status.error ? (
            <StatusBadge status="error" label="Unavailable" />
          ) : status.data ? (
            <div className="flex flex-col gap-1">
              <StatusBadge status={status.data.status} className="w-fit" />
              <span className="text-muted-foreground truncate font-mono text-xs" title={status.data.endpoint}>
                {status.data.endpoint} · {status.data.region}
              </span>
            </div>
          ) : null}
        </StatCard>
      </div>

      <DataTable
        title="General purpose buckets"
        data={data}
        columns={columns}
        rowId={(b) => b.name}
        loading={isLoading}
        error={error}
        onRefresh={() => mutate()}
        refreshing={isValidating}
        selection="single"
        selected={selected}
        onSelectedChange={setSelected}
        searchPlaceholder="Find buckets by name"
        defaultSort={{ id: "name" }}
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
                { label: "Delete", icon: <Trash2 />, destructive: true, onSelect: () => sel && setDeleteTarget(sel.name) },
              ]}
            />
            <Button size="sm" onClick={() => setCreateOpen(true)}>
              <Plus /> Create bucket
            </Button>
          </>
        }
        empty={
          <EmptyState
            icon={HardDrive}
            title="No buckets"
            description="You don't have any buckets yet. Create one to start uploading files."
            action={
              <Button size="sm" onClick={() => setCreateOpen(true)}>
                <Plus /> Create bucket
              </Button>
            }
          />
        }
      />

      <S3AccessCard />

      <CreateBucketDialog open={createOpen} onOpenChange={onCreateOpenChange} />
      <DeleteBucketDialog
        bucket={deleteTarget ?? ""}
        open={!!deleteTarget}
        onOpenChange={(o) => !o && setDeleteTarget(null)}
        onDeleted={() => setSelected([])}
      />
    </div>
  )
}
