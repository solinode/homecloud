"use client"

import { useMemo, useState } from "react"
import { CircleAlert, Loader2, Play, SearchX } from "lucide-react"
import { toast } from "sonner"

import { Alert, AlertDescription } from "@/components/ui/alert"
import { Button } from "@/components/ui/button"
import { Checkbox } from "@/components/ui/checkbox"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select"
import { Textarea } from "@/components/ui/textarea"
import { EmptyState } from "@/components/console/empty-state"
import { Field } from "@/components/console/form-field"
import { PageHeader } from "@/components/console/page-header"
import { Section } from "@/components/console/section"
import { api, errorMessage } from "@/lib/api"
import { formatBytes } from "@/lib/format"
import { useApi } from "@/lib/hooks"
import type { InsightsResults, LogGroup } from "@/lib/types"

const RANGES = [
  { value: 15, label: "Last 15 minutes" },
  { value: 60, label: "Last hour" },
  { value: 180, label: "Last 3 hours" },
  { value: 1440, label: "Last 24 hours" },
  { value: 10080, label: "Last 7 days" },
  { value: 43200, label: "Last 30 days" },
]

const DEFAULT_QUERY = "fields @timestamp, @message\n| sort @timestamp desc\n| limit 20"
const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms))

export function LogsInsights() {
  const groups = useApi<LogGroup[]>("/api/v1/logs/groups", { revalidateOnFocus: false })
  const [picked, setPicked] = useState<string[]>([])
  const [groupFilter, setGroupFilter] = useState("")
  const [query, setQuery] = useState(DEFAULT_QUERY)
  const [range, setRange] = useState("60")
  const [running, setRunning] = useState(false)
  const [result, setResult] = useState<InsightsResults | null>(null)
  const [error, setError] = useState("")

  const shown = (groups.data ?? []).filter((g) => g.name.toLowerCase().includes(groupFilter.toLowerCase()))
  const columns = useMemo(() => {
    const cols: string[] = []
    for (const row of result?.results ?? []) for (const f of row) if (f.field !== "@ptr" && !cols.includes(f.field)) cols.push(f.field)
    return cols
  }, [result])

  const toggle = (name: string, on: boolean) => setPicked((p) => (on ? [...p, name] : p.filter((n) => n !== name)))

  const run = async () => {
    setRunning(true)
    setError("")
    try {
      const now = Math.floor(Date.now() / 1000)
      const { queryId } = await api.post<{ queryId: string }>("/api/v1/logs/insights/queries", {
        logGroupNames: picked,
        startTime: now - Number(range) * 60,
        endTime: now,
        queryString: query,
      })
      let res: InsightsResults
      for (;;) {
        res = await api.get<InsightsResults>(`/api/v1/logs/insights/queries/${encodeURIComponent(queryId)}`)
        if (res.status !== "Running" && res.status !== "Scheduled") break
        await sleep(700)
      }
      setResult(res)
    } catch (err) {
      setError(errorMessage(err))
      setResult(null)
      toast.error(errorMessage(err))
    } finally {
      setRunning(false)
    }
  }

  return (
    <div className="flex flex-col gap-4">
      <PageHeader
        title="Logs Insights"
        description="Query one or more log groups with the CloudWatch Logs Insights language (fields, filter, stats, sort, limit, parse, dedup)."
        breadcrumbs={[{ label: "CloudWatch", href: "/cloudwatch/" }, { label: "Logs Insights" }]}
      />
      <div className="grid gap-4 lg:grid-cols-[320px_1fr]">
        <Section title="Log groups" description={`${picked.length} selected`}>
          <div className="flex flex-col gap-2">
            <Input value={groupFilter} onChange={(e) => setGroupFilter(e.target.value)} placeholder="Filter log groups" className="h-8" />
            <div className="flex max-h-64 flex-col gap-1 overflow-y-auto">
              {groups.data && shown.length === 0 && <p className="text-muted-foreground text-sm">No log groups.</p>}
              {shown.map((g) => (
                <Label key={g.name} className="flex min-w-0 items-center gap-2 text-sm font-normal">
                  <Checkbox checked={picked.includes(g.name)} onCheckedChange={(c) => toggle(g.name, c === true)} />
                  <span className="min-w-0 truncate font-mono text-[13px]" title={g.name}>
                    {g.name}
                  </span>
                </Label>
              ))}
            </div>
          </div>
        </Section>
        <Section title="Query">
          <div className="flex flex-col gap-3">
            <Field label="Time range" htmlFor="ins-range">
              <Select value={range} onValueChange={setRange}>
                <SelectTrigger id="ins-range" className="w-full sm:w-64">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {RANGES.map((r) => (
                    <SelectItem key={r.value} value={String(r.value)}>
                      {r.label}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </Field>
            <Field label="Query" htmlFor="ins-query" help="Commands are separated by | and run top to bottom.">
              <Textarea
                id="ins-query"
                value={query}
                onChange={(e) => setQuery(e.target.value)}
                rows={6}
                className="font-mono text-[13px]"
                spellCheck={false}
              />
            </Field>
            <div className="flex items-center gap-3">
              <Button onClick={run} disabled={running || picked.length === 0 || !query.trim()}>
                {running ? <Loader2 className="animate-spin" /> : <Play />} Run query
              </Button>
              {picked.length === 0 && <span className="text-muted-foreground text-sm">Select at least one log group.</span>}
            </div>
            {error && (
              <Alert variant="destructive">
                <CircleAlert />
                <AlertDescription>{error}</AlertDescription>
              </Alert>
            )}
          </div>
        </Section>
      </div>
      {result && (
        <Section
          title={`Results (${result.results.length})`}
          description={`${result.statistics.recordsMatched} matched of ${result.statistics.recordsScanned} scanned, ${formatBytes(result.statistics.bytesScanned)}`}
          flush
        >
          {result.results.length === 0 ? (
            <EmptyState icon={SearchX} title="No results" description="Nothing matched this query in the selected log groups and time range." />
          ) : (
            <div className="max-h-[60vh] overflow-auto">
              <table className="w-full text-sm">
                <thead className="bg-card text-muted-foreground sticky top-0 border-b text-left text-xs">
                  <tr>
                    {columns.map((c) => (
                      <th key={c} className="px-3 py-2 font-medium whitespace-nowrap">
                        {c}
                      </th>
                    ))}
                  </tr>
                </thead>
                <tbody>
                  {result.results.map((row, i) => {
                    const m = new Map(row.map((f) => [f.field, f.value]))
                    return (
                      <tr key={i} className="border-b align-top last:border-0">
                        {columns.map((c) => (
                          <td key={c} className="px-3 py-1.5 font-mono text-[12.5px] break-all whitespace-pre-wrap">
                            {m.get(c) ?? ""}
                          </td>
                        ))}
                      </tr>
                    )
                  })}
                </tbody>
              </table>
            </div>
          )}
        </Section>
      )}
    </div>
  )
}
