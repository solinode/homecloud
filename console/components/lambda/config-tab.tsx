"use client"

import { useEffect, useState } from "react"
import Link from "next/link"
import { ExternalLink, Globe, Info, Loader2, Lock } from "lucide-react"
import { toast } from "sonner"

import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { RadioGroup, RadioGroupItem } from "@/components/ui/radio-group"
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select"
import { Switch } from "@/components/ui/switch"
import { Textarea } from "@/components/ui/textarea"
import { CopyButton } from "@/components/console/copy-button"
import { Field } from "@/components/console/form-field"
import { KeyValueGrid } from "@/components/console/key-value"
import { Section } from "@/components/console/section"
import { TagsEditor, rowsToTags, tagsToRows, type TagRow } from "@/components/console/tags-editor"
import { api, errorMessage } from "@/lib/api"
import { formatMemoryMB } from "@/lib/format"
import { revalidate } from "@/lib/hooks"
import type { FunctionConfigInput, FunctionUrlConfig, LambdaFunction } from "@/lib/types"
import { cn } from "@/lib/utils"

import { HANDLER_RE, LAMBDA_PATH, MEMORY_PRESETS, fnPath, formatTimeout, useRuntimes } from "./common"
import { envErrors } from "./create-function"

export function ConfigurationTab({ fn }: { fn: LambdaFunction }) {
  return (
    <div className="flex flex-col gap-4">
      <GeneralConfig fn={fn} />
      <EnvironmentConfig fn={fn} />
    </div>
  )
}

