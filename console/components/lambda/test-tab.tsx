"use client"

import { useEffect, useMemo, useState } from "react"
import { AlertCircle, CheckCircle2, FilePlus2, Loader2, Play, Save, Trash2 } from "lucide-react"
import { toast } from "sonner"

import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Select, SelectContent, SelectGroup, SelectItem, SelectLabel, SelectTrigger, SelectValue } from "@/components/ui/select"
import { ConfirmDialog } from "@/components/console/confirm-dialog"
import { CopyButton } from "@/components/console/copy-button"
import { ErrorState } from "@/components/console/error-state"
import { Field } from "@/components/console/form-field"
import { JsonEditor, jsonError } from "@/components/console/json-editor"
import { KeyValueGrid } from "@/components/console/key-value"
import { Section } from "@/components/console/section"
import { request } from "@/lib/api"
import { formatNumber } from "@/lib/format"
import { revalidate } from "@/lib/hooks"
import type { InvokeResult, LambdaFunction } from "@/lib/types"
import { cn } from "@/lib/utils"

import { LAMBDA_PATH, fnPath } from "./common"
import { aliasTargets, useAliases, useVersions } from "./versions"

const pretty = (v: unknown) => JSON.stringify(v, null, 2)

function templates(fn: string): { id: string; label: string; body: unknown }[] {
  const now = new Date()
  return [
    { id: "hello", label: "Hello world", body: { name: "HomeCloud" } },
    {
      id: "apigw",
      label: "API Gateway HTTP API (v2)",
      body: {
        version: "2.0",
        routeKey: "GET /hello",
        rawPath: "/hello",
        rawQueryString: "name=HomeCloud",
        headers: { accept: "application/json", "user-agent": "curl/8.0" },
        queryStringParameters: { name: "HomeCloud" },
        requestContext: {
          apiId: "abcdef1234",
          routeKey: "GET /hello",
          stage: "$default",
          timeEpoch: now.getTime(),
          http: { method: "GET", path: "/hello", protocol: "HTTP/1.1", sourceIp: "192.168.1.10", userAgent: "curl/8.0" },
        },
        isBase64Encoded: false,
      },
    },
    {
      id: "sqs",
      label: "SQS message batch",
      body: {
        Records: [
          {
            messageId: "059f36b4-87a3-44ab-83d2-661975830a7d",
            receiptHandle: "AQEBwJnKyrHigUMZj6rYigCgxlaS3SLy0a",
            body: JSON.stringify({ order_id: 42, status: "paid" }),
            attributes: { ApproximateReceiveCount: "1", SentTimestamp: String(now.getTime()) },
            messageAttributes: {},
            eventSource: "aws:sqs",
            eventSourceARN: `arn:aws:sqs:us-east-1:000000000000:${fn}-queue`,
            awsRegion: "us-east-1",
          },
        ],
      },
    },
  ]
}

const storageKey = (fn: string) => `homecloud.lambda.tests.${fn}`

function loadSaved(fn: string): Record<string, string> {
  try {
    const raw = window.localStorage.getItem(storageKey(fn))
    const v = raw ? JSON.parse(raw) : {}
    return v && typeof v === "object" && !Array.isArray(v) ? (v as Record<string, string>) : {}
  } catch {
    return {}
  }
}

function storeSaved(fn: string, v: Record<string, string>) {
  try {
    window.localStorage.setItem(storageKey(fn), JSON.stringify(v))
    return true
  } catch {
    return false
  }
}

const NEW = "__new"

