"use client"

import { useEffect, useMemo, useState } from "react"
import { useRouter } from "next/navigation"
import { Loader2, Plus, ScrollText, Trash2 } from "lucide-react"
import { toast } from "sonner"

import { Button } from "@/components/ui/button"
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog"
import { Input } from "@/components/ui/input"
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select"
import { ActionsMenu } from "@/components/console/actions-menu"
import { ConfirmDialog } from "@/components/console/confirm-dialog"
import { CellLink, CellText, DataTable, type Column } from "@/components/console/data-table"
import { EmptyState } from "@/components/console/empty-state"
import { Field } from "@/components/console/form-field"
import { PageHeader } from "@/components/console/page-header"
import { Tag } from "@/components/console/tag"
import { TimeAgo } from "@/components/console/time-ago"
import { api, errorMessage, seg } from "@/lib/api"
import { formatBytes } from "@/lib/format"
import { revalidate, useApi, useQueryParam } from "@/lib/hooks"
import type { LogGroup } from "@/lib/types"

import { describeContainerGroup, logGroupHref, RETENTION_CHOICES, retentionLabel, useInstanceNames } from "./common"

export function SourceBadge({ source }: { source: LogGroup["source"] }) {
  return source === "container" ? (
    <Tag accent="info" mono={false}>
      Container
    </Tag>
  ) : (
    <Tag mono={false}>Stored</Tag>
  )
}

function RetentionSelect({ id, value, onChange }: { id?: string; value: number; onChange: (v: number) => void }) {
  return (
    <Select value={String(value)} onValueChange={(v) => onChange(Number(v))}>
      <SelectTrigger id={id} className="w-full">
        <SelectValue />
      </SelectTrigger>
      <SelectContent>
        <SelectItem value="0">Never expire</SelectItem>
        {!RETENTION_CHOICES.includes(value) && value !== 0 && <SelectItem value={String(value)}>{retentionLabel(value)}</SelectItem>}
        {RETENTION_CHOICES.map((d) => (
          <SelectItem key={d} value={String(d)}>
            {retentionLabel(d)}
          </SelectItem>
        ))}
      </SelectContent>
    </Select>
  )
}