function GeneralConfig({ fn }: { fn: LambdaFunction }) {
  const runtimes = useRuntimes()
  const [editing, setEditing] = useState(false)
  const [description, setDescription] = useState("")
  const [runtime, setRuntime] = useState("")
  const [handler, setHandler] = useState("")
  const [memory, setMemory] = useState("")
  const [timeout, setTimeoutValue] = useState("")
  const [pending, setPending] = useState(false)

  const reset = () => {
    setDescription(fn.description)
    setRuntime(fn.runtime)
    setHandler(fn.handler)
    setMemory(String(fn.memory_mb))
    setTimeoutValue(String(fn.timeout_seconds))
  }
  useEffect(() => {
    if (!editing) reset()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [fn, editing])

  const mem = Number(memory)
  const to = Number(timeout)
  const errors: Record<string, string> = {}
  if (!Number.isInteger(mem) || mem < 128 || mem > 10240) errors.memory = "128-10240 MB"
  if (!Number.isInteger(to) || to < 1 || to > 900) errors.timeout = "1-900 seconds"
  if (!HANDLER_RE.test(handler)) errors.handler = "Use the form file.function"
  const valid = Object.keys(errors).length === 0
  const rtLabel = (name: string) => runtimes.data?.find((r) => r.name === name)?.label ?? name

  const save = async (e: React.FormEvent) => {
    e.preventDefault()
    if (!valid) return
    const body: FunctionConfigInput = {}
    if (description !== fn.description) body.description = description
    if (runtime !== fn.runtime) body.runtime = runtime
    if (handler !== fn.handler) body.handler = handler
    if (mem !== fn.memory_mb) body.memory_mb = mem
    if (to !== fn.timeout_seconds) body.timeout_seconds = to
    if (Object.keys(body).length === 0) {
      setEditing(false)
      return
    }
    setPending(true)
    try {
      await api.patch(fnPath(fn.name), body)
      toast.success(`Updated the configuration of ${fn.name}`)
      await revalidate(LAMBDA_PATH)
      setEditing(false)
    } catch (err) {
      toast.error(errorMessage(err))
    } finally {
      setPending(false)
    }
  }

  if (!editing) {
    return (
      <Section
        title="General configuration"
        actions={
          <Button variant="outline" size="sm" onClick={() => setEditing(true)}>
            Edit
          </Button>
        }
      >
        <KeyValueGrid
          columns={3}
          items={[
            { label: "Description", value: fn.description, wide: true },
            { label: "Runtime", value: rtLabel(fn.runtime) },
            { label: "Handler", value: <span className="font-mono text-[13px]">{fn.handler}</span> },
            { label: "Memory", value: formatMemoryMB(fn.memory_mb) },
            { label: "Timeout", value: formatTimeout(fn.timeout_seconds) },
          ]}
        />
      </Section>
    )
  }

  return (
    <Section title="Edit general configuration">
      <form onSubmit={save} className="flex flex-col gap-5">
        <Field label="Description" htmlFor="cfg-desc" optional>
          <Textarea id="cfg-desc" rows={2} value={description} onChange={(e) => setDescription(e.target.value)} maxLength={256} className="max-w-xl" />
        </Field>
        <div className="grid grid-cols-1 gap-5 md:grid-cols-2">
          <Field label="Runtime" htmlFor="cfg-runtime" help="Changing the runtime does not convert your code.">
            <Select value={runtime} onValueChange={setRuntime}>
              <SelectTrigger id="cfg-runtime" className="w-full max-w-xs">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {(runtimes.data ?? [{ name: fn.runtime, label: fn.runtime }]).map((r) => (
                  <SelectItem key={r.name} value={r.name}>
                    {r.label} <span className="text-muted-foreground font-mono text-xs">{r.name}</span>
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </Field>
          <Field label="Handler" htmlFor="cfg-handler" error={errors.handler} help="file.function, e.g. lambda_function.lambda_handler or index.handler">
            <Input id="cfg-handler" value={handler} onChange={(e) => setHandler(e.target.value)} className="max-w-xs font-mono" spellCheck={false} />
          </Field>
          <Field label="Memory (MB)" htmlFor="cfg-memory" error={errors.memory} help="128-10240 MB; CPU scales with memory.">
            <div className="flex flex-wrap items-center gap-1.5">
              <Input id="cfg-memory" type="number" min={128} max={10240} value={memory} onChange={(e) => setMemory(e.target.value)} className="h-8 w-28" />
              {MEMORY_PRESETS.map((m) => (
                <Button key={m} type="button" size="sm" variant={mem === m ? "secondary" : "ghost"} className="h-7 px-2 text-xs" onClick={() => setMemory(String(m))}>
                  {formatMemoryMB(m)}
                </Button>
              ))}
            </div>
          </Field>
          <Field label="Timeout (seconds)" htmlFor="cfg-timeout" error={errors.timeout} help="1-900 seconds.">
            <Input id="cfg-timeout" type="number" min={1} max={900} value={timeout} onChange={(e) => setTimeoutValue(e.target.value)} className="h-8 w-28" />
          </Field>
        </div>
        <p className="text-muted-foreground flex gap-1.5 text-xs">
          <Info className="mt-px size-3.5 shrink-0" /> Saving restarts the function&apos;s execution environment; the next invocation is a cold start.
        </p>
        <div className="flex flex-wrap justify-end gap-2 border-t pt-4">
          <Button type="button" variant="outline" onClick={() => setEditing(false)} disabled={pending}>
            Cancel
          </Button>
          <Button type="submit" disabled={pending || !valid}>
            {pending && <Loader2 className="animate-spin" />}
            Save
          </Button>
        </div>
      </form>
    </Section>
  )
}

function EnvironmentConfig({ fn }: { fn: LambdaFunction }) {
  const [editing, setEditing] = useState(false)
  const [rows, setRows] = useState<TagRow[]>([])
  const [pending, setPending] = useState(false)
  const entries = Object.entries(fn.environment ?? {}).sort(([a], [b]) => a.localeCompare(b))
  const err = envErrors(rows)

  const save = async (e: React.FormEvent) => {
    e.preventDefault()
    if (err) return
    setPending(true)
    try {
      await api.patch(fnPath(fn.name), { environment: rowsToTags(rows) ?? {} })
      toast.success(`Saved environment variables of ${fn.name}`)
      await revalidate(LAMBDA_PATH)
      setEditing(false)
    } catch (e) {
      toast.error(errorMessage(e))
    } finally {
      setPending(false)
    }
  }

  return (
    <Section
      title={`Environment variables (${entries.length})`}
      description="Available to your code as process environment variables. They are stored unencrypted; use Secrets Manager for credentials."
      actions={
        !editing && (
          <Button
            variant="outline"
            size="sm"
            onClick={() => {
              setRows(tagsToRows(fn.environment))
              setEditing(true)
            }}
          >
            Edit
          </Button>
        )
      }
      flush={!editing && entries.length > 0}
    >
      {editing ? (
        <form onSubmit={save} className="flex flex-col gap-4">
          <TagsEditor rows={rows} onChange={setRows} keyPlaceholder="Key" valuePlaceholder="Value" addLabel="Add environment variable" />
          {err && <p className="text-destructive text-xs">{err}</p>}
          <div className="flex flex-wrap justify-end gap-2 border-t pt-4">
            <Button type="button" variant="outline" onClick={() => setEditing(false)} disabled={pending}>
              Cancel
            </Button>
            <Button type="submit" disabled={pending || !!err}>
              {pending && <Loader2 className="animate-spin" />}
              Save
            </Button>
          </div>
        </form>
      ) : entries.length === 0 ? (
        <p className="text-muted-foreground text-sm">No environment variables.</p>
      ) : (
        <div className="overflow-x-auto">
          <table className="w-full text-sm">
            <thead>
              <tr className="bg-muted/40 border-b">
                <th className="text-muted-foreground px-4 py-2 text-left text-xs font-semibold">Key</th>
                <th className="text-muted-foreground px-4 py-2 text-left text-xs font-semibold">Value</th>
              </tr>
            </thead>
            <tbody>
              {entries.map(([k, v]) => (
                <tr key={k} className="border-b last:border-0">
                  <td className="px-4 py-2 font-mono text-[13px]">{k}</td>
                  <td className="px-4 py-2 font-mono text-[13px] break-all">{v || <span className="text-muted-foreground">(empty)</span>}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </Section>
  )
}

// ---- Function URL ----

export function FunctionUrlTab({ fn }: { fn: LambdaFunction }) {
  const cfg = fn.function_url ?? { enabled: false, auth_type: "NONE" }
  const [pending, setPending] = useState<string | null>(null)

  const put = async (next: FunctionUrlConfig, what: string) => {
    setPending(what)
    try {
      await api.put(`${fnPath(fn.name)}/url`, { enabled: next.enabled, auth_type: next.auth_type })
      toast.success(
        what === "enabled" ? (next.enabled ? "Function URL enabled" : "Function URL disabled") : `Auth type set to ${next.auth_type === "NONE" ? "NONE" : "HC_IAM"}`,
      )
      await revalidate(LAMBDA_PATH)
    } catch (e) {
      toast.error(errorMessage(e))
    } finally {
      setPending(null)
    }
  }

  const url = cfg.url ?? ""
  const iam = cfg.auth_type === "HC_IAM"
  const curl = [
    `curl -X POST '${url}?name=HomeCloud'`,
    iam ? `  -H 'Authorization: Bearer <access_key_id>:<secret_access_key>'` : null,
    `  -H 'Content-Type: application/json'`,
    `  -d '{"hello": "world"}'`,
  ]
    .filter(Boolean)
    .join(" \\\n")

  return (
    <div className="flex flex-col gap-4">
      <Section title="Function URL" description="A dedicated HTTP endpoint that invokes the function with an API Gateway v2 (HTTP API) event.">
        <div className="flex flex-col gap-5">
          <div className="flex items-start justify-between gap-4 rounded-md border p-3">
            <div>
              <Label htmlFor="url-enabled" className="font-medium">
                Enable function URL
              </Label>
              <p className="text-muted-foreground mt-0.5 text-xs">When disabled, requests to the URL return 404.</p>
            </div>
            <div className="flex items-center gap-2">
              {pending === "enabled" && <Loader2 className="text-muted-foreground size-4 animate-spin" />}
              <Switch id="url-enabled" checked={cfg.enabled} disabled={!!pending} onCheckedChange={(v) => put({ ...cfg, enabled: v }, "enabled")} />
            </div>
          </div>

          <div className="flex flex-col gap-2">
            <span className="flex items-center gap-2 text-sm font-medium">
              Auth type {pending === "auth" && <Loader2 className="text-muted-foreground size-4 animate-spin" />}
            </span>
            <RadioGroup
              value={cfg.auth_type}
              onValueChange={(v) => put({ ...cfg, auth_type: v as FunctionUrlConfig["auth_type"] }, "auth")}
              disabled={!!pending}
              className="grid-cols-1 sm:grid-cols-2"
            >
              {(
                [
                  { v: "NONE", icon: Globe, title: "NONE", text: "Public: anyone who can reach this host can invoke the function." },
                  {
                    v: "HC_IAM",
                    icon: Lock,
                    title: "HC_IAM",
                    text: "Callers send HomeCloud access keys and need lambda:InvokeFunctionUrl on the function.",
                  },
                ] as const
              ).map((o) => (
                <label
                  key={o.v}
                  htmlFor={`auth-${o.v}`}
                  className={cn(
                    "flex cursor-pointer items-start gap-3 rounded-md border p-3",
                    cfg.auth_type === o.v && "border-primary bg-primary/5 dark:bg-primary/10",
                  )}
                >
                  <RadioGroupItem id={`auth-${o.v}`} value={o.v} className="mt-0.5" />
                  <span className="flex flex-col gap-0.5">
                    <span className="flex items-center gap-1.5 font-mono text-sm font-medium">
                      <o.icon className="size-3.5" /> {o.title}
                    </span>
                    <span className="text-muted-foreground text-xs">{o.text}</span>
                  </span>
                </label>
              ))}
            </RadioGroup>
          </div>

          {cfg.enabled && url ? (
            <KeyValueGrid
              columns={2}
              items={[
                {
                  label: "Function URL",
                  wide: true,
                  value: (
                    <span className="inline-flex max-w-full items-center gap-1">
                      <a href={url} target="_blank" rel="noreferrer" className="text-primary inline-flex min-w-0 items-center gap-1 font-mono text-[13px] hover:underline">
                        <span className="truncate">{url}</span>
                        <ExternalLink className="size-3.5 shrink-0" />
                      </a>
                      <CopyButton value={url} label="Copy URL" />
                    </span>
                  ),
                },
                { label: "Auth type", value: <span className="font-mono text-[13px]">{cfg.auth_type}</span> },
                { label: "Path", value: "Any path below the URL is passed to the function as rawPath." },
              ]}
            />
          ) : (
            <p className="text-muted-foreground text-sm">The function has no URL. Enable it to get an HTTP endpoint.</p>
          )}
        </div>
      </Section>

      {cfg.enabled && url && (
        <Section title="Invoke with curl" actions={<CopyButton value={curl} size="sm" label="Copy command" />}>
          <pre className="bg-muted/50 overflow-x-auto rounded-md border p-3 font-mono text-[12.5px] leading-5">{curl}</pre>
          <p className="text-muted-foreground mt-2 text-xs">
            The function receives the request as an API Gateway v2 event. If it returns {"{ statusCode, headers, body }"} that becomes the HTTP response;
            any other value is returned as a JSON body with status 200.
            {iam && (
              <>
                {" "}
                Replace the placeholders with an access key from{" "}
                <Link href="/iam/users/" className="text-primary hover:underline">
                  IAM
                </Link>
                .
              </>
            )}
          </p>
        </Section>
      )}
    </div>
  )
}
