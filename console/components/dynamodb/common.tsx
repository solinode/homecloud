"use client"

import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select"
import type { DynamoItem, DynamoTable, IndexProjection, KeyDef, KeyType, ProjectionType, StreamViewType, TableIndex } from "@/lib/types"
import { cn } from "@/lib/utils"

export const TABLES_PATH = "/api/v1/dynamodb/tables"

export const tableHref = (name: string, tab?: string) => `/dynamodb/table/?name=${encodeURIComponent(name)}${tab ? `&tab=${tab}` : ""}`

export const TABLE_NAME_RE = /^[a-zA-Z0-9_.-]{3,255}$/

export function tableNameError(name: string): string | null {
  if (!name) return "Enter a table name"
  if (name.length < 3 || name.length > 255) return "Table names are 3-255 characters"
  if (!TABLE_NAME_RE.test(name)) return "Use only letters, digits, underscores (_), hyphens (-) and dots (.)"
  return null
}

export const KEY_TYPE_LABEL: Record<KeyType, string> = { S: "String", N: "Number" }

/** keyLabel renders "customer (String)". */
export function keyLabel(k?: KeyDef | null): string {
  return k ? `${k.name} (${KEY_TYPE_LABEL[k.type] ?? k.type})` : "-"
}

/** KeyTypeSelect picks S (String) or N (Number). */
export function KeyTypeSelect({
  value,
  onChange,
  id,
  className,
  disabled,
}: {
  value: KeyType
  onChange: (v: KeyType) => void
  id?: string
  className?: string
  disabled?: boolean
}) {
  return (
    <Select value={value} onValueChange={(v) => onChange(v as KeyType)} disabled={disabled}>
      <SelectTrigger id={id} size="sm" className={cn("w-28", className)} aria-label="Key type">
        <SelectValue />
      </SelectTrigger>
      <SelectContent>
        <SelectItem value="S">String</SelectItem>
        <SelectItem value="N">Number</SelectItem>
      </SelectContent>
    </Select>
  )
}

/** KeySchema renders a table's or index's key schema as "PK: customer (S) · SK: ts (N)". */
export function KeySchema({ pk, sk, className }: { pk: KeyDef; sk?: KeyDef | null; className?: string }) {
  return (
    <span className={cn("inline-flex flex-wrap items-center gap-x-2 gap-y-0.5", className)}>
      <span>
        <span className="font-mono text-[13px]">{pk.name}</span> <span className="text-muted-foreground text-xs">({pk.type})</span>
      </span>
      {sk && (
        <span>
          <span className="text-muted-foreground text-xs">+</span> <span className="font-mono text-[13px]">{sk.name}</span>{" "}
          <span className="text-muted-foreground text-xs">({sk.type})</span>
        </span>
      )}
    </span>
  )
}

/**
 * parseKeyValue converts user input to a key attribute value of the given
 * type: strings must be non-empty, numbers must parse as finite numbers.
 */
export function parseKeyValue(type: KeyType, raw: string): { value?: string | number; error?: string } {
  if (type === "N") {
    const t = raw.trim()
    if (!t) return { error: "Enter a number" }
    const n = Number(t)
    if (!Number.isFinite(n)) return { error: `"${t}" is not a number` }
    return { value: n }
  }
  if (!raw) return { error: "Enter a value" }
  return { value: raw }
}

/** keyDefs lists the table's primary key attributes. */
export function primaryKeys(t: Pick<DynamoTable, "partition_key" | "sort_key">): KeyDef[] {
  return t.sort_key ? [t.partition_key, t.sort_key] : [t.partition_key]
}

/** itemKey extracts the primary key of an item. */
export function itemKey(t: Pick<DynamoTable, "partition_key" | "sort_key">, it: DynamoItem): DynamoItem {
  const k: DynamoItem = {}
  for (const d of primaryKeys(t)) k[d.name] = it[d.name]
  return k
}

/** keyId is a stable string id for an item's primary key. */
export function keyId(t: Pick<DynamoTable, "partition_key" | "sort_key">, it: DynamoItem): string {
  return JSON.stringify(primaryKeys(t).map((d) => it[d.name]))
}

/** keyError checks an item has well-typed primary key attributes. */
export function keyError(t: Pick<DynamoTable, "partition_key" | "sort_key">, it: DynamoItem): string | null {
  for (const d of primaryKeys(t)) {
    const v = it[d.name]
    if (v === undefined) return `The item must include the key attribute "${d.name}"`
    if (d.type === "S" && (typeof v !== "string" || v === "")) return `Key attribute "${d.name}" must be a non-empty string`
    if (d.type === "N" && (typeof v !== "number" || !Number.isFinite(v))) return `Key attribute "${d.name}" must be a number`
  }
  return null
}

/** keySkeleton is an empty item with the key attributes typed correctly. */
export function keySkeleton(t: Pick<DynamoTable, "partition_key" | "sort_key">): DynamoItem {
  const it: DynamoItem = {}
  for (const d of primaryKeys(t)) it[d.name] = d.type === "N" ? 0 : ""
  return it
}

