"use client"

import { useEffect, useMemo, useState, type ReactNode } from "react"
import Link from "next/link"
import { ArrowDown, ArrowUp, ChevronLeft, ChevronRight, ChevronsUpDown, RefreshCw, Rows2, Rows3, Search, X } from "lucide-react"

import { Button } from "@/components/ui/button"
import { Checkbox } from "@/components/ui/checkbox"
import { Input } from "@/components/ui/input"
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip"
import { EmptyState } from "@/components/console/empty-state"
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
  const [density, toggleDensity] = useDensity()
  const compact = density === "compact"
  const colSpan = columns.length + (selection !== "none" ? 1 : 0)
  // Long pages scroll inside the card so the column header can stay pinned.
  const pinned = visible.length > 15
  const first = sorted.length ? page * pageSize + 1 : 0
  const last = Math.min(sorted.length, (page + 1) * pageSize)

  return (
    <div className={cn("bg-card text-card-foreground overflow-hidden rounded-xl border shadow-xs", className)}>
      {showHeader && (
        <div className="flex flex-col gap-3 px-5 pt-4 sm:flex-row sm:items-center sm:justify-between">
          <div className="min-w-0">
            {title && (
              <h2 className="flex items-center gap-2 text-[15px] font-semibold tracking-[-0.015em]">
                {title}
                {data && (
                  <span className="bg-muted text-muted-foreground rounded-full border px-1.5 py-px font-mono text-[11px] font-medium tabular-nums">{total}</span>
                )}
              </h2>
            )}
            {description && <p className="text-muted-foreground mt-0.5 text-[13px]">{description}</p>}
          </div>
          <div className="flex shrink-0 flex-wrap items-center gap-2">
            {onRefresh && (
              <Tooltip>
                <TooltipTrigger asChild>
                  <Button variant="outline" size="icon-sm" onClick={onRefresh} aria-label="Refresh">
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
        <div className="flex flex-col gap-2 px-5 pt-3 pb-3 md:flex-row md:items-center">
          {!noSearch && (
            <div className="relative w-full md:max-w-sm">
              <Search className="text-faint pointer-events-none absolute top-1/2 left-2.5 size-4 -translate-y-1/2" />
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
          {rows.length > 0 && (
            <Tooltip>
              <TooltipTrigger asChild>
                <Button
                  variant="ghost"
                  size="icon-sm"
                  className="hidden md:ml-auto md:inline-flex"
                  onClick={toggleDensity}
                  aria-label={compact ? "Comfortable rows" : "Compact rows"}
                  aria-pressed={compact}
                >
                  {compact ? <Rows3 /> : <Rows2 />}
                </Button>
              </TooltipTrigger>
              <TooltipContent>{compact ? "Comfortable rows" : "Compact rows"}</TooltipContent>
            </Tooltip>
          )}
        </div>
      )}
      {!showHeader && noSearch && !filters && <div className="h-1" />}
      {error ? (
        <div className="px-5 pb-5">
          <ErrorState error={error} onRetry={onRetry ?? onRefresh} />
        </div>
      ) : loading && !data ? (
        <div className="border-t">
          <TableSkeleton cols={Math.min(columns.length, 5)} />
        </div>
      ) : rows.length === 0 ? (
        <div className="border-t">{empty ?? <EmptyState title="No resources yet" className="py-10" />}</div>
      ) : (
        <>
          <div className={cn("relative w-full overflow-x-auto border-t", pinned && "max-h-[min(70vh,880px)] overflow-y-auto")}>
            <table className="w-full text-sm">
              <thead className={cn(pinned && "sticky top-0 z-10")}>
                <tr className="bg-muted/80 shadow-[inset_0_-1px_0_var(--border)] backdrop-blur supports-[backdrop-filter]:bg-muted/70">
                  {selection !== "none" && (
                    <th className="w-10 py-2.5 pr-2 pl-5 text-left">
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
                        aria-sort={active ? (sort?.desc ? "descending" : "ascending") : undefined}
                        className={cn(
                          "text-faint h-9 px-3 text-left font-mono text-[11px] font-medium tracking-[0.06em] whitespace-nowrap uppercase first:pl-5 last:pr-5",
                          c.hideBelow && HIDE[c.hideBelow],
                          c.headerClassName,
                        )}
                      >
                        {sortable ? (
                          <button
                            type="button"
                            onClick={() => onSort(c)}
                            className={cn("hover:text-foreground group/sort inline-flex items-center gap-1 uppercase", active && "text-foreground")}
                          >
                            {c.header}
                            {active ? (
                              sort?.desc ? <ArrowDown className="size-3" /> : <ArrowUp className="size-3" />
                            ) : (
                              <ChevronsUpDown className="size-3 opacity-0 transition-opacity group-hover/sort:opacity-60" />
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
                    <td colSpan={colSpan} className="text-muted-foreground p-10 text-center">
                      No matches for “{query}”
                      <button type="button" onClick={() => setQuery("")} className="text-foreground ml-2 underline underline-offset-2">
                        Clear filter
                      </button>
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
                          isSel ? "bg-brand-soft" : "hover:bg-muted/60",
                          rowClassName?.(r),
                        )}
                      >
                        {selection !== "none" && (
                          <td className={cn("relative w-10 pr-2 pl-5", compact ? "py-1.5" : "py-2.5")} onClick={(e) => e.stopPropagation()}>
                            {isSel && <span className="bg-brand absolute inset-y-0 left-0 w-0.5" aria-hidden />}
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
                            className={cn(
                              "px-3 align-middle first:pl-5 last:pr-5 [&_a.font-mono]:whitespace-nowrap",
                              compact ? "py-1.5 text-[13px]" : "py-2.5",
                              c.hideBelow && HIDE[c.hideBelow],
                              c.className,
                            )}
                          >
                            {c.cell(r)}
                          </td>
                        ))}
                      </tr>
                      {ex && (
                        <tr className="bg-muted/40 border-b">
                          <td colSpan={colSpan} className="px-5 py-3">
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
          {pages > 1 && (
            <div className="text-muted-foreground flex items-center justify-between gap-3 border-t px-5 py-2.5 text-xs">
              <span className="tabular-nums">
                {first}–{last} of {sorted.length}
              </span>
              <div className="flex items-center gap-1">
                <Button variant="outline" size="xs" disabled={page === 0} onClick={() => setPage(page - 1)} aria-label="Previous page">
                  <ChevronLeft /> Prev
                </Button>
                <span className="px-2 font-mono tabular-nums">
                  {page + 1}/{pages}
                </span>
                <Button variant="outline" size="xs" disabled={page >= pages - 1} onClick={() => setPage(page + 1)} aria-label="Next page">
                  Next <ChevronRight />
                </Button>
              </div>
            </div>
          )}
        </>
      )}
    </div>
  )
}

function RowFragment({ children }: { children: ReactNode }) {
  return <>{children}</>
}

const DENSITY_KEY = "hc.table.density"
const densityListeners = new Set<(d: "comfortable" | "compact") => void>()

/** useDensity is the table row density, remembered per browser and shared by every table. */
function useDensity(): ["comfortable" | "compact", () => void] {
  const [d, setD] = useState<"comfortable" | "compact">("comfortable")
  useEffect(() => {
    try {
      if (window.localStorage.getItem(DENSITY_KEY) === "compact") setD("compact")
    } catch {
      // storage unavailable: default density
    }
    densityListeners.add(setD)
    return () => {
      densityListeners.delete(setD)
    }
  }, [])
  const toggle = () => {
    const next = d === "compact" ? "comfortable" : "compact"
    try {
      window.localStorage.setItem(DENSITY_KEY, next)
    } catch {
      // ignore
    }
    densityListeners.forEach((l) => l(next))
  }
  return [d, toggle]
}

/** cellLinkClass styles a resource link inside a table cell. */
export function cellLinkClass() {
  return "text-primary font-medium underline-offset-2 hover:underline"
}

/**
 * CellLink is the name/ID link of a table row: one line, truncated with the
 * full value in a tooltip, mono for IDs/ARNs. Clicks don't toggle selection.
 */
export function CellLink({
  href,
  children,
  title,
  mono,
  max = "22rem",
  className,
}: {
  href: string
  children: ReactNode
  title?: string
  mono?: boolean
  /** max width before truncating (CSS length) */
  max?: string
  className?: string
}) {
  return (
    <Link
      href={href}
      onClick={(e) => e.stopPropagation()}
      title={title ?? (typeof children === "string" ? children : undefined)}
      style={{ maxWidth: max }}
      className={cn(cellLinkClass(), "block truncate whitespace-nowrap", mono && "font-mono text-[13px]", className)}
    >
      {children}
    </Link>
  )
}

/** CellText is a one-line, truncated table value with the full text as a tooltip. */
export function CellText({
  children,
  title,
  mono,
  muted,
  max = "22rem",
  className,
}: {
  children: ReactNode
  title?: string
  mono?: boolean
  muted?: boolean
  max?: string
  className?: string
}) {
  if (children === null || children === undefined || children === "") return <span className="text-muted-foreground">-</span>
  return (
    <span
      title={title ?? (typeof children === "string" ? children : undefined)}
      style={{ maxWidth: max }}
      className={cn("block truncate whitespace-nowrap", mono && "font-mono text-[13px]", muted && "text-muted-foreground", className)}
    >
      {children}
    </span>
  )
}
