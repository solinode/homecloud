"use client"

import { useEffect, useMemo, useRef, useState } from "react"
import Link from "next/link"
import { useRouter } from "next/navigation"
import { AlertCircle, ArrowLeft, CheckCircle2, FileUp, Info, Layers, Loader2, ShieldCheck, Sparkles } from "lucide-react"
import { toast } from "sonner"

import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert"
import { Button } from "@/components/ui/button"
import { Checkbox } from "@/components/ui/checkbox"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select"
import { EmptyState } from "@/components/console/empty-state"
import { ErrorState } from "@/components/console/error-state"
import { Field } from "@/components/console/form-field"
import { DetailSkeleton } from "@/components/console/loading"
import { PageHeader } from "@/components/console/page-header"
import { Section } from "@/components/console/section"
import { ApiError, api, errorMessage } from "@/lib/api"
import { pluralize } from "@/lib/format"
import { revalidate, useApi, useQueryParam } from "@/lib/hooks"
import type { CfnParamDef, Stack, StackInput, ValidateTemplateResult } from "@/lib/types"
import { cn } from "@/lib/utils"
import {
  STACKS_PATH,
  StackStatusBadge,
  TYPE_META,
  VALIDATE_PATH,
  canUpdate,
  resourceHref,
  stackHref,
  stackNameError,
  stackPath,
  templateFormat,
  templateResourceTypes,
  valueText,
} from "./common"
import { PIPELINE_TEMPLATE } from "./examples"
import { TemplateEditor } from "./template-editor"

const MAX_TEMPLATE_BYTES = 1 << 20

type Validation =
  | { state: "idle" }
  | { state: "checking"; text: string }
  | { state: "done"; text: string; result: ValidateTemplateResult }
  | { state: "error"; text: string; message: string }

type ParamDefs = Record<string, CfnParamDef>

const hasDefault = (d: CfnParamDef) => d.Default !== undefined && d.Default !== null

/**
 * StackForm creates a stack (/cloudformation/create/) or updates one
 * (/cloudformation/create/?update=<name>): template editor with server-side
 * validation, a parameters form generated from the template, and options.
 */
