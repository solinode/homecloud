"use client"

import { useEffect, useState } from "react"
import Link from "next/link"
import { HardDrive, Loader2, Plus } from "lucide-react"
import { toast } from "sonner"

import { Button } from "@/components/ui/button"
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog"
import { Input } from "@/components/ui/input"
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select"
import { ActionsMenu } from "@/components/console/actions-menu"
import { ConfirmDialog } from "@/components/console/confirm-dialog"
import { DataTable, type Column } from "@/components/console/data-table"
import { EmptyState } from "@/components/console/empty-state"
import { Field } from "@/components/console/form-field"
import { PageHeader } from "@/components/console/page-header"
import { StatusBadge } from "@/components/console/status-badge"
import { TagsEditor, rowsToTags, type TagRow } from "@/components/console/tags-editor"
import { TimeAgo } from "@/components/console/time-ago"
import { api, errorMessage, seg } from "@/lib/api"
import { revalidate, useApi } from "@/lib/hooks"
import type { Volume } from "@/lib/types"
import { instanceHref } from "./instance-actions"

const VOLUMES_PATH = "/api/v1/ec2/volumes"
const ZONES = ["us-east-1a", "us-east-1b", "us-east-1c"]

const columns: Column<Volume>[] = [
  { id: "id", header: "Volume ID", cell: (v) => <span className="font-mono text-[13px] font-medium">{v.id}</span>, value: (v) => v.id },
  { id: "name", header: "Name", cell: (v) => v.name || <span className="text-muted-foreground">-</span>, value: (v) => v.name },
  { id: "size", header: "Size", cell: (v) => `${v.size_gb} GiB`, value: (v) => v.size_gb },
  { id: "state", header: "Volume state", cell: (v) => <StatusBadge status={v.state} />, value: (v) => v.state },
  {
    id: "attached",
    header: "Attached resources",
    cell: (v) =>
      v.attached_to ? (
        <span className="whitespace-nowrap">
          <Link href={instanceHref(v.attached_to)} onClick={(e) => e.stopPropagation()} className="text-primary font-mono text-[13px] hover:underline">
            {v.attached_to}
          </Link>
          {v.mount_path && <span className="text-muted-foreground font-mono text-[13px]">:{v.mount_path}</span>}
        </span>
      ) : (
        <span className="text-muted-foreground">-</span>
      ),
    value: (v) => `${v.attached_to ?? ""} ${v.mount_path ?? ""}`,
  },
  {
    id: "snapshot",
    header: "Snapshot",
    cell: (v) => (v.snapshot_id ? <span className="font-mono text-[13px]">{v.snapshot_id}</span> : <span className="text-muted-foreground">-</span>),
    value: (v) => v.snapshot_id ?? "",
    hideBelow: "lg",
  },
  { id: "az", header: "Availability zone", cell: (v) => v.availability_zone, value: (v) => v.availability_zone, hideBelow: "md" },
  { id: "created", header: "Created", cell: (v) => <TimeAgo value={v.created_at} />, value: (v) => v.created_at, hideBelow: "lg" },
]

export function VolumesList() {
  const { data, error, isLoading, isValidating, mutate } = useApi<Volume[]>(VOLUMES_PATH, { refreshInterval: 15_000 })
  const [selected, setSelected] = useState<string[]>([])
  const [creating, setCreating] = useState(false)
  const [deleting, setDeleting] = useState<Volume | null>(null)
  const sel = data?.find((v) => v.id === selected[0])

  return (
    <div className="flex flex-col gap-4">
      <PageHeader
        title="Volumes"
        description="EBS-style block storage backed by Docker volumes. Attach them at launch; they keep their data after the instance is terminated unless marked delete on termination."
        breadcrumbs={[{ label: "EC2", href: "/ec2/" }, { label: "Volumes" }]}
      />
      <DataTable
        title="Volumes"
        description="Sizes are advisory: Docker volumes are not capped."
        data={data}
        columns={columns}
        rowId={(v) => v.id}
        loading={isLoading}
        error={error}
        onRefresh={() => mutate()}
        refreshing={isValidating}
        selection="single"
        selected={selected}
        onSelectedChange={setSelected}
        searchPlaceholder="Find volume by ID, name or instance"
        defaultSort={{ id: "created", desc: true }}
        actions={
          <>
            <ActionsMenu
              disabled={!sel}
              items={[
                {
                  label: "Delete volume",
                  destructive: true,
                  onSelect: () => sel && setDeleting(sel),
                  disabled: !sel || sel.state !== "available",
                  hint: "Only available (detached) volumes can be deleted",
                },
              ]}
            />
            <Button size="sm" onClick={() => setCreating(true)}>
              <Plus /> Create volume
            </Button>
          </>
        }
        empty={
          <EmptyState
            icon={HardDrive}
            title="No volumes"
            description="Create a volume here, or add volumes when launching an instance."
            action={
              <Button size="sm" onClick={() => setCreating(true)}>
                <Plus /> Create volume
              </Button>
            }
          />
        }
      />
      <ConfirmDialog
        open={!!deleting}
        onOpenChange={(o) => !o && setDeleting(null)}
        title={`Delete ${deleting?.name || deleting?.id}?`}
        description="The volume and all of its data are permanently deleted."
        confirmText={deleting?.id}
        actionLabel="Delete"
        onConfirm={async () => {
          if (!deleting) return
          await api.del(`${VOLUMES_PATH}/${seg(deleting.id)}`)
          toast.success(`Deleted ${deleting.id}`)
          setSelected([])
          await mutate()
        }}
      />
      <CreateVolumeDialog open={creating} onOpenChange={setCreating} />
    </div>
  )
}

