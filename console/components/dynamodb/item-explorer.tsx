"use client"

import { useCallback, useEffect, useMemo, useRef, useState } from "react"
import { AlertTriangle, ChevronLeft, ChevronRight, Loader2, Pencil, Play, Plus, Trash2, X } from "lucide-react"
import { toast } from "sonner"

import { Button } from "@/components/ui/button"
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog"
import { Input } from "@/components/ui/input"
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select"
import { ConfirmDialog } from "@/components/console/confirm-dialog"
import { DataTable, type Column } from "@/components/console/data-table"
import { EmptyState } from "@/components/console/empty-state"
import { Field } from "@/components/console/form-field"
import { JsonEditor, jsonError } from "@/components/console/json-editor"
import { Section } from "@/components/console/section"
import { ApiError, api, errorMessage, seg } from "@/lib/api"
import { formatNumber, pluralize } from "@/lib/format"
import { revalidate } from "@/lib/hooks"
import type { Condition, ConditionOp, DynamoItem, DynamoTable, KeyDef, PageInput, PageResult, QueryInput } from "@/lib/types"
import { cn } from "@/lib/utils"
import { AttrValue, KeySchema, TABLES_PATH, compactJson, indexes, itemKey, keyError, keyId, keySkeleton, parseKeyValue, primaryKeys } from "./common"

type Mode = "scan" | "query"
type ValueType = "S" | "N" | "BOOL"

interface FilterRow {
  attr: string
  op: ConditionOp
  type: ValueType
  value: string
  value2: string
}

const FILTER_OPS: { op: ConditionOp; label: string }[] = [
  { op: "eq", label: "Equal to" },
  { op: "ne", label: "Not equal to" },
  { op: "lt", label: "Less than" },
  { op: "le", label: "Less than or equal to" },
  { op: "gt", label: "Greater than" },
  { op: "ge", label: "Greater than or equal to" },
  { op: "between", label: "Between" },
  { op: "begins_with", label: "Begins with" },
  { op: "contains", label: "Contains" },
  { op: "exists", label: "Exists" },
  { op: "not_exists", label: "Not exists" },
]

const SORT_OPS: { op: ConditionOp | "none"; label: string }[] = [
  { op: "none", label: "Any value" },
  { op: "eq", label: "Equal to" },
  { op: "lt", label: "Less than" },
  { op: "le", label: "Less than or equal to" },
  { op: "gt", label: "Greater than" },
  { op: "ge", label: "Greater than or equal to" },
  { op: "between", label: "Between" },
  { op: "begins_with", label: "Begins with" },
]

const PAGE_SIZES = ["10", "25", "50", "100", "250", "1000"]

const emptyFilter = (): FilterRow => ({ attr: "", op: "eq", type: "S", value: "", value2: "" })

interface Request {
  mode: Mode
  body: PageInput | QueryInput
  /** key schema the results are ordered by (table or index) */
  label: string
}

/** coerce converts a filter value typed as text to the selected JSON type. */
function coerce(type: ValueType, raw: string): { value?: unknown; error?: string } {
  if (type === "N") {
    const n = Number(raw.trim())
    if (!raw.trim() || !Number.isFinite(n)) return { error: "Enter a number" }
    return { value: n }
  }
  if (type === "BOOL") return { value: raw === "true" }
  return { value: raw }
}

