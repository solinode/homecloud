"use client"

import { useEffect, useLayoutEffect, useMemo, useRef, useState } from "react"
import useSWR from "swr"
import { ArrowDown, ChevronDown, ChevronRight, Download, Loader2, Pause, RefreshCw, Search, Send, ScrollText, X } from "lucide-react"
import { toast } from "sonner"

import { Button } from "@/components/ui/button"
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select"
import { Skeleton } from "@/components/ui/skeleton"
import { Switch } from "@/components/ui/switch"
import { Tabs, TabsList, TabsTrigger } from "@/components/ui/tabs"
import { Textarea } from "@/components/ui/textarea"
import { MetricFilters, SubscriptionFilters } from "./log-filters"
import { CopyButton } from "@/components/console/copy-button"
import { EmptyState } from "@/components/console/empty-state"
import { ErrorState } from "@/components/console/error-state"
import { Field } from "@/components/console/form-field"
import { PageHeader } from "@/components/console/page-header"
import { api, errorMessage, seg } from "@/lib/api"
import { formatBytes, timeAgo } from "@/lib/format"
import { revalidate, useApi, useQueryParam } from "@/lib/hooks"
import type { LogEvent, LogGroup, LogStream } from "@/lib/types"
import { cn } from "@/lib/utils"

import { describeContainerGroup, retentionLabel, useInstanceNames } from "./common"
import { RetentionDialog, SourceBadge } from "./log-groups"

const RANGES = [
  { value: "5", label: "Last 5 minutes" },
  { value: "15", label: "Last 15 minutes" },
  { value: "60", label: "Last hour" },
  { value: "720", label: "Last 12 hours" },
  { value: "1440", label: "Last 24 hours" },
  { value: "10080", label: "Last 7 days" },
  { value: "all", label: "All time" },
]
const LIMITS = [100, 500, 1000, 5000]

function pad(n: number, w = 2) {
  return String(n).padStart(w, "0")
}

