"use client"

import { useEffect, useMemo, useRef, useState } from "react"
import Link from "next/link"
import { useRouter } from "next/navigation"
import { Box, Cpu, FileArchive, FileCode2, Loader2, Package, Rocket, Upload, X } from "lucide-react"
import { toast } from "sonner"

import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Skeleton } from "@/components/ui/skeleton"
import { CodeBlock } from "@/components/console/code-block"
import { ErrorState } from "@/components/console/error-state"
import { Field } from "@/components/console/form-field"
import { PageHeader } from "@/components/console/page-header"
import { OptionCard, OptionGroup } from "@/components/console/option-card"
import { Section } from "@/components/console/section"
import { StatusBadge } from "@/components/console/status-badge"
import { TagsEditor, rowsToTags, type TagRow } from "@/components/console/tags-editor"
import { RolePicker } from "@/components/iam/role-picker"
import { api, errorMessage } from "@/lib/api"
import { formatBytes, formatMemoryMB } from "@/lib/format"
import { revalidate } from "@/lib/hooks"
import type { CreateFunctionInput, LambdaFunction } from "@/lib/types"

import {
  ARCHITECTURES,
  ENV_KEY_RE,
  FUNCTIONS_PATH,
  FUNCTION_NAME_RE,
  LAMBDA_PATH,
  MAX_ZIP_BYTES,
  MEMORY_PRESETS,
  RuntimeBadge,
  fileToBase64,
  formatTimeout,
  functionHref,
  handlerError,
  handlerHelp,
  useRuntimes,
} from "./common"
import { ImageUriPicker } from "./image-picker"
import { LayersPicker } from "./pickers"

