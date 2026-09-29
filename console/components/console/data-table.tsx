"use client"

import { useEffect, useMemo, useState, type ReactNode } from "react"
import { ArrowDown, ArrowUp, ChevronLeft, ChevronRight, ChevronsUpDown, RefreshCw, Search, X } from "lucide-react"

import { Button } from "@/components/ui/button"
import { Checkbox } from "@/components/ui/checkbox"
import { Input } from "@/components/ui/input"
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip"
import { ErrorState } from "@/components/console/error-state"
import { TableSkeleton } from "@/components/console/loading"
import { cn } from "@/lib/utils"

export interface Column<T> {
  id: string
  header: ReactNode
  cell: (row: T) => ReactNode
  /** Plain value used for sorting and for the text filter. */
  value?: (row: T) => string | number | boolean | null | undefined
  /** Defaults to true when `value` is set. */
  sortable?: boolean
  className?: string
  headerClassName?: string
  /** Hide the column on narrow screens. */
  hideBelow?: "sm" | "md" | "lg"
}

export interface DataTableProps<T> {
  title?: ReactNode
  description?: ReactNode
  /** Shown as "(n)" after the title; defaults to the row count. */
  count?: number
  data: T[] | undefined
  columns: Column<T>[]
  rowId: (row: T) => string
  loading?: boolean
  error?: unknown
  onRetry?: () => void
  selection?: "single" | "multi" | "none"
  selected?: string[]
  onSelectedChange?: (ids: string[]) => void
  /** Toolbar buttons (Actions menu, Create ...), right of the refresh button. */
  actions?: ReactNode
  onRefresh?: () => void
  refreshing?: boolean
  searchPlaceholder?: string
  /** Custom text filter; defaults to matching any column value. */
  filter?: (row: T, query: string) => boolean
  /** Extra filter controls shown next to the search box. */
  filters?: ReactNode
  /** Hide the search box. */
  noSearch?: boolean
  empty?: ReactNode
  pageSize?: number
  defaultSort?: { id: string; desc?: boolean }
  onRowClick?: (row: T) => void
  rowClassName?: (row: T) => string | undefined
  /** Render an expanded detail row under a row. */
  expanded?: (row: T) => ReactNode | null
  className?: string
}

const HIDE = { sm: "hidden sm:table-cell", md: "hidden md:table-cell", lg: "hidden lg:table-cell" }

function norm(v: unknown): string {
  if (v === null || v === undefined) return ""
  return String(v).toLowerCase()
}

/**
 * DataTable is the console's resource list: header with count, refresh and
 * actions, a text filter, sortable columns, row selection and pagination.
 */
