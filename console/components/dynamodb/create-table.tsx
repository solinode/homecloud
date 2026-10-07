"use client"

import { useId, useMemo, useState } from "react"
import Link from "next/link"
import { useRouter } from "next/navigation"
import { AlertCircle, Gauge, Info, Loader2, Plus, X, Zap } from "lucide-react"
import { toast } from "sonner"

import { Alert, AlertDescription } from "@/components/ui/alert"
import { Button } from "@/components/ui/button"
import { Checkbox } from "@/components/ui/checkbox"
import { Input } from "@/components/ui/input"
import { Field } from "@/components/console/form-field"
import { OptionCard, OptionGroup } from "@/components/console/option-card"
import { Tag } from "@/components/console/tag"
import { PageHeader } from "@/components/console/page-header"
import { Section } from "@/components/console/section"
import { TagsEditor, rowsToTags, type TagRow } from "@/components/console/tags-editor"
import { api, errorMessage } from "@/lib/api"
import { pluralize } from "@/lib/format"
import { revalidate } from "@/lib/hooks"
import type { CreateTableInput, DynamoTable, KeyDef, KeyType, ProjectionType, StreamViewType, TableIndex } from "@/lib/types"
import {
  KeyTypeSelect,
  ProjectionTypeSelect,
  STREAM_VIEW_TYPES,
  StreamViewTypeSelect,
  TABLES_PATH,
  TABLE_NAME_RE,
  streamViewLabel,
  tableHref,
  tableNameError,
} from "./common"

export interface IndexRow {
  name: string
  pkName: string
  pkType: KeyType
  skName: string
  skType: KeyType
  projType: ProjectionType
  /** Comma-separated non-key attributes for INCLUDE projections. */
  include: string
}

export const emptyIndexRow = (): IndexRow => ({ name: "", pkName: "", pkType: "S", skName: "", skType: "S", projType: "ALL", include: "" })

const splitAttrs = (s: string) =>
  s
    .split(",")
    .map((a) => a.trim())
    .filter(Boolean)

/** rowToIndex builds an index; for a local index pk is the table's partition key. */
export function rowToIndex(r: IndexRow, pk?: KeyDef): TableIndex {
  return {
    name: r.name.trim(),
    partition_key: pk ?? { name: r.pkName.trim(), type: r.pkType },
    sort_key: r.skName.trim() ? { name: r.skName.trim(), type: r.skType } : undefined,
    projection: r.projType === "INCLUDE" ? { type: "INCLUDE", non_key_attributes: splitAttrs(r.include) } : { type: r.projType },
  }
}

export type IndexRowErrors = Partial<Record<"name" | "pk" | "sk" | "include", string>>

/**
 * indexRowErrors validates an index definition; taken lists names already in
 * use. Local indexes need a sort key and share the table's partition key.
 */
export function indexRowErrors(r: IndexRow, taken: string[], local = false): IndexRowErrors {
  const e: IndexRowErrors = {}
  const name = r.name.trim()
  if (!name) e.name = "Enter an index name"
  else if (!TABLE_NAME_RE.test(name)) e.name = "3-255 letters, digits, underscores, hyphens or dots"
  else if (taken.includes(name)) e.name = "Index names must be unique"
  if (!local && !r.pkName.trim()) e.pk = "Enter the index partition key"
  if (local && !r.skName.trim()) e.sk = "Local indexes need a sort key"
  if (!local && r.skName.trim() && r.skName.trim() === r.pkName.trim()) e.sk = "The sort key must differ from the partition key"
  if (r.projType === "INCLUDE" && splitAttrs(r.include).length === 0) e.include = "List at least one attribute"
  return e
}