export function TestTab({ fn }: { fn: LambdaFunction }) {
  const tpls = useMemo(() => templates(fn.name), [fn.name])
  const [saved, setSaved] = useState<Record<string, string>>({})
  const [selected, setSelected] = useState(NEW)
  const [eventName, setEventName] = useState("")
  const [body, setBody] = useState(pretty(tpls[0].body))
  const [pending, setPending] = useState(false)
  const [result, setResult] = useState<InvokeResult | null>(null)
  const [invokeError, setInvokeError] = useState<unknown>(null)
  const [deleting, setDeleting] = useState(false)
  const [qualifier, setQualifier] = useState("$LATEST")
  const [invocationType, setInvocationType] = useState<"RequestResponse" | "Event">("RequestResponse")
  const [queued, setQueued] = useState<{ request_id: string; qualifier: string } | null>(null)
  const versions = useVersions(fn.name)
  const aliases = useAliases(fn.name)

  useEffect(() => {
    const s = loadSaved(fn.name)
    setSaved(s)
    const first = Object.keys(s).sort()[0]
    if (first) {
      setSelected(first)
      setEventName(first)
      setBody(s[first])
    }
  }, [fn.name])

  const names = Object.keys(saved).sort((a, b) => a.localeCompare(b))
  const bodyErr = jsonError(body)
  const nameErr = eventName.trim() ? (eventName.trim().length > 64 ? "At most 64 characters" : null) : "Name the event to save it"

  const pick = (v: string) => {
    if (v === NEW) {
      setSelected(NEW)
      setEventName("")
      setBody(pretty(tpls[0].body))
      return
    }
    if (v.startsWith("tpl:")) {
      const t = tpls.find((x) => `tpl:${x.id}` === v)
      if (t) setBody(pretty(t.body))
      return
    }
    setSelected(v)
    setEventName(v)
    setBody(saved[v] ?? "{}")
  }

  const save = () => {
    const n = eventName.trim()
    if (nameErr || bodyErr) {
      toast.error(nameErr ?? "The event is not valid JSON")
      return
    }
    const next = { ...saved, [n]: body }
    if (selected !== NEW && selected !== n) delete next[selected]
    if (!storeSaved(fn.name, next)) {
      toast.error("Could not save: browser storage is unavailable")
      return
    }
    setSaved(next)
    setSelected(n)
    toast.success(`Saved test event ${n}`)
  }

  const remove = () => {
    if (selected === NEW) return
    const next = { ...saved }
    delete next[selected]
    storeSaved(fn.name, next)
    setSaved(next)
    toast.success(`Deleted test event ${selected}`)
    pick(NEW)
  }

  const invoke = async () => {
    if (bodyErr) {
      toast.error("The event is not valid JSON")
      return
    }
    setPending(true)
    setInvokeError(null)
    setQueued(null)
    const query = { qualifier: qualifier === "$LATEST" ? undefined : qualifier, invocation_type: invocationType === "Event" ? "Event" : undefined }
    try {
      // Send the event text verbatim: the API expects the raw event as the body.
      const r = await request<InvokeResult>("POST", `${fnPath(fn.name)}/invoke`, { body, query, headers: { "Content-Type": "application/json" } })
      if (invocationType === "Event") {
        setResult(null)
        setQueued({ request_id: r.request_id, qualifier })
      } else setResult(r)
    } catch (e) {
      setResult(null)
      setInvokeError(e)
    } finally {
      setPending(false)
      revalidate(LAMBDA_PATH)
      revalidate("/api/v1/logs")
    }
  }

  return (
    <div className="flex flex-col gap-4">
      <Section
        title="Test event"
        description="Invoke the function with a JSON event. Saved events are kept in this browser."
        actions={
          <Button size="sm" onClick={invoke} disabled={pending || !!bodyErr}>
            {pending ? <Loader2 className="animate-spin" /> : <Play />} Test
          </Button>
        }
      >
        <div className="flex flex-col gap-4">
          <div className="grid grid-cols-1 gap-3 md:grid-cols-[minmax(0,16rem)_minmax(0,1fr)_auto] md:items-end">
            <Field label="Saved events" htmlFor="test-pick">
              <Select value={selected} onValueChange={pick}>
                <SelectTrigger id="test-pick" className="w-full">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value={NEW}>
                    <FilePlus2 /> New event
                  </SelectItem>
                  {names.length > 0 && (
                    <SelectGroup>
                      <SelectLabel>Saved</SelectLabel>
                      {names.map((n) => (
                        <SelectItem key={n} value={n}>
                          {n}
                        </SelectItem>
                      ))}
                    </SelectGroup>
                  )}
                </SelectContent>
              </Select>
            </Field>
            <Field label="Event name" htmlFor="test-name">
              <Input id="test-name" value={eventName} onChange={(e) => setEventName(e.target.value)} placeholder="my-test-event" maxLength={64} />
            </Field>
            <div className="flex flex-wrap gap-2">
              <Button type="button" variant="outline" size="sm" className="h-9" onClick={save} disabled={!!bodyErr || !eventName.trim()}>
                <Save /> Save
              </Button>
              <Button
                type="button"
                variant="outline"
                size="sm"
                className="text-destructive hover:text-destructive h-9"
                onClick={() => setDeleting(true)}
                disabled={selected === NEW}
              >
                <Trash2 /> Delete
              </Button>
            </div>
          </div>
          <div className="grid grid-cols-1 gap-3 sm:grid-cols-2 md:max-w-2xl">
            <Field label="Version or alias" htmlFor="test-qualifier">
              <Select value={qualifier} onValueChange={setQualifier}>
                <SelectTrigger id="test-qualifier" className="w-full">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="$LATEST">
                    <span className="font-mono">$LATEST</span>
                  </SelectItem>
                  {(aliases.data ?? []).length > 0 && (
                    <SelectGroup>
                      <SelectLabel>Aliases</SelectLabel>
                      {(aliases.data ?? []).map((a) => (
                        <SelectItem key={`a-${a.name}`} value={a.name}>
                          {a.name} <span className="text-muted-foreground font-mono text-xs">→ {aliasTargets(a)}</span>
                        </SelectItem>
                      ))}
                    </SelectGroup>
                  )}
                  {(versions.data ?? []).filter((v) => v.version !== "$LATEST").length > 0 && (
                    <SelectGroup>
                      <SelectLabel>Versions</SelectLabel>
                      {(versions.data ?? [])
                        .filter((v) => v.version !== "$LATEST")
                        .reverse()
                        .map((v) => (
                          <SelectItem key={`v-${v.version}`} value={v.version!}>
                            Version <span className="font-mono">{v.version}</span>
                          </SelectItem>
                        ))}
                    </SelectGroup>
                  )}
                </SelectContent>
              </Select>
            </Field>
            <Field label="Invocation type" htmlFor="test-type">
              <Select value={invocationType} onValueChange={(v) => setInvocationType(v as typeof invocationType)}>
                <SelectTrigger id="test-type" className="w-full">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="RequestResponse">Synchronous (RequestResponse)</SelectItem>
                  <SelectItem value="Event">Asynchronous (Event)</SelectItem>
                </SelectContent>
              </Select>
            </Field>
          </div>
          <div className="flex flex-wrap items-center gap-2 text-sm">
            <span className="text-muted-foreground">Templates:</span>
            {tpls.map((t) => (
              <Button key={t.id} type="button" variant="secondary" size="sm" className="h-7 text-xs" onClick={() => pick(`tpl:${t.id}`)}>
                {t.label}
              </Button>
            ))}
          </div>
          <JsonEditor value={body} onChange={setBody} rows={12} />
        </div>
      </Section>

      {invokeError ? <ErrorState error={invokeError} onRetry={invoke} /> : null}
      {result && <InvokeResultView result={result} />}
      {queued && (
        <Section
          title={
            <span className="flex items-center gap-2 text-sky-700 dark:text-sky-400">
              <CheckCircle2 className="size-4" /> Event queued (202 Accepted)
            </span>
          }
          className="border-sky-600/30 dark:border-sky-400/30"
        >
          <KeyValueGrid
            columns={3}
            items={[
              { label: "Request ID", value: <span className="font-mono text-[13px] break-all">{queued.request_id}</span>, wide: true },
              { label: "Qualifier", value: <span className="font-mono text-[13px]">{queued.qualifier}</span> },
            ]}
          />
          <p className="text-muted-foreground mt-3 text-xs">
            The function runs in the background and is retried per the asynchronous invocation settings. See the Logs tab for its output; results go to the
            configured destinations.
          </p>
        </Section>
      )}

      <ConfirmDialog
        open={deleting}
        onOpenChange={setDeleting}
        title={`Delete test event ${selected}?`}
        description="The saved event is removed from this browser."
        onConfirm={remove}
      />
    </div>
  )
}

