"use client"

import { useEffect, useMemo, useState } from "react"
import Link from "next/link"
import { useRouter } from "next/navigation"
import { Boxes, Info, Loader2, Plus, X } from "lucide-react"
import { toast } from "sonner"

import { Alert, AlertDescription } from "@/components/ui/alert"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select"
import { Skeleton } from "@/components/ui/skeleton"
import { ErrorState } from "@/components/console/error-state"
import { Field } from "@/components/console/form-field"
import { PageHeader } from "@/components/console/page-header"
import { Section } from "@/components/console/section"
import { TagsEditor, rowsToTags, tagsToRows, type TagRow } from "@/components/console/tags-editor"
import { TimeAgo } from "@/components/console/time-ago"
import { api, errorMessage, seg } from "@/lib/api"
import { formatBytes, formatMemoryMB } from "@/lib/format"
import { revalidate, useApi, useQueryParam } from "@/lib/hooks"
import type { EcsRegisterTaskDefinitionInput, EcsTaskDefinition, Secret } from "@/lib/types"
import { cn } from "@/lib/utils"
import {
  ECS_PATH,
  TASK_DEFS_PATH,
  formatCpu,
  joinCommand,
  splitCommand,
  splitValueFrom,
  taskDefHref,
  type EcrRepositoryDetail,
  type EcrRepositoryInfo,
} from "./common"

const FAMILY_RE = /^[a-zA-Z0-9_-]{1,255}$/
const CPU_PRESETS = [0.125, 0.25, 0.5, 1, 2, 4, 8, 16]
const MEMORY_PRESETS = [128, 256, 512, 1024, 2048, 4096, 8192]

interface SecretRow {
  name: string
  secret: string
  key: string
}