export function CreateTable() {
  const router = useRouter()
  const [name, setName] = useState("")
  const [pkName, setPkName] = useState("")
  const [pkType, setPkType] = useState<KeyType>("S")
  const [useSort, setUseSort] = useState(false)
  const [skName, setSkName] = useState("")
  const [skType, setSkType] = useState<KeyType>("S")
  const [gsis, setGsis] = useState<IndexRow[]>([])
  const [lsis, setLsis] = useState<IndexRow[]>([])
  const [streamOn, setStreamOn] = useState(false)
  const [streamView, setStreamView] = useState<StreamViewType>("NEW_AND_OLD_IMAGES")
  const [protect, setProtect] = useState(false)
  const [ttlOn, setTtlOn] = useState(false)
  const [ttlAttr, setTtlAttr] = useState("expires_at")
  const [tagRows, setTagRows] = useState<TagRow[]>([])
  const [submitted, setSubmitted] = useState(false)
  const [pending, setPending] = useState(false)

  const errors = useMemo(() => {
    const e: Record<string, string> = {}
    const ne = tableNameError(name)
    if (ne) e.name = ne
    if (!pkName.trim()) e.pk = "Enter the partition key name"
    if (useSort) {
      if (!skName.trim()) e.sk = "Enter the sort key name"
      else if (skName.trim() === pkName.trim()) e.sk = "The sort key must differ from the partition key"
    }
    const all = [...gsis, ...lsis]
    all.forEach((g, i) => {
      const local = i >= gsis.length
      const ge = indexRowErrors(
        g,
        all.filter((_, j) => j < i).map((x) => x.name.trim()),
        local,
      )
      if (local && !ge.sk && g.skName.trim() === skName.trim()) ge.sk = "Use a sort key other than the table's"
      for (const [k, v] of Object.entries(ge)) e[`ix${i}${k}`] = v
    })
    if (lsis.length && !useSort) e.lsi = "Local secondary indexes need a table with a sort key"
    if (ttlOn && !ttlAttr.trim()) e.ttl = "Enter the TTL attribute name"
    const keys = tagRows.map((r) => r.key.trim()).filter(Boolean)
    if (new Set(keys).size !== keys.length) e.tags = "Tag keys must be unique"
    return e
  }, [name, pkName, useSort, skName, gsis, lsis, ttlOn, ttlAttr, tagRows])
  const err = (k: string) => (submitted ? errors[k] : undefined)
  const valid = Object.keys(errors).length === 0
  const ixErrors = (i: number): IndexRowErrors =>
    submitted ? { name: errors[`ix${i}name`], pk: errors[`ix${i}pk`], sk: errors[`ix${i}sk`], include: errors[`ix${i}include`] } : {}

  const setGsi = (i: number, patch: Partial<IndexRow>) => setGsis(gsis.map((g, j) => (j === i ? { ...g, ...patch } : g)))
  const setLsi = (i: number, patch: Partial<IndexRow>) => setLsis(lsis.map((g, j) => (j === i ? { ...g, ...patch } : g)))
  const tablePk: KeyDef = { name: pkName.trim(), type: pkType }

  const create = async () => {
    setSubmitted(true)
    if (!valid) {
      toast.error("Fix the highlighted fields before creating the table")
      return
    }
    const body: CreateTableInput = {
      name,
      partition_key: { name: pkName.trim(), type: pkType },
      sort_key: useSort ? { name: skName.trim(), type: skType } : undefined,
      global_secondary_indexes: gsis.length ? gsis.map((g) => rowToIndex(g)) : undefined,
      local_secondary_indexes: lsis.length ? lsis.map((g) => rowToIndex(g, tablePk)) : undefined,
      ttl_attribute: ttlOn ? ttlAttr.trim() : undefined,
      stream_view_type: streamOn ? streamView : undefined,
      deletion_protection: protect || undefined,
      tags: rowsToTags(tagRows),
    }
    setPending(true)
    try {
      const t = await api.post<DynamoTable>(TABLES_PATH, body)
      toast.success(`Created table ${t.name}`)
      await revalidate(TABLES_PATH)
      router.push(tableHref(t.name))
    } catch (e) {
      toast.error(errorMessage(e))
      setPending(false)
    }
  }

  return (
    <div className="flex flex-col gap-4">
      <PageHeader
        title="Create table"
        description="Tables are schemaless: only the key attributes are declared. Every item is a JSON document of up to 400 KB."
        breadcrumbs={[{ label: "DynamoDB", href: "/dynamodb/" }, { label: "Tables", href: "/dynamodb/" }, { label: "Create table" }]}
      />
      <form
        onSubmit={(e) => {
          e.preventDefault()
          create()
        }}
        className="grid grid-cols-1 items-start gap-4 xl:grid-cols-[minmax(0,1fr)_320px]"
      >
        <div className="flex min-w-0 flex-col gap-4">
          <Section title="Table details">
            <div className="flex flex-col gap-5">
              <Field label="Table name" htmlFor="tbl-name" error={err("name")} help="3-255 characters: letters, digits, underscores, hyphens and dots.">
                <Input
                  id="tbl-name"
                  value={name}
                  onChange={(e) => setName(e.target.value)}
                  placeholder="e.g. orders"
                  autoFocus
                  autoComplete="off"
                  spellCheck={false}
                  className="max-w-md"
                />
              </Field>
              <Field
                label="Partition key"
                htmlFor="tbl-pk"
                error={err("pk")}
                help="The partition key is part of the primary key. Queries always select a single partition value."
              >
                <div className="flex max-w-md gap-2">
                  <Input
                    id="tbl-pk"
                    value={pkName}
                    onChange={(e) => setPkName(e.target.value)}
                    placeholder="e.g. customer_id"
                    aria-label="Partition key name"
                    className="h-8 min-w-0 flex-1 font-mono"
                  />
                  <KeyTypeSelect value={pkType} onChange={setPkType} />
                </div>
              </Field>
              <div className="flex flex-col gap-2">
                <label className="flex w-fit cursor-pointer items-center gap-2 text-[13px] font-medium">
                  <Checkbox checked={useSort} onCheckedChange={(v) => setUseSort(v === true)} />
                  Add a sort key
                </label>
                {useSort && (
                  <Field
                    label="Sort key"
                    htmlFor="tbl-sk"
                    error={err("sk")}
                    help="Items with the same partition key are stored sorted by this key, enabling range conditions (between, begins_with, <, >)."
                  >
                    <div className="flex max-w-md gap-2">
                      <Input
                        id="tbl-sk"
                        value={skName}
                        onChange={(e) => setSkName(e.target.value)}
                        placeholder="e.g. created_at"
                        aria-label="Sort key name"
                        className="h-8 min-w-0 flex-1 font-mono"
                      />
                      <KeyTypeSelect value={skType} onChange={setSkType} />
                    </div>
                  </Field>
                )}
              </div>
              <Field label="Capacity mode" help="Tables always use on-demand capacity; there is no throughput to provision.">
                <OptionGroup label="Capacity mode" className="max-w-2xl">
                  <OptionCard
                    selected
                    onSelect={() => {}}
                    icon={Zap}
                    title="On-demand"
                    badge={<Tag accent="success">PAY_PER_REQUEST</Tag>}
                    description="Reads and writes scale with traffic. No throughput settings."
                  />
                  <OptionCard
                    selected={false}
                    onSelect={() => {}}
                    disabled
                    icon={Gauge}
                    title="Provisioned"
                    badge={<Tag>Not supported</Tag>}
                    description="Fixed read and write capacity units."
                  />
                </OptionGroup>
              </Field>
            </div>
          </Section>

          <Section
            title="Global secondary indexes"
            description="Query items by other attributes. Items without the index partition key are not included in the index."
          >
            <div className="flex flex-col gap-3">
              {gsis.length === 0 && <p className="text-muted-foreground text-sm">No secondary indexes.</p>}
              {gsis.map((g, i) => (
                <IndexRowEditor
                  key={i}
                  row={g}
                  onChange={(p) => setGsi(i, p)}
                  onRemove={() => setGsis(gsis.filter((_, j) => j !== i))}
                  errors={ixErrors(i)}
                />
              ))}
              <div>
                <Button type="button" variant="outline" size="sm" onClick={() => setGsis([...gsis, emptyIndexRow()])} disabled={gsis.length >= 20}>
                  <Plus /> Add global index
                </Button>
              </div>
            </div>
          </Section>

          <Section
            title="Local secondary indexes"
            description="Sort items within a partition by another attribute. Local indexes share the table's partition key and can only be defined when the table is created."
          >
            <div className="flex flex-col gap-3">
              {lsis.length === 0 && (
                <p className="text-muted-foreground text-sm">{useSort ? "No local indexes." : "Add a sort key to the table to define local indexes."}</p>
              )}
              {lsis.map((g, i) => (
                <IndexRowEditor
                  key={i}
                  row={g}
                  localPk={tablePk}
                  onChange={(p) => setLsi(i, p)}
                  onRemove={() => setLsis(lsis.filter((_, j) => j !== i))}
                  errors={ixErrors(gsis.length + i)}
                />
              ))}
              {err("lsi") && <p className="text-destructive text-xs">{err("lsi")}</p>}
              <div>
                <Button type="button" variant="outline" size="sm" onClick={() => setLsis([...lsis, emptyIndexRow()])} disabled={!useSort || lsis.length >= 5}>
                  <Plus /> Add local index
                </Button>
              </div>
            </div>
          </Section>

          <Section title="DynamoDB stream" description="Record every item change in a time-ordered log that DynamoDB Streams clients and Lambda triggers can read for 24 hours.">
            <div className="flex flex-col gap-3">
              <label className="flex w-fit cursor-pointer items-center gap-2 text-[13px] font-medium">
                <Checkbox checked={streamOn} onCheckedChange={(v) => setStreamOn(v === true)} />
                Enable stream
              </label>
              {streamOn && (
                <Field label="View type" htmlFor="stream-view" help={STREAM_VIEW_TYPES.find((s) => s.value === streamView)?.help}>
                  <StreamViewTypeSelect id="stream-view" value={streamView} onChange={setStreamView} className="max-w-md" />
                </Field>
              )}
            </div>
          </Section>

          <Section title="Time to live (TTL)" description="Expire items automatically when a numeric attribute holding an epoch time in seconds is in the past.">
            <div className="flex flex-col gap-3">
              <label className="flex w-fit cursor-pointer items-center gap-2 text-[13px] font-medium">
                <Checkbox checked={ttlOn} onCheckedChange={(v) => setTtlOn(v === true)} />
                Enable TTL
              </label>
              {ttlOn && (
                <Field
                  label="TTL attribute name"
                  htmlFor="ttl-attr"
                  error={err("ttl")}
                  help="Expired items disappear from reads immediately and are deleted by a background sweep within about a minute."
                >
                  <Input id="ttl-attr" value={ttlAttr} onChange={(e) => setTtlAttr(e.target.value)} className="h-8 max-w-md font-mono" />
                </Field>
              )}
            </div>
          </Section>

          <Section title="Deletion protection">
            <label className="flex items-start gap-2 text-sm">
              <Checkbox checked={protect} onCheckedChange={(v) => setProtect(v === true)} className="mt-0.5" />
              <span>
                <span className="font-medium">Turn on deletion protection</span>
                <span className="text-muted-foreground block text-xs">The table cannot be deleted until deletion protection is turned off.</span>
              </span>
            </label>
          </Section>

          <Section title="Tags">
            <Field label="Tags" optional error={err("tags")}>
              <TagsEditor rows={tagRows} onChange={setTagRows} />
            </Field>
          </Section>
        </div>

        <aside className="xl:sticky xl:top-4">
          <Section title="Summary">
            <div className="flex flex-col gap-4">
              <dl className="flex flex-col gap-3 text-sm">
                <SummaryItem label="Table name">
                  <span className="font-mono text-[13px] break-all">{name || "-"}</span>
                </SummaryItem>
                <SummaryItem label="Primary key">
                  {pkName.trim() ? (
                    <span className="font-mono text-[13px]">
                      {pkName.trim()} ({pkType}){useSort && skName.trim() ? ` + ${skName.trim()} (${skType})` : ""}
                    </span>
                  ) : (
                    "-"
                  )}
                </SummaryItem>
                <SummaryItem label="Global indexes">{gsis.length ? gsis.map((g) => g.name || "(unnamed)").join(", ") : "None"}</SummaryItem>
                <SummaryItem label="Local indexes">{lsis.length ? lsis.map((g) => g.name || "(unnamed)").join(", ") : "None"}</SummaryItem>
                <SummaryItem label="Stream">{streamOn ? streamViewLabel(streamView) : "Off"}</SummaryItem>
                <SummaryItem label="Deletion protection">{protect ? "On" : "Off"}</SummaryItem>
                <SummaryItem label="TTL">{ttlOn && ttlAttr.trim() ? <span className="font-mono text-[13px]">{ttlAttr.trim()}</span> : "Off"}</SummaryItem>
                <SummaryItem label="Capacity mode">On-demand (pay per request)</SummaryItem>
                <SummaryItem label="Tags">{pluralize(tagRows.filter((r) => r.key.trim()).length, "tag")}</SummaryItem>
              </dl>
              <Alert variant="info">
                <Info />
                <AlertDescription>
                  The key schema and local indexes cannot be changed after creation. Global indexes, streams and TTL can be changed later.
                </AlertDescription>
              </Alert>
              {submitted && !valid && (
                <Alert variant="destructive">
                  <AlertCircle />
                  <AlertDescription>Some settings need attention. Check the highlighted fields.</AlertDescription>
                </Alert>
              )}
              <div className="flex justify-end gap-2 border-t pt-4">
                <Button type="button" variant="outline" asChild>
                  <Link href="/dynamodb/">Cancel</Link>
                </Button>
                <Button type="submit" disabled={pending}>
                  {pending && <Loader2 className="animate-spin" />}
                  Create table
                </Button>
              </div>
            </div>
          </Section>
        </aside>
      </form>
    </div>
  )
}

