"use client"

import { useEffect, useMemo, useRef, useState } from "react"
import Link from "next/link"
import { useRouter } from "next/navigation"
import { FileArchive, FileCode2, Loader2, Rocket, Upload, X } from "lucide-react"
import { toast } from "sonner"

import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Skeleton } from "@/components/ui/skeleton"
import { ErrorState } from "@/components/console/error-state"
import { Field } from "@/components/console/form-field"
import { PageHeader } from "@/components/console/page-header"
import { Section } from "@/components/console/section"
import { TagsEditor, rowsToTags, type TagRow } from "@/components/console/tags-editor"
import { api, errorMessage } from "@/lib/api"
import { formatBytes, formatMemoryMB } from "@/lib/format"
import { revalidate } from "@/lib/hooks"
import type { CreateFunctionInput, LambdaFunction } from "@/lib/types"
import { cn } from "@/lib/utils"

import {
  ENV_KEY_RE,
  FUNCTIONS_PATH,
  HANDLER_RE,
  FUNCTION_NAME_RE,
  LAMBDA_PATH,
  MAX_ZIP_BYTES,
  MEMORY_PRESETS,
  fileToBase64,
  formatTimeout,
  functionHref,
  useRuntimes,
} from "./common"

type CodeSource = "template" | "zip"

/** envErrors validates environment variable rows; shared with the configuration tab. */
export function envErrors(rows: TagRow[]): string | null {
  const keys = rows.map((r) => r.key.trim()).filter(Boolean)
  if (new Set(keys).size !== keys.length) return "Variable names must be unique"
  if (rows.some((r) => !r.key.trim() && r.value.trim())) return "Every variable with a value needs a name"
  const bad = keys.find((k) => !ENV_KEY_RE.test(k))
  if (bad) return `"${bad}" is not a valid name: use letters, digits and underscores, not starting with a digit`
  return null
}

