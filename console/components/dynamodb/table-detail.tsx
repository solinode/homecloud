"use client"

import { useEffect, useState } from "react"
import Link from "next/link"
import { useRouter } from "next/navigation"
import { AlertCircle, ArrowLeft, Database, HardDrive, ListTree, Loader2, Plus, RefreshCw, Search, Settings2, ShieldCheck, ShieldOff, Trash2, Waves } from "lucide-react"
import { toast } from "sonner"

import { Button } from "@/components/ui/button"
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { Switch } from "@/components/ui/switch"
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs"
import { ActionsMenu } from "@/components/console/actions-menu"
import { ConfirmDialog } from "@/components/console/confirm-dialog"
import { CopyableText } from "@/components/console/copy-button"
import { CellText, DataTable, type Column } from "@/components/console/data-table"
import { EmptyState } from "@/components/console/empty-state"
import { ErrorState } from "@/components/console/error-state"
import { Field } from "@/components/console/form-field"
import { KeyValueGrid } from "@/components/console/key-value"
import { DetailSkeleton } from "@/components/console/loading"
import { PageHeader } from "@/components/console/page-header"
import { Section } from "@/components/console/section"
import { StatTile } from "@/components/console/stat-tile"
import { StatusBadge } from "@/components/console/status-badge"
import { TagList, TagsEditor, rowsToTags, tagsToRows, type TagRow } from "@/components/console/tags-editor"
import { TimeAgo } from "@/components/console/time-ago"
import { ApiError, api, errorMessage, seg } from "@/lib/api"
import { formatBytes, formatDate, formatNumber } from "@/lib/format"
import { revalidate, useApi, useQueryParam, useSetQueryParam } from "@/lib/hooks"
import type { DynamoTable, StreamViewType, TableIndex, UpdateTableInput } from "@/lib/types"
import {
  DELETION_PROTECTED_MESSAGE,
  KeyAttr,
  KeySchema,
  STREAM_VIEW_TYPES,
  StreamViewTypeSelect,
  TABLES_PATH,
  allIndexes,
  indexes,
  localIndexes,
  projectionLabel,
  streamViewLabel,
} from "./common"
import { IndexRowEditor, emptyIndexRow, indexRowErrors, rowToIndex, type IndexRow } from "./create-table"
import { ItemExplorer } from "./item-explorer"
import { DeleteTableDialog } from "./tables-list"

const TABS = ["items", "overview", "indexes", "settings"] as const
type Tab = (typeof TABS)[number]

