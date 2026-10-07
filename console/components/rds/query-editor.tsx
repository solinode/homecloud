"use client"

import { useEffect, useRef, useState } from "react"
import { AlertCircle, CheckCircle2, History, Loader2, Play, Trash2 } from "lucide-react"

import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover"
import { Textarea } from "@/components/ui/textarea"
import { CodeBlock } from "@/components/console/code-block"
import { CopyButton } from "@/components/console/copy-button"
import { Field } from "@/components/console/form-field"
import { Section } from "@/components/console/section"
import { Tag } from "@/components/console/tag"
import { api, errorMessage, seg } from "@/lib/api"
import { formatNumber, formatTime, pluralize } from "@/lib/format"
import type { DbInstance, DbQueryResult } from "@/lib/types"
import { cn } from "@/lib/utils"
import { DB_INSTANCES_PATH } from "./shared"

export interface HistoryEntry {
  text: string
  db?: string
  at: number
  ok: boolean
  ms?: number
}

const MAX_HISTORY = 25
const MAX_GRID_ROWS = 1000

/** useQueryHistory keeps a small per-database history in localStorage (best effort). */
export function useQueryHistory(id: string) {
  const key = `homecloud.rds.history.${id}`
  const [items, setItems] = useState<HistoryEntry[]>([])
  useEffect(() => {
    try {
      const raw = window.localStorage.getItem(key)
      setItems(raw ? (JSON.parse(raw) as HistoryEntry[]) : [])
    } catch {
      setItems([])
    }
  }, [key])
  const save = (next: HistoryEntry[]) => {
    setItems(next)
    try {
      window.localStorage.setItem(key, JSON.stringify(next))
    } catch {
      // storage unavailable; history lives for this page only
    }
  }
  return {
    items,
    add: (e: HistoryEntry) => save([e, ...items.filter((x) => x.text !== e.text || x.db !== e.db)].slice(0, MAX_HISTORY)),
    clear: () => save([]),
  }
}

const EXAMPLES: Record<string, { label: string; text: string }[]> = {
  postgres: [
    { label: "List tables", text: "SELECT table_schema, table_name\nFROM information_schema.tables\nWHERE table_schema NOT IN ('pg_catalog', 'information_schema')\nORDER BY 1, 2;" },
    { label: "Version", text: "SELECT version();" },
    { label: "Databases", text: "SELECT datname, pg_size_pretty(pg_database_size(datname)) AS size FROM pg_database;" },
    { label: "Connections", text: "SELECT pid, usename, datname, state, query FROM pg_stat_activity WHERE datname IS NOT NULL;" },
  ],
  mysql: [
    { label: "List tables", text: "SHOW TABLES;" },
    { label: "Version", text: "SELECT VERSION();" },
    { label: "Databases", text: "SHOW DATABASES;" },
    { label: "Processes", text: "SHOW PROCESSLIST;" },
  ],
  mongodb: [
    { label: "Collections", text: "db.getCollectionNames()" },
    { label: "Stats", text: "db.stats()" },
    { label: "Insert", text: 'db.items.insertOne({ name: "widget", qty: 5 })' },
    { label: "Find", text: "db.items.find().limit(20).toArray()" },
  ],
}
EXAMPLES.mariadb = EXAMPLES.mysql

type View =
  | { kind: "error"; text: string }
  | { kind: "grid"; columns: string[]; rows: string[][] }
  | { kind: "message"; text: string }
  | { kind: "raw"; text: string }

/** classify turns a DbQueryResult into what to render, papering over client quirks per engine. */
function classify(engine: string, r: DbQueryResult): View {
  const out = r.output ?? ""
  // The mysql client runs through `| grep -v`, which exits 1 when the statement printed nothing.
  if (engine === "mysql" && r.exit_code === 1 && !out.trim() && !r.error?.trim()) return { kind: "message", text: "Statement executed. No rows returned." }
  if (r.exit_code !== 0) return { kind: "error", text: r.error?.trim() || out.trim() || `The client exited with code ${r.exit_code}.` }
  // MySQL/MariaDB print errors on stdout (2>&1).
  if ((engine === "mysql" || engine === "mariadb") && /^ERROR \d+/.test(out)) return { kind: "error", text: out.trim() }
  if (r.columns?.length) {
    const rows = r.rows ?? []
    // psql prints a command tag ("CREATE TABLE", "INSERT 0 1") for statements without rows.
    if (engine === "postgres" && rows.length === 0 && r.columns.length === 1 && /^[A-Z]+( [A-Z]+)*( \d+)*$/.test(r.columns[0])) {
      return { kind: "message", text: r.columns[0] }
    }
    return { kind: "grid", columns: r.columns, rows }
  }
  if (!out.trim()) return { kind: "message", text: "Statement executed. No output." }
  return { kind: "raw", text: out }
}