export function CreateFunction() {
  const router = useRouter()
  const runtimes = useRuntimes()
  const [name, setName] = useState("")
  const [description, setDescription] = useState("")
  const [runtime, setRuntime] = useState("")
  const [source, setSource] = useState<CodeSource>("template")
  const [zip, setZip] = useState<File | null>(null)
  const [handler, setHandler] = useState("")
  const [handlerTouched, setHandlerTouched] = useState(false)
  const [memory, setMemory] = useState("128")
  const [timeout, setTimeoutValue] = useState("3")
  const [env, setEnv] = useState<TagRow[]>([])
  const [submitted, setSubmitted] = useState(false)
  const [pending, setPending] = useState(false)
  const fileInput = useRef<HTMLInputElement>(null)

  useEffect(() => {
    if (!runtime && runtimes.data?.length) setRuntime(runtimes.data.find((r) => r.name === "python3.12")?.name ?? runtimes.data[0].name)
  }, [runtimes.data, runtime])

  const rt = runtimes.data?.find((r) => r.name === runtime)
  // The handler follows the runtime until the user edits it; template code always uses the runtime default.
  useEffect(() => {
    if (rt && (!handlerTouched || source === "template")) setHandler(rt.default_handler)
  }, [rt, handlerTouched, source])

  const mem = Number(memory)
  const to = Number(timeout)
  const errors = useMemo(() => {
    const e: Record<string, string> = {}
    if (!FUNCTION_NAME_RE.test(name)) e.name = "1-64 characters: letters, digits, hyphens (-) and underscores (_)"
    if (!runtime) e.runtime = "Choose a runtime"
    if (!Number.isInteger(mem) || mem < 128 || mem > 10240) e.memory = "Enter a whole number of MB from 128 to 10240"
    if (!Number.isInteger(to) || to < 1 || to > 900) e.timeout = "Enter a whole number of seconds from 1 to 900"
    if (source === "zip") {
      if (!zip) e.zip = "Choose a .zip file"
      else if (zip.size > MAX_ZIP_BYTES) e.zip = `The package is ${formatBytes(zip.size)}; the limit is 50 MB`
      if (!HANDLER_RE.test(handler)) e.handler = "Use the form file.function, e.g. index.handler"
    }
    const envErr = envErrors(env)
    if (envErr) e.env = envErr
    return e
  }, [name, runtime, mem, to, source, zip, handler, env])
  const err = (k: string) => (submitted || (k === "name" && name) ? errors[k] : undefined)
  const valid = Object.keys(errors).length === 0

  const create = async () => {
    setSubmitted(true)
    if (!valid) {
      toast.error("Fix the highlighted fields before creating the function")
      return
    }
    setPending(true)
    try {
      const body: CreateFunctionInput = {
        name,
        runtime,
        description: description.trim() || undefined,
        memory_mb: mem,
        timeout_seconds: to,
        environment: rowsToTags(env),
      }
      if (source === "zip" && zip) {
        body.handler = handler
        body.code = { zip_base64: await fileToBase64(zip) }
      }
      const fn = await api.post<LambdaFunction>(FUNCTIONS_PATH, body)
      toast.success(`Created function ${fn.name}`)
      await revalidate(LAMBDA_PATH)
      router.push(functionHref(fn.name, source === "template" ? "code" : undefined))
    } catch (e) {
      toast.error(errorMessage(e))
      setPending(false)
    }
  }

  return (
    <div className="flex flex-col gap-4">
      <PageHeader
        title="Create function"
        description="Start from a runtime's hello-world template and edit it in the console, or upload a deployment package."
        breadcrumbs={[{ label: "Lambda", href: "/lambda/" }, { label: "Functions", href: "/lambda/" }, { label: "Create function" }]}
      />
      <form
        onSubmit={(e) => {
          e.preventDefault()
          create()
        }}
        className="grid grid-cols-1 items-start gap-4 xl:grid-cols-[minmax(0,1fr)_320px]"
      >
        <div className="flex min-w-0 flex-col gap-4">
          <Section title="Basic information">
            <div className="flex flex-col gap-5">
              <Field label="Function name" htmlFor="fn-name" error={err("name")} help="1-64 characters: letters, digits, hyphens and underscores. It cannot be changed later.">
                <Input
                  id="fn-name"
                  autoFocus
                  autoComplete="off"
                  spellCheck={false}
                  value={name}
                  onChange={(e) => setName(e.target.value)}
                  placeholder="my-function"
                  className="max-w-md font-mono"
                  aria-invalid={!!err("name")}
                />
              </Field>
              <Field label="Description" htmlFor="fn-desc" optional>
                <Input id="fn-desc" value={description} onChange={(e) => setDescription(e.target.value)} className="max-w-xl" maxLength={256} />
              </Field>
              <Field label="Runtime" error={err("runtime")}>
                {runtimes.error ? (
                  <ErrorState error={runtimes.error} onRetry={() => runtimes.mutate()} />
                ) : !runtimes.data ? (
                  <Skeleton className="h-20 rounded-md" />
                ) : (
                  <div role="radiogroup" className="grid grid-cols-1 gap-2 sm:grid-cols-2 lg:grid-cols-3">
                    {runtimes.data.map((r) => {
                      const active = r.name === runtime
                      return (
                        <button
                          key={r.name}
                          type="button"
                          role="radio"
                          aria-checked={active}
                          onClick={() => setRuntime(r.name)}
                          className={cn(
                            "flex flex-col items-start gap-0.5 rounded-md border px-3 py-2.5 text-left transition-colors",
                            active ? "border-primary bg-primary/5 ring-primary ring-1 dark:bg-primary/10" : "hover:bg-muted/50",
                          )}
                        >
                          <span className="text-sm font-medium">{r.label}</span>
                          <span className="text-muted-foreground font-mono text-xs">{r.name}</span>
                          <span className="text-muted-foreground text-xs">Image {r.image}</span>
                        </button>
                      )
                    })}
                  </div>
                )}
              </Field>
            </div>
          </Section>

          <Section title="Code source">
            <div className="flex flex-col gap-4">
              <div role="radiogroup" className="grid grid-cols-1 gap-2 sm:grid-cols-2">
                {(
                  [
                    { v: "template", icon: FileCode2, title: "Start from runtime template", text: "A hello-world handler you can edit in the console." },
                    { v: "zip", icon: FileArchive, title: "Upload a .zip file", text: "A deployment package with your code and dependencies." },
                  ] as const
                ).map((o) => (
                  <button
                    key={o.v}
                    type="button"
                    role="radio"
                    aria-checked={source === o.v}
                    onClick={() => setSource(o.v)}
                    className={cn(
                      "flex items-start gap-3 rounded-md border p-3 text-left transition-colors",
                      source === o.v ? "border-primary bg-primary/5 ring-primary ring-1 dark:bg-primary/10" : "hover:bg-muted/50",
                    )}
                  >
                    <o.icon className="text-muted-foreground mt-0.5 size-5 shrink-0" />
                    <span className="flex flex-col gap-0.5">
                      <span className="text-sm font-medium">{o.title}</span>
                      <span className="text-muted-foreground text-xs">{o.text}</span>
                    </span>
                  </button>
                ))}
              </div>

              {source === "template" ? (
                rt ? (
                  <div className="overflow-hidden rounded-md border">
                    <div className="bg-muted/50 flex items-center justify-between gap-2 border-b px-3 py-1.5 text-xs">
                      <span className="font-mono font-medium">{rt.default_file}</span>
                      <span className="text-muted-foreground">Read-only preview</span>
                    </div>
                    <pre className="bg-muted/20 max-h-80 overflow-auto p-3 font-mono text-[12.5px] leading-5">{rt.template}</pre>
                  </div>
                ) : (
                  <Skeleton className="h-40 rounded-md" />
                )
              ) : (
                <Field label="Deployment package" error={err("zip")} help="A .zip file up to 50 MB. Files at the root of the archive end up in /var/task.">
                  <input
                    ref={fileInput}
                    type="file"
                    accept=".zip,application/zip"
                    className="hidden"
                    onChange={(e) => {
                      setZip(e.target.files?.[0] ?? null)
                      e.target.value = ""
                    }}
                  />
                  <div className="flex flex-wrap items-center gap-2">
                    <Button type="button" variant="outline" size="sm" onClick={() => fileInput.current?.click()}>
                      <Upload /> {zip ? "Choose another file" : "Choose .zip file"}
                    </Button>
                    {zip && (
                      <span className="bg-muted inline-flex max-w-full items-center gap-1.5 rounded-md border px-2 py-1 text-sm">
                        <FileArchive className="size-4 shrink-0" />
                        <span className="truncate font-mono text-[13px]">{zip.name}</span>
                        <span className="text-muted-foreground text-xs">{formatBytes(zip.size)}</span>
                        <button type="button" onClick={() => setZip(null)} aria-label="Remove file" className="text-muted-foreground hover:text-foreground">
                          <X className="size-3.5" />
                        </button>
                      </span>
                    )}
                  </div>
                </Field>
              )}

              <Field
                label="Handler"
                htmlFor="fn-handler"
                error={err("handler")}
                help={
                  source === "template"
                    ? "Template code uses the runtime's default handler. You can change it after creation."
                    : "file.function: the module (without extension) and the exported function that receives the event."
                }
              >
                <Input
                  id="fn-handler"
                  value={handler}
                  disabled={source === "template"}
                  onChange={(e) => {
                    setHandler(e.target.value)
                    setHandlerTouched(true)
                  }}
                  className="max-w-md font-mono"
                  spellCheck={false}
                />
              </Field>
            </div>
          </Section>

          <Section title="Settings">
            <div className="flex flex-col gap-5">
              <Field label="Memory (MB)" htmlFor="fn-memory" error={err("memory")} help="128-10240 MB. CPU is allotted in proportion to memory.">
                <div className="flex flex-wrap items-center gap-2">
                  <Input id="fn-memory" type="number" min={128} max={10240} value={memory} onChange={(e) => setMemory(e.target.value)} className="h-8 w-28" />
                  {MEMORY_PRESETS.map((m) => (
                    <Button key={m} type="button" size="sm" variant={mem === m ? "secondary" : "ghost"} className="h-7 px-2 text-xs" onClick={() => setMemory(String(m))}>
                      {formatMemoryMB(m)}
                    </Button>
                  ))}
                </div>
              </Field>
              <Field label="Timeout (seconds)" htmlFor="fn-timeout" error={err("timeout")} help="1-900 seconds. Invocations that run longer are stopped.">
                <Input id="fn-timeout" type="number" min={1} max={900} value={timeout} onChange={(e) => setTimeoutValue(e.target.value)} className="h-8 w-28" />
              </Field>
              <Field label="Environment variables" optional error={err("env")}>
                <TagsEditor rows={env} onChange={setEnv} keyPlaceholder="Key" valuePlaceholder="Value" addLabel="Add environment variable" />
              </Field>
            </div>
          </Section>
        </div>

        <aside className="xl:sticky xl:top-4">
          <Section title="Summary">
            <div className="flex flex-col gap-4">
              <dl className="flex flex-col gap-3 text-sm">
                <SummaryItem label="Function name">
                  <span className="font-mono text-[13px] break-all">{name || "-"}</span>
                </SummaryItem>
                <SummaryItem label="Runtime">{rt?.label ?? "-"}</SummaryItem>
                <SummaryItem label="Code">
                  {source === "template" ? (
                    <>
                      Template <span className="text-muted-foreground font-mono text-xs">{rt?.default_file}</span>
                    </>
                  ) : zip ? (
                    <>
                      <span className="font-mono text-[13px] break-all">{zip.name}</span>{" "}
                      <span className="text-muted-foreground text-xs">({formatBytes(zip.size)})</span>
                    </>
                  ) : (
                    "No package chosen"
                  )}
                </SummaryItem>
                <SummaryItem label="Handler">
                  <span className="font-mono text-[13px]">{handler || "-"}</span>
                </SummaryItem>
                <SummaryItem label="Memory / timeout">
                  {Number.isInteger(mem) ? formatMemoryMB(mem) : "-"} / {Number.isInteger(to) && to > 0 ? formatTimeout(to) : "-"}
                </SummaryItem>
                <SummaryItem label="Environment variables">{rowsToTags(env) ? Object.keys(rowsToTags(env)!).length : "None"}</SummaryItem>
              </dl>
              {submitted && !valid && <p className="text-destructive text-xs">Some settings need attention. Check the highlighted fields.</p>}
              <div className="flex flex-col gap-2 border-t pt-4">
                <Button type="submit" disabled={pending || !runtime}>
                  {pending ? <Loader2 className="animate-spin" /> : <Rocket />}
                  Create function
                </Button>
                <Button type="button" variant="outline" asChild>
                  <Link href="/lambda/">Cancel</Link>
                </Button>
              </div>
            </div>
          </Section>
        </aside>
      </form>
    </div>
  )
}

function SummaryItem({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div>
      <dt className="text-muted-foreground text-xs font-medium">{label}</dt>
      <dd className="min-w-0 break-words">{children}</dd>
    </div>
  )
}
