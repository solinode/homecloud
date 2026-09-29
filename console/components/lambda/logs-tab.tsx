"use client"

import { useLayoutEffect, useRef, useState } from "react"
import Link from "next/link"
import useSWR from "swr"
import { ExternalLink, RefreshCw, ScrollText } from "lucide-react"

import { Button } from "@/components/ui/button"
import { Label } from "@/components/ui/label"
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select"
import { Switch } from "@/components/ui/switch"
import { EmptyState } from "@/components/console/empty-state"
import { ErrorState } from "@/components/console/error-state"
import { TableSkeleton } from "@/components/console/loading"
import { logGroupHref } from "@/components/cloudwatch/common"
import { ApiError, api, seg } from "@/lib/api"
import type { LambdaFunction, LogEvent } from "@/lib/types"
import { cn } from "@/lib/utils"

const RANGES = [
  { value: "15", label: "Last 15 minutes" },
  { value: "60", label: "Last hour" },
  { value: "720", label: "Last 12 hours" },
  { value: "1440", label: "Last 24 hours" },
  { value: "10080", label: "Last 7 days" },
  { value: "all", label: "All time" },
]
const LIMITS = [100, 500, 1000]

function pad(n: number, w = 2) {
  return String(n).padStart(w, "0")
}

function logTime(ts: string): string {
  const d = new Date(ts)
  if (Number.isNaN(d.getTime())) return "-"
  return `${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${pad(d.getHours())}:${pad(d.getMinutes())}:${pad(d.getSeconds())}.${pad(d.getMilliseconds(), 3)}`
}

function lineKind(msg: string): "start" | "end" | "report" | "error" | "plain" {
  if (msg.startsWith("START RequestId:")) return "start"
  if (msg.startsWith("END RequestId:")) return "end"
  if (msg.startsWith("REPORT RequestId:")) return "report"
  if (/Traceback|Error|Exception|Task timed out/.test(msg)) return "error"
  return "plain"
}

export function LogsTab({ fn }: { fn: LambdaFunction }) {
  const group = fn.log_group || `/aws/lambda/${fn.name}`
  const [range, setRange] = useState("60")
  const [limit, setLimit] = useState(100)
  const [live, setLive] = useState(false)
  const [atBottom, setAtBottom] = useState(true)
  const scroller = useRef<HTMLDivElement>(null)

  const events = useSWR<LogEvent[], ApiError>(
    ["lambda-logs", group, range, limit],
    () =>
      api.get<LogEvent[]>(`/api/v1/logs/groups/${seg(group)}/events`, {
        limit,
        start: range === "all" ? undefined : Date.now() - Number(range) * 60_000,
      }),
    { refreshInterval: live ? 2000 : 0, keepPreviousData: true, revalidateOnFocus: !live },
  )
  const list = events.data ?? []
  const noGroup = events.error instanceof ApiError && events.error.status === 404

  useLayoutEffect(() => {
    const el = scroller.current
    if (el && atBottom) el.scrollTop = el.scrollHeight
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [events.data])

  const onScroll = () => {
    const el = scroller.current
    if (el) setAtBottom(el.scrollHeight - el.scrollTop - el.clientHeight < 40)
  }

  return (
    <div className="bg-card flex flex-col rounded-lg border shadow-xs">
      <div className="flex flex-col gap-2 border-b p-3 md:flex-row md:flex-wrap md:items-center">
        <div className="min-w-0">
          <h2 className="text-base font-semibold">Recent log events</h2>
          <p className="text-muted-foreground font-mono text-xs break-all">{group}</p>
        </div>
        <div className="flex flex-wrap items-center gap-2 md:ml-auto">
          <Select value={range} onValueChange={setRange}>
            <SelectTrigger size="sm" aria-label="Time range">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {RANGES.map((r) => (
                <SelectItem key={r.value} value={r.value}>
                  {r.label}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
          <Select value={String(limit)} onValueChange={(v) => setLimit(Number(v))}>
            <SelectTrigger size="sm" aria-label="Number of events">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {LIMITS.map((l) => (
                <SelectItem key={l} value={String(l)}>
                  {l} events
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
          <div className="flex items-center gap-2">
            <Switch id="logs-live" checked={live} onCheckedChange={setLive} />
            <Label htmlFor="logs-live" className="text-sm font-normal">
              Live
            </Label>
          </div>
          <Button variant="outline" size="sm" onClick={() => events.mutate()} disabled={events.isValidating} aria-label="Refresh">
            <RefreshCw className={cn(events.isValidating && "animate-spin")} /> Refresh
          </Button>
          <Button variant="outline" size="sm" asChild>
            <Link href={logGroupHref(group)}>
              <ExternalLink /> View in CloudWatch Logs
            </Link>
          </Button>
        </div>
      </div>

      {noGroup ? (
        <EmptyState
          icon={ScrollText}
          title="No logs yet"
          description="The log group is created on the first invocation. Run a test event to see START, END and REPORT lines plus everything the function prints."
        />
      ) : events.error ? (
        <div className="p-4">
          <ErrorState error={events.error} onRetry={() => events.mutate()} />
        </div>
      ) : !events.data ? (
        <TableSkeleton rows={6} cols={2} />
      ) : list.length === 0 ? (
        <EmptyState icon={ScrollText} title="No events in this time range" description="Choose a longer time range or invoke the function." />
      ) : (
        <div ref={scroller} onScroll={onScroll} className="max-h-[60vh] overflow-auto">
          {list.map((e, i) => {
            const kind = lineKind(e.message)
            return (
              <div
                key={`${e.timestamp}-${i}`}
                className={cn(
                  "flex gap-3 border-b px-3 py-1 font-mono text-[12.5px] leading-5 last:border-0",
                  kind === "start" && "bg-muted/30",
                )}
              >
                <span className="text-muted-foreground shrink-0 whitespace-nowrap tabular-nums">{logTime(e.timestamp)}</span>
                <span
                  className={cn(
                    "min-w-0 flex-1 break-all whitespace-pre-wrap",
                    (kind === "start" || kind === "end") && "text-muted-foreground",
                    kind === "report" && "text-muted-foreground italic",
                    kind === "error" && "text-red-700 dark:text-red-400",
                  )}
                >
                  {e.message}
                </span>
              </div>
            )
          })}
        </div>
      )}
      {events.data && list.length > 0 && (
        <div className="text-muted-foreground flex flex-wrap items-center justify-between gap-2 border-t px-3 py-2 text-xs">
          <span>
            {list.length} event{list.length === 1 ? "" : "s"}
            {list.length >= limit ? ` (newest ${limit})` : ""}
          </span>
          {live && <span>{atBottom ? "Live: refreshing every 2 seconds" : "Scroll to the bottom to follow new events"}</span>}
        </div>
      )}
    </div>
  )
}