export function TableDetail() {
  const router = useRouter()
  const name = useQueryParam("name")
  const tabParam = useQueryParam("tab")
  const setParam = useSetQueryParam()
  const tab: Tab = (TABS as readonly string[]).includes(tabParam) ? (tabParam as Tab) : "items"

  const { data: table, error, isLoading, isValidating, mutate } = useApi<DynamoTable>(name ? `${TABLES_PATH}/${seg(name)}` : null, {
    refreshInterval: 15_000,
  })
  const [deleting, setDeleting] = useState(false)

  const crumbs = [{ label: "DynamoDB", href: "/dynamodb/" }, { label: "Tables", href: "/dynamodb/" }, { label: name || "Table" }]

  if (!name) {
    return (
      <>
        <PageHeader title="Table" breadcrumbs={crumbs} />
        <EmptyState title="No table selected" description="Open a table from the tables list." action={<BackButton />} />
      </>
    )
  }
  if (error && !table) {
    const notFound = error instanceof ApiError && error.status === 404
    return (
      <div className="flex flex-col gap-4">
        <PageHeader title={name} breadcrumbs={crumbs} />
        {notFound ? (
          <Section>
            <EmptyState icon={AlertCircle} title="Table not found" description={`Table ${name} does not exist. It may have been deleted.`} action={<BackButton />} />
          </Section>
        ) : (
          <ErrorState error={error} onRetry={() => mutate()} />
        )}
      </div>
    )
  }
  if (isLoading || !table) return <DetailSkeleton />

  return (
    <div className="flex flex-col gap-4">
      <PageHeader
        title={table.name}
        badge={<StatusBadge status={table.status} />}
        description={
          <span className="inline-flex flex-wrap items-center gap-x-3 gap-y-1">
            <KeySchema pk={table.partition_key} sk={table.sort_key} />
            <span>· {formatNumber(table.item_count)} items</span>
            <span>· {formatBytes(table.size_bytes)}</span>
          </span>
        }
        breadcrumbs={crumbs}
        actions={
          <>
            <Button variant="outline" size="sm" onClick={() => mutate()} disabled={isValidating}>
              {isValidating ? <Loader2 className="animate-spin" /> : <RefreshCw />}
              Refresh
            </Button>
            <ActionsMenu
              items={[
                { label: "Manage indexes", icon: <ListTree />, onSelect: () => setParam("tab", "indexes") },
                { label: "Edit settings", icon: <Settings2 />, onSelect: () => setParam("tab", "settings") },
                { separator: true },
                { label: "Delete table", icon: <Trash2 />, destructive: true, onSelect: () => setDeleting(true) },
              ]}
            />
            <Button size="sm" onClick={() => setParam("tab", null)} disabled={tab === "items"}>
              <Search /> Explore items
            </Button>
          </>
        }
      />

      <Tabs value={tab} onValueChange={(v) => setParam("tab", v === "items" ? null : v)}>
        <TabsList>
          <TabsTrigger value="overview">Overview</TabsTrigger>
          <TabsTrigger value="items">Explore items</TabsTrigger>
          <TabsTrigger value="indexes">Indexes</TabsTrigger>
          <TabsTrigger value="settings">Settings</TabsTrigger>
        </TabsList>
        <TabsContent value="overview">
          <OverviewTab table={table} />
        </TabsContent>
        <TabsContent value="items">
          <ItemExplorer table={table} />
        </TabsContent>
        <TabsContent value="indexes">
          <IndexesTab table={table} />
        </TabsContent>
        <TabsContent value="settings">
          <SettingsTab table={table} onDelete={() => setDeleting(true)} />
        </TabsContent>
      </Tabs>

      <DeleteTableDialog table={deleting ? table : null} onClose={() => setDeleting(false)} onDeleted={() => router.push("/dynamodb/")} />
    </div>
  )
}

function BackButton() {
  return (
    <Button variant="outline" size="sm" asChild>
      <Link href="/dynamodb/">
        <ArrowLeft /> Back to tables
      </Link>
    </Button>
  )
}

function OverviewTab({ table }: { table: DynamoTable }) {
  const all = allIndexes(table)
  return (
    <div className="flex flex-col gap-4">
      <div className="grid grid-cols-1 gap-4 sm:grid-cols-2 lg:grid-cols-4">
        <StatTile label="Items" value={formatNumber(table.item_count)} icon={<Database />} caption="Approximate item count" />
        <StatTile label="Table size" value={formatBytes(table.size_bytes)} icon={<HardDrive />} caption="Stored item bytes" />
        <StatTile
          label="Secondary indexes"
          value={all.length}
          icon={<ListTree />}
          caption={`${indexes(table).length} global · ${localIndexes(table).length} local`}
        />
        <StatTile
          label="Stream"
          value={table.stream_arn ? "On" : "Off"}
          tone={table.stream_arn ? "success" : "neutral"}
          icon={<Waves />}
          caption={table.stream_arn ? streamViewLabel(table.stream_view_type) : "Item changes are not recorded"}
        />
      </div>
      <Section title="General information">
        <KeyValueGrid
          columns={3}
          items={[
            { label: "Partition key", value: <KeyAttr k={table.partition_key} /> },
            { label: "Sort key", value: table.sort_key ? <KeyAttr k={table.sort_key} /> : "" },
            { label: "Table status", value: <StatusBadge status={table.status} /> },
            { label: "Item count", value: formatNumber(table.item_count) },
            { label: "Table size", value: formatBytes(table.size_bytes) },
            { label: "Capacity mode", value: table.billing_mode === "PAY_PER_REQUEST" ? "On-demand" : table.billing_mode },
            {
              label: "Time to live (TTL)",
              value: table.ttl_attribute ? (
                <span>
                  On · <span className="font-mono text-[13px]">{table.ttl_attribute}</span>
                </span>
              ) : (
                "Off"
              ),
            },
            { label: "Created", value: <span>{formatDate(table.created_at)} (<TimeAgo value={table.created_at} />)</span> },
            { label: "Secondary indexes", value: all.length ? all.map((g) => `${g.name}${g.local ? " (local)" : ""}`).join(", ") : "None" },
            { label: "DynamoDB stream", value: table.stream_arn ? `On · ${streamViewLabel(table.stream_view_type)}` : "Off" },
            {
              label: "Deletion protection",
              value: table.deletion_protection ? (
                <span className="inline-flex items-center gap-1">
                  <ShieldCheck className="text-success size-3.5" /> On
                </span>
              ) : (
                "Off"
              ),
            },
            { label: "Amazon Resource Name (ARN)", value: <CopyableText value={table.arn} />, wide: true },
            ...(table.stream_arn ? [{ label: "Latest stream ARN", value: <CopyableText value={table.stream_arn} />, wide: true }] : []),
          ]}
        />
      </Section>
      <Section title="Tags">
        <TagList tags={table.tags} />
      </Section>
    </div>
  )
}

