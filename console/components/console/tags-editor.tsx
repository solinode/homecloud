"use client"

import { Plus, X } from "lucide-react"

import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import type { Tags } from "@/lib/types"

export interface TagRow {
  key: string
  value: string
}

export function tagsToRows(tags?: Tags | null): TagRow[] {
  return Object.entries(tags ?? {}).map(([key, value]) => ({ key, value }))
}

/** rowsToTags drops rows without a key; returns undefined when empty. */
export function rowsToTags(rows: TagRow[]): Tags | undefined {
  const out: Tags = {}
  for (const r of rows) if (r.key.trim()) out[r.key.trim()] = r.value
  return Object.keys(out).length ? out : undefined
}

/** TagsEditor edits key/value pairs (resource tags, secret key/value JSON). */
export function TagsEditor({
  rows,
  onChange,
  keyPlaceholder = "Key",
  valuePlaceholder = "Value",
  addLabel = "Add tag",
  max = 50,
}: {
  rows: TagRow[]
  onChange: (rows: TagRow[]) => void
  keyPlaceholder?: string
  valuePlaceholder?: string
  addLabel?: string
  max?: number
}) {
  const set = (i: number, patch: Partial<TagRow>) => onChange(rows.map((r, j) => (j === i ? { ...r, ...patch } : r)))
  return (
    <div className="flex flex-col gap-2">
      {rows.length > 0 && (
        <div className="text-muted-foreground hidden grid-cols-[1fr_1fr_2rem] gap-2 text-xs font-medium sm:grid">
          <span>{keyPlaceholder}</span>
          <span>{valuePlaceholder}</span>
        </div>
      )}
      {rows.map((r, i) => (
        <div key={i} className="grid grid-cols-[1fr_1fr_2rem] gap-2">
          <Input value={r.key} onChange={(e) => set(i, { key: e.target.value })} placeholder={keyPlaceholder} className="h-8" />
          <Input value={r.value} onChange={(e) => set(i, { value: e.target.value })} placeholder={valuePlaceholder} className="h-8" />
          <Button type="button" variant="ghost" size="icon" className="size-8" onClick={() => onChange(rows.filter((_, j) => j !== i))} aria-label="Remove">
            <X />
          </Button>
        </div>
      ))}
      <div>
        <Button type="button" variant="outline" size="sm" onClick={() => onChange([...rows, { key: "", value: "" }])} disabled={rows.length >= max}>
          <Plus /> {addLabel}
        </Button>
      </div>
    </div>
  )
}

/** TagList renders tags as small chips. */
export function TagList({ tags }: { tags?: Tags | null }) {
  const entries = Object.entries(tags ?? {})
  if (!entries.length) return <span className="text-muted-foreground">No tags</span>
  return (
    <div className="flex flex-wrap gap-1.5">
      {entries.map(([k, v]) => (
        <span key={k} className="bg-muted inline-flex items-center rounded-md border px-2 py-0.5 font-mono text-xs">
          <span className="text-muted-foreground">{k}</span>
          {v && <span>={v}</span>}
        </span>
      ))}
    </div>
  )
}
