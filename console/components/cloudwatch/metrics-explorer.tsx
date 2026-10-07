"use client"

import { useEffect, useMemo, useState } from "react"
import { Activity, ChevronLeft, Info, LineChart, Search, X } from "lucide-react"

import { Alert, AlertDescription } from "@/components/ui/alert"
import { Button } from "@/components/ui/button"
import { Checkbox } from "@/components/ui/checkbox"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select"
import { Skeleton } from "@/components/ui/skeleton"
import { Switch } from "@/components/ui/switch"
import { EmptyState } from "@/components/console/empty-state"
import { ErrorState } from "@/components/console/error-state"
import { MetricChart, SERIES_COLORS, type ChartQuery } from "@/components/console/metric-chart"
import { PageHeader } from "@/components/console/page-header"
import { Section } from "@/components/console/section"
import { useApi, useQueryParam, useSetQueryParam } from "@/lib/hooks"
import type { MetricSeries, Statistic } from "@/lib/types"
import { cn } from "@/lib/utils"

import { dimsText, friendlyDims, PERIODS, RANGES, STATISTICS, useInstanceNames } from "./common"

const MAX_SERIES = 5

const keyOf = (s: MetricSeries) => `${s.namespace}|${s.name}|${dimsText(s.dimensions)}`

export function MetricsExplorer() {
  const nsParam = useQueryParam("namespace")
  const setParam = useSetQueryParam()
  const { data, error, isLoading, mutate } = useApi<MetricSeries[]>("/api/v1/cloudwatch/metrics", { refreshInterval: 60_000 })
  const names = useInstanceNames()

  const [search, setSearch] = useState("")
  const [selected, setSelected] = useState<MetricSeries[]>([])
  const [range, setRange] = useState(60)
  const [period, setPeriod] = useState(60)
  const [stat, setStat] = useState<Statistic>("Average")
  const [auto, setAuto] = useState(true)

  const namespaces = useMemo(() => {
    const m = new Map<string, number>()
    for (const s of data ?? []) m.set(s.namespace, (m.get(s.namespace) ?? 0) + 1)
    return [...m.entries()].sort(([a], [b]) => a.localeCompare(b))
  }, [data])

  const ns = nsParam
  const inNs = useMemo(() => {
    const q = search.trim().toLowerCase()
    return (data ?? [])
      .filter((s) => (ns ? s.namespace === ns : !!q))
      .filter((s) => !q || `${s.namespace} ${s.name} ${dimsText(s.dimensions)} ${friendlyDims(s.dimensions, names)}`.toLowerCase().includes(q))
  }, [data, ns, search, names])

  const grouped = useMemo(() => {
    const m = new Map<string, MetricSeries[]>()
    for (const s of inNs) {
      const k = ns ? s.name : `${s.namespace} / ${s.name}`
      m.set(k, [...(m.get(k) ?? []), s])
    }
    return [...m.entries()].sort(([a], [b]) => a.localeCompare(b))
  }, [inNs, ns])

  // Preselect the first CPU series when arriving from a service page.
  useEffect(() => {
    if (!data || selected.length || !ns) return
    const first = data.filter((s) => s.namespace === ns && s.name === "CPUUtilization").slice(0, MAX_SERIES)
    if (first.length) setSelected(first)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [data, ns])

  const selKeys = new Set(selected.map(keyOf))
  const full = selected.length >= MAX_SERIES
  const toggle = (s: MetricSeries) => {
    const k = keyOf(s)
    if (selKeys.has(k)) setSelected(selected.filter((x) => keyOf(x) !== k))
    else if (!full) setSelected([...selected, s])
  }

  const label = (s: MetricSeries) => {
    const d = friendlyDims(s.dimensions, names)
    return d ? `${s.name} ${d}` : s.name
  }

  // One chart per unit: a chart has a single y-axis.
  const byUnit = useMemo(() => {
    const m = new Map<string, MetricSeries[]>()
    for (const s of selected) m.set(s.unit || "None", [...(m.get(s.unit || "None") ?? []), s])
    return [...m.entries()]
  }, [selected])

  const colorOf = (s: MetricSeries) => {
    const grp = byUnit.find(([, list]) => list.some((x) => keyOf(x) === keyOf(s)))
    const idx = grp ? grp[1].findIndex((x) => keyOf(x) === keyOf(s)) : 0
    return SERIES_COLORS[idx % SERIES_COLORS.length]
  }

  return (
    <div className="flex flex-col gap-4">
      <PageHeader
        breadcrumbs={[{ label: "CloudWatch", href: "/cloudwatch/" }, { label: "Metrics" }]}
        title="Metrics"
        description="Browse metrics by namespace and graph up to 5 series. HomeCloud samples resource metrics every 30 seconds; custom metrics can be published with PutMetricData."
      />

      <div className="grid grid-cols-1 gap-4 xl:grid-cols-[minmax(340px,420px)_1fr]">
        <Section flush title="Browse" description={ns ? undefined : "Choose a namespace"} className="min-w-0 self-start">
          <div className="flex flex-col gap-2 p-3">
            {ns && (
              <button
                type="button"
                onClick={() => {
                  setParam("namespace", null)
                  setSearch("")
                }}
                className="text-primary flex items-center gap-1 self-start text-sm hover:underline"
              >
                <ChevronLeft className="size-4" /> All namespaces
              </button>
            )}
            <div className="relative">
              <Search className="text-muted-foreground pointer-events-none absolute top-1/2 left-2.5 size-4 -translate-y-1/2" />
              <Input
                value={search}
                onChange={(e) => setSearch(e.target.value)}
                placeholder={ns ? `Search ${ns}` : "Search all metrics"}
                className="h-8 pr-8 pl-8"
              />
              {search && (
                <button
                  type="button"
                  aria-label="Clear search"
                  onClick={() => setSearch("")}
                  className="text-muted-foreground hover:text-foreground absolute top-1/2 right-2 -translate-y-1/2"
                >
                  <X className="size-4" />
                </button>
              )}
            </div>
            {full && <p className="text-warning text-xs">5 series selected: the maximum for one graph. Remove one to add another.</p>}
          </div>
          {error ? (
            <div className="p-3 pt-0">
              <ErrorState error={error} onRetry={() => mutate()} />
            </div>
          ) : isLoading && !data ? (
            <div className="flex flex-col gap-2 p-3">
              {Array.from({ length: 4 }, (_, i) => (
                <Skeleton key={i} className="h-8 w-full" />
              ))}
            </div>
          ) : !ns && !search.trim() ? (
            namespaces.length === 0 ? (
              <EmptyState icon={Activity} title="No metrics yet" description="Metrics appear once a resource is running." />
            ) : (
              <ul className="border-t">
                {namespaces.map(([n, count]) => (
                  <li key={n}>
                    <button
                      type="button"
                      onClick={() => setParam("namespace", n)}
                      className="hover:bg-muted/50 flex w-full items-center justify-between gap-2 border-b px-4 py-2.5 text-left text-sm last:border-0"
                    >
                      <span className="text-primary font-medium">{n}</span>
                      <span className="text-muted-foreground text-xs tabular-nums">{count} metrics</span>
                    </button>
                  </li>
                ))}
              </ul>
            )
          ) : grouped.length === 0 ? (
            <p className="text-muted-foreground border-t p-6 text-center text-sm">
              {ns && !namespaces.some(([n]) => n === ns) ? `No data in namespace ${ns} yet.` : "No metrics match."}
            </p>
          ) : (
            <div className="max-h-[560px] overflow-auto border-t">
              <table className="w-full text-sm">
                <thead className="bg-muted/60 sticky top-0 z-10 backdrop-blur">
                  <tr className="text-muted-foreground border-b text-left text-xs">
                    <th className="w-9 px-3 py-2" />
                    <th className="px-2 py-2 font-semibold">Dimensions</th>
                    <th className="px-3 py-2 text-right font-semibold">Unit</th>
                  </tr>
                </thead>
                <tbody>
                  {grouped.map(([metric, list]) => (
                    <MetricGroup key={metric} title={metric}>
                      {list.map((s) => {
                        const k = keyOf(s)
                        const on = selKeys.has(k)
                        const disabled = !on && full
                        return (
                          <tr
                            key={k}
                            className={cn("border-b last:border-0", disabled ? "opacity-60" : "hover:bg-muted/40 cursor-pointer", on && "bg-brand-soft")}
                            onClick={() => !disabled && toggle(s)}
                            title={disabled ? "At most 5 series per graph" : undefined}
                          >
                            <td className="px-3 py-1.5" onClick={(e) => e.stopPropagation()}>
                              <Checkbox checked={on} disabled={disabled} onCheckedChange={() => toggle(s)} aria-label={`Graph ${label(s)}`} />
                            </td>
                            <td className="min-w-0 px-2 py-1.5">
                              <div className="flex flex-col">
                                <span className="font-mono text-xs break-all">{dimsText(s.dimensions) || <span className="text-muted-foreground">(no dimensions)</span>}</span>
                                {s.dimensions?.InstanceId && names.get(s.dimensions.InstanceId) && (
                                  <span className="text-muted-foreground text-xs">{names.get(s.dimensions.InstanceId)}</span>
                                )}
                              </div>
                            </td>
                            <td className="text-muted-foreground px-3 py-1.5 text-right text-xs">{s.unit || "None"}</td>
                          </tr>
                        )
                      })}
                    </MetricGroup>
                  ))}
                </tbody>
              </table>
            </div>
          )}
        </Section>

        <div className="flex min-w-0 flex-col gap-4">
          <Section
            title="Graph"
            description={selected.length ? `${selected.length} of ${MAX_SERIES} series` : undefined}
            actions={
              <div className="flex flex-wrap items-center gap-2">
                <div className="bg-muted inline-flex rounded-md p-0.5">
                  {RANGES.map((r) => (
                    <button
                      key={r.value}
                      type="button"
                      onClick={() => setRange(r.value)}
                      className={cn(
                        "rounded px-2.5 py-1 text-xs font-medium transition-colors",
                        range === r.value ? "bg-background text-foreground shadow-xs" : "text-muted-foreground hover:text-foreground",
                      )}
                    >
                      {r.label}
                    </button>
                  ))}
                </div>
                <Select value={String(period)} onValueChange={(v) => setPeriod(Number(v))}>
                  <SelectTrigger size="sm" aria-label="Period">
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    {PERIODS.map((p) => (
                      <SelectItem key={p.value} value={String(p.value)}>
                        Period: {p.label}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
                <Select value={stat} onValueChange={(v) => setStat(v as Statistic)}>
                  <SelectTrigger size="sm" aria-label="Statistic">
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    {STATISTICS.map((s) => (
                      <SelectItem key={s} value={s}>
                        {s}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
                <div className="flex items-center gap-2 pl-1">
                  <Switch id="metrics-auto" checked={auto} onCheckedChange={setAuto} />
                  <Label htmlFor="metrics-auto" className="text-xs font-normal">
                    Auto refresh
                  </Label>
                </div>
              </div>
            }
          >
            {selected.length === 0 ? (
              <EmptyState icon={LineChart} title="No metrics selected" description="Pick up to 5 series from the list to graph them together." />
            ) : (
              <div className="flex flex-col gap-6">
                {byUnit.length > 1 && (
                  <Alert variant="info">
                    <Info />
                    <AlertDescription>The selected series use different units, so they are drawn on {byUnit.length} separate graphs (one per unit).</AlertDescription>
                  </Alert>
                )}
                {byUnit.map(([unit, list]) => {
                  const queries: ChartQuery[] = list.map((s) => ({
                    namespace: s.namespace,
                    name: s.name,
                    dimensions: s.dimensions ?? undefined,
                    label: label(s),
                  }))
                  return (
                    <div key={unit} className="flex flex-col gap-2">
                      {byUnit.length > 1 && <h3 className="text-muted-foreground text-xs font-medium">Unit: {unit}</h3>}
                      <MetricChart queries={queries} rangeMinutes={range} period={period} stat={stat} height={300} refreshMs={auto ? 30_000 : 0} />
                    </div>
                  )
                })}
              </div>
            )}
          </Section>

          {selected.length > 0 && (
            <Section flush title="Selected series" actions={<Button variant="ghost" size="sm" onClick={() => setSelected([])}>Clear all</Button>}>
              <ul className="divide-y">
                {selected.map((s) => (
                  <li key={keyOf(s)} className="flex items-center gap-3 px-4 py-2 text-sm">
                    <span aria-hidden className="h-[3px] w-4 shrink-0 rounded-full" style={{ background: colorOf(s) }} />
                    <div className="min-w-0 flex-1">
                      <div className="truncate font-medium">{s.name}</div>
                      <div className="text-muted-foreground truncate font-mono text-xs">
                        {s.namespace} {dimsText(s.dimensions) && `| ${dimsText(s.dimensions)}`}
                      </div>
                    </div>
                    <span className="text-muted-foreground shrink-0 text-xs">{s.unit || "None"}</span>
                    <Button variant="ghost" size="icon" className="size-7" onClick={() => toggle(s)} aria-label="Remove series">
                      <X />
                    </Button>
                  </li>
                ))}
              </ul>
            </Section>
          )}
        </div>
      </div>
    </div>
  )
}

function MetricGroup({ title, children }: { title: string; children: React.ReactNode }) {
  return (
    <>
      <tr className="bg-muted/30 border-b">
        <td colSpan={3} className="px-3 py-1.5 text-xs font-semibold">
          {title}
        </td>
      </tr>
      {children}
    </>
  )
}
