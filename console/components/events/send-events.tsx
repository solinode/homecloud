"use client"

import { useState } from "react"
import Link from "next/link"
import { Info, Loader2, Plus, Send, X } from "lucide-react"
import { toast } from "sonner"

import { Alert, AlertDescription } from "@/components/ui/alert"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { CopyableText } from "@/components/console/copy-button"
import { CellText, DataTable, type Column } from "@/components/console/data-table"
import { Field } from "@/components/console/form-field"
import { JsonEditor, jsonError } from "@/components/console/json-editor"
import { PageHeader } from "@/components/console/page-header"
import { Section } from "@/components/console/section"
import { StatusBadge } from "@/components/console/status-badge"
import { api, errorMessage } from "@/lib/api"
import { pluralize } from "@/lib/format"
import { revalidate } from "@/lib/hooks"
import type { EventEntry, PutEventsResult } from "@/lib/types"
import { EVENTS_PATH, RULES_PATH } from "./common"

interface EntryRow {
  source: string
  detailType: string
  detail: string
  resources: string
}

const newEntry = (): EntryRow => ({
  source: "my.app",
  detailType: "order.paid",
  detail: JSON.stringify({ order_id: "o-1001", total: 42 }, null, 2),
  resources: "",
})

const objectOnly = (v: unknown) => (!v || typeof v !== "object" || Array.isArray(v) ? "Detail must be a JSON object" : null)

interface SentRow {
  source: string
  detailType: string
  eventId: string
  matched: number
}

const resultColumns: Column<SentRow & { n: number }>[] = [
  { id: "n", header: "#", cell: (r) => <span className="tabular-nums">{r.n}</span>, value: (r) => r.n, headerClassName: "w-10" },
  { id: "id", header: "Event ID", cell: (r) => <CopyableText value={r.eventId} className="max-w-56" />, value: (r) => r.eventId },
  {
    id: "src",
    header: "Source / detail type",
    cell: (r) => <CellText mono>{`${r.source} / ${r.detailType}`}</CellText>,
    value: (r) => `${r.source} ${r.detailType}`,
    hideBelow: "sm",
  },
  {
    id: "matched",
    header: "Matched rules",
    cell: (r) =>
      r.matched ? <StatusBadge status="success" label={pluralize(r.matched, "rule")} /> : <StatusBadge status="none" tone="neutral" label="No match" />,
    value: (r) => r.matched,
  },
]