function IndexesTab({ table }: { table: DynamoTable }) {
  const gsis = indexes(table)
  const lsis = localIndexes(table)
  const [creating, setCreating] = useState(false)
  const [removing, setRemoving] = useState<TableIndex | null>(null)

  return (
    <div className="flex flex-col gap-4">
      <DataTable
        title="Global secondary indexes"
        description="Query items by attributes other than the primary key. A new index is backfilled from existing items when it is created."
        data={gsis}
        columns={indexColumns(setRemoving)}
        rowId={(g) => g.name}
        noSearch
        defaultSort={{ id: "name" }}
        actions={
          <Button size="sm" onClick={() => setCreating(true)}>
            <Plus /> Create index
          </Button>
        }
        empty={
          <EmptyState
            icon={ListTree}
            title="No secondary indexes"
            description="Create an index to query items by another attribute, for example orders by status."
            action={
              <Button size="sm" onClick={() => setCreating(true)}>
                <Plus /> Create index
              </Button>
            }
          />
        }
      />
      <DataTable
        title="Local secondary indexes"
        description="Alternate sort keys within a partition. Local indexes are defined when the table is created and cannot be added or removed later."
        data={lsis}
        columns={indexColumns()}
        rowId={(g) => g.name}
        noSearch
        defaultSort={{ id: "name" }}
        empty={<EmptyState icon={ListTree} title="No local secondary indexes" description="Local indexes can only be defined when a table is created." />}
      />
      <CreateIndexDialog table={table} open={creating} onOpenChange={setCreating} />
      <ConfirmDialog
        open={!!removing}
        onOpenChange={(o) => !o && setRemoving(null)}
        title={`Delete index ${removing?.name ?? ""}?`}
        description="Queries that use this index will fail. Items in the table are not affected."
        actionLabel="Delete index"
        onConfirm={async () => {
          if (!removing) return
          await api.patch(`${TABLES_PATH}/${seg(table.name)}`, { remove_index: removing.name })
          toast.success(`Deleted index ${removing.name}`)
          await revalidate(TABLES_PATH)
        }}
      />
    </div>
  )
}

/** indexColumns lists secondary index columns; onDelete adds a delete action per row. */
function indexColumns(onDelete?: (ix: TableIndex) => void): Column<TableIndex>[] {
  const cols: Column<TableIndex>[] = [
    { id: "name", header: "Name", cell: (g) => <CellText mono className="font-medium">{g.name}</CellText>, value: (g) => g.name },
    { id: "status", header: "Status", cell: (g) => <StatusBadge status={g.status || "ACTIVE"} />, value: (g) => g.status || "ACTIVE" },
    { id: "pk", header: "Partition key", cell: (g) => <KeyAttr k={g.partition_key} />, value: (g) => g.partition_key.name },
    {
      id: "sk",
      header: "Sort key",
      cell: (g) => (g.sort_key ? <KeyAttr k={g.sort_key} /> : <span className="text-muted-foreground">-</span>),
      value: (g) => g.sort_key?.name ?? "",
      hideBelow: "sm",
    },
    {
      id: "projection",
      header: "Projected attributes",
      cell: (g) => <CellText max="16rem">{projectionLabel(g.projection)}</CellText>,
      value: (g) => projectionLabel(g.projection),
      hideBelow: "md",
    },
  ]
  if (onDelete)
    cols.push({
      id: "actions",
      header: <span className="sr-only">Actions</span>,
      sortable: false,
      className: "w-px text-right",
      cell: (g) => (
        <Button variant="ghost" size="sm" className="text-destructive hover:text-destructive" onClick={() => onDelete(g)}>
          <Trash2 /> Delete
        </Button>
      ),
    })
  return cols
}