type CodeSource = "template" | "zip"
type PackageType = "Zip" | "Image"

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
  const [pkg, setPkg] = useState<PackageType>("Zip")
  const [imageUri, setImageUri] = useState("")
  const [runtime, setRuntime] = useState("")
  const [arch, setArch] = useState<string>("x86_64")
  const [role, setRole] = useState("")
  const [layers, setLayers] = useState<string[]>([])
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
  // Runtimes without a hello-world template (Java, .NET, OS-only) need a deployment package.
  const hasTemplate = !!rt?.template
  useEffect(() => {
    if (rt && !rt.template && source === "template") setSource("zip")
  }, [rt, source])
  // The handler follows the runtime until the user edits it; template code always uses the runtime default.
  useEffect(() => {
    if (rt && (!handlerTouched || source === "template")) setHandler(rt.default_handler)
  }, [rt, handlerTouched, source])

  const mem = Number(memory)
  const to = Number(timeout)
  const errors = useMemo(() => {
    const e: Record<string, string> = {}
    if (!FUNCTION_NAME_RE.test(name)) e.name = "1-64 characters: letters, digits, hyphens (-) and underscores (_)"
    if (pkg === "Zip" && !runtime) e.runtime = "Choose a runtime"
    if (pkg === "Image" && !/^\S+$/.test(imageUri.trim())) e.image = "Enter a container image URI, e.g. localhost:5500/my-repo:latest"
    if (!Number.isInteger(mem) || mem < 128 || mem > 10240) e.memory = "Enter a whole number of MB from 128 to 10240"
    if (!Number.isInteger(to) || to < 1 || to > 900) e.timeout = "Enter a whole number of seconds from 1 to 900"
    if (pkg === "Zip" && source === "zip") {
      if (!zip) e.zip = "Choose a .zip file"
      else if (zip.size > MAX_ZIP_BYTES) e.zip = `The package is ${formatBytes(zip.size)}; the limit is 50 MB`
      const he = handlerError(runtime, handler)
      if (he) e.handler = he
    }
    const envErr = envErrors(env)
    if (envErr) e.env = envErr
    return e
  }, [name, pkg, imageUri, runtime, mem, to, source, zip, handler, env])
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
        description: description.trim() || undefined,
        memory_mb: mem,
        timeout_seconds: to,
        environment: rowsToTags(env),
        architectures: [arch],
        role: role || undefined,
      }
      if (pkg === "Image") {
        body.package_type = "Image"
        body.image_uri = imageUri.trim()
      } else {
        body.runtime = runtime
        if (layers.length) body.layers = layers
        if (source === "zip" && zip) {
          body.handler = handler
          body.code = { zip_base64: await fileToBase64(zip) }
        }
      }
      const fn = await api.post<LambdaFunction>(FUNCTIONS_PATH, body)
      toast.success(`Created function ${fn.name}`)
      await revalidate(LAMBDA_PATH)
      router.push(functionHref(fn.name, pkg === "Zip" && source === "template" ? "code" : undefined))
    } catch (e) {
      toast.error(errorMessage(e))
      setPending(false)
    }
  }

  return (
    <div className="flex flex-col gap-4">
      <PageHeader
        title="Create function"
        description="Start from a runtime's hello-world template, upload a deployment package, or run a container image."
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
              <Field label="Package type">
                <OptionGroup label="Package type">
                  <OptionCard
                    selected={pkg === "Zip"}
                    onSelect={() => setPkg("Zip")}
                    icon={Package}
                    title="Zip archive"
                    description="Pick a managed runtime; author code in the console or upload a .zip."
                  />
                  <OptionCard
                    selected={pkg === "Image"}
                    onSelect={() => setPkg("Image")}
                    icon={Box}
                    title="Container image"
                    description="Run an image that implements the Lambda Runtime API, e.g. from ECR."
                  />
                </OptionGroup>
              </Field>
              {pkg === "Image" ? (
                <Field label="Container image URI" htmlFor="fn-image" error={err("image")} help="The image is pulled when the function is created and whenever its code is updated.">
                  <ImageUriPicker id="fn-image" value={imageUri} onChange={setImageUri} invalid={!!err("image")} />
                </Field>
              ) : (
                <Field label="Runtime" error={err("runtime")}>
                  {runtimes.error ? (
                    <ErrorState error={runtimes.error} onRetry={() => runtimes.mutate()} />
                  ) : !runtimes.data ? (
                    <Skeleton className="h-20 rounded-md" />
                  ) : (
                    <OptionGroup label="Runtime" columns={3}>
                      {runtimes.data.map((r) => (
                        <OptionCard
                          key={r.name}
                          selected={r.name === runtime}
                          onSelect={() => setRuntime(r.name)}
                          title={r.label}
                          badge={r.deprecated ? <StatusBadge status="deprecated" label="Deprecated" tone="warning" /> : undefined}
                          description={
                            <span className="block truncate" title={r.image}>
                              {r.template ? `Image ${r.image}` : "Deployment package required"}
                            </span>
                          }
                        >
                          <span className="mt-1.5">
                            <RuntimeBadge runtime={r.name} />
                          </span>
                        </OptionCard>
                      ))}
                    </OptionGroup>
                  )}
                </Field>
              )}
              <Field label="Architecture" help="The instruction set of the execution environment.">
                <OptionGroup label="Architecture">
                  {ARCHITECTURES.map((a) => (
                    <OptionCard
                      key={a}
                      selected={arch === a}
                      onSelect={() => setArch(a)}
                      icon={Cpu}
                      title={<span className="font-mono">{a}</span>}
                      description={a === "arm64" ? "64-bit ARM (Graviton, Apple silicon, Raspberry Pi)" : "64-bit x86 (Intel and AMD)"}
                    />
                  ))}
                </OptionGroup>
              </Field>
            </div>
          </Section>

          {pkg === "Zip" && (
            <Section title="Code source">
              <div className="flex flex-col gap-4">
                <OptionGroup label="Code source">
                  <OptionCard
                    selected={source === "template"}
                    disabled={!hasTemplate}
                    onSelect={() => setSource("template")}
                    icon={FileCode2}
                    title="Start from runtime template"
                    description={hasTemplate ? "A hello-world handler you can edit in the console." : `${rt?.label ?? "This runtime"} has no template: upload a package.`}
                  />
                  <OptionCard
                    selected={source === "zip"}
                    onSelect={() => setSource("zip")}
                    icon={FileArchive}
                    title="Upload a .zip file"
                    description="A deployment package with your code and dependencies."
                  />
                </OptionGroup>

                {source === "template" ? (
                  rt ? (
                    <CodeBlock
                      title={rt.default_file}
                      code={rt.template}
                      maxHeight="20rem"
                      actions={<span className="text-code-muted mr-1 hidden sm:inline">Read-only preview</span>}
                    />
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
                        <span className="bg-muted inline-flex h-8 max-w-full items-center gap-1.5 rounded-md border px-2 text-sm">
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
                  help={source === "template" ? "Template code uses the runtime's default handler. You can change it after creation." : handlerHelp(runtime)}
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
                <Field label="Layers" optional help="Shared libraries and code, extracted to /opt in order before the function starts.">
                  <LayersPicker value={layers} onChange={setLayers} runtime={runtime} architecture={arch} />
                </Field>
              </div>
            </Section>
          )}

          <Section title="Permissions">
            <Field
              label="Execution role"
              htmlFor="fn-role"
              optional
              help="The function's code receives temporary credentials for this role in AWS_ACCESS_KEY_ID and friends. Without a role it runs without credentials."
            >
              <RolePicker id="fn-role" value={role} onChange={(v) => setRole(v)} service="lambda.amazonaws.com" allowNone placeholder="No execution role" className="max-w-xl" />
            </Field>
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
                {pkg === "Image" ? (
                  <SummaryItem label="Container image">
                    <span className="font-mono text-[13px] break-all">{imageUri || "-"}</span>
                  </SummaryItem>
                ) : (
                  <>
                    <SummaryItem label="Runtime">{rt ? <RuntimeBadge runtime={rt.name} label={rt.label} /> : "-"}</SummaryItem>
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
                      <span className="font-mono text-[13px] break-all">{handler || "-"}</span>
                    </SummaryItem>
                    {layers.length > 0 && <SummaryItem label="Layers">{layers.length}</SummaryItem>}
                  </>
                )}
                <SummaryItem label="Architecture">
                  <span className="font-mono text-[13px]">{arch}</span>
                </SummaryItem>
                <SummaryItem label="Execution role">
                  <span className="font-mono text-[13px] break-all">{role ? role.split("/").pop() : "None"}</span>
                </SummaryItem>
                <SummaryItem label="Memory / timeout">
                  {Number.isInteger(mem) ? formatMemoryMB(mem) : "-"} / {Number.isInteger(to) && to > 0 ? formatTimeout(to) : "-"}
                </SummaryItem>
                <SummaryItem label="Environment variables">{rowsToTags(env) ? Object.keys(rowsToTags(env)!).length : "None"}</SummaryItem>
              </dl>
              {submitted && !valid && <p className="text-destructive text-xs" role="alert">Some settings need attention. Check the highlighted fields.</p>}
              <div className="flex gap-2 border-t pt-4">
                <Button type="button" variant="outline" asChild>
                  <Link href="/lambda/">Cancel</Link>
                </Button>
                <Button type="submit" className="flex-1" disabled={pending || (pkg === "Zip" && !runtime)}>
                  {pending ? <Loader2 className="animate-spin" /> : <Rocket />}
                  Create function
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
      <dt className="text-faint text-xs font-medium">{label}</dt>
      <dd className="min-w-0 break-words">{children}</dd>
    </div>
  )
}