export function RetentionDialog({ open, onOpenChange, group }: { open: boolean; onOpenChange: (o: boolean) => void; group: LogGroup }) {
  const [days, setDays] = useState(group.retention_days)
  const [pending, setPending] = useState(false)
  useEffect(() => {
    if (open) setDays(group.retention_days)
  }, [open, group.retention_days])

  const submit = async (e: React.FormEvent) => {
    e.preventDefault()
    setPending(true)
    try {
      await api.put(`/api/v1/logs/groups/${seg(group.name)}/retention`, { retention_days: days })
      toast.success(`Retention of ${group.name} set to ${retentionLabel(days).toLowerCase()}`)
      revalidate("/api/v1/logs")
      onOpenChange(false)
    } catch (err) {
      toast.error(errorMessage(err))
    } finally {
      setPending(false)
    }
  }

  return (
    <Dialog open={open} onOpenChange={(o) => !pending && onOpenChange(o)}>
      <DialogContent className="sm:max-w-md">
        <form onSubmit={submit} className="flex flex-col gap-5">
          <DialogHeader>
            <DialogTitle>Edit retention setting</DialogTitle>
            <DialogDescription className="break-all">{group.name}</DialogDescription>
          </DialogHeader>
          <Field label="Expire events after" htmlFor="retention" help="Streams with no events newer than the retention period are deleted.">
            <RetentionSelect id="retention" value={days} onChange={setDays} />
          </Field>
          <DialogFooter>
            <Button type="button" variant="outline" onClick={() => onOpenChange(false)} disabled={pending}>
              Cancel
            </Button>
            <Button type="submit" disabled={pending}>
              {pending && <Loader2 className="animate-spin" />}
              Save
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}

function CreateLogGroupDialog({ open, onOpenChange, existing }: { open: boolean; onOpenChange: (o: boolean) => void; existing?: LogGroup[] }) {
  const router = useRouter()
  const [name, setName] = useState("")
  const [days, setDays] = useState(0)
  const [touched, setTouched] = useState(false)
  const [pending, setPending] = useState(false)

  useEffect(() => {
    if (open) {
      setName("")
      setDays(0)
      setTouched(false)
    }
  }, [open])

  const n = name.trim()
  const err = !n
    ? "Name is required"
    : n.startsWith("/hc/")
      ? "The /hc/ prefix is reserved for container log groups"
      : n.length > 512
        ? "At most 512 characters"
        : !/^[\w\-./#]+$/.test(n)
          ? "Use letters, numbers and _ - . / # only"
          : existing?.some((g) => g.name === n)
            ? "A log group with this name already exists"
            : null

  const submit = async (e: React.FormEvent) => {
    e.preventDefault()
    setTouched(true)
    if (err) return
    setPending(true)
    try {
      await api.post<LogGroup>("/api/v1/logs/groups", { name: n, retention_days: days })
      toast.success(`Log group ${n} created`)
      revalidate("/api/v1/logs")
      onOpenChange(false)
      router.push(logGroupHref(n))
    } catch (e2) {
      toast.error(errorMessage(e2))
    } finally {
      setPending(false)
    }
  }

  return (
    <Dialog open={open} onOpenChange={(o) => !pending && onOpenChange(o)}>
      <DialogContent className="sm:max-w-md">
        <form onSubmit={submit} className="flex flex-col gap-5">
          <DialogHeader>
            <DialogTitle>Create log group</DialogTitle>
            <DialogDescription>Applications write events to streams inside a log group with PutLogEvents.</DialogDescription>
          </DialogHeader>
          <Field label="Log group name" htmlFor="lg-name" error={touched || name ? err : undefined}>
            <Input id="lg-name" autoFocus className="font-mono" value={name} onChange={(e) => setName(e.target.value)} placeholder="/myapp/production" spellCheck={false} />
          </Field>
          <Field label="Retention" htmlFor="lg-retention">
            <RetentionSelect id="lg-retention" value={days} onChange={setDays} />
          </Field>
          <DialogFooter>
            <Button type="button" variant="outline" onClick={() => onOpenChange(false)} disabled={pending}>
              Cancel
            </Button>
            <Button type="submit" disabled={pending}>
              {pending && <Loader2 className="animate-spin" />}
              Create
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}

export function LogGroupList() {
  const { data, error, isLoading, isValidating, mutate } = useApi<LogGroup[]>("/api/v1/logs/groups", { refreshInterval: 30_000 })
  const names = useInstanceNames()
  const createParam = useQueryParam("create")
  const [selected, setSelected] = useState<string[]>([])
  const [createOpen, setCreateOpen] = useState(false)
  const [retentionOpen, setRetentionOpen] = useState(false)
  const [deleteOpen, setDeleteOpen] = useState(false)

  useEffect(() => {
    if (createParam === "1") setCreateOpen(true)
  }, [createParam])

  const group = data?.find((g) => g.name === selected[0])
  const stored = group?.source === "stored"

  const columns = useMemo<Column<LogGroup>[]>(
    () => [
      {
        id: "name",
        header: "Log group",
        value: (g) => `${g.name} ${describeContainerGroup(g.name, names)}`,
        cell: (g) => (
          <div className="flex min-w-0 flex-col">
            <CellLink href={logGroupHref(g.name)} mono max="28rem">
              {g.name}
            </CellLink>
            {g.source === "container" && (
              <CellText muted max="28rem" className="text-xs">
                {describeContainerGroup(g.name, names)}
              </CellText>
            )}
          </div>
        ),
      },
      { id: "source", header: "Source", value: (g) => g.source, cell: (g) => <SourceBadge source={g.source} /> },
      {
        id: "retention",
        header: "Retention",
        value: (g) => (g.source === "container" ? -1 : g.retention_days || 1e9),
        cell: (g) => (g.source === "container" ? <span className="text-muted-foreground">Container lifetime</span> : retentionLabel(g.retention_days)),
        hideBelow: "sm",
      },
      {
        id: "bytes",
        header: "Stored bytes",
        value: (g) => g.stored_bytes,
        cell: (g) => (g.source === "container" ? <span className="text-muted-foreground">-</span> : <span className="tabular-nums">{formatBytes(g.stored_bytes)}</span>),
        hideBelow: "md",
      },
      { id: "created", header: "Created", value: (g) => g.created_at, cell: (g) => <TimeAgo value={g.created_at} />, hideBelow: "md" },
    ],
    [names],
  )

  const onDelete = async () => {
    if (!group) return
    await api.del(`/api/v1/logs/groups/${seg(group.name)}`)
    toast.success(`Log group ${group.name} deleted`)
    setSelected([])
    revalidate("/api/v1/logs")
  }

  return (
    <div className="flex flex-col gap-4">
      <PageHeader
        breadcrumbs={[{ label: "CloudWatch", href: "/cloudwatch/" }, { label: "Log groups" }]}
        title="Log groups"
        description={
          <>
            Stored log groups keep events your applications send with PutLogEvents. Container log groups (<span className="font-mono">/hc/...</span>) are
            read-only views of the stdout and stderr of running HomeCloud containers.
          </>
        }
      />
      <DataTable
        title="Log groups"
        data={data}
        columns={columns}
        rowId={(g) => g.name}
        loading={isLoading}
        error={error}
        onRefresh={() => mutate()}
        refreshing={isValidating}
        selection="single"
        selected={selected}
        onSelectedChange={setSelected}
        searchPlaceholder="Filter log groups"
        defaultSort={{ id: "name" }}
        actions={
          <>
            <ActionsMenu
              disabled={!group}
              items={[
                {
                  label: "Edit retention setting",
                  disabled: !stored,
                  hint: !stored ? "Container log groups live as long as the container" : undefined,
                  onSelect: () => setRetentionOpen(true),
                },
                { separator: true },
                {
                  label: "Delete log group",
                  destructive: true,
                  icon: <Trash2 />,
                  disabled: !stored,
                  hint: !stored ? "Container log groups are read-only" : undefined,
                  onSelect: () => setDeleteOpen(true),
                },
              ]}
            />
            <Button size="sm" onClick={() => setCreateOpen(true)}>
              <Plus /> Create log group
            </Button>
          </>
        }
        empty={
          <EmptyState
            icon={ScrollText}
            title="No log groups"
            description="Log groups appear here when containers run or when you create one."
            action={
              <Button size="sm" onClick={() => setCreateOpen(true)}>
                <Plus /> Create log group
              </Button>
            }
          />
        }
      />
      <CreateLogGroupDialog open={createOpen} onOpenChange={setCreateOpen} existing={data} />
      {group && stored && <RetentionDialog open={retentionOpen} onOpenChange={setRetentionOpen} group={group} />}
      {group && stored && (
        <ConfirmDialog
          open={deleteOpen}
          onOpenChange={setDeleteOpen}
          title="Delete log group?"
          description={
            <>
              <span className="font-mono break-all">{group.name}</span> and all of its streams and events ({formatBytes(group.stored_bytes)}) will be deleted
              permanently.
            </>
          }
          confirmText={group.name}
          actionLabel="Delete log group"
          onConfirm={onDelete}
        />
      )}
    </div>
  )
}