/**
 * IndexRowEditor edits one secondary index: name, keys and projection. With
 * localPk set it edits a local index, whose partition key is the table's.
 */
export function IndexRowEditor({
  row,
  onChange,
  onRemove,
  errors = {},
  localPk,
}: {
  row: IndexRow
  onChange: (patch: Partial<IndexRow>) => void
  onRemove?: () => void
  errors?: IndexRowErrors
  localPk?: { name: string; type: KeyType }
}) {
  const id = useId()
  return (
    <div className="bg-muted/30 relative grid grid-cols-1 gap-3 rounded-lg border p-3 md:grid-cols-3">
      <Field label="Index name" htmlFor={`${id}-name`} error={errors.name}>
        <Input
          id={`${id}-name`}
          value={row.name}
          onChange={(e) => onChange({ name: e.target.value })}
          placeholder="e.g. by-status"
          className="h-8 font-mono"
          autoComplete="off"
          spellCheck={false}
        />
      </Field>
      <Field label="Partition key" htmlFor={`${id}-pk`} error={errors.pk} help={localPk ? "Same as the table" : undefined}>
        {localPk ? (
          <div className="flex gap-2">
            <Input id={`${id}-pk`} value={localPk.name || "(table partition key)"} disabled className="h-8 min-w-0 flex-1 font-mono" />
            <KeyTypeSelect value={localPk.type} onChange={() => {}} className="w-24" disabled />
          </div>
        ) : (
          <div className="flex gap-2">
            <Input
              id={`${id}-pk`}
              value={row.pkName}
              onChange={(e) => onChange({ pkName: e.target.value })}
              placeholder="attribute"
              className="h-8 min-w-0 flex-1 font-mono"
              aria-label="Index partition key name"
            />
            <KeyTypeSelect value={row.pkType} onChange={(v) => onChange({ pkType: v })} className="w-24" />
          </div>
        )}
      </Field>
      <Field label="Sort key" htmlFor={`${id}-sk`} optional={!localPk} error={errors.sk}>
        <div className="flex gap-2">
          <Input
            id={`${id}-sk`}
            value={row.skName}
            onChange={(e) => onChange({ skName: e.target.value })}
            placeholder="attribute"
            className="h-8 min-w-0 flex-1 font-mono"
            aria-label="Index sort key name"
          />
          <KeyTypeSelect value={row.skType} onChange={(v) => onChange({ skType: v })} className="w-24" />
          {onRemove && (
            <Button type="button" variant="ghost" size="icon" className="size-8 shrink-0" onClick={onRemove} aria-label="Remove index">
              <X />
            </Button>
          )}
        </div>
      </Field>
      <Field label="Projected attributes" help="Attributes copied into the index and returned by index reads.">
        <ProjectionTypeSelect value={row.projType} onChange={(v) => onChange({ projType: v })} />
      </Field>
      {row.projType === "INCLUDE" && (
        <Field
          label="Included attributes"
          htmlFor={`${id}-include`}
          error={errors.include}
          help="Comma-separated. Key attributes are always included."
          className="md:col-span-2"
        >
          <Input
            id={`${id}-include`}
            value={row.include}
            onChange={(e) => onChange({ include: e.target.value })}
            placeholder="e.g. status, total"
            className="h-8 font-mono"
            aria-label="Included attributes"
          />
        </Field>
      )}
    </div>
  )
}

function SummaryItem({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div>
      <dt className="text-faint mb-0.5 text-xs font-medium">{label}</dt>
      <dd className="min-w-0 break-words">{children}</dd>
    </div>
  )
}
