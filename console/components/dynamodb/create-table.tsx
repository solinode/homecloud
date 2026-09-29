"use client"

import { useMemo, useState } from "react"
import Link from "next/link"
import { useRouter } from "next/navigation"
import { Info, Loader2, Plus, X } from "lucide-react"
import { toast } from "sonner"

import { Button } from "@/components/ui/button"
import { Checkbox } from "@/components/ui/checkbox"
import { Input } from "@/components/ui/input"
import { Field } from "@/components/console/form-field"
import { PageHeader } from "@/components/console/page-header"
import { Section } from "@/components/console/section"
import { TagsEditor, rowsToTags, type TagRow } from "@/components/console/tags-editor"
import { api, errorMessage } from "@/lib/api"
import { pluralize } from "@/lib/format"
import { revalidate } from "@/lib/hooks"
import type { CreateTableInput, DynamoTable, KeyType, TableIndex } from "@/lib/types"
import { KeyTypeSelect, TABLES_PATH, TABLE_NAME_RE, tableHref, tableNameError } from "./common"

export interface IndexRow {
  name: string
  pkName: string
  pkType: KeyType
  skName: string
  skType: KeyType
}

export const emptyIndexRow = (): IndexRow => ({ name: "", pkName: "", pkType: "S", skName: "", skType: "S" })

export function rowToIndex(r: IndexRow): TableIndex {
  return {
    name: r.name.trim(),
    partition_key: { name: r.pkName.trim(), type: r.pkType },
    sort_key: r.skName.trim() ? { name: r.skName.trim(), type: r.skType } : undefined,
  }
}

/** indexRowErrors validates a GSI definition; taken lists names already in use. */
export function indexRowErrors(r: IndexRow, taken: string[]): Partial<Record<"name" | "pk", string>> {
  const e: Partial<Record<"name" | "pk", string>> = {}
  const name = r.name.trim()
  if (!name) e.name = "Enter an index name"
  else if (!TABLE_NAME_RE.test(name)) e.name = "3-255 letters, digits, underscores, hyphens or dots"
  else if (taken.includes(name)) e.name = "Index names must be unique"
  if (!r.pkName.trim()) e.pk = "Enter the index partition key"
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
    gsis.forEach((g, i) => {
      const ge = indexRowErrors(
        g,
        gsis.filter((_, j) => j < i).map((x) => x.name.trim()),
      )
      if (ge.name) e[`gsi${i}name`] = ge.name
      if (ge.pk) e[`gsi${i}pk`] = ge.pk
    })
    if (ttlOn && !ttlAttr.trim()) e.ttl = "Enter the TTL attribute name"
    const keys = tagRows.map((r) => r.key.trim()).filter(Boolean)
    if (new Set(keys).size !== keys.length) e.tags = "Tag keys must be unique"
    return e
  }, [name, pkName, useSort, skName, gsis, ttlOn, ttlAttr, tagRows])
  const err = (k: string) => (submitted ? errors[k] : undefined)
  const valid = Object.keys(errors).length === 0

  const setGsi = (i: number, patch: Partial<IndexRow>) => setGsis(gsis.map((g, j) => (j === i ? { ...g, ...patch } : g)))

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
      global_secondary_indexes: gsis.length ? gsis.map(rowToIndex) : undefined,
      ttl_attribute: ttlOn ? ttlAttr.trim() : undefined,
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
                error={err("pk")}
                help="The partition key is part of the primary key. Queries always select a single partition value."
              >
                <div className="flex max-w-md gap-2">
                  <Input
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
                <label className="flex items-center gap-2 text-sm font-medium">
                  <Checkbox checked={useSort} onCheckedChange={(v) => setUseSort(v === true)} />
                  Add a sort key
                </label>
                {useSort && (
                  <Field
                    label="Sort key"
                    error={err("sk")}
                    help="Items with the same partition key are stored sorted by this key, enabling range conditions (between, begins_with, <, >)."
                  >
                    <div className="flex max-w-md gap-2">
                      <Input
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
                  nameError={err(`gsi${i}name`)}
                  pkError={err(`gsi${i}pk`)}
                />
              ))}
              <div>
                <Button type="button" variant="outline" size="sm" onClick={() => setGsis([...gsis, emptyIndexRow()])} disabled={gsis.length >= 20}>
                  <Plus /> Add index
                </Button>
              </div>
            </div>
          </Section>

          <Section title="Time to live (TTL)" description="Expire items automatically when a numeric attribute holding an epoch time in seconds is in the past.">
            <div className="flex flex-col gap-3">
              <label className="flex items-center gap-2 text-sm font-medium">
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
                <SummaryItem label="Secondary indexes">{gsis.length ? gsis.map((g) => g.name || "(unnamed)").join(", ") : "None"}</SummaryItem>
                <SummaryItem label="TTL">{ttlOn && ttlAttr.trim() ? <span className="font-mono text-[13px]">{ttlAttr.trim()}</span> : "Off"}</SummaryItem>
                <SummaryItem label="Capacity mode">On-demand (pay per request)</SummaryItem>
                <SummaryItem label="Tags">{pluralize(tagRows.filter((r) => r.key.trim()).length, "tag")}</SummaryItem>
              </dl>
              <p className="text-muted-foreground flex gap-1.5 border-t pt-3 text-xs">
                <Info className="mt-px size-3.5 shrink-0" />
                The key schema cannot be changed after creation. Indexes and TTL can be changed later.
              </p>
              {submitted && !valid && <p className="text-destructive text-xs">Some settings need attention. Check the highlighted fields.</p>}
              <div className="flex flex-col gap-2 border-t pt-4">
                <Button type="submit" disabled={pending}>
                  {pending && <Loader2 className="animate-spin" />}
                  Create table
                </Button>
                <Button type="button" variant="outline" asChild>
                  <Link href="/dynamodb/">Cancel</Link>
                </Button>
              </div>
            </div>
          </Section>
        </aside>
      </form>
    </div>
  )
}

/** IndexRowEditor edits one GSI: name, partition key and optional sort key. */
export function IndexRowEditor({
  row,
  onChange,
  onRemove,
  nameError,
  pkError,
}: {
  row: IndexRow
  onChange: (patch: Partial<IndexRow>) => void
  onRemove?: () => void
  nameError?: string
  pkError?: string
}) {
  return (
    <div className="relative grid grid-cols-1 gap-3 rounded-md border p-3 md:grid-cols-3">
      <Field label="Index name" error={nameError}>
        <Input value={row.name} onChange={(e) => onChange({ name: e.target.value })} placeholder="e.g. by-status" className="h-8 font-mono" aria-label="Index name" />
      </Field>
      <Field label="Partition key" error={pkError}>
        <div className="flex gap-2">
          <Input
            value={row.pkName}
            onChange={(e) => onChange({ pkName: e.target.value })}
            placeholder="attribute"
            className="h-8 min-w-0 flex-1 font-mono"
            aria-label="Index partition key name"
          />
          <KeyTypeSelect value={row.pkType} onChange={(v) => onChange({ pkType: v })} className="w-24" />
        </div>
      </Field>
      <Field label="Sort key" optional>
        <div className="flex gap-2">
          <Input
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
    </div>
  )
}

function SummaryItem({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div>
      <dt className="text-muted-foreground text-xs font-medium">{label}</dt>
      <dd className="min-w-0 break-words">{children}</dd>
    </div>
  )
}