export function StackForm() {
  const router = useRouter()
  const updateName = useQueryParam("update")
  const updating = !!updateName

  const existing = useApi<Stack>(updating ? stackPath(updateName) : null, { revalidateOnFocus: false, keepPreviousData: false })

  const [name, setName] = useState("")
  const [text, setText] = useState("")
  const [values, setValues] = useState<Record<string, string>>({})
  const [disableRollback, setDisableRollback] = useState(false)
  const [submitted, setSubmitted] = useState(false)
  const [pending, setPending] = useState(false)
  const [validation, setValidation] = useState<Validation>({ state: "idle" })
  const [defs, setDefs] = useState<ParamDefs>({})
  const loaded = useRef(false)
  const seq = useRef(0)
  const fileRef = useRef<HTMLInputElement>(null)

  // Update mode: start from the stack's current template.
  useEffect(() => {
    const st = existing.data
    if (st && !loaded.current) {
      loaded.current = true
      setName(st.name)
      setText(st.template)
    }
  }, [existing.data])

  const validate = async (src: string): Promise<ValidateTemplateResult | null> => {
    const my = ++seq.current
    setValidation({ state: "checking", text: src })
    try {
      const result = await api.post<ValidateTemplateResult>(VALIDATE_PATH, { template: src })
      if (my === seq.current) {
        setValidation({ state: "done", text: src, result })
        if (result.valid) setDefs(result.parameters ?? {})
      }
      return result
    } catch (e) {
      if (my === seq.current) setValidation({ state: "error", text: src, message: errorMessage(e) })
      return null
    }
  }

  // Validate as the user types (debounced).
  useEffect(() => {
    if (!text.trim()) {
      seq.current++
      setValidation({ state: "idle" })
      return
    }
    const t = setTimeout(() => validate(text), 700)
    return () => clearTimeout(t)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [text])

  // Seed parameter values when the declared parameters change; keep what the user typed.
  const current = existing.data?.parameters
  useEffect(() => {
    setValues((prev) => {
      const next: Record<string, string> = {}
      for (const [k, d] of Object.entries(defs)) {
        if (k in prev) next[k] = prev[k]
        else if (updating && current && k in current) next[k] = d.NoEcho ? "" : valueText(current[k])
        else next[k] = hasDefault(d) ? valueText(d.Default) : ""
      }
      return next
    })
  }, [defs, updating, current])

  const fresh = validation.state !== "idle" && validation.text === text
  const result = validation.state === "done" && validation.text === text ? validation.result : null
  const valid = !!result?.valid
  const types = useMemo(() => templateResourceTypes(text), [text])
  const paramNames = Object.keys(defs).sort()

  const paramError = (k: string): string | null => {
    const d = defs[k]
    const v = (values[k] ?? "").trim()
    const keepsOld = updating && !!current && k in current
    if (!v) return hasDefault(d) || keepsOld ? null : "A value is required"
    if (d.Type === "Number" && Number.isNaN(Number(v))) return "Enter a number"
    if (d.AllowedValues?.length && !d.AllowedValues.some((a) => String(a) === v)) return `Must be one of ${d.AllowedValues.map(String).join(", ")}`
    return null
  }
  const nameErr = updating ? null : stackNameError(name.trim())
  const paramsOk = paramNames.every((k) => !paramError(k))
  const err = <T,>(v: T) => (submitted ? v : null)

  const buildParams = () => {
    const out: Record<string, unknown> = {}
    for (const k of paramNames) {
      const v = (values[k] ?? "").trim()
      if (!v) continue // server applies the default (create) or keeps the current value (update)
      out[k] = defs[k].Type === "Number" ? Number(v) : v
    }
    return out
  }

  const submit = async () => {
    setSubmitted(true)
    if (nameErr || !text.trim()) return
    let r = result
    if (!r) r = await validate(text)
    if (!r) return
    if (!r.valid) {
      toast.error("Fix the template errors first")
      return
    }
    // Parameters may have just appeared; re-check against the fresh definitions.
    if (!paramsOk || Object.keys(r.parameters ?? {}).some((k) => !(k in defs))) {
      toast.error("Some parameters need attention")
      return
    }
    setPending(true)
    try {
      const body: StackInput = { template: text, parameters: buildParams() }
      const stackName = updating ? updateName : name.trim()
      if (updating) {
        await api.put(stackPath(stackName), body)
        toast.success(`Updating stack ${stackName}`)
      } else {
        await api.post(STACKS_PATH, { ...body, name: stackName, disable_rollback: disableRollback })
        toast.success(`Creating stack ${stackName}`)
      }
      await revalidate(STACKS_PATH)
      router.push(stackHref(stackName, "events"))
    } catch (e) {
      toast.error(errorMessage(e))
      setPending(false)
    }
  }

  const loadExample = () => {
    setText(PIPELINE_TEMPLATE)
    if (!updating && !name.trim()) setName("upload-pipeline")
    toast.success("Loaded the upload pipeline example")
  }

  const onFile = async (f: File | undefined) => {
    if (!f) return
    if (f.size > MAX_TEMPLATE_BYTES) {
      toast.error("Templates are at most 1 MB")
      return
    }
    try {
      setText(await f.text())
      toast.success(`Loaded ${f.name}`)
    } catch (e) {
      toast.error(errorMessage(e))
    }
  }

  const crumbs = [
    { label: "CloudFormation", href: "/cloudformation/" },
    { label: "Stacks", href: "/cloudformation/" },
    ...(updating ? [{ label: updateName, href: stackHref(updateName) }, { label: "Update" }] : [{ label: "Create stack" }]),
  ]
  const title = updating ? `Update stack ${updateName}` : "Create stack"

  if (updating && existing.error && !existing.data) {
    const notFound = existing.error instanceof ApiError && existing.error.status === 404
    return (
      <div className="flex flex-col gap-4">
        <PageHeader title={title} breadcrumbs={crumbs} />
        {notFound ? (
          <Section>
            <EmptyState
              icon={AlertCircle}
              title="Stack not found"
              description={`Stack ${updateName} does not exist.`}
              action={
                <Button variant="outline" size="sm" asChild>
                  <Link href="/cloudformation/">
                    <ArrowLeft /> Back to stacks
                  </Link>
                </Button>
              }
            />
          </Section>
        ) : (
          <ErrorState error={existing.error} onRetry={() => existing.mutate()} />
        )}
      </div>
    )
  }
  if (updating && !existing.data) return <DetailSkeleton />

  const st = existing.data
  const blocked = !!st && !canUpdate(st.status)
  const order = result?.valid ? result.creation_order : []

  return (
    <div className="flex flex-col gap-4">
      <PageHeader
        title={title}
        description={
          updating
            ? "Edit the template or parameters. Resources whose resolved properties change are replaced (deleted, then created again); resources removed from the template are deleted."
            : "Declare resources of any HomeCloud service in a YAML or JSON template. HomeCloud creates them in dependency order and rolls back on failure."
        }
        breadcrumbs={crumbs}
      />

      {st && blocked && (
        <Alert variant="destructive">
          <AlertCircle />
          <AlertTitle>This stack cannot be updated now</AlertTitle>
          <AlertDescription>
            <span>
              The stack is <StackStatusBadge status={st.status} />.{" "}
              {st.status === "ROLLBACK_COMPLETE" ? "A stack whose creation was rolled back can only be deleted." : "Wait for the current operation to finish."}
            </span>
          </AlertDescription>
        </Alert>
      )}

      <form
        onSubmit={(e) => {
          e.preventDefault()
          submit()
        }}
        className="grid grid-cols-1 items-start gap-4 xl:grid-cols-[minmax(0,1fr)_320px]"
      >
        <div className="flex min-w-0 flex-col gap-4">
          {!updating && (
            <Section title="Stack name">
              <Field
                label="Stack name"
                htmlFor="stack-name"
                error={err(nameErr)}
                help="Starts with a letter; letters, digits and hyphens, up to 128 characters. Available to the template as Ref HC::StackName."
              >
                <Input id="stack-name" value={name} onChange={(e) => setName(e.target.value)} placeholder="my-stack" autoComplete="off" className="max-w-md" />
              </Field>
            </Section>
          )}

          <Section
            title="Template"
            description="YAML (with short tags such as !Ref, !GetAtt, !Sub) or JSON. Resource properties use the HomeCloud API's own field names."
            actions={
              <>
                <Button type="button" variant="outline" size="sm" onClick={loadExample}>
                  <Sparkles /> Load example
                </Button>
                <Button type="button" variant="outline" size="sm" onClick={() => fileRef.current?.click()}>
                  <FileUp /> Upload template file
                </Button>
                <Button type="button" variant="outline" size="sm" onClick={() => validate(text)} disabled={!text.trim() || validation.state === "checking"}>
                  {validation.state === "checking" ? <Loader2 className="animate-spin" /> : <ShieldCheck />} Validate
                </Button>
                <input
                  ref={fileRef}
                  type="file"
                  accept=".yaml,.yml,.json,.template,.txt,application/json,application/yaml,text/yaml,text/plain"
                  className="hidden"
                  onChange={(e) => {
                    onFile(e.target.files?.[0])
                    e.target.value = ""
                  }}
                />
              </>
            }
          >
            <div className="flex flex-col gap-2">
              <TemplateEditor id="template" value={text} onChange={setText} invalid={(submitted && !text.trim()) || (!!result && !result.valid)} />
              <div className="text-muted-foreground flex flex-wrap items-center justify-between gap-2 text-xs">
                <ValidationLine validation={validation} fresh={fresh} empty={!text.trim()} submitted={submitted} />
                {text.trim() && (
                  <span>
                    {templateFormat(text)} · {pluralize(text.split("\n").length, "line")}
                  </span>
                )}
              </div>
            </div>
          </Section>

          {result && !result.valid && (
            <Alert variant="destructive">
              <AlertCircle />
              <AlertTitle>Template is not valid</AlertTitle>
              <AlertDescription>
                <ul className="list-disc pl-4">
                  {result.error.split("\n").map((l, i) => (
                    <li key={i} className="font-mono text-[13px] break-words">
                      {l}
                    </li>
                  ))}
                </ul>
              </AlertDescription>
            </Alert>
          )}

          {result?.valid && (
            <Section
              title="Resources"
              description={
                result.description ? (
                  <>
                    <span className="text-foreground">{result.description}</span> · created in this order
                  </>
                ) : (
                  "Created in this order; deleted in reverse."
                )
              }
              flush
            >
              <ol className="divide-y">
                {order.map((id, i) => {
                  const t = types[id]
                  const meta = t ? TYPE_META[t] : undefined
                  const was = st?.resources?.[id]
                  return (
                    <li key={id} className="flex flex-wrap items-center gap-x-3 gap-y-1 px-4 py-2 text-sm">
                      <span className="text-muted-foreground w-6 text-right tabular-nums">{i + 1}.</span>
                      <span className="font-medium">{id}</span>
                      {t && <span className="text-muted-foreground font-mono text-[13px]">{t}</span>}
                      {meta?.waits && <span className="text-muted-foreground text-xs">(waits until {meta.waits})</span>}
                      {updating && (
                        <span className="text-muted-foreground ml-auto text-xs">
                          {was ? (
                            <>
                              existing{" "}
                              {resourceHref(was.type, was.physical_id) ? (
                                <Link href={resourceHref(was.type, was.physical_id)!} className="text-primary font-mono hover:underline">
                                  {was.physical_id}
                                </Link>
                              ) : (
                                <span className="font-mono">{was.physical_id}</span>
                              )}
                            </>
                          ) : (
                            "new"
                          )}
                        </span>
                      )}
                    </li>
                  )
                })}
              </ol>
              {updating && st?.order && st.order.some((id) => !order.includes(id)) && (
                <p className="border-t px-4 py-2 text-sm text-amber-700 dark:text-amber-300">
                  Will be deleted (no longer in the template): {st.order.filter((id) => !order.includes(id)).join(", ")}
                </p>
              )}
            </Section>
          )}

          <Section title="Parameters" description={paramNames.length ? "Values for the template's Parameters section." : undefined}>
            {paramNames.length === 0 ? (
              <p className="text-muted-foreground text-sm">
                {valid || !text.trim() ? "This template declares no parameters." : "Parameters appear here once the template validates."}
              </p>
            ) : (
              <div className="grid grid-cols-1 gap-4 md:grid-cols-2">
                {paramNames.map((k) => (
                  <ParamField
                    key={k}
                    name={k}
                    def={defs[k]}
                    value={values[k] ?? ""}
                    onChange={(v) => setValues((p) => ({ ...p, [k]: v }))}
                    error={err(paramError(k))}
                    keepsCurrent={updating && !!current && k in current}
                  />
                ))}
              </div>
            )}
          </Section>

          <Section title="Stack options">
            {updating ? (
              <p className="text-muted-foreground flex gap-2 text-sm">
                <Info className="mt-0.5 size-4 shrink-0" />
                Updates do not roll back: if a resource fails, the stack becomes UPDATE_FAILED and resources already replaced keep their new
                configuration. Fix the template and update again.
              </p>
            ) : (
              <div className="flex items-start gap-3">
                <Checkbox id="disable-rollback" checked={disableRollback} onCheckedChange={(v) => setDisableRollback(v === true)} className="mt-0.5" />
                <div className="flex flex-col gap-0.5">
                  <Label htmlFor="disable-rollback" className="font-medium">
                    Disable rollback
                  </Label>
                  <p className="text-muted-foreground text-xs">
                    By default a failed creation deletes the resources already created (ROLLBACK_COMPLETE). With rollback disabled they are kept for
                    troubleshooting and the stack becomes CREATE_FAILED.
                  </p>
                </div>
              </div>
            )}
          </Section>
        </div>

        <aside className="xl:sticky xl:top-4">
          <Section title="Summary">
            <div className="flex flex-col gap-4">
              <dl className="flex flex-col gap-3 text-sm">
                <SummaryItem label="Stack name">{(updating ? updateName : name.trim()) || "-"}</SummaryItem>
                <SummaryItem label="Template">
                  {!text.trim() ? (
                    "-"
                  ) : valid ? (
                    <span className="flex items-center gap-1 text-emerald-700 dark:text-emerald-400">
                      <CheckCircle2 className="size-3.5" /> Valid {templateFormat(text)}
                    </span>
                  ) : result ? (
                    <span className="text-destructive flex items-center gap-1">
                      <AlertCircle className="size-3.5" /> Invalid
                    </span>
                  ) : (
                    <span className="text-muted-foreground">Not validated yet</span>
                  )}
                </SummaryItem>
                <SummaryItem label="Resources">{valid ? pluralize(order.length, "resource") : "-"}</SummaryItem>
                <SummaryItem label="Parameters">
                  {paramNames.length
                    ? paramNames.map((k) => (
                        <div key={k} className="flex min-w-0 gap-1">
                          <span className="text-muted-foreground">{k}:</span>
                          <span className="truncate font-mono text-[13px]">
                            {defs[k].NoEcho ? (values[k] ? "****" : "-") : values[k] || (updating && current && k in current ? "(current)" : "-")}
                          </span>
                        </div>
                      ))
                    : "None"}
                </SummaryItem>
                {!updating && <SummaryItem label="Rollback on failure">{disableRollback ? "Disabled" : "Enabled"}</SummaryItem>}
              </dl>
              {submitted && (nameErr || !paramsOk || !text.trim()) && (
                <p className="text-destructive text-xs">Some settings need attention. Check the highlighted fields.</p>
              )}
              <div className="flex flex-col gap-2 border-t pt-4">
                <Button type="submit" disabled={pending || blocked || (fresh && !!result && !result.valid)}>
                  {pending ? <Loader2 className="animate-spin" /> : <Layers />}
                  {updating ? "Update stack" : "Create stack"}
                </Button>
                <Button type="button" variant="outline" asChild>
                  <Link href={updating ? stackHref(updateName) : "/cloudformation/"}>Cancel</Link>
                </Button>
              </div>
            </div>
          </Section>
        </aside>
      </form>
    </div>
  )
}

function ValidationLine({ validation, fresh, empty, submitted }: { validation: Validation; fresh: boolean; empty: boolean; submitted: boolean }) {
  if (empty)
    return (
      <span className={cn(submitted && "text-destructive")}>{submitted ? "Enter a template, load the example or upload a file" : "Enter a template"}</span>
    )
  if (!fresh || validation.state === "checking")
    return (
      <span className="flex items-center gap-1">
        <Loader2 className="size-3.5 animate-spin" /> Validating...
      </span>
    )
  if (validation.state === "error")
    return (
      <span className="text-destructive flex items-center gap-1">
        <AlertCircle className="size-3.5" /> Could not validate: {validation.message}
      </span>
    )
  if (validation.state === "done" && !validation.result.valid)
    return (
      <span className="text-destructive flex min-w-0 items-center gap-1">
        <AlertCircle className="size-3.5 shrink-0" /> <span className="truncate">{validation.result.error}</span>
      </span>
    )
  return (
    <span className="flex items-center gap-1 text-emerald-600 dark:text-emerald-400">
      <CheckCircle2 className="size-3.5" /> Template is valid
    </span>
  )
}

function ParamField({
  name,
  def,
  value,
  onChange,
  error,
  keepsCurrent,
}: {
  name: string
  def: CfnParamDef
  value: string
  onChange: (v: string) => void
  error: string | null
  keepsCurrent: boolean
}) {
  const id = `param-${name}`
  const allowed = (def.AllowedValues ?? []).map(String)
  const defaultText = hasDefault(def) ? valueText(def.Default) : ""
  const help = [
    def.Description,
    def.Type === "CommaDelimitedList" ? "Comma-separated list." : "",
    keepsCurrent && def.NoEcho ? "Leave empty to keep the current value." : "",
    !keepsCurrent && defaultText && !def.NoEcho ? `Default: ${defaultText}` : "",
  ]
    .filter(Boolean)
    .join(" ")
  return (
    <Field
      label={
        <span className="flex flex-wrap items-center gap-2">
          <span className="font-mono text-[13px]">{name}</span>
          <span className="text-muted-foreground text-xs font-normal">{def.Type || "String"}</span>
          {def.NoEcho && <span className="text-muted-foreground text-xs font-normal">· NoEcho</span>}
        </span>
      }
      htmlFor={id}
      help={help || undefined}
      error={error}
      optional={hasDefault(def) || keepsCurrent}
    >
      {allowed.length > 0 ? (
        <Select value={value} onValueChange={(v) => v && onChange(v)}>
          <SelectTrigger id={id} className="w-full">
            <SelectValue placeholder="Choose a value" />
          </SelectTrigger>
          <SelectContent>
            {allowed.map((a) => (
              <SelectItem key={a} value={a}>
                {a}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
      ) : (
        <Input
          id={id}
          value={value}
          onChange={(e) => onChange(e.target.value)}
          type={def.NoEcho ? "password" : def.Type === "Number" ? "number" : "text"}
          autoComplete={def.NoEcho ? "new-password" : "off"}
          placeholder={keepsCurrent && def.NoEcho ? "Unchanged" : defaultText && !def.NoEcho ? defaultText : undefined}
          className={cn(def.Type !== "String" && "font-mono text-[13px]")}
        />
      )}
    </Field>
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
