"use client"

import { useEffect, useState } from "react"
import Link from "next/link"
import { useRouter } from "next/navigation"
import { AlertCircle, ArrowLeft, Loader2, Plus, Trash2 } from "lucide-react"
import { toast } from "sonner"

import { Button } from "@/components/ui/button"
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { Switch } from "@/components/ui/switch"
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs"
import { ConfirmDialog } from "@/components/console/confirm-dialog"
import { CopyableText } from "@/components/console/copy-button"
import { EmptyState } from "@/components/console/empty-state"
import { ErrorState } from "@/components/console/error-state"
import { Field } from "@/components/console/form-field"
import { KeyValueGrid } from "@/components/console/key-value"
import { DetailSkeleton } from "@/components/console/loading"
import { PageHeader } from "@/components/console/page-header"
import { Section } from "@/components/console/section"
import { StatusBadge } from "@/components/console/status-badge"
import { TagList, TagsEditor, rowsToTags, tagsToRows, type TagRow } from "@/components/console/tags-editor"
import { TimeAgo } from "@/components/console/time-ago"
import { ApiError, api, errorMessage, seg } from "@/lib/api"
import { formatBytes, formatDate, formatNumber } from "@/lib/format"
import { revalidate, useApi, useQueryParam, useSetQueryParam } from "@/lib/hooks"
import type { DynamoTable, TableIndex } from "@/lib/types"
import { KeySchema, TABLES_PATH, indexes, keyLabel } from "./common"
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
              {isValidating && <Loader2 className="animate-spin" />}
              Refresh
            </Button>
            <Button variant="outline" size="sm" onClick={() => setDeleting(true)}>
              <Trash2 /> Delete
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
  const gsis = indexes(table)
  return (
    <div className="flex flex-col gap-4">
      <Section title="General information">
        <KeyValueGrid
          columns={3}
          items={[
            { label: "Partition key", value: keyLabel(table.partition_key) },
            { label: "Sort key", value: table.sort_key ? keyLabel(table.sort_key) : "" },
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
            { label: "Secondary indexes", value: gsis.length ? gsis.map((g) => g.name).join(", ") : "None" },
            { label: "Amazon Resource Name (ARN)", value: <CopyableText value={table.arn} />, wide: true },
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
  const [creating, setCreating] = useState(false)
  const [removing, setRemoving] = useState<TableIndex | null>(null)

  return (
    <div className="flex flex-col gap-4">
      <Section
        title={`Global secondary indexes (${gsis.length})`}
        description="Query items by attributes other than the primary key. Indexes are evaluated on read, so a new index covers existing items immediately."
        actions={
          <Button size="sm" onClick={() => setCreating(true)}>
            <Plus /> Create index
          </Button>
        }
        flush
      >
        {gsis.length ? (
          <div className="overflow-x-auto">
            <table className="w-full text-sm">
              <thead>
                <tr className="bg-muted/40 border-b">
                  <th className="text-muted-foreground px-4 py-2 text-left text-xs font-semibold">Name</th>
                  <th className="text-muted-foreground px-3 py-2 text-left text-xs font-semibold">Partition key</th>
                  <th className="text-muted-foreground px-3 py-2 text-left text-xs font-semibold">Sort key</th>
                  <th className="px-4 py-2" />
                </tr>
              </thead>
              <tbody>
                {gsis.map((g) => (
                  <tr key={g.name} className="border-b last:border-0">
                    <td className="px-4 py-2 font-medium">{g.name}</td>
                    <td className="px-3 py-2 whitespace-nowrap">{keyLabel(g.partition_key)}</td>
                    <td className="px-3 py-2 whitespace-nowrap">{g.sort_key ? keyLabel(g.sort_key) : <span className="text-muted-foreground">-</span>}</td>
                    <td className="px-4 py-2 text-right">
                      <Button variant="ghost" size="sm" className="text-destructive hover:text-destructive" onClick={() => setRemoving(g)}>
                        <Trash2 /> Delete
                      </Button>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        ) : (
          <EmptyState
            title="No secondary indexes"
            description="Create an index to query items by another attribute, for example orders by status."
            action={
              <Button size="sm" variant="outline" onClick={() => setCreating(true)}>
                <Plus /> Create index
              </Button>
            }
          />
        )}
      </Section>
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
    indexes(table).map((g) => g.name),
  )

  const submit = async (e: React.FormEvent) => {
    e.preventDefault()
    setSubmitted(true)
    if (errors.name || errors.pk) return
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
            <DialogDescription>Items that lack the index partition key attribute are not returned by queries on the index.</DialogDescription>
          </DialogHeader>
          <IndexRowEditor row={row} onChange={(p) => setRow({ ...row, ...p })} nameError={submitted ? errors.name : undefined} pkError={submitted ? errors.pk : undefined} />
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
          <p className="text-muted-foreground text-sm">Deleting the table removes all of its items and indexes permanently.</p>
          <Button variant="destructive" size="sm" onClick={onDelete} className="shrink-0">
            <Trash2 /> Delete table
          </Button>
        </div>
      </Section>
    </div>
  )
}
