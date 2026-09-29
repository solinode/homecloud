"use client"

import { useEffect, useRef, useState } from "react"
import { AlertCircle, Eraser } from "lucide-react"

import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert"
import { Button } from "@/components/ui/button"
import { Section } from "@/components/console/section"
import { api, errorMessage, seg } from "@/lib/api"
import type { DbInstance, DbQueryResult } from "@/lib/types"
import { cn } from "@/lib/utils"
import { useQueryHistory } from "./query-editor"
import { DB_INSTANCES_PATH } from "./shared"

interface Line {
  n: number
  cmd: string
  out?: string
  error?: boolean
  pending?: boolean
  ms?: number
}

const EXAMPLES = ["PING", "SET greeting hello", "GET greeting", "KEYS *", "INFO memory", "DBSIZE"]
const BLOCKING = /^(MONITOR|SUBSCRIBE|PSUBSCRIBE|SSUBSCRIBE|SYNC|PSYNC)$/i
const REDIS_ERR = /^(ERR|WRONGTYPE|NOAUTH|NOPERM|EXECABORT|BUSY|NOSCRIPT|OOM|READONLY|LOADING|MOVED|ASK|CROSSSLOT|CLUSTERDOWN|MISCONF|NOREPLICAS)\b/

/**
 * splitArgs tokenizes a command line the way redis-cli does: whitespace
 * separated, with "double quotes" (backslash escapes) and 'single quotes'.
 */
export function splitArgs(line: string): string[] | null {
  const out: string[] = []
  let i = 0
  while (i < line.length) {
    while (i < line.length && /\s/.test(line[i])) i++
    if (i >= line.length) break
    let cur = ""
    while (i < line.length && !/\s/.test(line[i])) {
      const c = line[i]
      if (c === '"') {
        i++
        while (i < line.length && line[i] !== '"') {
          if (line[i] === "\\" && i + 1 < line.length) {
            const nx = line[i + 1]
            cur += { n: "\n", r: "\r", t: "\t", '"': '"', "\\": "\\" }[nx] ?? nx
            i += 2
          } else cur += line[i++]
        }
        if (i >= line.length) return null
        i++
      } else if (c === "'") {
        i++
        while (i < line.length && line[i] !== "'") cur += line[i++]
        if (i >= line.length) return null
        i++
      } else cur += line[i++]
    }
    out.push(cur)
  }
  return out
}