export function InvokeResultView({ result }: { result: InvokeResult }) {
  const failed = !!result.function_error
  const payload = typeof result.payload === "string" ? result.payload : pretty(result.payload)
  return (
    <Section
      title={
        <span
          className={cn(
            "flex items-center gap-2",
            failed ? "text-red-700 dark:text-red-400" : "text-emerald-700 dark:text-emerald-400",
          )}
        >
          {failed ? <AlertCircle className="size-4" /> : <CheckCircle2 className="size-4" />}
          Execution result: {failed ? "failed" : "succeeded"}
        </span>
      }
      className={cn(failed ? "border-red-600/30 dark:border-red-400/30" : "border-emerald-600/30 dark:border-emerald-400/30")}
      bodyClassName="flex flex-col gap-4"
    >
      <KeyValueGrid
        columns={4}
        items={[
          { label: "Status code", value: String(result.status_code) },
          { label: "Duration", value: `${formatNumber(result.duration_ms, 2)} ms` },
          { label: "Billed duration", value: `${formatNumber(result.billed_duration_ms, 0)} ms` },
          { label: "Cold start", value: result.cold_start ? "Yes (new execution environment)" : "No" },
          { label: "Executed version", value: <span className="font-mono text-[13px]">{result.executed_version ?? "$LATEST"}</span> },
          { label: "Function error", value: result.function_error ?? "" },
          { label: "Request ID", value: <span className="font-mono text-[13px] break-all">{result.request_id}</span>, wide: true },
        ]}
      />
      <div className="flex flex-col gap-1.5">
        <div className="flex items-center justify-between gap-2">
          <h3 className="text-sm font-medium">Response</h3>
          <CopyButton value={payload} label="Copy response" />
        </div>
        <pre className="bg-muted/50 max-h-96 overflow-auto rounded-md border p-3 font-mono text-[12.5px] leading-5 whitespace-pre-wrap break-all">{payload}</pre>
      </div>
      <div className="flex flex-col gap-1.5">
        <div className="flex items-center justify-between gap-2">
          <h3 className="text-sm font-medium">Function logs</h3>
          {result.logs && <CopyButton value={result.logs} label="Copy logs" />}
        </div>
        {result.logs ? (
          <pre className="bg-muted/50 max-h-80 overflow-auto rounded-md border p-3 font-mono text-[12.5px] leading-5 whitespace-pre-wrap break-all">
            {result.logs.split("\n").map((line, i) => (
              <span key={i} className={cn(/^(START|END|REPORT|INIT_START) /.test(line) && "text-muted-foreground font-semibold")}>
                {line}
                {"\n"}
              </span>
            ))}
          </pre>
        ) : (
          <p className="text-muted-foreground text-sm">The function wrote nothing to stdout or stderr.</p>
        )}
        <p className="text-muted-foreground text-xs">The last 4 KB of output are shown here; the full log is in the Logs tab.</p>
      </div>
    </Section>
  )
}
