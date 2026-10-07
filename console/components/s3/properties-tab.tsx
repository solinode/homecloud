"use client"

import { useEffect, useState } from "react"
import { AlertTriangle, ExternalLink, Info, Loader2, Pencil, Plus, X } from "lucide-react"
import { toast } from "sonner"

import { Alert, AlertDescription } from "@/components/ui/alert"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { Switch } from "@/components/ui/switch"
import { ConfirmDialog } from "@/components/console/confirm-dialog"
import { CopyableText } from "@/components/console/copy-button"
import { CellText, DataTable, type Column } from "@/components/console/data-table"
import { Field } from "@/components/console/form-field"
import { JsonEditor } from "@/components/console/json-editor"
import { KeyValueGrid } from "@/components/console/key-value"
import { Section } from "@/components/console/section"
import { StatTile } from "@/components/console/stat-tile"
import { StatusBadge } from "@/components/console/status-badge"
import { TimeAgo } from "@/components/console/time-ago"
import { TagList } from "@/components/console/tags-editor"
import { api, errorMessage, seg } from "@/lib/api"
import { formatBytes, formatNumber } from "@/lib/format"
import { useAction } from "@/lib/hooks"
import type { Bucket, BucketDetail, LifecycleRule } from "@/lib/types"

import { S3AccessCard } from "./common"

type Props = { bucket: BucketDetail; listing?: Bucket; onChanged: () => void }

function EditActions({ editing, pending, onEdit, onCancel, onSave, saveDisabled }: {
  editing: boolean
  pending?: boolean
  onEdit: () => void
  onCancel: () => void
  onSave: () => void
  saveDisabled?: boolean
}) {
  if (!editing)
    return (
      <Button variant="outline" size="sm" onClick={onEdit}>
        <Pencil /> Edit
      </Button>
    )
  return (
    <>
      <Button variant="outline" size="sm" onClick={onCancel} disabled={pending}>
        Cancel
      </Button>
      <Button size="sm" onClick={onSave} disabled={pending || saveDisabled}>
        {pending && <Loader2 className="animate-spin" />}
        Save changes
      </Button>
    </>
  )
}

function VersioningSection({ bucket, onChanged }: Props) {
  const enabled = bucket.versioning === "Enabled"
  const [editing, setEditing] = useState(false)
  const [value, setValue] = useState(enabled)
  const { pending, run } = useAction()
  useEffect(() => setValue(enabled), [enabled, editing])

  const save = async () => {
    const ok = await run(async () => {
      await api.put(`/api/v1/s3/buckets/${seg(bucket.name)}/versioning`, { enabled: value })
      return true
    }, `Versioning ${value ? "enabled" : "suspended"}`)
    if (ok) {
      setEditing(false)
      onChanged()
    }
  }

  return (
    <Section
      title="Bucket versioning"
      description="Keep multiple versions of an object in the same bucket to recover from unintended overwrites and deletions."
      actions={<EditActions editing={editing} pending={pending} onEdit={() => setEditing(true)} onCancel={() => setEditing(false)} onSave={save} />}
    >
      {editing ? (
        <div className="flex items-center gap-3">
          <Switch id="ver-switch" checked={value} onCheckedChange={setValue} />
          <Label htmlFor="ver-switch">{value ? "Enable" : "Suspend"}</Label>
          {enabled && !value && <span className="text-muted-foreground text-xs">Existing object versions are kept; new writes won&apos;t create versions.</span>}
        </div>
      ) : (
        <KeyValueGrid
          items={[
            {
              label: "Bucket versioning",
              value: enabled ? <StatusBadge status="enabled" /> : bucket.versioning === "Suspended" ? <StatusBadge status="suspended" /> : <StatusBadge status="disabled" />,
            },
          ]}
        />
      )}
    </Section>
  )
}

