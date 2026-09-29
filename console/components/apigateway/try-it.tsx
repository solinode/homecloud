"use client"

import { useEffect, useMemo, useRef, useState } from "react"
import { AlertCircle, Info, Loader2, Send, Square } from "lucide-react"

import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select"
import { Textarea } from "@/components/ui/textarea"
import { CopyButton } from "@/components/console/copy-button"
import { Field } from "@/components/console/form-field"
import { Section } from "@/components/console/section"
import { TagsEditor, type TagRow } from "@/components/console/tags-editor"
import { formatBytes, formatNumber } from "@/lib/format"
import type { HttpApi } from "@/lib/types"
import { cn } from "@/lib/utils"

const METHODS = ["GET", "POST", "PUT", "PATCH", "DELETE", "HEAD", "OPTIONS"]

export interface TryPreset {
  method: string
  path: string
  /** changes on every click so the same route can be re-applied */
  nonce: number
}

interface Result {
  status: number
  statusText: string
  ms: number
  headers: [string, string][]
  body: string
  size: number
}

/** params lists the {name} / {name+} placeholders of a route path. */
function params(path: string): { name: string; greedy: boolean }[] {
  const out: { name: string; greedy: boolean }[] = []
  for (const m of path.matchAll(/\{([^}/]+?)(\+?)\}/g)) out.push({ name: m[1], greedy: m[2] === "+" })
  return out
}

function resolvePath(path: string, values: Record<string, string>): string {
  return path.replace(/\{([^}/]+?)(\+?)\}/g, (_, name: string, plus: string) => {
    const v = values[name] ?? ""
    return plus ? v.split("/").map(encodeURIComponent).join("/") : encodeURIComponent(v)
  })
}

function statusTone(s: number) {
  if (s >= 500) return "text-red-700 bg-red-50 ring-red-600/20 dark:text-red-300 dark:bg-red-500/10 dark:ring-red-400/20"
  if (s >= 400) return "text-amber-700 bg-amber-50 ring-amber-600/20 dark:text-amber-300 dark:bg-amber-500/10 dark:ring-amber-400/20"
  if (s >= 300) return "text-blue-700 bg-blue-50 ring-blue-600/20 dark:text-blue-300 dark:bg-blue-500/10 dark:ring-blue-400/20"
  return "text-emerald-700 bg-emerald-50 ring-emerald-600/20 dark:text-emerald-300 dark:bg-emerald-500/10 dark:ring-emerald-400/20"
}

