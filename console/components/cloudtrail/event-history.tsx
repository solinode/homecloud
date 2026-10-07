"use client"

import { useEffect, useMemo, useState } from "react"
import { ChevronDown, ChevronRight, Download, History, Search, X } from "lucide-react"

import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select"
import { Switch } from "@/components/ui/switch"
import { CodeBlock } from "@/components/console/code-block"
import { CopyableText } from "@/components/console/copy-button"
import { CellText, DataTable, type Column } from "@/components/console/data-table"
import { EmptyState } from "@/components/console/empty-state"
import { KeyValueGrid } from "@/components/console/key-value"
import { PageHeader } from "@/components/console/page-header"
import { StatusBadge } from "@/components/console/status-badge"
import { TimeAgo } from "@/components/console/time-ago"
import { formatDate } from "@/lib/format"
import { useApi, useQueryParam } from "@/lib/hooks"
import type { TrailEvent } from "@/lib/types"

interface UserLite {
  name: string
}

function useDebounced<T>(value: T, ms: number): T {
  const [v, setV] = useState(value)
  useEffect(() => {
    const t = setTimeout(() => setV(value), ms)
    return () => clearTimeout(t)
  }, [value, ms])
  return v
}

function ResultBadge({ status }: { status: number }) {
  if (status === 403) return <StatusBadge status="failed" label={`Access denied (${status})`} />
  if (status >= 400) return <StatusBadge status="failed" label={`Failed (${status})`} />
  return <StatusBadge status="success" label={`Success (${status})`} />
}

function TextFilter({ value, onChange, placeholder, label, mono }: { value: string; onChange: (v: string) => void; placeholder: string; label: string; mono?: boolean }) {
  return (
    <div className="relative w-full sm:w-56">
      <Search className="text-muted-foreground pointer-events-none absolute top-1/2 left-2.5 size-4 -translate-y-1/2" />
      <Input aria-label={label} value={value} onChange={(e) => onChange(e.target.value)} placeholder={placeholder} className={`h-8 pr-8 pl-8 ${mono ? "font-mono text-[13px]" : ""}`} />
      {value && (
        <button type="button" aria-label={`Clear ${label}`} onClick={() => onChange("")} className="text-muted-foreground hover:text-foreground absolute top-1/2 right-2 -translate-y-1/2">
          <X className="size-4" />
        </button>
      )}
    </div>
  )
}

function EventDetail({ e }: { e: TrailEvent }) {
  const json = JSON.stringify(e, null, 2)
  return (
    <div className="flex flex-col gap-4">
      <KeyValueGrid
        columns={4}
        items={[
          { label: "Event ID", value: <CopyableText value={e.id} /> },
          { label: "Event time", value: `${formatDate(e.time)} (${new Date(e.time).toISOString()})` },
          { label: "Event name", value: <span className="font-mono text-[13px]">{e.action}</span> },
          { label: "Result", value: <ResultBadge status={e.status} /> },
          { label: "User name", value: e.user },
          { label: "User ARN", value: <CopyableText value={e.user_arn} /> },
          { label: "Access key", value: e.access_key ? <CopyableText value={e.access_key} /> : "Console session" },
          { label: "Source IP", value: <span className="font-mono text-[13px]">{e.source_ip}</span> },
          { label: "Resource", value: <CopyableText value={e.resource} />, wide: true },
          { label: "Request", value: <span className="font-mono text-[13px] break-all">{`${e.method} ${e.path}`}</span>, wide: true },
          { label: "User agent", value: <span className="break-all">{e.user_agent}</span>, wide: true },
          { label: "Latency", value: `${e.latency_ms} ms` },
        ]}
      />
      <CodeBlock code={json} title={`Event record · ${e.id}`} copyLabel="Copy JSON" maxHeight="24rem" />
    </div>
  )
}

