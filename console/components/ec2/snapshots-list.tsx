"use client"

import { useState } from "react"
import { Camera, Plus } from "lucide-react"
import { toast } from "sonner"

import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select"
import { ActionsMenu } from "@/components/console/actions-menu"
import { ConfirmDialog } from "@/components/console/confirm-dialog"
import { CellText, DataTable, type Column } from "@/components/console/data-table"
import { EmptyState } from "@/components/console/empty-state"
import { Field } from "@/components/console/form-field"
import { FormDialog } from "@/components/console/form-dialog"
import { PageHeader } from "@/components/console/page-header"
import { StatusBadge } from "@/components/console/status-badge"
import { TimeAgo } from "@/components/console/time-ago"
import { api, seg } from "@/lib/api"
import { revalidate, useApi } from "@/lib/hooks"
import type { Snapshot, Volume } from "@/lib/types"

export const SNAPSHOTS_PATH = "/api/v1/ec2/snapshots"

/** CreateSnapshotDialog snapshots a volume (the volume is fixed when volumeId is given). */
export function CreateSnapshotDialog({ onClose, volumeId }: { onClose: () => void; volumeId?: string }) {
  const volumes = useApi<Volume[]>("/api/v1/ec2/volumes")
  const [vol, setVol] = useState(volumeId ?? "")
  const [desc, setDesc] = useState("")
  return (
    <FormDialog
      open
      onOpenChange={(o) => !o && onClose()}
      title="Create snapshot"
      description="Copies the volume's files into a snapshot. It stays pending until the copy finishes."
      submitLabel="Create snapshot"
      disabled={!vol}
      onSubmit={async () => {
        const s = await api.post<Snapshot>(SNAPSHOTS_PATH, { volume_id: vol, description: desc.trim() || undefined })
        toast.success(`Creating ${s.id}`)
        await revalidate(SNAPSHOTS_PATH)
      }}
    >
      <Field label="Volume" htmlFor="snap-vol">
        <Select value={vol} onValueChange={setVol} disabled={!!volumeId}>
          <SelectTrigger id="snap-vol" className="w-full">
            <SelectValue placeholder="Select a volume" />
          </SelectTrigger>
          <SelectContent>
            {(volumes.data ?? []).map((v) => (
              <SelectItem key={v.id} value={v.id}>
                {v.id} {v.name ? `(${v.name})` : ""} - {v.size_gb} GiB
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
      </Field>
      <Field label="Description" htmlFor="snap-desc" optional>
        <Input id="snap-desc" value={desc} onChange={(e) => setDesc(e.target.value)} />
      </Field>
    </FormDialog>
  )
}

const columns: Column<Snapshot>[] = [
  { id: "id", header: "Snapshot ID", cell: (s) => <CellText mono className="font-medium">{s.id}</CellText>, value: (s) => s.id },
  { id: "volume", header: "Volume ID", cell: (s) => <CellText mono>{s.volume_id}</CellText>, value: (s) => s.volume_id },
  { id: "size", header: "Size", cell: (s) => <span className="whitespace-nowrap tabular-nums">{s.volume_size} GiB</span>, value: (s) => s.volume_size, hideBelow: "sm" },
  {
    id: "state",
    header: "Status",
    cell: (s) => <StatusBadge status={s.state} />,
    value: (s) => s.state,
  },
  { id: "desc", header: "Description", cell: (s) => <CellText muted>{s.description}</CellText>, value: (s) => s.description, hideBelow: "md" },
  { id: "started", header: "Started", cell: (s) => <TimeAgo value={s.start_time} />, value: (s) => s.start_time, hideBelow: "lg" },
]

export function SnapshotsList() {
  const { data, error, isLoading, isValidating, mutate } = useApi<Snapshot[]>(SNAPSHOTS_PATH, {
    refreshInterval: (d) => (d?.some((s) => s.state === "pending") ? 3000 : 15_000),
  })
  const [selected, setSelected] = useState<string[]>([])
  const [dialog, setDialog] = useState<"create" | "delete" | null>(null)
  const sel = data?.find((s) => s.id === selected[0])

  return (
    <div className="flex flex-col gap-4">
      <PageHeader
        title="Snapshots"
        description="Point-in-time copies of volumes. Restore one by creating a volume from it."
        breadcrumbs={[{ label: "EC2", href: "/ec2/" }, { label: "Snapshots" }]}
      />
      <DataTable
        title="Snapshots"
        data={data}
        columns={columns}
        rowId={(s) => s.id}
        loading={isLoading}
        error={error}
        onRefresh={() => mutate()}
        refreshing={isValidating}
        selection="single"
        selected={selected}
        onSelectedChange={setSelected}
        searchPlaceholder="Find snapshot by ID or volume"
        defaultSort={{ id: "started", desc: true }}
        actions={
          <>
            <ActionsMenu disabled={!sel} items={[{ label: "Delete snapshot", destructive: true, onSelect: () => setDialog("delete") }]} />
            <Button size="sm" onClick={() => setDialog("create")}>
              <Plus /> Create snapshot
            </Button>
          </>
        }
        empty={
          <EmptyState
            icon={Camera}
            title="No snapshots"
            description="Create a snapshot of a volume."
            action={
              <Button size="sm" onClick={() => setDialog("create")}>
                <Plus /> Create snapshot
              </Button>
            }
          />
        }
      />
      {dialog === "create" && <CreateSnapshotDialog onClose={() => setDialog(null)} />}
      {sel && (
        <ConfirmDialog
          open={dialog === "delete"}
          onOpenChange={(o) => !o && setDialog(null)}
          title={`Delete ${sel.id}?`}
          description="The snapshot and its data are permanently deleted."
          onConfirm={async () => {
            await api.del(`${SNAPSHOTS_PATH}/${seg(sel.id)}`)
            toast.success(`Deleted ${sel.id}`)
            setSelected([])
            await mutate()
          }}
        />
      )}
    </div>
  )
}