function AccessSection({ bucket, onChanged }: Props) {
  const [confirm, setConfirm] = useState<boolean | null>(null)
  const { pending, run } = useAction()

  const makePrivate = async () => {
    const ok = await run(async () => {
      await api.put(`/api/v1/s3/buckets/${seg(bucket.name)}/access`, { public: false })
      return true
    }, "Public access blocked")
    if (ok) onChanged()
  }

  return (
    <Section title="Block public access" description="Control whether anyone can read objects in this bucket without credentials.">
      <div className="flex flex-col gap-3">
        <div className="flex items-center justify-between gap-4">
          <div className="flex items-center gap-3">
            <Switch
              id="block-public"
              checked={!bucket.public}
              disabled={pending}
              onCheckedChange={(v) => (v ? makePrivate() : setConfirm(true))}
            />
            <Label htmlFor="block-public">Block all public access</Label>
            {pending && <Loader2 className="text-muted-foreground size-4 animate-spin" />}
          </div>
          {bucket.public ? <StatusBadge status="public" label="Public" tone="warning" /> : <StatusBadge status="private" label="On" tone="success" />}
        </div>
        {bucket.public && (
          <Alert variant="warning">
            <AlertTriangle />
            <AlertDescription>Objects in this bucket are publicly readable by anyone who can reach the S3 endpoint.</AlertDescription>
          </Alert>
        )}
      </div>
      <ConfirmDialog
        open={!!confirm}
        onOpenChange={(o) => !o && setConfirm(null)}
        title="Make this bucket public?"
        description="Turning off Block all public access attaches a bucket policy that lets anyone read every object in this bucket without credentials."
        confirmText={bucket.name}
        actionLabel="Make public"
        onConfirm={async () => {
          await api.put(`/api/v1/s3/buckets/${seg(bucket.name)}/access`, { public: true })
          toast.success(`Bucket ${bucket.name} is now public`)
          onChanged()
        }}
      />
    </Section>
  )
}

function WebsiteSection({ bucket, onChanged }: Props) {
  const [editing, setEditing] = useState(false)
  const [enabled, setEnabled] = useState(bucket.website)
  const [index, setIndex] = useState(bucket.index_document || "index.html")
  const [errorDoc, setErrorDoc] = useState(bucket.error_document)
  const { pending, run } = useAction()

  useEffect(() => {
    setEnabled(bucket.website)
    setIndex(bucket.index_document || "index.html")
    setErrorDoc(bucket.error_document)
  }, [bucket.website, bucket.index_document, bucket.error_document, editing])

  const save = async () => {
    const ok = await run(
      async () => {
        await api.put(`/api/v1/s3/buckets/${seg(bucket.name)}/website`, { enabled, index_document: index.trim() || "index.html", error_document: errorDoc.trim() })
        return true
      },
      enabled ? "Static website hosting enabled" : "Static website hosting disabled",
    )
    if (ok) {
      setEditing(false)
      onChanged()
    }
  }

  return (
    <Section
      title="Static website hosting"
      description="Serve the bucket as a website. Visitors don't need credentials: the website endpoint serves objects without authentication, even when public access is blocked."
      actions={<EditActions editing={editing} pending={pending} onEdit={() => setEditing(true)} onCancel={() => setEditing(false)} onSave={save} />}
    >
      {editing ? (
        <div className="flex max-w-lg flex-col gap-4">
          <div className="flex items-center gap-3">
            <Switch id="web-switch" checked={enabled} onCheckedChange={setEnabled} />
            <Label htmlFor="web-switch">{enabled ? "Enabled" : "Disabled"}</Label>
          </div>
          {enabled && (
            <>
              <Field label="Index document" htmlFor="web-index" help="Returned for requests to the root or to a folder.">
                <Input id="web-index" value={index} onChange={(e) => setIndex(e.target.value)} placeholder="index.html" />
              </Field>
              <Field label="Error document" htmlFor="web-error" optional help="Returned with status 404 when an object is not found.">
                <Input id="web-error" value={errorDoc} onChange={(e) => setErrorDoc(e.target.value)} placeholder="error.html" />
              </Field>
            </>
          )}
        </div>
      ) : (
        <KeyValueGrid
          items={[
            { label: "Static website hosting", value: bucket.website ? <StatusBadge status="enabled" /> : <StatusBadge status="disabled" /> },
            ...(bucket.website
              ? [
                  { label: "Index document", value: <span className="font-mono text-[13px]">{bucket.index_document || "index.html"}</span> },
                  { label: "Error document", value: bucket.error_document ? <span className="font-mono text-[13px]">{bucket.error_document}</span> : "" },
                  {
                    label: "Bucket website endpoint",
                    wide: true,
                    value: (
                      <a href={bucket.website_url} target="_blank" rel="noreferrer" className="text-primary inline-flex items-center gap-1 break-all hover:underline">
                        {bucket.website_url} <ExternalLink className="size-3.5 shrink-0" />
                      </a>
                    ),
                  },
                ]
              : []),
          ]}
        />
      )}
    </Section>
  )
}