const shellQuote = (s: string) => `'${s.replace(/'/g, `'\\''`)}'`

export function TryIt({ api: target, preset }: { api: HttpApi; preset: TryPreset | null }) {
  const [method, setMethod] = useState("GET")
  const [path, setPath] = useState("/")
  const [values, setValues] = useState<Record<string, string>>({})
  const [query, setQuery] = useState("")
  const [headers, setHeaders] = useState<TagRow[]>([])
  const [body, setBody] = useState("")
  const [pending, setPending] = useState(false)
  const [result, setResult] = useState<Result | null>(null)
  const [netError, setNetError] = useState<string | null>(null)
  const abort = useRef<AbortController | null>(null)
  const panel = useRef<HTMLDivElement>(null)

  useEffect(() => {
    if (!preset) return
    setMethod(preset.method === "ANY" ? "GET" : preset.method)
    setPath(preset.path)
    setValues({})
    setResult(null)
    setNetError(null)
    panel.current?.scrollIntoView({ behavior: "smooth", block: "start" })
  }, [preset])

  const ps = useMemo(() => params(path), [path])
  const hasBody = method !== "GET" && method !== "HEAD"
  const resolved = resolvePath(path.trim() || "/", values)
  const qs = query.trim().replace(/^\?/, "")
  const url = `${target.endpoint}${resolved.startsWith("/") ? resolved : `/${resolved}`}${qs ? `?${qs}` : ""}`
  const missing = ps.filter((p) => !values[p.name]?.trim())

  const reqHeaders = () => {
    const h: Record<string, string> = {}
    for (const r of headers) if (r.key.trim()) h[r.key.trim()] = r.value
    if (hasBody && body.trim() && !Object.keys(h).some((k) => k.toLowerCase() === "content-type")) {
      try {
        JSON.parse(body)
        h["Content-Type"] = "application/json"
      } catch {
        h["Content-Type"] = "text/plain"
      }
    }
    return h
  }

  const curl = useMemo(() => {
    const parts = [`curl -i -X ${method} ${shellQuote(url)}`]
    for (const [k, v] of Object.entries(reqHeaders())) parts.push(`  -H ${shellQuote(`${k}: ${v}`)}`)
    if (hasBody && body) parts.push(`  --data ${shellQuote(body)}`)
    return parts.join(" \\\n")
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [method, url, headers, body, hasBody])

  const send = async (e: React.FormEvent) => {
    e.preventDefault()
    abort.current?.abort()
    const ctl = new AbortController()
    abort.current = ctl
    setPending(true)
    setNetError(null)
    setResult(null)
    const t0 = performance.now()
    try {
      const res = await fetch(url, { method, headers: reqHeaders(), body: hasBody && body ? body : undefined, signal: ctl.signal, cache: "no-store" })
      const buf = await res.arrayBuffer()
      const ms = performance.now() - t0
      let text = new TextDecoder().decode(buf)
      try {
        if (text.trim()) text = JSON.stringify(JSON.parse(text), null, 2)
      } catch {
        // not JSON: show as-is
      }
      const hs: [string, string][] = []
      res.headers.forEach((v, k) => hs.push([k, v]))
      setResult({ status: res.status, statusText: res.statusText, ms, headers: hs.sort(([a], [b]) => a.localeCompare(b)), body: text, size: buf.byteLength })
    } catch (err) {
      if (err instanceof DOMException && err.name === "AbortError") setNetError("Request cancelled.")
      else setNetError(err instanceof Error ? err.message : String(err))
    } finally {
      setPending(false)
    }
  }

  return (
    <div ref={panel} className="scroll-mt-4">
      <Section title="Try it" description="Send a request from this browser to the API's public invoke URL.">
        <form onSubmit={send} className="flex flex-col gap-4">
          <div className="grid grid-cols-[7.5rem_minmax(0,1fr)] gap-2">
            <Select value={method} onValueChange={setMethod}>
              <SelectTrigger aria-label="Method" className="w-full font-mono">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {METHODS.map((m) => (
                  <SelectItem key={m} value={m} className="font-mono">
                    {m}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
            <Input aria-label="Path" value={path} onChange={(e) => setPath(e.target.value)} className="font-mono" placeholder="/items/{id}" spellCheck={false} />
          </div>

          {ps.length > 0 && (
            <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
              {ps.map((p) => (
                <Field key={p.name} label={<span className="font-mono">{`{${p.name}${p.greedy ? "+" : ""}}`}</span>} htmlFor={`param-${p.name}`}>
                  <Input
                    id={`param-${p.name}`}
                    value={values[p.name] ?? ""}
                    onChange={(e) => setValues({ ...values, [p.name]: e.target.value })}
                    placeholder={p.greedy ? "any/sub/path" : "value"}
                    className="h-8 font-mono"
                  />
                </Field>
              ))}
            </div>
          )}

          <Field label="Query string" htmlFor="try-qs" optional>
            <Input id="try-qs" value={query} onChange={(e) => setQuery(e.target.value)} placeholder="name=HomeCloud&limit=10" className="font-mono" spellCheck={false} />
          </Field>

          <Field label="Headers" optional>
            <TagsEditor rows={headers} onChange={setHeaders} keyPlaceholder="Header" valuePlaceholder="Value" addLabel="Add header" max={20} />
          </Field>

          {hasBody && (
            <Field label="Body" htmlFor="try-body" optional help="JSON bodies are sent with Content-Type: application/json unless you set it.">
              <Textarea
                id="try-body"
                rows={6}
                value={body}
                onChange={(e) => setBody(e.target.value)}
                className="font-mono text-[13px]"
                spellCheck={false}
                placeholder='{"name": "HomeCloud"}'
              />
            </Field>
          )}

          <div className="bg-muted/40 flex flex-col gap-1 rounded-md border p-2.5">
            <span className="text-muted-foreground text-xs">Request URL</span>
            <span className="font-mono text-[13px] break-all">
              <span className="font-semibold">{method}</span> {url}
            </span>
          </div>

          <p className="text-muted-foreground flex gap-1.5 text-xs">
            <Info className="mt-px size-3.5 shrink-0" />
            <span>
              Browsers enforce CORS: only the Authorization, Content-Type and X-HC-Meta-* request headers pass the preflight, and only CORS-safelisted
              response headers (Content-Type, Content-Length, Cache-Control, ...) are readable here. Use the curl command to see everything.
            </span>
          </p>

          <div className="flex flex-wrap items-center gap-2">
            <Button type="submit" disabled={pending || missing.length > 0}>
              {pending ? <Loader2 className="animate-spin" /> : <Send />} Send
            </Button>
            {pending && (
              <Button type="button" variant="outline" onClick={() => abort.current?.abort()}>
                <Square /> Cancel
              </Button>
            )}
            <CopyButton value={curl} size="sm" label="Copy as curl" />
            {missing.length > 0 && <span className="text-muted-foreground text-xs">Fill in {missing.map((p) => `{${p.name}}`).join(", ")}</span>}
          </div>
        </form>

        {netError && (
          <div className="border-destructive/30 bg-destructive/5 text-destructive mt-4 flex items-start gap-2 rounded-md border p-3 text-sm">
            <AlertCircle className="mt-0.5 size-4 shrink-0" />
            <span>
              <span className="font-medium">No response: {netError}</span>
              <br />
              <span className="text-destructive/80 text-xs">
                The server may be unreachable, or the browser blocked the request (a custom header that fails the CORS preflight, or an http endpoint
                called from an https page). Try the curl command.
              </span>
            </span>
          </div>
        )}

        {result && (
          <div className="mt-4 flex flex-col gap-3 border-t pt-4">
            <div className="flex flex-wrap items-center gap-3 text-sm">
              <span className={cn("rounded px-2 py-0.5 font-mono font-semibold ring-1 ring-inset", statusTone(result.status))}>
                {result.status} {result.statusText}
              </span>
              <span className="text-muted-foreground">{formatNumber(result.ms, 0)} ms</span>
              <span className="text-muted-foreground">{formatBytes(result.size)}</span>
            </div>
            {result.headers.length > 0 && (
              <div className="flex flex-col gap-1">
                <h3 className="text-sm font-medium">Readable response headers</h3>
                <dl className="bg-muted/40 grid grid-cols-[auto_minmax(0,1fr)] gap-x-4 gap-y-0.5 rounded-md border p-2.5 font-mono text-[12.5px]">
                  {result.headers.map(([k, v]) => (
                    <div key={k} className="contents">
                      <dt className="text-muted-foreground">{k}</dt>
                      <dd className="break-all">{v}</dd>
                    </div>
                  ))}
                </dl>
              </div>
            )}
            <div className="flex flex-col gap-1">
              <div className="flex items-center justify-between gap-2">
                <h3 className="text-sm font-medium">Body</h3>
                {result.body && <CopyButton value={result.body} label="Copy body" />}
              </div>
              {result.body ? (
                <pre className="bg-muted/50 max-h-96 overflow-auto rounded-md border p-3 font-mono text-[12.5px] leading-5 break-all whitespace-pre-wrap">
                  {result.body}
                </pre>
              ) : (
                <p className="text-muted-foreground text-sm">Empty body.</p>
              )}
            </div>
          </div>
        )}
      </Section>
    </div>
  )
}