export function DataTable<T>({
  title,
  description,
  count,
  data,
  columns,
  rowId,
  loading,
  error,
  onRetry,
  selection = "none",
  selected: selectedProp,
  onSelectedChange,
  actions,
  onRefresh,
  refreshing,
  searchPlaceholder = "Filter resources",
  filter,
  filters,
  noSearch,
  empty,
  pageSize = 25,
  defaultSort,
  onRowClick,
  rowClassName,
  expanded,
  className,
}: DataTableProps<T>) {
  const [query, setQuery] = useState("")
  const [sort, setSort] = useState<{ id: string; desc?: boolean } | undefined>(defaultSort)
  const [page, setPage] = useState(0)
  const [innerSelected, setInnerSelected] = useState<string[]>([])
  const selected = selectedProp ?? innerSelected
  const setSelected = (ids: string[]) => {
    if (onSelectedChange) onSelectedChange(ids)
    if (selectedProp === undefined) setInnerSelected(ids)
  }

  const rows = useMemo(() => data ?? [], [data])

  const filtered = useMemo(() => {
    const q = query.trim().toLowerCase()
    if (!q) return rows
    return rows.filter((r) => (filter ? filter(r, q) : columns.some((c) => c.value && norm(c.value(r)).includes(q))))
  }, [rows, query, filter, columns])

  const sorted = useMemo(() => {
    if (!sort) return filtered
    const col = columns.find((c) => c.id === sort.id)
    if (!col?.value) return filtered
    const out = [...filtered]
    out.sort((a, b) => {
      const va = col.value!(a)
      const vb = col.value!(b)
      let r: number
      if (typeof va === "number" && typeof vb === "number") r = va - vb
      else r = norm(va).localeCompare(norm(vb), undefined, { numeric: true })
      return sort.desc ? -r : r
    })
    return out
  }, [filtered, sort, columns])

  const pages = Math.max(1, Math.ceil(sorted.length / pageSize))
  useEffect(() => {
    if (page >= pages) setPage(pages - 1)
  }, [page, pages])
  const visible = sorted.slice(page * pageSize, page * pageSize + pageSize)

  // Drop selections for rows that disappeared (deleted, filtered by the server).
  const ids = useMemo(() => new Set(rows.map(rowId)), [rows, rowId])
  useEffect(() => {
    if (!data) return
    const kept = selected.filter((id) => ids.has(id))
    if (kept.length !== selected.length) setSelected(kept)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [ids])

  const toggle = (id: string) => {
    if (selection === "single") setSelected(selected[0] === id ? [] : [id])
    else setSelected(selected.includes(id) ? selected.filter((x) => x !== id) : [...selected, id])
  }
  const visibleIds = visible.map(rowId)
  const allVisibleSelected = visibleIds.length > 0 && visibleIds.every((id) => selected.includes(id))
  const someVisibleSelected = visibleIds.some((id) => selected.includes(id))

  const onSort = (c: Column<T>) => {
    if (!(c.sortable ?? !!c.value)) return
    setSort((s) => (s?.id === c.id ? (s.desc ? undefined : { id: c.id, desc: true }) : { id: c.id }))
  }

  const total = count ?? rows.length
  const showHeader = title || actions || onRefresh

  return (
    <div className={cn("bg-card text-card-foreground rounded-lg border shadow-xs", className)}>
      {showHeader && (
        <div className="flex flex-col gap-3 px-4 pt-4 sm:flex-row sm:items-start sm:justify-between">
          <div className="min-w-0">
            {title && (
              <h2 className="text-base font-semibold">
                {title}{" "}
                {data && <span className="text-muted-foreground font-normal">({total})</span>}
              </h2>
            )}
            {description && <p className="text-muted-foreground mt-0.5 text-sm">{description}</p>}
          </div>
          <div className="flex shrink-0 flex-wrap items-center gap-2">
            {onRefresh && (
              <Tooltip>
                <TooltipTrigger asChild>
                  <Button variant="outline" size="icon" className="size-8" onClick={onRefresh} aria-label="Refresh">
                    <RefreshCw className={cn(refreshing && "animate-spin")} />
                  </Button>
                </TooltipTrigger>
                <TooltipContent>Refresh</TooltipContent>
              </Tooltip>
            )}
            {actions}
          </div>
        </div>
      )}
      {(!noSearch || filters) && (
        <div className="flex flex-col gap-2 px-4 pt-3 pb-3 md:flex-row md:items-center">
          {!noSearch && (
            <div className="relative w-full md:max-w-sm">
              <Search className="text-muted-foreground pointer-events-none absolute top-1/2 left-2.5 size-4 -translate-y-1/2" />
              <Input
                value={query}
                onChange={(e) => {
                  setQuery(e.target.value)
                  setPage(0)
                }}
                placeholder={searchPlaceholder}
                className="h-8 pr-8 pl-8"
              />
              {query && (
                <button
                  type="button"
                  aria-label="Clear filter"
                  onClick={() => setQuery("")}
                  className="text-muted-foreground hover:text-foreground absolute top-1/2 right-2 -translate-y-1/2"
                >
                  <X className="size-4" />
                </button>
              )}
            </div>
          )}
          {filters && <div className="flex flex-wrap items-center gap-2">{filters}</div>}
          <div className="text-muted-foreground flex items-center gap-1 text-sm md:ml-auto">
            {pages > 1 && (
              <>
                <Button variant="ghost" size="icon" className="size-7" disabled={page === 0} onClick={() => setPage(page - 1)} aria-label="Previous page">
                  <ChevronLeft />
                </Button>
                <span className="tabular-nums">
                  {page + 1} / {pages}
                </span>
                <Button variant="ghost" size="icon" className="size-7" disabled={page >= pages - 1} onClick={() => setPage(page + 1)} aria-label="Next page">
                  <ChevronRight />
                </Button>
              </>
            )}
          </div>
        </div>
      )}
      {!showHeader && noSearch && !filters && <div className="h-1" />}
      {error ? (
        <div className="px-4 pb-4">
          <ErrorState error={error} onRetry={onRetry ?? onRefresh} />
        </div>
      ) : loading && !data ? (
        <div className="border-t">
          <TableSkeleton cols={Math.min(columns.length, 5)} />
        </div>
      ) : rows.length === 0 ? (
        <div className="border-t">{empty ?? <p className="text-muted-foreground p-8 text-center text-sm">No resources</p>}</div>
      ) : (
        <div className="relative w-full overflow-x-auto border-t">
          <table className="w-full text-sm">
            <thead>
              <tr className="bg-muted/40 border-b">
                {selection !== "none" && (
                  <th className="w-10 px-4 py-2 text-left">
                    {selection === "multi" && (
                      <Checkbox
                        aria-label="Select all"
                        checked={allVisibleSelected ? true : someVisibleSelected ? "indeterminate" : false}
                        onCheckedChange={(v) =>
                          setSelected(v ? Array.from(new Set([...selected, ...visibleIds])) : selected.filter((id) => !visibleIds.includes(id)))
                        }
                      />
                    )}
                  </th>
                )}
                {columns.map((c) => {
                  const sortable = c.sortable ?? !!c.value
                  const active = sort?.id === c.id
                  return (
                    <th
                      key={c.id}
                      className={cn(
                        "text-muted-foreground px-3 py-2 text-left text-xs font-semibold tracking-wide whitespace-nowrap first:pl-4 last:pr-4",
                        c.hideBelow && HIDE[c.hideBelow],
                        c.headerClassName,
                      )}
                    >
                      {sortable ? (
                        <button type="button" onClick={() => onSort(c)} className="hover:text-foreground inline-flex items-center gap-1">
                          {c.header}
                          {active ? (
                            sort?.desc ? <ArrowDown className="size-3" /> : <ArrowUp className="size-3" />
                          ) : (
                            <ChevronsUpDown className="size-3 opacity-40" />
                          )}
                        </button>
                      ) : (
                        c.header
                      )}
                    </th>
                  )
                })}
              </tr>
            </thead>
            <tbody>
              {visible.length === 0 && (
                <tr>
                  <td colSpan={columns.length + (selection !== "none" ? 1 : 0)} className="text-muted-foreground p-8 text-center">
                    No matches for “{query}”
                  </td>
                </tr>
              )}
              {visible.map((r) => {
                const id = rowId(r)
                const isSel = selected.includes(id)
                const ex = expanded?.(r)
                return (
                  <RowFragment key={id}>
                    <tr
                      data-state={isSel ? "selected" : undefined}
                      onClick={() => {
                        if (onRowClick) onRowClick(r)
                        else if (selection !== "none") toggle(id)
                      }}
                      className={cn(
                        "border-b transition-colors last:border-0",
                        (selection !== "none" || onRowClick) && "cursor-pointer",
                        isSel ? "bg-primary/5 dark:bg-primary/10" : "hover:bg-muted/40",
                        rowClassName?.(r),
                      )}
                    >
                      {selection !== "none" && (
                        <td className="w-10 px-4 py-2" onClick={(e) => e.stopPropagation()}>
                          {selection === "multi" ? (
                            <Checkbox aria-label={`Select ${id}`} checked={isSel} onCheckedChange={() => toggle(id)} />
                          ) : (
                            <input
                              type="radio"
                              aria-label={`Select ${id}`}
                              className="accent-primary size-4 cursor-pointer align-middle"
                              checked={isSel}
                              onChange={() => toggle(id)}
                              onClick={() => isSel && setSelected([])}
                            />
                          )}
                        </td>
                      )}
                      {columns.map((c) => (
                        <td
                          key={c.id}
                          className={cn("px-3 py-2 align-middle first:pl-4 last:pr-4 [&_a.font-mono]:whitespace-nowrap", c.hideBelow && HIDE[c.hideBelow], c.className)}
                        >
                          {c.cell(r)}
                        </td>
                      ))}
                    </tr>
                    {ex && (
                      <tr className="bg-muted/30 border-b">
                        <td colSpan={columns.length + (selection !== "none" ? 1 : 0)} className="px-4 py-3">
                          {ex}
                        </td>
                      </tr>
                    )}
                  </RowFragment>
                )
              })}
            </tbody>
          </table>
        </div>
      )}
    </div>
  )
}

function RowFragment({ children }: { children: ReactNode }) {
  return <>{children}</>
}

/** TableLink is a primary-colored link for use inside table cells. */
export function cellLinkClass() {
  return "text-primary font-medium hover:underline"
}
