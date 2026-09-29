"use client"

import type { ReactNode } from "react"
import { Plus, X } from "lucide-react"

import { Button } from "@/components/ui/button"
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/components/ui/dialog"
import { Input } from "@/components/ui/input"
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select"
import { CopyButton, CopyableText } from "@/components/console/copy-button"
import { KeyValueGrid } from "@/components/console/key-value"
import { formatBytes, formatDate } from "@/lib/format"
import type { MessageAttribute } from "@/lib/types"

// ---- message attributes editor (shared by SQS send and SNS publish) ----

export interface AttrRow {
  name: string
  type: "String" | "Number"
  value: string
}

export const newAttrRow = (): AttrRow => ({ name: "", type: "String", value: "" })

/** attrRowsError validates attribute rows: names required and unique, Number values numeric. */
export function attrRowsError(rows: AttrRow[]): string | null {
  const names = new Set<string>()
  for (const r of rows) {
    const n = r.name.trim()
    if (!n) {
      if (r.value.trim()) return "Every attribute with a value needs a name"
      continue
    }
    if (!/^[A-Za-z0-9_.-]{1,256}$/.test(n)) return `Attribute name "${n}" may contain only letters, digits, _ . and -`
    if (names.has(n)) return `Attribute names must be unique ("${n}")`
    names.add(n)
    if (!r.value) return `Attribute "${n}" needs a value`
    if (r.type === "Number" && !Number.isFinite(Number(r.value.trim()))) return `Attribute "${n}" must be a number`
  }
  return null
}

/** attrRowsToMap drops empty rows; returns undefined when there are none. */
export function attrRowsToMap(rows: AttrRow[]): Record<string, MessageAttribute> | undefined {
  const out: Record<string, MessageAttribute> = {}
  for (const r of rows) {
    const n = r.name.trim()
    if (n) out[n] = { data_type: r.type, string_value: r.type === "Number" ? r.value.trim() : r.value }
  }
  return Object.keys(out).length ? out : undefined
}

export function AttributesEditor({ rows, onChange, max = 10 }: { rows: AttrRow[]; onChange: (rows: AttrRow[]) => void; max?: number }) {
  const set = (i: number, patch: Partial<AttrRow>) => onChange(rows.map((r, j) => (j === i ? { ...r, ...patch } : r)))
  return (
    <div className="flex flex-col gap-2">
      {rows.length > 0 && (
        <div className="text-muted-foreground hidden grid-cols-[1fr_7rem_1fr_2rem] gap-2 text-xs font-medium sm:grid">
          <span>Name</span>
          <span>Type</span>
          <span>Value</span>
        </div>
      )}
      {rows.map((r, i) => (
        <div key={i} className="grid grid-cols-[1fr_6.5rem_2rem] gap-2 sm:grid-cols-[1fr_7rem_1fr_2rem]">
          <Input value={r.name} onChange={(e) => set(i, { name: e.target.value })} placeholder="Name" aria-label="Attribute name" className="h-8" />
          <Select value={r.type} onValueChange={(v) => set(i, { type: v as AttrRow["type"] })}>
            <SelectTrigger size="sm" className="w-full" aria-label="Attribute type">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="String">String</SelectItem>
              <SelectItem value="Number">Number</SelectItem>
            </SelectContent>
          </Select>
          <Input
            value={r.value}
            onChange={(e) => set(i, { value: e.target.value })}
            placeholder="Value"
            aria-label="Attribute value"
            inputMode={r.type === "Number" ? "decimal" : undefined}
            className="col-span-2 row-start-2 h-8 sm:col-span-1 sm:row-start-auto"
          />
          <Button
            type="button"
            variant="ghost"
            size="icon"
            className="col-start-3 row-start-1 size-8 sm:col-start-auto"
            onClick={() => onChange(rows.filter((_, j) => j !== i))}
            aria-label="Remove attribute"
          >
            <X />
          </Button>
        </div>
      ))}
      <div>
        <Button type="button" variant="outline" size="sm" onClick={() => onChange([...rows, newAttrRow()])} disabled={rows.length >= max}>
          <Plus /> Add attribute
        </Button>
      </div>
    </div>
  )
}

// ---- message body / detail ----

/** prettyBody pretty-prints JSON bodies; other text is returned as-is. */
export function prettyBody(body: string): { text: string; json: boolean } {
  const t = body.trim()
  if (t.startsWith("{") || t.startsWith("[")) {
    try {
      return { text: JSON.stringify(JSON.parse(t), null, 2), json: true }
    } catch {
      // not JSON
    }
  }
  return { text: body, json: false }
}