/** logTime renders "2026-09-29 14:05:09.123" in local time. */
function logTime(ts: string): string {
  const d = new Date(ts)
  if (Number.isNaN(d.getTime()) || d.getFullYear() < 1971) return "-"
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${pad(d.getHours())}:${pad(d.getMinutes())}:${pad(d.getSeconds())}.${pad(d.getMilliseconds(), 3)}`
}

function prettyJson(msg: string): string | null {
  const t = msg.trim()
  if (!(t.startsWith("{") || t.startsWith("["))) return null
  try {
    return JSON.stringify(JSON.parse(t), null, 2)
  } catch {
    return null
  }
}

function useDebounced<T>(value: T, ms: number): T {
  const [v, setV] = useState(value)
  useEffect(() => {
    const t = setTimeout(() => setV(value), ms)
    return () => clearTimeout(t)
  }, [value, ms])
  return v
}

function EventRow({ e, showStream }: { e: LogEvent; showStream: boolean }) {
  const [open, setOpen] = useState(false)
  const json = useMemo(() => prettyJson(e.message), [e.message])
  const long = e.message.length > 240 || e.message.includes("\n")
  const expandable = !!json || long
  return (
    <div className={cn("group border-b last:border-0", open && "bg-muted/40")}>
      <div
        className={cn("flex gap-3 px-3 py-1 font-mono text-[12.5px] leading-5", expandable && "hover:bg-muted/40 cursor-pointer")}
        onClick={() => expandable && setOpen(!open)}
      >
        <span className="text-muted-foreground w-4 shrink-0">
          {expandable && (open ? <ChevronDown className="mt-0.5 size-3.5" /> : <ChevronRight className="mt-0.5 size-3.5" />)}
        </span>
        <span className="text-muted-foreground shrink-0 whitespace-nowrap tabular-nums">{logTime(e.timestamp)}</span>
        {showStream && (
          <span className="text-muted-foreground hidden max-w-40 shrink-0 truncate md:inline" title={e.stream}>
            {e.stream}
          </span>
        )}
        <span className={cn("min-w-0 flex-1 break-all whitespace-pre-wrap", !open && long && "line-clamp-2")}>{e.message}</span>
      </div>
      {open && (
        <div className="flex flex-col gap-2 px-10 pt-1 pb-3">
          <pre className="bg-background max-h-96 overflow-auto rounded-md border p-3 font-mono text-[12.5px] break-all whitespace-pre-wrap">{json ?? e.message}</pre>
          <div className="text-muted-foreground flex flex-wrap items-center gap-3 text-xs">
            <span>{new Date(e.timestamp).toISOString()}</span>
            <span>stream {e.stream}</span>
            <CopyButton value={e.message} size="sm" label="Copy message" />
          </div>
        </div>
      )}
    </div>
  )
}

function PutEventsDialog({ open, onOpenChange, group, streams }: { open: boolean; onOpenChange: (o: boolean) => void; group: string; streams: string[] }) {
  const [stream, setStream] = useState("")
  const [message, setMessage] = useState("")
  const [pending, setPending] = useState(false)
  const [touched, setTouched] = useState(false)
  useEffect(() => {
    if (open) {
      setStream(streams[0] ?? "console-test")
      setMessage("")
      setTouched(false)
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open])

  const streamErr = !stream.trim() ? "Stream name is required" : stream.includes(":") ? "Stream names cannot contain :" : null
  const msgErr = !message.trim() ? "Enter a message" : null

  const submit = async (e: React.FormEvent) => {
    e.preventDefault()
    setTouched(true)
    if (streamErr || msgErr) return
    setPending(true)
    try {
      const now = new Date().toISOString()
      const lines = message.split("\n").filter((l) => l.trim())
      await api.post(`/api/v1/logs/groups/${seg(group)}/streams/${seg(stream.trim())}/events`, {
        events: lines.map((m) => ({ timestamp: now, message: m })),
      })
      toast.success(lines.length === 1 ? "Log event added" : `${lines.length} log events added`)
      revalidate("/api/v1/logs")
      onOpenChange(false)
    } catch (err) {
      toast.error(errorMessage(err))
    } finally {
      setPending(false)
    }
  }

  return (
    <Dialog open={open} onOpenChange={(o) => !pending && onOpenChange(o)}>
      <DialogContent className="sm:max-w-lg">
        <form onSubmit={submit} className="flex flex-col gap-5">
          <DialogHeader>
            <DialogTitle>Put log events</DialogTitle>
            <DialogDescription>Append test events to a stream of {group}. The stream is created if it does not exist.</DialogDescription>
          </DialogHeader>
          <Field label="Log stream" htmlFor="pe-stream" error={touched ? streamErr : undefined}>
            <Input id="pe-stream" list="pe-streams" className="font-mono" value={stream} onChange={(e) => setStream(e.target.value)} />
            <datalist id="pe-streams">
              {streams.map((s) => (
                <option key={s} value={s} />
              ))}
            </datalist>
          </Field>
          <Field label="Message" htmlFor="pe-msg" error={touched ? msgErr : undefined} help="One event per line. JSON messages are pretty-printed in the viewer.">
            <Textarea id="pe-msg" rows={4} className="font-mono text-[13px]" value={message} onChange={(e) => setMessage(e.target.value)} placeholder='{"level":"info","msg":"hello from the console"}' />
          </Field>
          <DialogFooter>
            <Button type="button" variant="outline" onClick={() => onOpenChange(false)} disabled={pending}>
              Cancel
            </Button>
            <Button type="submit" disabled={pending}>
              {pending && <Loader2 className="animate-spin" />}
              Put events
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}

export function LogEventsViewer() {
  const name = useQueryParam("name")
  const names = useInstanceNames()
  const isContainer = name.startsWith("/hc/")
  const groups = useApi<LogGroup[]>(name ? "/api/v1/logs/groups" : null, { query: { prefix: name } })
  const group = groups.data?.find((g) => g.name === name)
  const streams = useApi<LogStream[]>(name ? `/api/v1/logs/groups/${seg(name)}/streams` : null, { refreshInterval: 30_000 })

  const [stream, setStream] = useState("")
  const [filterText, setFilterText] = useState("")
  const filter = useDebounced(filterText.trim(), 400)
  const [range, setRange] = useState("60")
  const [limit, setLimit] = useState(500)
  const [live, setLive] = useState(false)
  const [atBottom, setAtBottom] = useState(true)
  const [tab, setTab] = useState("events")
  const [putOpen, setPutOpen] = useState(false)
  const [retentionOpen, setRetentionOpen] = useState(false)
  const scroller = useRef<HTMLDivElement>(null)

  const key = name ? ["log-events", name, stream, filter, range, limit] : null
  const events = useSWR<LogEvent[]>(
    key,
    () =>
      api.get<LogEvent[]>(`/api/v1/logs/groups/${seg(name)}/events`, {
        stream: stream || undefined,
        filter: filter || undefined,
        limit,
        start: range === "all" ? undefined : Date.now() - Number(range) * 60_000,
      }),
    { refreshInterval: live ? 2000 : 0, keepPreviousData: true, revalidateOnFocus: !live },
  )
  const list = events.data ?? []

  const scrollToBottom = () => {
    const el = scroller.current
    if (el) el.scrollTop = el.scrollHeight
  }

  // Keep the newest event in view (terminal style) unless the user scrolled up.
  useLayoutEffect(() => {
    if (atBottom) scrollToBottom()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [events.data])

  const onScroll = () => {
    const el = scroller.current
    if (!el) return
    setAtBottom(el.scrollHeight - el.scrollTop - el.clientHeight < 40)
  }

  const download = () => {
    const text = list.map((e) => `${new Date(e.timestamp).toISOString()}\t${e.stream}\t${e.message}`).join("\n") + "\n"
    const blob = new Blob([text], { type: "text/plain;charset=utf-8" })
    const url = URL.createObjectURL(blob)
    const a = document.createElement("a")
    a.href = url
    a.download = `${name.replace(/^\/+/, "").replace(/[^\w.-]+/g, "_") || "logs"}${stream ? `_${stream.replace(/[^\w.-]+/g, "_")}` : ""}.log`
    a.click()
    setTimeout(() => URL.revokeObjectURL(url), 1000)
  }

  const streamNames = (streams.data ?? []).map((s) => s.name)
  const showStream = !stream && streamNames.length > 1
  const paused = live && !atBottom

  if (!name) return <ErrorState error={new Error("No log group given. Open a group from the log groups list.")} />

  return (
    <div className="flex flex-col gap-4">
      <PageHeader
        breadcrumbs={[{ label: "CloudWatch", href: "/cloudwatch/" }, { label: "Log groups", href: "/cloudwatch/logs/" }, { label: name }]}
        title={<span className="font-mono text-lg break-all sm:text-xl">{name}</span>}
        badge={<SourceBadge source={group?.source ?? (isContainer ? "container" : "stored")} />}
        description={
          isContainer ? (
            <>
              {describeContainerGroup(name, names)}. Container log groups are read-only views of the container&apos;s stdout and stderr and exist while the
              container does.
            </>
          ) : group ? (
            <>
              Retention: {retentionLabel(group.retention_days)}{" "}
              <button type="button" className="text-primary hover:underline" onClick={() => setRetentionOpen(true)}>
                (edit)
              </button>
              {" | "}
              {formatBytes(group.stored_bytes)} stored in {streamNames.length} stream(s)
            </>
          ) : undefined
        }
        actions={
          !isContainer && (
            <Button size="sm" variant="outline" onClick={() => setPutOpen(true)}>
              <Send /> Put log events
            </Button>
          )
        }
      />

      {!isContainer && (
        <Tabs value={tab} onValueChange={setTab}>
          <TabsList>
            <TabsTrigger value="events">Log events</TabsTrigger>
            <TabsTrigger value="metric">Metric filters</TabsTrigger>
            <TabsTrigger value="subscription">Subscription filters</TabsTrigger>
          </TabsList>
        </Tabs>
      )}
      {tab === "metric" && !isContainer && <MetricFilters group={name} />}
      {tab === "subscription" && !isContainer && <SubscriptionFilters group={name} />}

      <div className={cn("bg-card flex flex-col rounded-lg border shadow-xs", tab !== "events" && !isContainer && "hidden")}>
        <div className="flex flex-col gap-2 border-b p-3 lg:flex-row lg:flex-wrap lg:items-center">
          <div className="relative w-full lg:max-w-sm lg:flex-1">
            <Search className="text-muted-foreground pointer-events-none absolute top-1/2 left-2.5 size-4 -translate-y-1/2" />
            <Input value={filterText} onChange={(e) => setFilterText(e.target.value)} placeholder="Filter events (substring, case-insensitive)" className="h-8 pr-8 pl-8" />
            {filterText && (
              <button type="button" aria-label="Clear filter" onClick={() => setFilterText("")} className="text-muted-foreground hover:text-foreground absolute top-1/2 right-2 -translate-y-1/2">
                <X className="size-4" />
              </button>
            )}
          </div>
          <div className="flex flex-wrap items-center gap-2">
            <Select value={stream || "__all"} onValueChange={(v) => setStream(v === "__all" ? "" : v)}>
              <SelectTrigger size="sm" className="w-full sm:w-56" aria-label="Log stream">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="__all">All streams</SelectItem>
                {(streams.data ?? []).map((s) => (
                  <SelectItem key={s.name} value={s.name}>
                    <span className="font-mono text-xs">{s.name}</span>
                    {!isContainer && (
                      <span className="text-muted-foreground text-xs">
                        {formatBytes(s.stored_bytes)}, {timeAgo(s.last_event_time)}
                      </span>
                    )}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
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
              <SelectTrigger size="sm" aria-label="Limit">
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
          </div>
          <div className="flex flex-wrap items-center gap-3 lg:ml-auto">
            <div className="flex items-center gap-2">
              <Switch
                id="live-tail"
                checked={live}
                onCheckedChange={(v) => {
                  setLive(v)
                  if (v) {
                    setAtBottom(true)
                    requestAnimationFrame(scrollToBottom)
                  }
                }}
              />
              <Label htmlFor="live-tail" className="flex items-center gap-1.5 text-sm font-normal">
                Live tail
                {live &&
                  (paused ? (
                    <Pause className="size-3.5 text-amber-600 dark:text-amber-400" />
                  ) : (
                    <span className="relative flex size-2">
                      <span className="absolute inline-flex size-full animate-ping rounded-full bg-emerald-500 opacity-75" />
                      <span className="relative inline-flex size-2 rounded-full bg-emerald-500" />
                    </span>
                  ))}
              </Label>
            </div>
            <Button size="sm" variant="outline" onClick={() => events.mutate()} disabled={events.isValidating}>
              {events.isValidating ? <Loader2 className="animate-spin" /> : <RefreshCw />} Refresh
            </Button>
            <Button size="sm" variant="outline" onClick={download} disabled={!list.length}>
              <Download /> Download
            </Button>
          </div>
        </div>

        <div className="text-muted-foreground flex items-center justify-between gap-2 border-b px-3 py-1.5 text-xs">
          <span>
            {events.data ? `${list.length} event${list.length === 1 ? "" : "s"}` : "Loading"}
            {list.length >= limit && ` (showing the newest ${limit}; narrow the time range or raise the limit to see more)`}
          </span>
          {paused && <span className="text-amber-700 dark:text-amber-400">Live tail paused while you scroll. Jump to latest to resume.</span>}
        </div>

        <div className="relative">
          <div ref={scroller} onScroll={onScroll} className="h-[calc(100vh-360px)] min-h-[320px] overflow-y-auto">
            {events.error ? (
              <div className="p-4">
                <ErrorState error={events.error} onRetry={() => events.mutate()} />
              </div>
            ) : !events.data ? (
              <div className="flex flex-col gap-2 p-3">
                {Array.from({ length: 10 }, (_, i) => (
                  <Skeleton key={i} className="h-4 w-full" />
                ))}
              </div>
            ) : list.length === 0 ? (
              <EmptyState
                icon={ScrollText}
                title="No events"
                description={
                  filter
                    ? `No events match "${filter}" in this time range.`
                    : range === "all"
                      ? "This log group has no events yet."
                      : "No events in this time range. Try a longer range, or turn on live tail to watch for new events."
                }
                action={
                  range !== "all" ? (
                    <Button size="sm" variant="outline" onClick={() => setRange("all")}>
                      Show all time
                    </Button>
                  ) : undefined
                }
              />
            ) : (
              <div>
                {list.map((e, i) => (
                  <EventRow key={`${e.timestamp}-${i}`} e={e} showStream={showStream} />
                ))}
              </div>
            )}
          </div>
          {!atBottom && list.length > 0 && (
            <Button
              size="sm"
              className="absolute right-4 bottom-4 shadow-md"
              onClick={() => {
                setAtBottom(true)
                scrollToBottom()
              }}
            >
              <ArrowDown /> Jump to latest
            </Button>
          )}
        </div>
      </div>

      {!isContainer && <PutEventsDialog
          open={putOpen}
          onOpenChange={(o) => {
            setPutOpen(o)
            if (!o) events.mutate()
          }}
          group={name} streams={streamNames} />}
      {group && group.source === "stored" && <RetentionDialog open={retentionOpen} onOpenChange={setRetentionOpen} group={group} />}
    </div>
  )
}