export function EventHistory() {
  const initialUser = useQueryParam("user")
  const [user, setUser] = useState(initialUser)
  const [actionText, setActionText] = useState("")
  const [qText, setQText] = useState("")
  const [errorsOnly, setErrorsOnly] = useState(false)
  const [limit, setLimit] = useState(200)
  const [auto, setAuto] = useState(false)
  const [open, setOpen] = useState<string | null>(null)
  const action = useDebounced(actionText.trim(), 400)
  const q = useDebounced(qText.trim(), 400)

  useEffect(() => {
    if (initialUser) setUser(initialUser)
  }, [initialUser])

  const users = useApi<UserLite[]>("/api/v1/iam/users")
  const { data, error, isLoading, isValidating, mutate } = useApi<TrailEvent[]>("/api/v1/cloudtrail/events", {
    query: { user: user || undefined, action: action || undefined, q: q || undefined, errors: errorsOnly ? "true" : undefined, limit },
    refreshInterval: auto ? 10_000 : 0,
  })

  const filtered = !!(user || action || q || errorsOnly)

  const columns = useMemo<Column<TrailEvent>[]>(
    () => [
      {
        id: "time",
        header: "Event time",
        value: (e) => e.time,
        cell: (e) => (
          <span className="flex items-center gap-1.5 whitespace-nowrap">
            {open === e.id ? <ChevronDown className="text-muted-foreground size-3.5" /> : <ChevronRight className="text-muted-foreground size-3.5" />}
            <span className="flex flex-col">
              <span className="tabular-nums">{formatDate(e.time)}</span>
              <span className="text-muted-foreground text-xs">
                <TimeAgo value={e.time} />
              </span>
            </span>
          </span>
        ),
      },
      {
        id: "user",
        header: "User name",
        value: (e) => e.user,
        cell: (e) => (e.user ? <CellText max="14rem">{e.user}</CellText> : <span className="text-muted-foreground">anonymous</span>),
      },
      { id: "action", header: "Event name", value: (e) => e.action, cell: (e) => <CellText mono>{e.action}</CellText> },
      {
        id: "resource",
        header: "Resource",
        value: (e) => e.resource,
        cell: (e) => (
          <CellText mono max="18rem">
            {e.resource}
          </CellText>
        ),
        hideBelow: "md",
      },
      { id: "ip", header: "Source IP", value: (e) => e.source_ip, cell: (e) => <CellText mono>{e.source_ip}</CellText>, hideBelow: "lg" },
      { id: "result", header: "Result", value: (e) => e.status, cell: (e) => <ResultBadge status={e.status} /> },
      { id: "latency", header: "Latency", value: (e) => e.latency_ms, cell: (e) => <span className="tabular-nums whitespace-nowrap">{e.latency_ms} ms</span>, hideBelow: "lg", className: "text-right", headerClassName: "text-right" },
    ],
    [open],
  )

  const download = () => {
    const blob = new Blob([JSON.stringify(data ?? [], null, 2)], { type: "application/json" })
    const url = URL.createObjectURL(blob)
    const a = document.createElement("a")
    a.href = url
    a.download = `cloudtrail-events-${new Date().toISOString().replace(/[:.]/g, "-")}.json`
    a.click()
    setTimeout(() => URL.revokeObjectURL(url), 1000)
  }

  const clear = () => {
    setUser("")
    setActionText("")
    setQText("")
    setErrorsOnly(false)
  }

  return (
    <div className="flex flex-col gap-4">
      <PageHeader
        breadcrumbs={[{ label: "CloudTrail" }, { label: "Event history" }]}
        title="Event history"
        description="CloudTrail records every mutating API call (create, update, delete) plus every call that was denied, from the console, the CLI and SDKs. Events are kept for 90 days."
      />
      <DataTable
        title="Events"
        description={data && data.length >= limit ? `Showing the newest ${limit} matching events` : undefined}
        data={data}
        columns={columns}
        rowId={(e) => e.id}
        loading={isLoading}
        error={error}
        onRefresh={() => mutate()}
        refreshing={isValidating}
        noSearch
        pageSize={50}
        onRowClick={(e) => setOpen(open === e.id ? null : e.id)}
        expanded={(e) => (open === e.id ? <EventDetail e={e} /> : null)}
        rowClassName={(e) => (e.status >= 400 ? "bg-danger-soft" : undefined)}
        filters={
          <>
            <Select value={user || "__all"} onValueChange={(v) => setUser(v === "__all" ? "" : v)}>
              <SelectTrigger size="sm" className="w-full sm:w-44" aria-label="User">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="__all">All users</SelectItem>
                {(users.data ?? []).map((u) => (
                  <SelectItem key={u.name} value={u.name}>
                    {u.name}
                  </SelectItem>
                ))}
                {user && !users.data?.some((u) => u.name === user) && <SelectItem value={user}>{user}</SelectItem>}
              </SelectContent>
            </Select>
            <TextFilter label="event name" value={actionText} onChange={setActionText} placeholder="Event name, e.g. s3:Put" mono />
            <TextFilter label="search" value={qText} onChange={setQText} placeholder="Search resource, user, event" />
            <Select value={String(limit)} onValueChange={(v) => setLimit(Number(v))}>
              <SelectTrigger size="sm" aria-label="Limit">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {[50, 200, 1000].map((l) => (
                  <SelectItem key={l} value={String(l)}>
                    {l} events
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
            <div className="flex items-center gap-2">
              <Switch id="ct-errors" checked={errorsOnly} onCheckedChange={setErrorsOnly} />
              <Label htmlFor="ct-errors" className="text-sm font-normal whitespace-nowrap">
                Errors only
              </Label>
            </div>
            <div className="flex items-center gap-2">
              <Switch id="ct-auto" checked={auto} onCheckedChange={setAuto} />
              <Label htmlFor="ct-auto" className="text-sm font-normal whitespace-nowrap">
                Auto refresh
              </Label>
            </div>
          </>
        }
        actions={
          <Button size="sm" variant="outline" onClick={download} disabled={!data?.length}>
            <Download /> Download JSON
          </Button>
        }
        empty={
          <EmptyState
            icon={History}
            title={filtered ? "No matching events" : "No events yet"}
            description={filtered ? "No events match these filters." : "Events appear here after the first create, update or delete call."}
            action={
              filtered ? (
                <Button size="sm" variant="outline" onClick={clear}>
                  Clear filters
                </Button>
              ) : undefined
            }
          />
        }
      />
    </div>
  )
}
