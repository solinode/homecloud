"use client"

import { useEffect, useState } from "react"
import { Copy, HardDrive, Plus, Trash2 } from "lucide-react"

import { Button } from "@/components/ui/button"
import { ActionsMenu } from "@/components/console/actions-menu"
import { copyText } from "@/components/console/copy-button"
import { CellLink, CellText, DataTable, type Column } from "@/components/console/data-table"
import { EmptyState } from "@/components/console/empty-state"
import { PageHeader } from "@/components/console/page-header"
import { StatTile } from "@/components/console/stat-tile"
import { StatusBadge, statusLabel, statusTone } from "@/components/console/status-badge"
import { TimeAgo } from "@/components/console/time-ago"
import { useApi, useQueryParam, useSetQueryParam } from "@/lib/hooks"
import type { Bucket, S3Status } from "@/lib/types"
import { toast } from "sonner"

import { CreateBucketDialog, DeleteBucketDialog } from "./bucket-dialogs"
import { bucketHref, S3AccessCard } from "./common"

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
      cell: (b) => <CellLink href={bucketHref(b.name)}>{b.name}</CellLink>,
    },
    { id: "region", header: "Region", value: (b) => b.region, cell: (b) => <CellText mono>{b.region}</CellText>, hideBelow: "sm" },
    {
      id: "access",
      header: "Access",
      value: (b) => (b.public ? "Public" : "Private"),
      cell: (b) =>
        b.public ? <StatusBadge status="public" label="Public" tone="warning" /> : <CellText muted>Bucket and objects not public</CellText>,
    },
    {
      id: "website",
      header: "Static website hosting",
      value: (b) => (b.website ? "Enabled" : "Disabled"),
      cell: (b) => (b.website ? <StatusBadge status="enabled" label="Enabled" /> : <span className="text-muted-foreground">Disabled</span>),
      hideBelow: "md",
    },
    { id: "created", header: "Creation date", value: (b) => b.created_at, cell: (b) => <TimeAgo value={b.created_at} />, hideBelow: "sm" },
  ]

  return (
    <div className="flex flex-col gap-6">
      <PageHeader
        title="Buckets"
        description="Amazon S3-compatible object storage. Store any amount of data and access it from the console, SDKs and the AWS CLI."
        breadcrumbs={[{ label: "Amazon S3", href: "/s3/" }, { label: "Buckets" }]}
      />

      <div className="grid grid-cols-1 gap-4 sm:grid-cols-3">
        <StatTile label="Total buckets" value={data?.length ?? 0} loading={isLoading} />
        <StatTile label="Public buckets" value={publicCount} tone={publicCount > 0 ? "warning" : undefined} loading={isLoading} />
        <StatTile
          label="S3 endpoint"
          loading={status.isLoading}
          tone={status.error ? "danger" : status.data ? statusTone(status.data.status) : undefined}
          value={<span className="text-[22px]">{status.error ? "Unavailable" : status.data ? statusLabel(status.data.status) : "-"}</span>}
          caption={
            status.data ? (
              <span className="font-mono text-xs" title={status.data.endpoint}>
                {status.data.endpoint} · {status.data.region}
              </span>
            ) : undefined
          }
        />
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