function CreateIndexDialog({ table, open, onOpenChange }: { table: DynamoTable; open: boolean; onOpenChange: (o: boolean) => void }) {
  const [row, setRow] = useState<IndexRow>(emptyIndexRow())
  const [submitted, setSubmitted] = useState(false)
  const [pending, setPending] = useState(false)
  useEffect(() => {
    if (open) {
      setRow(emptyIndexRow())
      setSubmitted(false)
    }
  }, [open])

  const errors = indexRowErrors(
    row,
    allIndexes(table).map((g) => g.name),
  )

  const submit = async (e: React.FormEvent) => {
    e.preventDefault()
    setSubmitted(true)
    if (Object.values(errors).some(Boolean)) return
    setPending(true)
    try {
      const ix = rowToIndex(row)
      await api.patch(`${TABLES_PATH}/${seg(table.name)}`, { add_index: ix })
      toast.success(`Created index ${ix.name}`)
      await revalidate(TABLES_PATH)
      onOpenChange(false)
    } catch (err) {
      toast.error(errorMessage(err))
    } finally {
      setPending(false)
    }
  }

  return (
    <Dialog open={open} onOpenChange={(o) => !pending && onOpenChange(o)}>
      <DialogContent className="sm:max-w-3xl">
        <form onSubmit={submit} className="flex flex-col gap-4">
          <DialogHeader>
            <DialogTitle>Create global secondary index</DialogTitle>
            <DialogDescription>
              Existing items are backfilled into the index. Items that lack the index partition key attribute are not returned by queries on the index.
            </DialogDescription>
          </DialogHeader>
          <IndexRowEditor row={row} onChange={(p) => setRow({ ...row, ...p })} errors={submitted ? errors : {}} />
          <DialogFooter>
            <Button type="button" variant="outline" onClick={() => onOpenChange(false)} disabled={pending}>
              Cancel
            </Button>
            <Button type="submit" disabled={pending}>
              {pending && <Loader2 className="animate-spin" />}
              Create index
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}

function SettingsTab({ table, onDelete }: { table: DynamoTable; onDelete: () => void }) {
  const path = `${TABLES_PATH}/${seg(table.name)}`
  const [ttlOn, setTtlOn] = useState(!!table.ttl_attribute)
  const [ttlAttr, setTtlAttr] = useState(table.ttl_attribute || "expires_at")
  const [ttlPending, setTtlPending] = useState(false)
  const [tagRows, setTagRows] = useState<TagRow[]>(() => tagsToRows(table.tags))
  const [tagErr, setTagErr] = useState<string | null>(null)
  const [tagPending, setTagPending] = useState(false)

  // Pick up changes made elsewhere (another tab, the CLI).
  useEffect(() => {
    setTtlOn(!!table.ttl_attribute)
    setTtlAttr(table.ttl_attribute || "expires_at")
  }, [table.ttl_attribute])
  const tagsKey = JSON.stringify(table.tags ?? {})
  useEffect(() => {
    setTagRows(tagsToRows(table.tags))
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [tagsKey])

  const ttlTarget = ttlOn ? ttlAttr.trim() : ""
  const ttlDirty = ttlTarget !== (table.ttl_attribute ?? "")

  const saveTtl = async (e: React.FormEvent) => {
    e.preventDefault()
    if (ttlOn && !ttlAttr.trim()) return
    setTtlPending(true)
    try {
      await api.patch(path, { ttl_attribute: ttlTarget })
      toast.success(ttlTarget ? `TTL enabled on ${ttlTarget}` : "TTL disabled")
      await revalidate(TABLES_PATH)
    } catch (err) {
      toast.error(errorMessage(err))
    } finally {
      setTtlPending(false)
    }
  }

  const saveTags = async (e: React.FormEvent) => {
    e.preventDefault()
    const keys = tagRows.map((r) => r.key.trim()).filter(Boolean)
    if (new Set(keys).size !== keys.length) {
      setTagErr("Tag keys must be unique")
      return
    }
    setTagPending(true)
    try {
      await api.patch(path, { tags: rowsToTags(tagRows) ?? {} })
      toast.success("Saved tags")
      await revalidate(TABLES_PATH)
    } catch (err) {
      toast.error(errorMessage(err))
    } finally {
      setTagPending(false)
    }
  }

  return (
    <div className="flex flex-col gap-4">
      <Section title="Time to live (TTL)" description="Items whose TTL attribute holds an epoch time in seconds that is in the past are hidden from reads and deleted within about a minute.">
        <form onSubmit={saveTtl} className="flex flex-col gap-4">
          <div className="flex items-center gap-2">
            <Switch id="ttl-on" checked={ttlOn} onCheckedChange={setTtlOn} />
            <Label htmlFor="ttl-on">{ttlOn ? "Enabled" : "Disabled"}</Label>
          </div>
          {ttlOn && (
            <Field label="TTL attribute name" htmlFor="ttl-attr" error={!ttlAttr.trim() ? "Enter an attribute name" : undefined}>
              <Input id="ttl-attr" value={ttlAttr} onChange={(e) => setTtlAttr(e.target.value)} className="h-8 max-w-sm font-mono" />
            </Field>
          )}
          <div>
            <Button type="submit" size="sm" disabled={ttlPending || !ttlDirty || (ttlOn && !ttlAttr.trim())}>
              {ttlPending && <Loader2 className="animate-spin" />}
              Save TTL settings
            </Button>
          </div>
        </form>
      </Section>

      <StreamSection table={table} />

      <DeletionProtectionSection table={table} />

      <Section title="Tags">
        <form onSubmit={saveTags} className="flex flex-col gap-3">
          <TagsEditor rows={tagRows} onChange={(r) => (setTagRows(r), setTagErr(null))} />
          {tagErr && <p className="text-destructive text-xs">{tagErr}</p>}
          <div>
            <Button type="submit" size="sm" disabled={tagPending}>
              {tagPending && <Loader2 className="animate-spin" />}
              Save tags
            </Button>
          </div>
        </form>
      </Section>

      <Section title="Delete table" className="border-destructive/40">
        <div className="flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between">
          <p className="text-muted-foreground text-sm">
            {table.deletion_protection ? DELETION_PROTECTED_MESSAGE : "Deleting the table removes all of its items and indexes permanently."}
          </p>
          <Button variant="destructive" size="sm" onClick={onDelete} className="shrink-0" disabled={table.deletion_protection}>
            <Trash2 /> Delete table
          </Button>
        </div>
      </Section>
    </div>
  )
}

function StreamSection({ table }: { table: DynamoTable }) {
  const path = `${TABLES_PATH}/${seg(table.name)}`
  const current = table.stream_arn ? table.stream_view_type : undefined
  const [on, setOn] = useState(!!current)
  const [view, setView] = useState<StreamViewType>(current ?? "NEW_AND_OLD_IMAGES")
  const [pending, setPending] = useState(false)
  // Pick up changes made elsewhere.
  useEffect(() => {
    setOn(!!current)
    setView(current ?? "NEW_AND_OLD_IMAGES")
  }, [current])

  const target: StreamViewType | "" = on ? view : ""
  const dirty = target !== (current ?? "")
  const history = [...(table.streams ?? [])].reverse()
  const latest = history[0]

  const save = async (e: React.FormEvent) => {
    e.preventDefault()
    setPending(true)
    try {
      const body: UpdateTableInput = { stream_view_type: target }
      await api.patch(path, body)
      toast.success(!target ? "Stream disabled" : current ? `Stream view type changed to ${streamViewLabel(target)}` : "Stream enabled")
      await revalidate(TABLES_PATH)
    } catch (err) {
      toast.error(errorMessage(err))
    } finally {
      setPending(false)
    }
  }

  return (
    <Section
      title="DynamoDB stream"
      description="Captures item-level changes in order. Stream records can be read with the DynamoDB Streams API or delivered to Lambda for 24 hours."
    >
      <form onSubmit={save} className="flex flex-col gap-4">
        <div className="flex items-center gap-2">
          <Switch id="stream-on" checked={on} onCheckedChange={setOn} />
          <Label htmlFor="stream-on">{on ? "Enabled" : "Disabled"}</Label>
        </div>
        {on && (
          <Field label="View type" htmlFor="stream-view" help={STREAM_VIEW_TYPES.find((s) => s.value === view)?.help}>
            <StreamViewTypeSelect id="stream-view" value={view} onChange={setView} />
          </Field>
        )}
        {current && on && view !== current && (
          <p className="text-muted-foreground text-xs">Changing the view type disables the current stream and starts a new one with a new ARN.</p>
        )}
        <div>
          <Button type="submit" size="sm" disabled={pending || !dirty}>
            {pending && <Loader2 className="animate-spin" />}
            Save stream settings
          </Button>
        </div>
        {latest && (
          <KeyValueGrid
            columns={3}
            className="border-t pt-4"
            items={[
              { label: "Latest stream label", value: <span className="font-mono text-[13px]">{latest.label}</span> },
              { label: "View type", value: streamViewLabel(latest.view_type) },
              {
                label: "Status",
                value: latest.disabled ? <StatusBadge status="disabled" label="Disabled" tone="neutral" /> : <StatusBadge status="ENABLED" label="Enabled" />,
              },
              { label: "Latest stream ARN", value: <CopyableText value={`${table.arn}/stream/${latest.label}`} />, wide: true },
            ]}
          />
        )}
        {history.length > 1 && (
          <DataTable
            title="Stream history"
            data={history}
            rowId={(s) => s.label}
            noSearch
            className="shadow-none"
            columns={[
              { id: "label", header: "Stream label", cell: (s) => <CellText mono>{s.label}</CellText>, value: (s) => s.label },
              { id: "view", header: "View type", cell: (s) => <CellText>{streamViewLabel(s.view_type)}</CellText>, value: (s) => s.view_type, hideBelow: "sm" },
              { id: "created", header: "Created", cell: (s) => <TimeAgo value={s.created} />, value: (s) => s.created },
              {
                id: "disabled",
                header: "State",
                cell: (s) =>
                  s.disabled ? (
                    <span className="inline-flex items-center gap-2 whitespace-nowrap">
                      <StatusBadge status="disabled" /> <TimeAgo value={s.disabled} className="text-muted-foreground" />
                    </span>
                  ) : (
                    <StatusBadge status="active" />
                  ),
                value: (s) => s.disabled ?? "",
              },
            ]}
          />
        )}
      </form>
    </Section>
  )
}

function DeletionProtectionSection({ table }: { table: DynamoTable }) {
  const path = `${TABLES_PATH}/${seg(table.name)}`
  const on = !!table.deletion_protection
  const [pending, setPending] = useState(false)

  const toggle = async (next: boolean) => {
    setPending(true)
    try {
      const body: UpdateTableInput = { deletion_protection: next }
      const t = await api.patch<DynamoTable>(path, body)
      if (!!t?.deletion_protection !== next) throw new Error("The server did not apply the deletion protection change.")
      toast.success(next ? "Deletion protection turned on" : "Deletion protection turned off")
      await revalidate(TABLES_PATH)
    } catch (err) {
      toast.error(errorMessage(err))
    } finally {
      setPending(false)
    }
  }

  return (
    <Section title="Deletion protection" description="Protects the table from being deleted by accident from the console, the CLI or the AWS SDKs.">
      <div className="flex items-center gap-2">
        <Switch id="del-protect" checked={on} onCheckedChange={toggle} disabled={pending} />
        <Label htmlFor="del-protect" className="inline-flex items-center gap-1.5">
          {on ? <ShieldCheck className="text-success size-4" /> : <ShieldOff className="text-muted-foreground size-4" />}
          {on ? "On" : "Off"}
        </Label>
        {pending && <Loader2 className="text-muted-foreground size-4 animate-spin" />}
      </div>
    </Section>
  )
}