/** QueryEditor runs SQL (PostgreSQL, MySQL, MariaDB) or mongosh JavaScript (MongoDB) against a database. */
export function QueryEditor({ inst }: { inst: DbInstance }) {
  const mongo = inst.engine === "mongodb"
  const [text, setText] = useState(mongo ? "db.getCollectionNames()" : "")
  const [database, setDatabase] = useState(inst.db_name ?? "")
  const [running, setRunning] = useState(false)
  const [result, setResult] = useState<{ r: DbQueryResult; ran: string; at: number } | null>(null)
  const [reqError, setReqError] = useState<string | null>(null)
  const history = useQueryHistory(inst.id)
  const ref = useRef<HTMLTextAreaElement>(null)
  const available = inst.status === "available"

  const run = async () => {
    const el = ref.current
    const selected = el && el.selectionStart !== el.selectionEnd ? text.slice(el.selectionStart, el.selectionEnd) : ""
    const q = (selected || text).trim()
    if (!q || running || !available) return
    setRunning(true)
    setReqError(null)
    try {
      const r = await api.post<DbQueryResult>(`${DB_INSTANCES_PATH}/${seg(inst.id)}/query`, { sql: q, database: database.trim() || undefined })
      setResult({ r, ran: q, at: Date.now() })
      history.add({ text: q, db: database.trim() || undefined, at: Date.now(), ok: classify(inst.engine, r).kind !== "error", ms: r.duration_ms })
    } catch (e) {
      setResult(null)
      setReqError(errorMessage(e))
      history.add({ text: q, db: database.trim() || undefined, at: Date.now(), ok: false })
    } finally {
      setRunning(false)
    }
  }

  const view = result ? classify(inst.engine, result.r) : null
  const examples = EXAMPLES[inst.engine] ?? []

  return (
    <div className="flex flex-col gap-4">
      {!available && (
        <Alert variant="warning">
          <AlertCircle />
          <AlertTitle>The database is {inst.status}</AlertTitle>
          <AlertDescription>Queries can run only while the database is available.</AlertDescription>
        </Alert>
      )}
      <Section
        title={mongo ? "Shell (mongosh)" : "Query editor"}
        description={
          mongo
            ? "Runs JavaScript with mongosh --eval as the master user, against the database below. The value of the last expression is printed."
            : "Runs SQL as the master user. Separate statements with semicolons; the result of the last statement is shown."
        }
        actions={
          <Popover>
            <PopoverTrigger asChild>
              <Button variant="outline" size="sm" disabled={!history.items.length}>
                <History /> History{history.items.length ? ` (${history.items.length})` : ""}
              </Button>
            </PopoverTrigger>
            <PopoverContent align="end" className="w-[min(92vw,28rem)] p-0">
              <div className="flex items-center justify-between border-b px-3 py-2">
                <span className="text-sm font-medium">Recent {mongo ? "commands" : "queries"}</span>
                <Button variant="ghost" size="sm" className="h-7" onClick={history.clear}>
                  <Trash2 /> Clear
                </Button>
              </div>
              <ul className="max-h-72 overflow-y-auto">
                {history.items.map((h) => (
                  <li key={`${h.at}-${h.text}`}>
                    <button
                      type="button"
                      onClick={() => (setText(h.text), h.db && setDatabase(h.db))}
                      className="hover:bg-muted/60 flex w-full flex-col gap-0.5 border-b px-3 py-2 text-left last:border-0"
                    >
                      <span className="line-clamp-2 font-mono text-xs break-all whitespace-pre-wrap">{h.text}</span>
                      <span className="text-muted-foreground flex items-center gap-1.5 text-[11px]">
                        {h.ok ? <CheckCircle2 className="text-success size-3" /> : <AlertCircle className="text-danger size-3" />}
                        {formatTime(h.at)}
                        {h.db && <> · {h.db}</>}
                        {h.ms !== undefined && <> · {h.ms} ms</>}
                      </span>
                    </button>
                  </li>
                ))}
              </ul>
            </PopoverContent>
          </Popover>
        }
      >
        <div className="flex flex-col gap-4">
          <Field label={mongo ? "JavaScript" : "SQL"} htmlFor="q-text" help="Ctrl/⌘ + Enter runs the editor, or only the selected text if any.">
            <Textarea
              id="q-text"
              ref={ref}
              value={text}
              onChange={(e) => setText(e.target.value)}
              onKeyDown={(e) => {
                if ((e.metaKey || e.ctrlKey) && e.key === "Enter") {
                  e.preventDefault()
                  run()
                }
              }}
              rows={8}
              spellCheck={false}
              placeholder={mongo ? "db.getCollectionNames()" : "SELECT * FROM my_table LIMIT 100;"}
              className="bg-muted/30 min-h-40 font-mono text-[13px] leading-relaxed"
            />
          </Field>
          {examples.length > 0 && (
            <div className="flex flex-wrap items-center gap-1.5">
              <span className="text-muted-foreground text-xs">Examples:</span>
              {examples.map((x) => (
                <button key={x.label} type="button" onClick={() => setText(x.text)}>
                  <Tag mono={false} className="hover:bg-accent hover:text-foreground cursor-pointer transition-colors">
                    {x.label}
                  </Tag>
                </button>
              ))}
            </div>
          )}
          <div className="flex flex-wrap items-end gap-3">
            <Field label="Database" htmlFor="q-db" optional className="w-full sm:w-56">
              <Input id="q-db" value={database} onChange={(e) => setDatabase(e.target.value)} placeholder={inst.db_name} className="font-mono text-[13px]" />
            </Field>
            <Button onClick={run} disabled={running || !available || !text.trim()} className="sm:ml-auto">
              {running ? <Loader2 className="animate-spin" /> : <Play />}
              Run
            </Button>
          </div>
        </div>
      </Section>

      {reqError && (
        <Alert variant="destructive">
          <AlertCircle />
          <AlertTitle>Request failed</AlertTitle>
          <AlertDescription>{reqError}</AlertDescription>
        </Alert>
      )}

      {result && view && (
        <Section
          title="Results"
          description={
            <span className="flex flex-wrap gap-x-2">
              {view.kind === "grid" && <span>{pluralize(view.rows.length, "row")}</span>}
              <span>{formatNumber(result.r.duration_ms, 0)} ms</span>
              <span>· {formatTime(result.at)}</span>
            </span>
          }
          actions={result.r.output ? <CopyButton value={result.r.output} size="sm" label={view.kind === "grid" ? "Copy raw" : "Copy"} /> : undefined}
          flush={view.kind === "grid"}
        >
          {view.kind === "error" ? (
            <CodeBlock code={view.text} tone="danger" wrap maxHeight="20rem" noCopy />
          ) : view.kind === "message" ? (
            <p className="flex items-center gap-2 text-sm">
              <CheckCircle2 className="text-success size-4" />
              <span className="font-mono text-[13px]">{view.text}</span>
            </p>
          ) : view.kind === "raw" ? (
            <CodeBlock code={view.text} wrap maxHeight="30rem" noCopy />
          ) : (
            <ResultGrid columns={view.columns} rows={view.rows} nullText={inst.engine === "postgres" ? undefined : "NULL"} />
          )}
        </Section>
      )}
    </div>
  )
}