/** The API passes the text to a shell; quote each argument so globs (KEYS *) and metacharacters reach redis-cli verbatim. */
const shellQuote = (args: string[]) => args.map((a) => `'${a.replace(/'/g, `'\\''`)}'`).join(" ")

/** CommandConsole is a redis-cli style console for Redis and Valkey clusters. */
export function CommandConsole({ inst }: { inst: DbInstance }) {
  const [lines, setLines] = useState<Line[]>([])
  const [input, setInput] = useState("")
  const [pos, setPos] = useState(-1)
  const [busy, setBusy] = useState(false)
  const history = useQueryHistory(inst.id)
  const scroller = useRef<HTMLDivElement>(null)
  const inputRef = useRef<HTMLInputElement>(null)
  const counter = useRef(0)
  const available = inst.status === "available"
  const cli = inst.engine === "valkey" ? "valkey-cli" : "redis-cli"
  const prompt = `${inst.endpoint.address || inst.id}:${inst.endpoint.port}>`

  useEffect(() => {
    const el = scroller.current
    if (el) el.scrollTop = el.scrollHeight
  }, [lines])

  const push = (l: Omit<Line, "n">) => {
    const n = ++counter.current
    setLines((ls) => [...ls.slice(-199), { ...l, n }])
    return n
  }
  const patch = (n: number, p: Partial<Line>) => setLines((ls) => ls.map((l) => (l.n === n ? { ...l, ...p } : l)))

  const run = async (raw: string) => {
    const cmd = raw.trim()
    setInput("")
    setPos(-1)
    if (!cmd) return
    if (/^clear$/i.test(cmd)) {
      setLines([])
      return
    }
    const args = splitArgs(cmd)
    if (!args) {
      push({ cmd, out: "Invalid argument(s): unbalanced quotes", error: true })
      return
    }
    if (BLOCKING.test(args[0])) {
      push({ cmd, out: `${args[0].toUpperCase()} streams forever and is not supported in the console. Connect with ${cli} instead.`, error: true })
      return
    }
    const n = push({ cmd, pending: true })
    setBusy(true)
    try {
      const r = await api.post<DbQueryResult>(`${DB_INSTANCES_PATH}/${seg(inst.id)}/query`, { sql: shellQuote(args) })
      const out = (r.output ?? "").replace(/\r/g, "").replace(/\n+$/, "")
      const err = r.exit_code !== 0 || REDIS_ERR.test(out)
      patch(n, { pending: false, out: r.exit_code !== 0 ? r.error || out || `exit code ${r.exit_code}` : out, error: err, ms: r.duration_ms })
      history.add({ text: cmd, at: Date.now(), ok: !err, ms: r.duration_ms })
    } catch (e) {
      patch(n, { pending: false, out: errorMessage(e), error: true })
    } finally {
      setBusy(false)
      requestAnimationFrame(() => inputRef.current?.focus())
    }
  }

  const onKey = (e: React.KeyboardEvent<HTMLInputElement>) => {
    const items = history.items
    if (e.key === "Enter") {
      e.preventDefault()
      if (!busy) run(input)
    } else if (e.key === "ArrowUp") {
      if (!items.length) return
      e.preventDefault()
      const p = Math.min(pos + 1, items.length - 1)
      setPos(p)
      setInput(items[p].text)
    } else if (e.key === "ArrowDown") {
      e.preventDefault()
      const p = pos - 1
      setPos(Math.max(p, -1))
      setInput(p >= 0 ? items[p].text : "")
    } else if (e.key === "l" && e.ctrlKey) {
      e.preventDefault()
      setLines([])
    }
  }

  return (
    <div className="flex flex-col gap-4">
      {!available && (
        <Alert>
          <AlertCircle />
          <AlertTitle>The cluster is {inst.status}</AlertTitle>
          <AlertDescription>Commands can run only while the cluster is available.</AlertDescription>
        </Alert>
      )}
      <Section
        title="Command console"
        description={`Runs ${inst.engine === "valkey" ? "Valkey" : "Redis"} commands with ${cli}, authenticated with the cluster's auth token. Use quotes for values with spaces.`}
        actions={
          <Button variant="outline" size="sm" onClick={() => setLines([])} disabled={!lines.length}>
            <Eraser /> Clear
          </Button>
        }
      >
        <div className="flex flex-col gap-3">
          <div
            className="flex h-[420px] cursor-text flex-col overflow-hidden rounded-md border border-zinc-800 bg-zinc-950 font-mono text-[12.5px] leading-relaxed text-zinc-100"
            onClick={() => window.getSelection()?.toString() || inputRef.current?.focus()}
          >
            <div ref={scroller} className="flex-1 overflow-auto p-3">
              {lines.length === 0 && (
                <p className="text-zinc-500">
                  Connected to {inst.id} ({inst.engine} {inst.engine_version}). Type a command and press Enter. ↑/↓ browse history, Ctrl+L clears.
                </p>
              )}
              {lines.map((l) => (
                <div key={l.n} className="mb-1.5">
                  <div className="break-all whitespace-pre-wrap">
                    <span className="text-emerald-400">&gt; </span>
                    {l.cmd}
                  </div>
                  {l.pending ? (
                    <div className="text-zinc-500">...</div>
                  ) : l.out === "" ? (
                    <div className="text-zinc-500 italic">(nil or empty)</div>
                  ) : (
                    <div className={cn("break-all whitespace-pre-wrap", l.error ? "text-red-400" : "text-zinc-200")}>{l.out}</div>
                  )}
                </div>
              ))}
            </div>
            <div className="flex items-center gap-2 border-t border-zinc-800 px-3 py-2">
              <span className="hidden shrink-0 text-emerald-400 sm:inline">{prompt}</span>
              <span className="shrink-0 text-emerald-400 sm:hidden">&gt;</span>
              <input
                ref={inputRef}
                value={input}
                onChange={(e) => (setInput(e.target.value), setPos(-1))}
                onKeyDown={onKey}
                disabled={!available}
                spellCheck={false}
                autoComplete="off"
                autoCapitalize="off"
                aria-label="Command"
                placeholder={available ? "PING" : ""}
                className="min-w-0 flex-1 bg-transparent text-zinc-100 outline-none placeholder:text-zinc-600 disabled:opacity-50"
              />
            </div>
          </div>
          <div className="flex flex-wrap items-center gap-1.5">
            <span className="text-muted-foreground text-xs">Try:</span>
            {EXAMPLES.map((x) => (
              <button
                key={x}
                type="button"
                disabled={!available || busy}
                onClick={() => run(x)}
                className="bg-muted hover:bg-accent rounded border px-2 py-0.5 font-mono text-xs transition-colors disabled:opacity-50"
              >
                {x}
              </button>
            ))}
          </div>
        </div>
      </Section>
    </div>
  )
}
