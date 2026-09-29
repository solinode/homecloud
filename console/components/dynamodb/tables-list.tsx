"use client"

import { useState } from "react"
import Link from "next/link"
import { Plus, Table2, Trash2 } from "lucide-react"
import { toast } from "sonner"

import { Button } from "@/components/ui/button"
import { ConfirmDialog } from "@/components/console/confirm-dialog"
import { DataTable, cellLinkClass, type Column } from "@/components/console/data-table"
import { EmptyState } from "@/components/console/empty-state"
import { PageHeader } from "@/components/console/page-header"
import { StatusBadge } from "@/components/console/status-badge"
import { TimeAgo } from "@/components/console/time-ago"
import { api, seg } from "@/lib/api"
import { formatBytes, formatNumber, pluralize } from "@/lib/format"
import { revalidate, useApi } from "@/lib/hooks"
import type { DynamoTable } from "@/lib/types"
import { TABLES_PATH, indexes, tableHref } from "./common"

const columns: Column<DynamoTable>[] = [
  {
    id: "name",
    header: "Name",
    cell: (t) => (
      <Link href={tableHref(t.name)} onClick={(e) => e.stopPropagation()} className={cellLinkClass()}>
        {t.name}
      </Link>
    ),
    value: (t) => t.name,
  },
  { id: "status", header: "Status", cell: (t) => <StatusBadge status={t.status} />, value: (t) => t.status, hideBelow: "sm" },
  {
    id: "pk",
    header: "Partition key",
    cell: (t) => (
      <span className="whitespace-nowrap">
        <span className="font-mono text-[13px]">{t.partition_key.name}</span> <span className="text-muted-foreground text-xs">({t.partition_key.type})</span>
      </span>
    ),
    value: (t) => t.partition_key.name,
  },
  {
    id: "sk",
    header: "Sort key",
    cell: (t) =>
      t.sort_key ? (
        <span className="whitespace-nowrap">
          <span className="font-mono text-[13px]">{t.sort_key.name}</span> <span className="text-muted-foreground text-xs">({t.sort_key.type})</span>
        </span>
      ) : (
        <span className="text-muted-foreground">-</span>
      ),
    value: (t) => t.sort_key?.name ?? "",
    hideBelow: "md",
  },
  { id: "indexes", header: "Indexes", cell: (t) => indexes(t).length, value: (t) => indexes(t).length, hideBelow: "md" },
  { id: "items", header: "Item count", cell: (t) => <span className="tabular-nums">{formatNumber(t.item_count)}</span>, value: (t) => t.item_count },
  { id: "size", header: "Size", cell: (t) => <span className="tabular-nums">{formatBytes(t.size_bytes)}</span>, value: (t) => t.size_bytes, hideBelow: "sm" },
  { id: "created", header: "Created", cell: (t) => <TimeAgo value={t.created_at} />, value: (t) => t.created_at, hideBelow: "lg" },
]

export function TablesList() {
  const { data, error, isLoading, isValidating, mutate } = useApi<DynamoTable[]>(TABLES_PATH, { refreshInterval: 15_000 })
  const [selected, setSelected] = useState<string[]>([])
  const [deleting, setDeleting] = useState<DynamoTable | null>(null)
  const sel = (data ?? []).find((t) => t.name === selected[0]) ?? null

  return (
    <div className="flex flex-col gap-4">
      <PageHeader
        title="Tables"
        description="Key-value and document tables addressed by a partition key and an optional sort key, with range queries, secondary indexes and TTL."
        breadcrumbs={[{ label: "DynamoDB", href: "/dynamodb/" }, { label: "Tables" }]}
      />
      <DataTable
        title="Tables"
        data={data}
        columns={columns}
        rowId={(t) => t.name}
        loading={isLoading}
        error={error}
        onRefresh={() => mutate()}
        refreshing={isValidating}
        selection="single"
        selected={selected}
        onSelectedChange={setSelected}
        searchPlaceholder="Find tables by name or key"
        defaultSort={{ id: "name" }}
        actions={
          <>
            <Button variant="outline" size="sm" disabled={!sel} onClick={() => sel && setDeleting(sel)}>
              <Trash2 /> Delete
            </Button>
            <Button size="sm" asChild>
              <Link href="/dynamodb/create/">
                <Plus /> Create table
              </Link>
            </Button>
          </>
        }
        empty={
          <EmptyState
            icon={Table2}
            title="No tables"
            description="Create a table to store JSON items by key. Items can be queried by partition key and sort key range, or scanned with filters."
            action={
              <Button size="sm" asChild>
                <Link href="/dynamodb/create/">
                  <Plus /> Create table
                </Link>
              </Button>
            }
          />
        }
      />
      <DeleteTableDialog table={deleting} onClose={() => setDeleting(null)} />
    </div>
  )
}

/** DeleteTableDialog deletes a table and all of its items after typing its name. */
export function DeleteTableDialog({ table, onClose, onDeleted }: { table: DynamoTable | null; onClose: () => void; onDeleted?: () => void }) {
  return (
    <ConfirmDialog
      open={!!table}
      onOpenChange={(o) => !o && onClose()}
      title={`Delete table ${table?.name ?? ""}?`}
      description={
        <>
          The table and {table ? pluralize(table.item_count, "item") : "its items"} ({formatBytes(table?.size_bytes ?? 0)}) are deleted permanently, including
          its indexes. This cannot be undone.
        </>
      }
      confirmText={table?.name}
      actionLabel="Delete table"
      onConfirm={async () => {
        if (!table) return
        await api.del(`${TABLES_PATH}/${seg(table.name)}`)
        toast.success(`Deleted table ${table.name}`)
        await revalidate(TABLES_PATH)
        onDeleted?.()
      }}
    />
  )
}