export function bodyPreview(body: string, max = 80) {
  const one = body.replace(/\s+/g, " ").trim()
  return one.length > max ? `${one.slice(0, max)}…` : one
}

export function AttributesTable({ attrs }: { attrs?: Record<string, MessageAttribute> | null }) {
  const entries = Object.entries(attrs ?? {})
  if (!entries.length) return <p className="text-muted-foreground text-sm">No message attributes.</p>
  return (
    <div className="overflow-x-auto rounded-md border">
      <table className="w-full text-sm">
        <thead>
          <tr className="bg-muted/40 border-b">
            <th className="text-muted-foreground px-3 py-1.5 text-left text-xs font-semibold">Name</th>
            <th className="text-muted-foreground px-3 py-1.5 text-left text-xs font-semibold">Type</th>
            <th className="text-muted-foreground px-3 py-1.5 text-left text-xs font-semibold">Value</th>
          </tr>
        </thead>
        <tbody>
          {entries.map(([k, a]) => (
            <tr key={k} className="border-b last:border-0">
              <td className="px-3 py-1.5 font-mono text-[13px]">{k}</td>
              <td className="px-3 py-1.5">{a.data_type}</td>
              <td className="px-3 py-1.5 font-mono text-[13px] break-all">{a.string_value}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  )
}

export interface MessageView {
  message_id: string
  body: string
  receipt_handle?: string
  md5_of_body?: string
  sent_at?: string | number
  /** SQS system attributes (received messages) */
  attributes?: Record<string, string>
  message_attributes?: Record<string, MessageAttribute> | null
  extra?: { label: string; value: ReactNode }[]
}

const TIMESTAMP_ATTRS = new Set(["SentTimestamp", "ApproximateFirstReceiveTimestamp"])

/** MessageDetailDialog shows a message's full body (pretty JSON), attributes and receipt handle. */
export function MessageDetailDialog({ message, onClose, actions }: { message: MessageView | null; onClose: () => void; actions?: ReactNode }) {
  const body = message ? prettyBody(message.body) : { text: "", json: false }
  return (
    <Dialog open={!!message} onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="max-h-[90vh] overflow-y-auto sm:max-w-2xl">
        <DialogHeader>
          <DialogTitle>Message details</DialogTitle>
          <DialogDescription className="font-mono text-[13px] break-all">{message?.message_id}</DialogDescription>
        </DialogHeader>
        {message && (
          <div className="flex min-w-0 flex-col gap-4">
            <div className="flex min-w-0 flex-col gap-1.5">
              <div className="flex items-center justify-between gap-2">
                <span className="text-sm font-medium">
                  Body{" "}
                  <span className="text-muted-foreground text-xs font-normal">
                    ({formatBytes(new TextEncoder().encode(message.body).length)}
                    {body.json ? ", JSON" : ""})
                  </span>
                </span>
                <CopyButton value={message.body} size="sm" />
              </div>
              <pre className="bg-muted/50 max-h-80 overflow-auto rounded-md border p-3 font-mono text-[12.5px] whitespace-pre-wrap break-all">{body.text}</pre>
            </div>
            <KeyValueGrid
              columns={2}
              items={[
                { label: "Message ID", value: <CopyableText value={message.message_id} /> },
                ...(message.sent_at ? [{ label: "Sent", value: formatDate(message.sent_at) }] : []),
                ...(message.md5_of_body ? [{ label: "MD5 of body", value: <span className="font-mono text-[13px]">{message.md5_of_body}</span> }] : []),
                ...(message.extra ?? []),
                ...Object.entries(message.attributes ?? {}).map(([k, v]) => ({
                  label: k,
                  value: TIMESTAMP_ATTRS.has(k) ? formatDate(Number(v)) : <span className="font-mono text-[13px] break-all">{v}</span>,
                })),
                ...(message.receipt_handle
                  ? [{ label: "Receipt handle", value: <CopyableText value={message.receipt_handle} className="w-full" />, wide: true }]
                  : []),
              ]}
            />
            <div className="flex flex-col gap-1.5">
              <span className="text-sm font-medium">Message attributes</span>
              <AttributesTable attrs={message.message_attributes} />
            </div>
            {actions && <div className="flex flex-wrap justify-end gap-2 border-t pt-4">{actions}</div>}
          </div>
        )}
      </DialogContent>
    </Dialog>
  )
}