const lifecycleColumns: Column<LifecycleRule>[] = [
  { id: "id", header: "Rule name", value: (r) => r.id, cell: (r) => <CellText className="font-medium">{r.id}</CellText> },
  {
    id: "status",
    header: "Status",
    value: (r) => r.status || "Enabled",
    cell: (r) => <StatusBadge status={(r.status || "Enabled").toLowerCase()} label={r.status || "Enabled"} />,
  },
  {
    id: "scope",
    header: "Scope",
    value: (r) => r.prefix,
    cell: (r) => (r.prefix ? <CellText mono title={r.prefix}>{`Prefix: ${r.prefix}`}</CellText> : <CellText muted>Entire bucket</CellText>),
  },
  {
    id: "expiration",
    header: "Expiration",
    value: (r) => r.expiration_days,
    cell: (r) => (
      <span className="whitespace-nowrap">
        {r.expiration_days} {r.expiration_days === 1 ? "day" : "days"} after creation
      </span>
    ),
  },
]

interface RuleRow {
  id: string
  prefix: string
  days: string
}

function LifecycleSection({ bucket, onChanged }: Props) {
  const current: RuleRow[] = (bucket.lifecycle_rules ?? []).map((r) => ({ id: r.id, prefix: r.prefix, days: String(r.expiration_days) }))
  const [editing, setEditing] = useState(false)
  const [rows, setRows] = useState<RuleRow[]>(current)
  const [pending, setPending] = useState(false)
  const currentKey = JSON.stringify(current)

  useEffect(() => {
    setRows(JSON.parse(currentKey))
  }, [currentKey, editing])

  const errors = rows.map((r) => {
    const n = Number(r.days)
    if (!r.days.trim() || !Number.isInteger(n) || n < 1) return "Expiration must be a whole number of days (at least 1)"
    return null
  })
  const hasErrors = errors.some(Boolean)
  const set = (i: number, p: Partial<RuleRow>) => setRows(rows.map((r, j) => (j === i ? { ...r, ...p } : r)))

  const save = async () => {
    if (hasErrors) return
    setPending(true)
    try {
      await api.put(`/api/v1/s3/buckets/${seg(bucket.name)}/lifecycle`, {
        rules: rows.map((r) => ({ id: r.id.trim(), prefix: r.prefix.trim(), expiration_days: Number(r.days) })),
      })
      toast.success(rows.length ? "Lifecycle rules saved" : "Lifecycle rules removed")
      setEditing(false)
      onChanged()
    } catch (e) {
      toast.error(errorMessage(e))
    } finally {
      setPending(false)
    }
  }

  return (
    <Section
      title={`Lifecycle rules (${current.length})`}
      description="Expire (delete) objects automatically a number of days after they were created."
      actions={<EditActions editing={editing} pending={pending} onEdit={() => setEditing(true)} onCancel={() => setEditing(false)} onSave={save} saveDisabled={hasErrors} />}
      flush
    >
      {editing ? (
        <div className="flex flex-col gap-3 p-5">
          {rows.length > 0 && (
            <div className="text-muted-foreground hidden grid-cols-[1fr_1fr_9rem_2rem] gap-2 text-xs font-medium sm:grid">
              <span>Rule name</span>
              <span>Prefix filter</span>
              <span>Expire after (days)</span>
            </div>
          )}
          {rows.map((r, i) => (
            <div key={i} className="flex flex-col gap-1">
              <div className="grid grid-cols-1 gap-2 sm:grid-cols-[1fr_1fr_9rem_2rem]">
                <Input className="h-8" value={r.id} onChange={(e) => set(i, { id: e.target.value })} placeholder={`rule-${i + 1}`} aria-label="Rule name" />
                <Input className="h-8" value={r.prefix} onChange={(e) => set(i, { prefix: e.target.value })} placeholder="Whole bucket" aria-label="Prefix" />
                <Input
                  className="h-8"
                  type="number"
                  min={1}
                  value={r.days}
                  onChange={(e) => set(i, { days: e.target.value })}
                  aria-label="Expiration days"
                  aria-invalid={!!errors[i]}
                />
                <Button type="button" variant="ghost" size="icon" className="size-8" onClick={() => setRows(rows.filter((_, j) => j !== i))} aria-label="Remove rule">
                  <X />
                </Button>
              </div>
              {errors[i] && <p className="text-destructive text-xs">{errors[i]}</p>}
            </div>
          ))}
          {rows.length === 0 && <p className="text-muted-foreground text-sm">No rules. Saving removes the lifecycle configuration.</p>}
          <div>
            <Button type="button" variant="outline" size="sm" onClick={() => setRows([...rows, { id: "", prefix: "", days: "30" }])}>
              <Plus /> Add rule
            </Button>
          </div>
        </div>
      ) : current.length === 0 ? (
        <p className="text-muted-foreground p-5 text-sm">There are no lifecycle rules for this bucket.</p>
      ) : (
        <DataTable
          data={bucket.lifecycle_rules ?? []}
          columns={lifecycleColumns}
          rowId={(r) => r.id}
          noSearch
          className="rounded-none border-0 shadow-none"
        />
      )}
    </Section>
  )
}