export function indexes(t: Pick<DynamoTable, "global_secondary_indexes">): TableIndex[] {
  return t.global_secondary_indexes ?? []
}

export function localIndexes(t: Pick<DynamoTable, "local_secondary_indexes">): TableIndex[] {
  return t.local_secondary_indexes ?? []
}

/** allIndexes lists global then local secondary indexes, each tagged with its kind. */
export function allIndexes(t: Pick<DynamoTable, "global_secondary_indexes" | "local_secondary_indexes">): (TableIndex & { local: boolean })[] {
  return [...indexes(t).map((i) => ({ ...i, local: false })), ...localIndexes(t).map((i) => ({ ...i, local: true }))]
}

export const PROJECTION_LABEL: Record<ProjectionType, string> = { ALL: "All attributes", KEYS_ONLY: "Keys only", INCLUDE: "Include" }

/** projectionLabel renders an index projection: "All attributes", "Include: a, b". */
export function projectionLabel(p?: IndexProjection | null): string {
  if (!p || p.type === "ALL") return PROJECTION_LABEL.ALL
  if (p.type === "INCLUDE") return `Include: ${(p.non_key_attributes ?? []).join(", ") || "-"}`
  return PROJECTION_LABEL[p.type] ?? p.type
}

export const STREAM_VIEW_TYPES: { value: StreamViewType; label: string; help: string }[] = [
  { value: "NEW_AND_OLD_IMAGES", label: "New and old images", help: "The item before and after each change." },
  { value: "NEW_IMAGE", label: "New image", help: "The item as it appears after the change." },
  { value: "OLD_IMAGE", label: "Old image", help: "The item as it appeared before the change." },
  { value: "KEYS_ONLY", label: "Keys only", help: "Only the key attributes of the changed item." },
]

export function streamViewLabel(v?: string | null): string {
  return STREAM_VIEW_TYPES.find((s) => s.value === v)?.label ?? v ?? "-"
}

/** StreamViewTypeSelect picks a stream view type. */
export function StreamViewTypeSelect({
  value,
  onChange,
  id,
  className,
  disabled,
}: {
  value: StreamViewType
  onChange: (v: StreamViewType) => void
  id?: string
  className?: string
  disabled?: boolean
}) {
  return (
    <Select value={value} onValueChange={(v) => onChange(v as StreamViewType)} disabled={disabled}>
      <SelectTrigger id={id} size="sm" className={cn("w-full max-w-sm", className)} aria-label="Stream view type">
        <SelectValue />
      </SelectTrigger>
      <SelectContent>
        {STREAM_VIEW_TYPES.map((s) => (
          <SelectItem key={s.value} value={s.value}>
            {s.label}
          </SelectItem>
        ))}
      </SelectContent>
    </Select>
  )
}

/** ProjectionTypeSelect picks the attributes an index copies. */
export function ProjectionTypeSelect({
  value,
  onChange,
  className,
}: {
  value: ProjectionType
  onChange: (v: ProjectionType) => void
  className?: string
}) {
  return (
    <Select value={value} onValueChange={(v) => onChange(v as ProjectionType)}>
      <SelectTrigger size="sm" className={cn("w-full", className)} aria-label="Projected attributes">
        <SelectValue />
      </SelectTrigger>
      <SelectContent>
        <SelectItem value="ALL">All attributes</SelectItem>
        <SelectItem value="KEYS_ONLY">Keys only</SelectItem>
        <SelectItem value="INCLUDE">Keys + selected attributes</SelectItem>
      </SelectContent>
    </Select>
  )
}

export const DELETION_PROTECTED_MESSAGE = "Deletion protection is on for this table. Turn deletion protection off before deleting the table."

/** compactJson renders a value as single-line JSON, truncated. */
export function compactJson(v: unknown, max = 60): string {
  let s: string
  try {
    s = JSON.stringify(v) ?? String(v)
  } catch {
    s = String(v)
  }
  return s.length > max ? `${s.slice(0, max - 1)}…` : s
}

/** AttrValue renders an attribute value in a results cell. */
export function AttrValue({ value, ttl }: { value: unknown; ttl?: boolean }) {
  if (value === undefined) return <span className="text-muted-foreground/60">-</span>
  if (value === null) return <span className="text-muted-foreground font-mono text-[13px]">null</span>
  if (typeof value === "string") {
    return (
      <span className="block max-w-72 truncate" title={value.length > 40 ? value : undefined}>
        {value}
      </span>
    )
  }
  if (typeof value === "number") {
    const date = ttl && value > 0 ? new Date(value * 1000) : null
    return (
      <span className="font-mono text-[13px] tabular-nums" title={date ? `${date.toISOString()}${value * 1000 < Date.now() ? " (expired)" : ""}` : undefined}>
        {value}
      </span>
    )
  }
  if (typeof value === "boolean") return <span className="font-mono text-[13px] text-blue-700 dark:text-blue-300">{String(value)}</span>
  const full = JSON.stringify(value)
  return (
    <span className="text-muted-foreground block max-w-72 truncate font-mono text-[12.5px]" title={full.length > 60 ? full : undefined}>
      {compactJson(value)}
    </span>
  )
}