export function RegisterTaskDefinition() {
  const router = useRouter()
  const from = useQueryParam("from")
  const source = useApi<EcsTaskDefinition>(from ? `${TASK_DEFS_PATH}/${seg(from)}` : null, { revalidateOnFocus: false })
  const secretsList = useApi<Secret[]>("/api/v1/secrets", { revalidateOnFocus: false })

  const [family, setFamily] = useState("")
  const [image, setImage] = useState("")
  const [cpu, setCpu] = useState("0.25")
  const [memory, setMemory] = useState("512")
  const [port, setPort] = useState("")
  const [command, setCommand] = useState("")
  const [entrypoint, setEntrypoint] = useState("")
  const [envRows, setEnvRows] = useState<TagRow[]>([])
  const [secretRows, setSecretRows] = useState<SecretRow[]>([])
  const [browse, setBrowse] = useState(false)
  const [loaded, setLoaded] = useState("")
  const [submitted, setSubmitted] = useState(false)
  const [pending, setPending] = useState(false)

  // Prefill from ?from=family:revision once.
  useEffect(() => {
    const td = source.data
    if (!td || loaded === from) return
    setFamily(td.family)
    setImage(td.image)
    setCpu(String(td.cpu))
    setMemory(String(td.memory_mb))
    setPort(td.container_port ? String(td.container_port) : "")
    setCommand(joinCommand(td.command))
    setEntrypoint(joinCommand(td.entrypoint))
    setEnvRows(tagsToRows(td.environment))
    setSecretRows(
      (td.secrets ?? []).map((s) => {
        const [secret, key] = splitValueFrom(s.value_from)
        return { name: s.name, secret, key }
      }),
    )
    setLoaded(from)
  }, [source.data, from, loaded])

  const cmd = splitCommand(command)
  const ep = splitCommand(entrypoint)
  const c = Number(cpu)
  const m = Number(memory)
  const p = Number(port)

  const errors = useMemo(() => {
    const e: Record<string, string> = {}
    if (!FAMILY_RE.test(family)) e.family = "1-255 letters, digits, hyphens or underscores"
    if (!image.trim()) e.image = "Enter an image or choose one from ECR"
    else if (/\s/.test(image.trim())) e.image = "Image references cannot contain spaces"
    if (!(c >= 0.125 && c <= 16)) e.cpu = "0.125-16 vCPU"
    if (!Number.isInteger(m) || m < 64 || m > 122880) e.memory = "64-122880 MB"
    if (port && (!Number.isInteger(p) || p < 1 || p > 65535)) e.port = "1-65535"
    if (cmd.error) e.command = cmd.error
    if (ep.error) e.entrypoint = ep.error
    const envKeys = envRows.map((r) => r.key.trim()).filter(Boolean)
    const secretKeys = secretRows.map((r) => r.name.trim()).filter(Boolean)
    if (new Set(envKeys).size !== envKeys.length) e.env = "Variable names must be unique"
    else if (envRows.some((r) => !r.key.trim() && r.value)) e.env = "Every value needs a variable name"
    else if (envKeys.some((k) => !/^[A-Za-z_][A-Za-z0-9_]*$/.test(k))) e.env = "Names are letters, digits and underscores, not starting with a digit"
    secretRows.forEach((r, i) => {
      if (!/^[A-Za-z_][A-Za-z0-9_]*$/.test(r.name.trim())) e[`sec${i}name`] = "Enter a variable name"
      else if (secretKeys.filter((k) => k === r.name.trim()).length > 1 || envKeys.includes(r.name.trim())) e[`sec${i}name`] = "Name already used"
      if (!r.secret) e[`sec${i}secret`] = "Choose a secret"
    })
    return e
  }, [family, image, c, m, port, p, cmd.error, ep.error, envRows, secretRows])
  const err = (k: string) => (submitted ? errors[k] : undefined)
  const valid = Object.keys(errors).length === 0

  const body: EcsRegisterTaskDefinitionInput = {
    family,
    image: image.trim(),
    cpu: c,
    memory_mb: m,
    container_port: port ? p : undefined,
    command: cmd.argv.length ? cmd.argv : undefined,
    entrypoint: ep.argv.length ? ep.argv : undefined,
    environment: rowsToTags(envRows),
    secrets: secretRows.length ? secretRows.map((r) => ({ name: r.name.trim(), value_from: r.key.trim() ? `${r.secret}:${r.key.trim()}` : r.secret })) : undefined,
  }

  const register = async () => {
    setSubmitted(true)
    if (!valid) {
      toast.error("Fix the highlighted fields before registering")
      return
    }
    setPending(true)
    try {
      const td = await api.post<EcsTaskDefinition>(TASK_DEFS_PATH, body)
      toast.success(`Registered ${td.family}:${td.revision}`)
      await revalidate(ECS_PATH)
      router.push(taskDefHref(td.family, td.revision))
    } catch (e) {
      toast.error(errorMessage(e))
      setPending(false)
    }
  }

  const setSecret = (i: number, patch: Partial<SecretRow>) => setSecretRows(secretRows.map((r, j) => (j === i ? { ...r, ...patch } : r)))
  const cpuOptions = CPU_PRESETS.includes(c) || !cpu ? CPU_PRESETS : [...CPU_PRESETS, c].sort((a, b) => a - b)
  const newRevision = !!from && loaded === from

  return (
    <div className="flex flex-col gap-4">
      <PageHeader
        title={newRevision ? `Create new revision of ${family || from}` : "Register task definition"}
        description="Registering under an existing family creates its next revision. Revisions are immutable."
        breadcrumbs={[
          { label: "ECS", href: "/ecs/" },
          { label: "Task definitions", href: "/ecs/task-definitions/" },
          { label: newRevision ? "New revision" : "Register" },
        ]}
      />
      {from && source.error && <ErrorState error={source.error} onRetry={() => source.mutate()} />}
      {newRevision && (
        <Alert>
          <Info />
          <AlertDescription>
            Prefilled from <span className="font-mono">{from}</span>. Change what you need and register to create the next revision.
          </AlertDescription>
        </Alert>
      )}

      <form
        onSubmit={(e) => {
          e.preventDefault()
          register()
        }}
        className="grid grid-cols-1 items-start gap-4 xl:grid-cols-[minmax(0,1fr)_320px]"
      >
        <div className="flex min-w-0 flex-col gap-4">
          <Section title="Task definition family">
            <Field
              label="Family"
              htmlFor="td-family"
              error={err("family")}
              help="Revisions of the same family are numbered 1, 2, 3... Services reference family:revision."
            >
              <Input id="td-family" value={family} onChange={(e) => setFamily(e.target.value)} placeholder="e.g. web" className="max-w-md font-mono" />
            </Field>
          </Section>

          <Section title="Container">
            <div className="flex flex-col gap-4">
              <Field
                label="Image URI"
                htmlFor="td-image"
                error={err("image")}
                help="An image in ECR (localhost:5500/repo:tag) or any public registry (e.g. nginx:alpine)."
              >
                <div className="flex flex-col gap-2 sm:flex-row">
                  <Input
                    id="td-image"
                    value={image}
                    onChange={(e) => setImage(e.target.value)}
                    placeholder="localhost:5500/my-app:latest"
                    className="font-mono text-[13px]"
                    spellCheck={false}
                  />
                  <Button type="button" variant="outline" onClick={() => setBrowse(!browse)} className="shrink-0">
                    <Boxes /> {browse ? "Hide ECR images" : "Browse ECR"}
                  </Button>
                </div>
              </Field>
              {browse && (
                <EcrPicker
                  value={image}
                  onPick={(uri) => {
                    setImage(uri)
                    setBrowse(false)
                  }}
                />
              )}
              <div className="grid grid-cols-1 gap-4 sm:grid-cols-3">
                <Field label="CPU" htmlFor="td-cpu" error={err("cpu")}>
                  <Select value={cpu} onValueChange={(v) => v && setCpu(v)}>
                    <SelectTrigger id="td-cpu" className="w-full">
                      <SelectValue />
                    </SelectTrigger>
                    <SelectContent>
                      {cpuOptions.map((v) => (
                        <SelectItem key={v} value={String(v)}>
                          {formatCpu(v)}
                        </SelectItem>
                      ))}
                    </SelectContent>
                  </Select>
                </Field>
                <Field
                  label="Memory (MB)"
                  htmlFor="td-mem"
                  error={err("memory")}
                  help={Number.isInteger(m) && m >= 64 ? formatMemoryMB(m) : "64-122880"}
                >
                  <Input id="td-mem" type="number" min={64} max={122880} list="td-mem-presets" value={memory} onChange={(e) => setMemory(e.target.value)} />
                  <datalist id="td-mem-presets">
                    {MEMORY_PRESETS.map((v) => (
                      <option key={v} value={v} />
                    ))}
                  </datalist>
                </Field>
                <Field label="Container port" htmlFor="td-port" optional error={err("port")} help="Used by load balancers and published for standalone tasks.">
                  <Input id="td-port" type="number" min={1} max={65535} value={port} onChange={(e) => setPort(e.target.value)} placeholder="8080" />
                </Field>
              </div>
              <Field
                label="Command"
                htmlFor="td-cmd"
                optional
                error={err("command")}
                help={
                  cmd.argv.length ? (
                    <span className="font-mono break-all">{JSON.stringify(cmd.argv)}</span>
                  ) : (
                    'Overrides the image\'s CMD. Shell-style (quotes allowed) or a JSON array, e.g. python app.py --port 8000 or ["sh", "-c", "exec app"].'
                  )
                }
              >
                <Input
                  id="td-cmd"
                  value={command}
                  onChange={(e) => setCommand(e.target.value)}
                  placeholder="image default"
                  className="font-mono text-[13px]"
                  spellCheck={false}
                />
              </Field>
              <Field
                label="Entrypoint"
                htmlFor="td-ep"
                optional
                error={err("entrypoint")}
                help={ep.argv.length ? <span className="font-mono break-all">{JSON.stringify(ep.argv)}</span> : "Overrides the image's ENTRYPOINT."}
              >
                <Input
                  id="td-ep"
                  value={entrypoint}
                  onChange={(e) => setEntrypoint(e.target.value)}
                  placeholder="image default"
                  className="font-mono text-[13px]"
                  spellCheck={false}
                />
              </Field>
            </div>
          </Section>

          <Section title="Environment variables" description="Plain-text variables. Use secrets below for passwords and keys.">
            <div className="flex flex-col gap-2">
              <TagsEditor rows={envRows} onChange={setEnvRows} keyPlaceholder="Name" valuePlaceholder="Value" addLabel="Add variable" max={200} />
              {err("env") && <p className="text-destructive text-xs">{err("env")}</p>}
            </div>
          </Section>

          <Section
            title="Secrets"
            description="Values are read from Secrets Manager when each task starts. Add a JSON key to extract one field of a key/value secret."
            actions={
              <Link href="/secrets/create/" className="text-primary text-sm hover:underline">
                Create secret
              </Link>
            }
          >
            <div className="flex flex-col gap-3">
              {secretRows.length === 0 && <p className="text-muted-foreground text-sm">No secrets.</p>}
              {secretRows.length > 0 && (
                <div className="text-muted-foreground hidden grid-cols-[minmax(0,1fr)_minmax(0,1.4fr)_minmax(0,0.8fr)_2rem] gap-2 text-xs font-medium sm:grid">
                  <span>Environment variable</span>
                  <span>Secret</span>
                  <span>JSON key (optional)</span>
                </div>
              )}
              {secretRows.map((r, i) => (
                <div key={i} className="grid grid-cols-[minmax(0,1fr)_2rem] gap-2 rounded-md border p-2 sm:grid-cols-[minmax(0,1fr)_minmax(0,1.4fr)_minmax(0,0.8fr)_2rem] sm:border-0 sm:p-0">
                  <div className="flex flex-col gap-1">
                    <Input
                      value={r.name}
                      onChange={(e) => setSecret(i, { name: e.target.value })}
                      placeholder="DB_PASSWORD"
                      className="h-8 font-mono text-[13px]"
                      aria-label="Environment variable"
                    />
                    {err(`sec${i}name`) && <span className="text-destructive text-xs">{err(`sec${i}name`)}</span>}
                  </div>
                  <Button
                    type="button"
                    variant="ghost"
                    size="icon"
                    className="col-start-2 row-start-1 size-8 sm:col-start-4"
                    onClick={() => setSecretRows(secretRows.filter((_, j) => j !== i))}
                    aria-label="Remove secret"
                  >
                    <X />
                  </Button>
                  <div className="col-span-2 flex flex-col gap-1 sm:col-span-1 sm:col-start-2 sm:row-start-1">
                    <Select value={r.secret} onValueChange={(v) => v && setSecret(i, { secret: v })} disabled={!secretsList.data}>
                      <SelectTrigger size="sm" className="h-8 w-full" aria-label="Secret">
                        <SelectValue placeholder={secretsList.data ? "Choose a secret" : "Loading secrets..."} />
                      </SelectTrigger>
                      <SelectContent>
                        {r.secret && secretsList.data && !secretsList.data.some((s) => s.name === r.secret) && (
                          <SelectItem value={r.secret}>
                            <span className="font-mono text-[13px]">{r.secret}</span>
                            <span className="text-destructive text-xs">(not found)</span>
                          </SelectItem>
                        )}
                        {(secretsList.data ?? []).map((s) => (
                          <SelectItem key={s.name} value={s.name}>
                            <span className="font-mono text-[13px]">{s.name}</span>
                          </SelectItem>
                        ))}
                      </SelectContent>
                    </Select>
                    {err(`sec${i}secret`) && <span className="text-destructive text-xs">{err(`sec${i}secret`)}</span>}
                  </div>
                  <Input
                    value={r.key}
                    onChange={(e) => setSecret(i, { key: e.target.value })}
                    placeholder="password"
                    className="col-span-2 h-8 font-mono text-[13px] sm:col-span-1 sm:col-start-3 sm:row-start-1"
                    aria-label="JSON key"
                  />
                </div>
              ))}
              {secretsList.error && <ErrorState error={secretsList.error} onRetry={() => secretsList.mutate()} />}
              <div>
                <Button type="button" variant="outline" size="sm" onClick={() => setSecretRows([...secretRows, { name: "", secret: "", key: "" }])}>
                  <Plus /> Add secret
                </Button>
              </div>
            </div>
          </Section>
        </div>

        <aside className="flex flex-col gap-4 xl:sticky xl:top-4">
          <Section title="Summary">
            <div className="flex flex-col gap-4">
              <dl className="flex flex-col gap-3 text-sm">
                <SummaryItem label="Family">
                  <span className="font-mono text-[13px]">{family || "-"}</span>
                </SummaryItem>
                <SummaryItem label="Image">
                  <span className="font-mono text-xs break-all">{image || "-"}</span>
                </SummaryItem>
                <SummaryItem label="Size">
                  {formatCpu(c || 0)}, {Number.isInteger(m) ? formatMemoryMB(m) : "-"}
                </SummaryItem>
                <SummaryItem label="Container port">{port || "None"}</SummaryItem>
                <SummaryItem label="Environment">
                  {Object.keys(body.environment ?? {}).length} variables, {secretRows.length} secrets
                </SummaryItem>
              </dl>
              <details className="border-t pt-3 text-sm">
                <summary className="text-muted-foreground cursor-pointer text-xs font-medium">Request JSON</summary>
                <pre className="bg-muted/50 mt-2 max-h-72 overflow-auto rounded-md border p-2 font-mono text-[11.5px]">{JSON.stringify(body, null, 2)}</pre>
              </details>
              {submitted && !valid && <p className="text-destructive text-xs">Some settings need attention. Check the highlighted fields.</p>}
              <div className="flex flex-col gap-2 border-t pt-4">
                <Button type="submit" disabled={pending || (!!from && source.isLoading)}>
                  {pending && <Loader2 className="animate-spin" />}
                  {newRevision ? "Create new revision" : "Register"}
                </Button>
                <Button type="button" variant="outline" asChild>
                  <Link href="/ecs/task-definitions/">Cancel</Link>
                </Button>
              </div>
            </div>
          </Section>
        </aside>
      </form>
    </div>
  )
}