export function PropertiesTab({ bucket, listing, onChanged }: Props) {
  let policy = ""
  if (bucket.policy) {
    try {
      policy = JSON.stringify(JSON.parse(bucket.policy), null, 2)
    } catch {
      policy = bucket.policy
    }
  }
  return (
    <div className="flex flex-col gap-4">
      <div className="grid grid-cols-1 gap-4 sm:grid-cols-3">
        <StatTile
          label="Objects"
          value={
            <>
              {formatNumber(bucket.object_count)}
              {bucket.stats_truncated && "+"}
            </>
          }
        />
        <StatTile
          label="Total size"
          value={
            <>
              {formatBytes(bucket.size_bytes)}
              {bucket.stats_truncated && "+"}
            </>
          }
        />
        <StatTile label="Lifecycle rules" value={(bucket.lifecycle_rules ?? []).length} caption={bucket.versioning === "Enabled" ? "Versioning enabled" : "Versioning off"} />
      </div>
      <Section title="Bucket overview">
        <KeyValueGrid
          items={[
            { label: "AWS Region", value: <span className="font-mono text-[13px]">{bucket.region}</span> },
            { label: "Amazon Resource Name (ARN)", value: <CopyableText value={bucket.arn} /> },
            { label: "Creation date", value: listing ? <TimeAgo value={listing.created_at} /> : "" },
            {
              label: "Total objects",
              value: (
                <>
                  {formatNumber(bucket.object_count)}
                  {bucket.stats_truncated && "+"}
                </>
              ),
            },
            {
              label: "Total size",
              value: (
                <>
                  {formatBytes(bucket.size_bytes)}
                  {bucket.stats_truncated && "+"}
                </>
              ),
            },
            { label: "S3 URI", value: <CopyableText value={`s3://${bucket.name}`} /> },
          ]}
        />
        {bucket.stats_truncated && (
          <p className="text-muted-foreground mt-3 flex items-center gap-1.5 text-xs">
            <Info className="size-3.5" /> Totals were computed from the first 100,000 objects only.
          </p>
        )}
      </Section>
      <VersioningSection bucket={bucket} onChanged={onChanged} />
      <AccessSection bucket={bucket} onChanged={onChanged} />
      <WebsiteSection bucket={bucket} onChanged={onChanged} />
      <LifecycleSection bucket={bucket} onChanged={onChanged} />
      <Section title="Tags" description="Tags are set when the bucket is created and can't be changed afterwards.">
        <TagList tags={bucket.tags} />
      </Section>
      <Section title="Bucket policy" description={policy ? "The resource-based policy attached to this bucket (read-only)." : undefined}>
        {policy ? <JsonEditor value={policy} readOnly rows={4} /> : <p className="text-muted-foreground text-sm">No policy to display.</p>}
      </Section>
      <S3AccessCard bucket={bucket.name} />
    </div>
  )
}