export function SendEvents() {
  const [entries, setEntries] = useState<EntryRow[]>([newEntry()])
  const [submitted, setSubmitted] = useState(false)
  const [pending, setPending] = useState(false)
  const [results, setResults] = useState<SentRow[] | null>(null)

  const errors: Record<string, string> = {}
  entries.forEach((e, i) => {
    const src = e.source.trim()
    if (!src) errors[`${i}source`] = "Enter a source"
    else if (src.startsWith("aws.")) errors[`${i}source`] = "The aws. prefix is reserved for HomeCloud events"
    if (!e.detailType.trim()) errors[`${i}type`] = "Enter a detail type"
    const je = jsonError(e.detail) ?? objectOnly(JSON.parse(e.detail))
    if (je) errors[`${i}detail`] = je
  })
  const err = (k: string) => (submitted ? errors[k] : undefined)
  const valid = Object.keys(errors).length === 0

  const set = (i: number, patch: Partial<EntryRow>) => setEntries(entries.map((e, j) => (j === i ? { ...e, ...patch } : e)))

  const send = async (ev: React.FormEvent) => {
    ev.preventDefault()
    setSubmitted(true)
    if (!valid) {
      toast.error("Fix the highlighted fields before sending")
      return
    }
    const body: { entries: EventEntry[] } = {
      entries: entries.map((e) => {
        const resources = e.resources
          .split(",")
          .map((s) => s.trim())
          .filter(Boolean)
        return { source: e.source.trim(), detail_type: e.detailType.trim(), detail: JSON.parse(e.detail), resources: resources.length ? resources : undefined }
      }),
    }
    setPending(true)
    try {
      const r = await api.post<PutEventsResult>(`${EVENTS_PATH}/events`, body)
      const rows = r.entries.map((x, i) => ({ source: body.entries[i].source, detailType: body.entries[i].detail_type, eventId: x.event_id, matched: x.matched_rules }))
      setResults(rows)
      const matched = rows.reduce((a, x) => a + x.matched, 0)
      toast.success(`Sent ${pluralize(rows.length, "event")}${matched ? `, matched ${pluralize(matched, "rule")}` : ", no rules matched"}`)
      // Matching rules fire asynchronously; refresh their stats shortly after.
      setTimeout(() => revalidate(RULES_PATH), 1500)
    } catch (e) {
      toast.error(errorMessage(e))
    } finally {
      setPending(false)
    }
  }

  return (
    <div className="flex flex-col gap-4">
      <PageHeader
        title="Send events"
        description="Publish custom events to the default event bus. Every enabled rule whose event pattern matches delivers the event to its targets."
        breadcrumbs={[{ label: "EventBridge", href: "/events/" }, { label: "Send events" }]}
      />

      <Alert>
        <Info />
        <AlertDescription>
          <span>
            Events are delivered as <span className="font-mono text-[13px]">{`{version, id, detail-type, source, account, time, region, resources, detail}`}</span>.
            Sources starting with <span className="font-mono text-[13px]">aws.</span> are reserved. Rules with a matching{" "}
            <Link href="/events/" className="text-primary hover:underline">
              event pattern
            </Link>{" "}
            fire their targets; schedule rules ignore published events.
          </span>
        </AlertDescription>
      </Alert>

      <form onSubmit={send} className="flex flex-col gap-4">
        {entries.map((e, i) => (
          <Section
            key={i}
            title={`Event ${i + 1}`}
            actions={
              entries.length > 1 && (
                <Button type="button" variant="ghost" size="sm" onClick={() => setEntries(entries.filter((_, j) => j !== i))}>
                  <X /> Remove
                </Button>
              )
            }
          >
            <div className="flex flex-col gap-4">
              <div className="grid grid-cols-1 gap-4 md:grid-cols-2">
                <Field label="Source" htmlFor={`src-${i}`} error={err(`${i}source`)} help="Identifies the application that sent the event, e.g. my.app.">
                  <Input id={`src-${i}`} value={e.source} onChange={(x) => set(i, { source: x.target.value })} className="font-mono" spellCheck={false} />
                </Field>
                <Field label="Detail type" htmlFor={`dt-${i}`} error={err(`${i}type`)} help="What happened, e.g. order.paid.">
                  <Input id={`dt-${i}`} value={e.detailType} onChange={(x) => set(i, { detailType: x.target.value })} className="font-mono" spellCheck={false} />
                </Field>
              </div>
              <Field label="Detail" error={err(`${i}detail`)}>
                <JsonEditor value={e.detail} onChange={(v) => set(i, { detail: v })} rows={6} validate={objectOnly} />
              </Field>
              <Field label="Resources" htmlFor={`res-${i}`} optional help="Comma-separated ARNs the event relates to.">
                <Input
                  id={`res-${i}`}
                  value={e.resources}
                  onChange={(x) => set(i, { resources: x.target.value })}
                  placeholder="arn:aws:dynamodb:us-east-1:123456789012:table/orders"
                  className="font-mono text-[13px]"
                  spellCheck={false}
                />
              </Field>
            </div>
          </Section>
        ))}
        <div className="flex flex-wrap items-center gap-2 border-t pt-4">
          <Button
            type="button"
            variant="outline"
            size="sm"
            disabled={entries.length >= 10}
            onClick={() => setEntries([...entries, { ...(entries[entries.length - 1] ?? newEntry()) }])}
          >
            <Plus /> Add event
          </Button>
          <span className="text-muted-foreground text-xs">{entries.length} of 10</span>
          <Button type="submit" disabled={pending} className="ml-auto">
            {pending ? <Loader2 className="animate-spin" /> : <Send />}
            Send {entries.length > 1 ? `${entries.length} events` : "event"}
          </Button>
        </div>
      </form>

      {results && (
        <DataTable
          title="Results"
          description={
            <>
              Matching rules deliver asynchronously. Check invocations and errors on the{" "}
              <Link href="/events/" className="text-primary hover:underline">
                rules page
              </Link>
              .
            </>
          }
          data={results.map((r, i) => ({ ...r, n: i + 1 }))}
          columns={resultColumns}
          rowId={(r) => r.eventId}
          noSearch
        />
      )}
    </div>
  )
}