function ResultGrid({ columns, rows, nullText }: { columns: string[]; rows: string[][]; nullText?: string }) {
  const shown = rows.slice(0, MAX_GRID_ROWS)
  return (
    <div className="flex flex-col">
      <div className="max-h-[480px] overflow-auto">
        <table className="w-full border-collapse text-sm">
          <thead className="bg-muted sticky top-0 z-10">
            <tr>
              <th className="text-muted-foreground w-10 border-b px-3 py-2 text-right text-xs font-medium">#</th>
              {columns.map((c, i) => (
                <th key={i} className="border-b border-l px-3 py-2 text-left font-mono text-xs font-semibold whitespace-nowrap">
                  {c}
                </th>
              ))}
            </tr>
          </thead>
          <tbody>
            {shown.length === 0 ? (
              <tr>
                <td colSpan={columns.length + 1} className="text-muted-foreground px-3 py-6 text-center text-sm">
                  No rows returned.
                </td>
              </tr>
            ) : (
              shown.map((r, i) => (
                <tr key={i} className="hover:bg-muted/40 border-b last:border-0">
                  <td className="text-muted-foreground px-3 py-1.5 text-right font-mono text-xs">{i + 1}</td>
                  {columns.map((_, j) => {
                    const v = r[j] ?? ""
                    const isNull = nullText !== undefined && v === nullText
                    return (
                      <td
                        key={j}
                        title={v.length > 60 ? v : undefined}
                        className={cn("max-w-[28rem] truncate border-l px-3 py-1.5 font-mono text-[12.5px] whitespace-pre", isNull && "text-muted-foreground italic")}
                      >
                        {v}
                      </td>
                    )
                  })}
                </tr>
              ))
            )}
          </tbody>
        </table>
      </div>
      {rows.length > MAX_GRID_ROWS && (
        <p className="text-muted-foreground border-t px-4 py-2 text-xs">
          Showing the first {formatNumber(MAX_GRID_ROWS)} of {formatNumber(rows.length)} rows. Add a LIMIT to narrow the result.
        </p>
      )}
    </div>
  )
}