/** EcrPicker lists ECR repositories and their images; picking one yields "<repo uri>:<tag>" (or the digest URI for untagged images). */
function EcrPicker({ value, onPick }: { value: string; onPick: (uri: string) => void }) {
  const repos = useApi<EcrRepositoryInfo[]>("/api/v1/ecr/repositories", { revalidateOnFocus: false })
  const [repo, setRepo] = useState("")
  useEffect(() => {
    if (repo || !repos.data?.length) return
    const match = repos.data.find((r) => value.startsWith(`${r.uri}:`) || value.startsWith(`${r.uri}@`))
    setRepo((match ?? repos.data[0]).name)
  }, [repos.data, repo, value])
  const detail = useApi<EcrRepositoryDetail>(repo ? `/api/v1/ecr/repositories/${seg(repo)}` : null)
  const images = detail.data?.images ?? []

  return (
    <div className="bg-muted/30 flex flex-col gap-3 rounded-md border p-3">
      {repos.error ? (
        <ErrorState error={repos.error} onRetry={() => repos.mutate()} />
      ) : !repos.data ? (
        <Skeleton className="h-8 w-64" />
      ) : repos.data.length === 0 ? (
        <p className="text-muted-foreground text-sm">
          No ECR repositories.{" "}
          <Link href="/ecr/" className="text-primary hover:underline">
            Create a repository
          </Link>{" "}
          and push an image, or enter any registry image above.
        </p>
      ) : (
        <>
          <Select value={repo} onValueChange={(v) => v && setRepo(v)}>
            <SelectTrigger size="sm" className="w-full max-w-md" aria-label="ECR repository">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {repos.data.map((r) => (
                <SelectItem key={r.name} value={r.name}>
                  <span className="font-mono text-[13px]">{r.name}</span>
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
          {detail.error ? (
            <ErrorState error={detail.error} onRetry={() => detail.mutate()} />
          ) : !detail.data ? (
            <Skeleton className="h-16" />
          ) : images.length === 0 ? (
            <div className="text-muted-foreground flex flex-col gap-1 text-sm">
              <span>This repository has no images yet. Push one with:</span>
              <pre className="bg-card overflow-x-auto rounded border p-2 font-mono text-[12px]">{(detail.data.push_commands ?? []).join("\n")}</pre>
            </div>
          ) : (
            <div className="bg-card divide-y overflow-hidden rounded-md border">
              {images.flatMap((im) => {
                const refs = im.tags?.length ? im.tags.map((t) => `${detail.data!.repository.uri}:${t}`) : [im.uri]
                return refs.map((uri) => (
                  <button
                    key={uri}
                    type="button"
                    onClick={() => onPick(uri)}
                    className={cn(
                      "hover:bg-muted/50 flex w-full flex-col gap-0.5 px-3 py-2 text-left sm:flex-row sm:items-center sm:gap-3",
                      uri === value && "bg-primary/5 dark:bg-primary/10",
                    )}
                  >
                    <span className="min-w-0 flex-1 truncate font-mono text-[13px]">{uri}</span>
                    <span className="text-muted-foreground flex shrink-0 gap-3 text-xs">
                      <span>{formatBytes(im.size_bytes)}</span>
                      <span>
                        pushed <TimeAgo value={im.pushed_at} />
                      </span>
                    </span>
                  </button>
                ))
              })}
            </div>
          )}
        </>
      )}
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