export function ItemExplorer({ table }: { table: DynamoTable }) {
  const path = `${TABLES_PATH}/${seg(table.name)}`
  const gsis = indexes(table)

  // ---- form ----
  const [mode, setMode] = useState<Mode>("scan")
  const [index, setIndex] = useState("")
  const [pv, setPv] = useState("")
  const [sortOp, setSortOp] = useState<ConditionOp | "none">("none")
  const [sv, setSv] = useState("")
  const [sv2, setSv2] = useState("")
  const [forward, setForward] = useState(true)
  const [filters, setFilters] = useState<FilterRow[]>([])
  const [limit, setLimit] = useState("25")
  const [formErrors, setFormErrors] = useState<Record<string, string>>({})

  const ix = gsis.find((g) => g.name === index)
  const pkDef: KeyDef = ix ? ix.partition_key : table.partition_key
  const skDef: KeyDef | null | undefined = ix ? ix.sort_key : table.sort_key

  // ---- results ----
  const [req, setReq] = useState<Request | null>(null)
  const [starts, setStarts] = useState<(DynamoItem | undefined)[]>([undefined])
  const [page, setPage] = useState<PageResult | null>(null)
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState<unknown>(null)
  const [selected, setSelected] = useState<string[]>([])
  const seq = useRef(0)

  const [editing, setEditing] = useState<{ item: DynamoItem | null } | null>(null)
  const [deleting, setDeleting] = useState<DynamoItem[] | null>(null)

  const fetchPage = useCallback(
    async (r: Request, start: DynamoItem | undefined) => {
      const n = ++seq.current
      setLoading(true)
      setError(null)
      try {
        const res = await api.post<PageResult>(`${path}/${r.mode}`, { ...r.body, start_key: start })
        if (n !== seq.current) return false
        setPage(res)
        setSelected([])
        return true
      } catch (e) {
        if (n === seq.current) {
          setError(e)
          setPage(null)
        }
        return false
      } finally {
        if (n === seq.current) setLoading(false)
      }
    },
    [path],
  )

  const buildRequest = (): Request | null => {
    const e: Record<string, string> = {}
    const conds: Condition[] = []
    filters.forEach((f, i) => {
      if (!f.attr.trim()) {
        e[`f${i}attr`] = "Enter an attribute name"
        return
      }
      const c: Condition = { attr: f.attr.trim(), op: f.op }
      if (f.op !== "exists" && f.op !== "not_exists") {
        const v = coerce(f.type, f.value)
        if (v.error) e[`f${i}value`] = v.error
        c.value = v.value
        if (f.op === "between") {
          const v2 = coerce(f.type, f.value2)
          if (v2.error) e[`f${i}value2`] = v2.error
          c.value2 = v2.value
        }
      }
      conds.push(c)
    })
    const body: PageInput = { limit: Number(limit), filter: conds.length ? conds : undefined }
    let label = `Table: ${table.name}`
    let q: QueryInput | null = null
    if (mode === "query") {
      const p = parseKeyValue(pkDef.type, pv)
      if (p.error) e.pv = p.error
      q = { ...body, partition_value: p.value, index: index || undefined, forward }
      if (skDef && sortOp !== "none") {
        const a = parseKeyValue(skDef.type, sv)
        if (a.error) e.sv = a.error
        const sc: Condition = { op: sortOp, value: a.value }
        if (sortOp === "between") {
          const b = parseKeyValue(skDef.type, sv2)
          if (b.error) e.sv2 = b.error
          sc.value2 = b.value
        }
        q.sort_condition = sc
      }
      if (index) label = `Index: ${index}`
    }
    setFormErrors(e)
    if (Object.keys(e).length) return null
    return { mode, body: q ?? body, label }
  }

  const run = async () => {
    const r = buildRequest()
    if (!r) return
    setReq(r)
    setStarts([undefined])
    await fetchPage(r, undefined)
  }

  const current = starts[starts.length - 1]
  const reload = () => req && fetchPage(req, current)
  const next = async () => {
    if (!req || !page?.last_evaluated_key) return
    const k = page.last_evaluated_key
    if (await fetchPage(req, k)) setStarts([...starts, k])
  }
  const prev = async () => {
    if (!req || starts.length < 2) return
    const s = starts.slice(0, -1)
    if (await fetchPage(req, s[s.length - 1])) setStarts(s)
  }

  // Scan the first page when the tab opens.
  useEffect(() => {
    const r: Request = { mode: "scan", body: { limit: 25 }, label: `Table: ${table.name}` }
    setReq(r)
    fetchPage(r, undefined)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [table.name])

  const reset = () => {
    setIndex("")
    setPv("")
    setSortOp("none")
    setSv("")
    setSv2("")
    setForward(true)
    setFilters([])
    setLimit("25")
    setFormErrors({})
  }

  const setFilter = (i: number, patch: Partial<FilterRow>) => setFilters(filters.map((f, j) => (j === i ? { ...f, ...patch } : f)))

  // ---- result columns ----
  const items = useMemo(() => page?.items ?? [], [page])
  const columns = useMemo<Column<DynamoItem>[]>(() => {
    const keys = primaryKeys(table).map((k) => k.name)
    if (ix) for (const k of [ix.partition_key, ix.sort_key]) if (k && !keys.includes(k.name)) keys.push(k.name)
    const others: string[] = []
    for (const it of items) for (const a of Object.keys(it)) if (!keys.includes(a) && !others.includes(a)) others.push(a)
    const attrCol = (a: string, isKey: boolean): Column<DynamoItem> => ({
      id: `a:${a}`,
      header: <span className={cn(isKey && "text-foreground")}>{a}</span>,
      cell: (it) => <AttrValue value={it[a]} ttl={a === table.ttl_attribute} />,
      value: (it) => {
        const v = it[a]
        return typeof v === "number" || typeof v === "string" ? v : v === undefined ? "" : JSON.stringify(v)
      },
      className: isKey ? "font-medium" : undefined,
    })
    return [
      ...keys.map((k) => attrCol(k, true)),
      ...others.map((a) => attrCol(a, false)),
      {
        id: "actions",
        header: <span className="sr-only">Actions</span>,
        sortable: false,
        className: "w-px",
        cell: (it) => (
          <div className="flex items-center justify-end gap-0.5" onClick={(e) => e.stopPropagation()}>
            <Button variant="ghost" size="icon" className="size-7" onClick={() => setEditing({ item: it })} aria-label="Edit item" title="Edit item">
              <Pencil className="size-3.5" />
            </Button>
            <Button variant="ghost" size="icon" className="text-destructive hover:text-destructive size-7" onClick={() => setDeleting([it])} aria-label="Delete item" title="Delete item">
              <Trash2 className="size-3.5" />
            </Button>
          </div>
        ),
      },
    ]
  }, [items, table, ix])

  const selItems = items.filter((it) => selected.includes(keyId(table, it)))
  const pageNo = starts.length

  const afterWrite = async () => {
    await reload()
    await revalidate(TABLES_PATH)
  }

  return (
    <div className="flex flex-col gap-4">
      <Section title="Scan or query items">
        <form
          onSubmit={(e) => {
            e.preventDefault()
            run()
          }}
          className="flex flex-col gap-4"
        >
          <div className="bg-muted inline-flex w-fit rounded-md p-0.5 text-sm" role="tablist">
            {(["scan", "query"] as Mode[]).map((m) => (
              <button
                key={m}
                type="button"
                role="tab"
                aria-selected={mode === m}
                onClick={() => (setMode(m), setFormErrors({}))}
                className={cn(
                  "rounded px-4 py-1 font-medium capitalize transition-colors",
                  mode === m ? "bg-background text-foreground shadow-xs" : "text-muted-foreground hover:text-foreground",
                )}
              >
                {m}
              </button>
            ))}
          </div>

          {mode === "query" && (
            <div className="flex flex-col gap-4">
              <Field label="Table or index" htmlFor="q-index" help={<KeySchema pk={pkDef} sk={skDef} />}>
                <Select
                  value={index || "__table"}
                  onValueChange={(v) => {
                    setIndex(v === "__table" ? "" : v)
                    setPv("")
                    setSortOp("none")
                    setSv("")
                    setSv2("")
                  }}
                >
                  <SelectTrigger id="q-index" size="sm" className="w-full max-w-md">
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value="__table">Table: {table.name}</SelectItem>
                    {gsis.map((g) => (
                      <SelectItem key={g.name} value={g.name}>
                        Index: {g.name}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              </Field>
              <div className="grid grid-cols-1 gap-4 md:grid-cols-2">
                <Field
                  label={
                    <>
                      <span className="font-mono">{pkDef.name}</span> (partition key)
                    </>
                  }
                  htmlFor="q-pk"
                  error={formErrors.pv}
                >
                  <Input
                    id="q-pk"
                    value={pv}
                    onChange={(e) => setPv(e.target.value)}
                    placeholder={pkDef.type === "N" ? "Enter a number" : "Enter a string value"}
                    inputMode={pkDef.type === "N" ? "decimal" : undefined}
                    className="h-8"
                  />
                </Field>
                {skDef && (
                  <Field
                    label={
                      <>
                        <span className="font-mono">{skDef.name}</span> (sort key) <span className="text-muted-foreground font-normal">- optional</span>
                      </>
                    }
                    error={formErrors.sv || formErrors.sv2}
                  >
                    <div className="flex flex-wrap gap-2">
                      <Select value={sortOp} onValueChange={(v) => setSortOp(v as ConditionOp | "none")}>
                        <SelectTrigger size="sm" className="w-44" aria-label="Sort key condition">
                          <SelectValue />
                        </SelectTrigger>
                        <SelectContent>
                          {SORT_OPS.filter((o) => o.op !== "begins_with" || skDef.type === "S").map((o) => (
                            <SelectItem key={o.op} value={o.op}>
                              {o.label}
                            </SelectItem>
                          ))}
                        </SelectContent>
                      </Select>
                      {sortOp !== "none" && (
                        <Input
                          value={sv}
                          onChange={(e) => setSv(e.target.value)}
                          placeholder={skDef.type === "N" ? "Number" : "Value"}
                          aria-label="Sort key value"
                          className="h-8 w-32 min-w-0 flex-1"
                        />
                      )}
                      {sortOp === "between" && (
                        <>
                          <span className="text-muted-foreground self-center text-sm">and</span>
                          <Input
                            value={sv2}
                            onChange={(e) => setSv2(e.target.value)}
                            placeholder={skDef.type === "N" ? "Number" : "Value"}
                            aria-label="Sort key upper bound"
                            className="h-8 w-32 min-w-0 flex-1"
                          />
                        </>
                      )}
                    </div>
                  </Field>
                )}
              </div>
              <Field label="Sort order">
                <div className="bg-muted inline-flex w-fit rounded-md p-0.5 text-sm">
                  {[
                    [true, "Ascending"],
                    [false, "Descending"],
                  ].map(([f, label]) => (
                    <button
                      key={String(f)}
                      type="button"
                      aria-pressed={forward === f}
                      onClick={() => setForward(f as boolean)}
                      className={cn(
                        "rounded px-3 py-1 font-medium transition-colors",
                        forward === f ? "bg-background text-foreground shadow-xs" : "text-muted-foreground hover:text-foreground",
                      )}
                    >
                      {label}
                    </button>
                  ))}
                </div>
              </Field>
            </div>
          )}

          <div className="flex flex-col gap-2">
            <span className="text-sm font-medium">
              Filters <span className="text-muted-foreground font-normal">- optional, applied after items are read</span>
            </span>
            {filters.map((f, i) => {
              const noValue = f.op === "exists" || f.op === "not_exists"
              return (
                <div key={i} className="flex flex-wrap items-start gap-2 rounded-md border p-2">
                  <div className="flex min-w-36 flex-1 flex-col gap-1">
                    <Input value={f.attr} onChange={(e) => setFilter(i, { attr: e.target.value })} placeholder="Attribute name" aria-label="Attribute name" className="h-8 font-mono" />
                    {formErrors[`f${i}attr`] && <span className="text-destructive text-xs">{formErrors[`f${i}attr`]}</span>}
                  </div>
                  <Select value={f.op} onValueChange={(v) => setFilter(i, { op: v as ConditionOp })}>
                    <SelectTrigger size="sm" className="w-44" aria-label="Condition">
                      <SelectValue />
                    </SelectTrigger>
                    <SelectContent>
                      {FILTER_OPS.map((o) => (
                        <SelectItem key={o.op} value={o.op}>
                          {o.label}
                        </SelectItem>
                      ))}
                    </SelectContent>
                  </Select>
                  {!noValue && (
                    <>
                      <Select value={f.type} onValueChange={(v) => setFilter(i, { type: v as ValueType, value: v === "BOOL" ? "true" : f.value, value2: "" })}>
                        <SelectTrigger size="sm" className="w-28" aria-label="Value type">
                          <SelectValue />
                        </SelectTrigger>
                        <SelectContent>
                          <SelectItem value="S">String</SelectItem>
                          <SelectItem value="N">Number</SelectItem>
                          <SelectItem value="BOOL">Boolean</SelectItem>
                        </SelectContent>
                      </Select>
                      <FilterValue type={f.type} value={f.value} onChange={(v) => setFilter(i, { value: v })} error={formErrors[`f${i}value`]} label="Value" />
                      {f.op === "between" && (
                        <FilterValue type={f.type} value={f.value2} onChange={(v) => setFilter(i, { value2: v })} error={formErrors[`f${i}value2`]} label="And" />
                      )}
                    </>
                  )}
                  <Button type="button" variant="ghost" size="icon" className="size-8" onClick={() => setFilters(filters.filter((_, j) => j !== i))} aria-label="Remove filter">
                    <X />
                  </Button>
                </div>
              )
            })}
            <div>
              <Button type="button" variant="outline" size="sm" onClick={() => setFilters([...filters, emptyFilter()])} disabled={filters.length >= 10}>
                <Plus /> Add filter
              </Button>
            </div>
          </div>

          <div className="flex flex-wrap items-end gap-3 border-t pt-4">
            <Field label="Page size" htmlFor="q-limit">
              <Select value={limit} onValueChange={setLimit}>
                <SelectTrigger id="q-limit" size="sm" className="w-28">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {PAGE_SIZES.map((s) => (
                    <SelectItem key={s} value={s}>
                      {s}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </Field>
            <div className="flex gap-2">
              <Button type="submit" size="sm" disabled={loading}>
                {loading ? <Loader2 className="animate-spin" /> : <Play />}
                Run
              </Button>
              <Button type="button" variant="outline" size="sm" onClick={reset}>
                Reset
              </Button>
            </div>
          </div>
          {table.ttl_attribute && (
            <p className="text-muted-foreground text-xs">
              TTL is enabled on <span className="font-mono">{table.ttl_attribute}</span>: expired items are not returned.
            </p>
          )}
        </form>
      </Section>

      <DataTable
        title={req ? `Items returned · ${req.mode === "query" ? "Query" : "Scan"} (${req.label})` : "Items returned"}
        description={
          page ? (
            <>
              {pluralize(page.count, "item")} returned, {formatNumber(page.scanned_count)} scanned · page {pageNo}
              {page.last_evaluated_key ? " · more items available" : ""}
            </>
          ) : undefined
        }
        count={page?.count}
        data={page ? items : undefined}
        columns={columns}
        rowId={(it) => keyId(table, it)}
        loading={loading}
        error={error}
        onRetry={reload}
        onRefresh={reload}
        refreshing={loading}
        selection="multi"
        selected={selected}
        onSelectedChange={setSelected}
        noSearch
        pageSize={1000}
        actions={
          <>
            <Button variant="outline" size="sm" disabled={!selItems.length} onClick={() => setDeleting(selItems)}>
              <Trash2 /> Delete{selItems.length ? ` (${selItems.length})` : ""}
            </Button>
            <Button size="sm" onClick={() => setEditing({ item: null })}>
              <Plus /> Create item
            </Button>
            <div className="flex items-center gap-1">
              <Button variant="outline" size="icon" className="size-8" disabled={loading || starts.length < 2} onClick={prev} aria-label="Previous page">
                <ChevronLeft />
              </Button>
              <span className="text-muted-foreground min-w-6 text-center text-sm tabular-nums">{pageNo}</span>
              <Button variant="outline" size="icon" className="size-8" disabled={loading || !page?.last_evaluated_key} onClick={next} aria-label="Next page">
                <ChevronRight />
              </Button>
            </div>
          </>
        }
        empty={
          <EmptyState
            title="No items returned"
            description={
              page && page.scanned_count > 0
                ? `${pluralize(page.scanned_count, "item")} were read but none matched. Adjust the key condition or filters${page.last_evaluated_key ? ", or open the next page" : ""}.`
                : req?.mode === "query"
                  ? "No items have this partition key value."
                  : "This table has no items yet."
            }
            action={
              <Button size="sm" variant="outline" onClick={() => setEditing({ item: null })}>
                <Plus /> Create item
              </Button>
            }
          />
        }
      />

      <ItemEditorDialog table={table} state={editing} onClose={() => setEditing(null)} onSaved={afterWrite} />
      <ConfirmDialog
        open={!!deleting}
        onOpenChange={(o) => !o && setDeleting(null)}
        title={deleting?.length === 1 ? "Delete item?" : `Delete ${pluralize(deleting?.length ?? 0, "item")}?`}
        description="Deleted items cannot be recovered."
        actionLabel="Delete"
        onConfirm={async () => {
          const list = deleting ?? []
          if (list.length === 1) {
            await api.post(`${path}/items/delete`, { key: itemKey(table, list[0]) })
            toast.success("Deleted item")
          } else {
            await api.post(`${path}/batch-write`, { deletes: list.map((it) => itemKey(table, it)) })
            toast.success(`Deleted ${pluralize(list.length, "item")}`)
          }
          await afterWrite()
        }}
      >
        {deleting && (
          <ul className="bg-muted/40 max-h-40 overflow-y-auto rounded-md border p-2 font-mono text-[12.5px]">
            {deleting.map((it) => (
              <li key={keyId(table, it)} className="truncate py-0.5">
                {compactJson(itemKey(table, it), 120)}
              </li>
            ))}
          </ul>
        )}
      </ConfirmDialog>
    </div>
  )
}

function FilterValue({ type, value, onChange, error, label }: { type: ValueType; value: string; onChange: (v: string) => void; error?: string; label: string }) {
  if (type === "BOOL") {
    return (
      <Select value={value || "true"} onValueChange={onChange}>
        <SelectTrigger size="sm" className="w-24" aria-label={label}>
          <SelectValue />
        </SelectTrigger>
        <SelectContent>
          <SelectItem value="true">true</SelectItem>
          <SelectItem value="false">false</SelectItem>
        </SelectContent>
      </Select>
    )
  }
  return (
    <div className="flex min-w-28 flex-1 flex-col gap-1">
      <Input
        value={value}
        onChange={(e) => onChange(e.target.value)}
        placeholder={type === "N" ? "Number" : label === "And" ? "Upper bound" : "Value"}
        inputMode={type === "N" ? "decimal" : undefined}
        aria-label={label}
        className="h-8"
      />
      {error && <span className="text-destructive text-xs">{error}</span>}
    </div>
  )
}

const MAX_ITEM_BYTES = 400 * 1024

/**
 * ItemEditorDialog creates an item (state.item null) or replaces an existing
 * one. The whole document is written with PutItem.
 */
export function ItemEditorDialog({
  table,
  state,
  onClose,
  onSaved,
}: {
  table: DynamoTable
  state: { item: DynamoItem | null } | null
  onClose: () => void
  onSaved: () => Promise<unknown> | void
}) {
  const [text, setText] = useState("")
  const [pending, setPending] = useState(false)
  const original = state?.item ?? null

  useEffect(() => {
    if (state) setText(JSON.stringify(state.item ?? keySkeleton(table), null, 2))
    // Only reset when the dialog opens: the table object is re-fetched periodically.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [state])

  const validate = useCallback(
    (v: unknown) => {
      if (!v || typeof v !== "object" || Array.isArray(v)) return "An item must be a JSON object"
      return keyError(table, v as DynamoItem)
    },
    [table],
  )

  const parsed = useMemo(() => {
    if (jsonError(text)) return null
    const v = JSON.parse(text) as unknown
    return validate(v) ? null : (v as DynamoItem)
  }, [text, validate])
  const keyChanged = !!original && !!parsed && keyId(table, parsed) !== keyId(table, original)
  const tooBig = new Blob([text]).size > MAX_ITEM_BYTES

  const save = async (e: React.FormEvent) => {
    e.preventDefault()
    if (!parsed || tooBig) return
    const isNew = !original || keyChanged
    setPending(true)
    try {
      await api.post(`${TABLES_PATH}/${seg(table.name)}/items`, {
        item: parsed,
        // Never silently overwrite a different item when creating or re-keying.
        condition: isNew ? [{ attr: table.partition_key.name, op: "not_exists" }] : undefined,
      })
      toast.success(isNew ? "Created item" : "Saved item")
      onClose()
      await onSaved()
    } catch (err) {
      if (err instanceof ApiError && err.code === "ConditionalCheckFailedException") toast.error("An item with this key already exists. Edit that item instead.")
      else toast.error(errorMessage(err))
    } finally {
      setPending(false)
    }
  }

  return (
    <Dialog open={!!state} onOpenChange={(o) => !o && !pending && onClose()}>
      <DialogContent className="max-h-[92vh] overflow-y-auto sm:max-w-2xl">
        <form onSubmit={save} className="flex min-w-0 flex-col gap-4">
          <DialogHeader>
            <DialogTitle>{original ? "Edit item" : "Create item"}</DialogTitle>
            <DialogDescription>
              Items are JSON documents. The key attributes <KeySchema pk={table.partition_key} sk={table.sort_key} className="inline-flex" /> are required
              {primaryKeys(table).some((k) => k.type === "N") ? "; Number keys must be JSON numbers (no quotes)" : ""}.
            </DialogDescription>
          </DialogHeader>
          <JsonEditor value={text} onChange={setText} rows={14} validate={validate} />
          {keyChanged && (
            <p className="flex gap-2 rounded-md border border-amber-600/30 bg-amber-50 p-3 text-sm text-amber-800 dark:border-amber-400/30 dark:bg-amber-500/10 dark:text-amber-300">
              <AlertTriangle className="mt-0.5 size-4 shrink-0" />
              <span>
                You changed a key attribute. Saving creates a new item with key <span className="font-mono">{compactJson(itemKey(table, parsed!), 80)}</span> and
                keeps the original item <span className="font-mono">{compactJson(itemKey(table, original!), 80)}</span>.
              </span>
            </p>
          )}
          {tooBig && <p className="text-destructive text-xs">Items are limited to 400 KB.</p>}
          <DialogFooter>
            <Button type="button" variant="outline" onClick={onClose} disabled={pending}>
              Cancel
            </Button>
            <Button type="submit" disabled={pending || !parsed || tooBig}>
              {pending && <Loader2 className="animate-spin" />}
              {original && !keyChanged ? "Save changes" : "Create item"}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}