function CreateVolumeDialog({ open, onOpenChange }: { open: boolean; onOpenChange: (o: boolean) => void }) {
  const [name, setName] = useState("")
  const [size, setSize] = useState("8")
  const [az, setAz] = useState(ZONES[0])
  const [snapshot, setSnapshot] = useState("")
  const [tags, setTags] = useState<TagRow[]>([])
  const [submitted, setSubmitted] = useState(false)
  const [pending, setPending] = useState(false)

  useEffect(() => {
    if (open) {
      setName("")
      setSize("8")
      setAz(ZONES[0])
      setSnapshot("")
      setTags([])
      setSubmitted(false)
    }
  }, [open])

  const n = Number(size)
  const fromSnap = !!snapshot.trim() && !size.trim()
  const sizeErr = fromSnap ? undefined : !Number.isInteger(n) || n < 1 || n > 16384 ? "Enter a whole number from 1 to 16384" : undefined
  const nameErr = name.length > 128 ? "At most 128 characters" : undefined
  const snapErr = snapshot.trim() && !/^snap-[0-9a-f]+$/.test(snapshot.trim()) ? "Snapshot IDs look like snap-0123456789abcdef0" : undefined

  const submit = async (e: React.FormEvent) => {
    e.preventDefault()
    setSubmitted(true)
    if (sizeErr || nameErr || snapErr) return
    setPending(true)
    try {
      const v = await api.post<Volume>(VOLUMES_PATH, { name: name.trim(), size_gb: fromSnap ? undefined : n, availability_zone: az, snapshot_id: snapshot.trim() || undefined, tags: rowsToTags(tags) })
      toast.success(`Created ${v.id}`)
      await revalidate(VOLUMES_PATH)
      onOpenChange(false)
    } catch (err) {
      toast.error(errorMessage(err))
    } finally {
      setPending(false)
    }
  }

  return (
    <Dialog open={open} onOpenChange={(o) => !pending && onOpenChange(o)}>
      <DialogContent>
        <form onSubmit={submit} className="flex flex-col gap-4">
          <DialogHeader>
            <DialogTitle>Create volume</DialogTitle>
            <DialogDescription>A new volume, empty or restored from a snapshot. Attach it to an instance at launch.</DialogDescription>
          </DialogHeader>
          <Field label="Name" htmlFor="vol-name" optional error={submitted ? nameErr : undefined}>
            <Input id="vol-name" value={name} onChange={(e) => setName(e.target.value)} placeholder="db-data" autoFocus />
          </Field>
          <div className="grid grid-cols-2 gap-3">
            <Field label="Size (GiB)" htmlFor="vol-size" error={submitted ? sizeErr : undefined} help={snapshot.trim() ? "Leave empty to use the snapshot's size." : "Advisory: Docker volumes are not capped."}>
              <Input id="vol-size" type="number" min={1} value={size} onChange={(e) => setSize(e.target.value)} />
            </Field>
            <Field label="Availability zone" htmlFor="vol-az">
              <Select value={az} onValueChange={setAz}>
                <SelectTrigger id="vol-az" className="w-full">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {ZONES.map((z) => (
                    <SelectItem key={z} value={z}>
                      {z}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </Field>
          </div>
          <Field
            label="Snapshot ID"
            htmlFor="vol-snap"
            optional
            error={submitted ? snapErr : undefined}
            help="Restore the volume's data from an EBS snapshot (created with aws ec2 create-snapshot)."
          >
            <Input id="vol-snap" value={snapshot} onChange={(e) => setSnapshot(e.target.value)} placeholder="snap-0123456789abcdef0" className="font-mono" spellCheck={false} />
          </Field>
          <Field label="Tags" optional>
            <TagsEditor rows={tags} onChange={setTags} />
          </Field>
          <DialogFooter>
            <Button type="button" variant="outline" onClick={() => onOpenChange(false)} disabled={pending}>
              Cancel
            </Button>
            <Button type="submit" disabled={pending}>
              {pending && <Loader2 className="animate-spin" />}
              Create volume
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}
