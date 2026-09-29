"use client"

import { useEffect, useMemo, useRef, useState } from "react"
import Link from "next/link"
import { useRouter } from "next/navigation"
import { AlertCircle, ArrowLeft, CheckCircle2, Loader2, Save } from "lucide-react"
import { toast } from "sonner"

import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Select, SelectContent, SelectGroup, SelectItem, SelectLabel, SelectTrigger, SelectValue } from "@/components/ui/select"
import { Skeleton } from "@/components/ui/skeleton"
import { EmptyState } from "@/components/console/empty-state"
import { ErrorState } from "@/components/console/error-state"
import { Field } from "@/components/console/form-field"
import { JsonEditor, jsonError } from "@/components/console/json-editor"
import { PageHeader } from "@/components/console/page-header"
import { Section } from "@/components/console/section"
import { TagList, TagsEditor, rowsToTags, type TagRow } from "@/components/console/tags-editor"
import { ApiError, api, errorMessage } from "@/lib/api"
import { revalidate, useApi, useQueryParam } from "@/lib/hooks"
import type { AslMachine, LambdaFunction, StateMachine, StateMachineDetail, StateMachineSummary, ValidateDefinitionResult } from "@/lib/types"
import { MACHINES_PATH, SFN_PATH, lambdaSample, machineHref, machinePath, nameError, parallelSample, sampleDefinition } from "./common"
import { StateMachineGraph } from "./graph"

const fmt = (d: AslMachine) => JSON.stringify(d, null, 2)

type Validation = { state: "idle" | "checking" } | { state: "done"; result: ValidateDefinitionResult } | { state: "error"; message: string }

/** StateMachineEditor creates a state machine, or edits one when ?name= is set. */
export function StateMachineEditor() {
  const router = useRouter()
  const editName = useQueryParam("name")
  const editing = !!editName

  const existing = useApi<StateMachineDetail>(editing ? machinePath(editName) : null, { revalidateOnFocus: false, keepPreviousData: false })
  const all = useApi<StateMachineSummary[]>(editing ? null : MACHINES_PATH, { revalidateOnFocus: false })
  const fns = useApi<LambdaFunction[]>("/api/v1/lambda/functions", { revalidateOnFocus: false })

  const [name, setName] = useState("")
  const [text, setText] = useState(() => fmt(sampleDefinition()))
  const [tags, setTags] = useState<TagRow[]>([])
  const [submitted, setSubmitted] = useState(false)
  const [pending, setPending] = useState(false)
  const loaded = useRef(false)

  useEffect(() => {
    const m = existing.data?.state_machine
    if (m && !loaded.current) {
      loaded.current = true
      setName(m.name)
      setText(fmt(m.definition))
    }
  }, [existing.data])

  // Live validation against the server, debounced.
  const [validation, setValidation] = useState<Validation>({ state: "idle" })
  const localError = jsonError(text)
  useEffect(() => {
    if (localError) {
      setValidation({ state: "idle" })
      return
    }
    setValidation({ state: "checking" })
    let cancelled = false
    const t = setTimeout(async () => {
      try {
        const result = await api.post<ValidateDefinitionResult>(`${SFN_PATH}/validate`, { definition: text })
        if (!cancelled) setValidation({ state: "done", result })
      } catch (e) {
        if (!cancelled) setValidation({ state: "error", message: errorMessage(e) })
      }
    }, 500)
    return () => {
      cancelled = true
      clearTimeout(t)
    }
  }, [text, localError])

  // The preview keeps showing the last definition that parsed while the JSON is mid-edit.
  const [preview, setPreview] = useState<unknown>(() => sampleDefinition())
  useEffect(() => {
    if (!localError) setPreview(JSON.parse(text))
  }, [text, localError])

  const errors = useMemo(() => {
    const e: Record<string, string> = {}
    if (!editing) {
      const ne = nameError(name)
      if (ne) e.name = ne
      else if (all.data?.some((m) => m.name === name)) e.name = `A state machine named ${name} already exists`
    }
    if (localError) e.definition = `The definition is not valid JSON: ${localError}`
    else if (validation.state === "done" && !validation.result.valid) e.definition = "Fix the definition errors listed below the editor"
    const keys = tags.map((t) => t.key.trim()).filter(Boolean)
    if (new Set(keys).size !== keys.length) e.tags = "Tag keys must be unique"
    return e
  }, [editing, name, all.data, localError, validation, tags])
  const err = (k: string) => (submitted || (k === "name" && name) ? errors[k] : undefined)
  const valid = Object.keys(errors).length === 0

  const applyTemplate = (v: string) => {
    if (!v) return
    if (v === "choice") setText(fmt(sampleDefinition()))
    else if (v === "parallel") setText(fmt(parallelSample()))
    else if (v.startsWith("fn:")) setText(fmt(lambdaSample(v.slice(3))))
  }

  const save = async () => {
    setSubmitted(true)
    if (!valid) {
      toast.error(editing ? "Fix the definition before saving" : "Fix the highlighted fields before creating the state machine")
      return
    }
    setPending(true)
    try {
      const definition = JSON.parse(text) as AslMachine
      if (editing) {
        await api.put<StateMachine>(machinePath(editName), { definition })
        toast.success(`Saved state machine ${editName}`)
      } else {
        await api.post<StateMachine>(MACHINES_PATH, { name, definition, tags: rowsToTags(tags) })
        toast.success(`Created state machine ${name}`)
      }
      await revalidate(SFN_PATH)
      router.push(machineHref(editing ? editName : name, editing ? "definition" : undefined))
    } catch (e) {
      toast.error(errorMessage(e))
      setPending(false)
    }
  }

  const title = editing ? `Edit ${editName}` : "Create state machine"
  const crumbs = [
    { label: "Step Functions", href: "/sfn/" },
    { label: "State machines", href: "/sfn/" },
    ...(editing ? [{ label: editName, href: machineHref(editName) }, { label: "Edit" }] : [{ label: "Create" }]),
  ]

  if (editing && existing.error) {
    const notFound = existing.error instanceof ApiError && existing.error.status === 404
    return (
      <div className="flex flex-col gap-4">
        <PageHeader title={title} breadcrumbs={crumbs} />
        {notFound ? (
          <Section>
            <EmptyState
              icon={AlertCircle}
              title="State machine not found"
              description={`State machine ${editName} does not exist.`}
              action={
                <Button variant="outline" size="sm" asChild>
                  <Link href="/sfn/">
                    <ArrowLeft /> Back to state machines
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

  const cancelHref = editing ? machineHref(editName) : "/sfn/"
  const fnList = fns.data ?? []

  return (
    <div className="flex flex-col gap-4">
      <PageHeader
        title={title}
        description="Write the workflow in Amazon States Language. The graph and validation update as you type."
        breadcrumbs={crumbs}
      />
      <form
        onSubmit={(e) => {
          e.preventDefault()
          save()
        }}
        className="flex flex-col gap-4"
      >
        <Section title="Details">
          <div className="grid grid-cols-1 gap-5 lg:grid-cols-2">
            <Field
              label="State machine name"
              htmlFor="sm-name"
              error={err("name")}
              help={editing ? "The name cannot be changed." : "1-80 letters, digits, hyphens and underscores. It cannot be changed later."}
            >
              <Input
                id="sm-name"
                autoFocus={!editing}
                autoComplete="off"
                spellCheck={false}
                value={name}
                disabled={editing}
                onChange={(e) => setName(e.target.value)}
                placeholder="order-pipeline"
                className="max-w-md font-mono text-[13px]"
              />
            </Field>
            <Field label="Type" help="HomeCloud runs standard workflows: every execution keeps a full event history.">
              <Input value="Standard" disabled className="max-w-md" />
            </Field>
            {editing ? (
              existing.data?.state_machine.tags && Object.keys(existing.data.state_machine.tags).length > 0 ? (
                <Field label="Tags" help="Tags are set when the state machine is created." className="lg:col-span-2">
                  <TagList tags={existing.data.state_machine.tags} />
                </Field>
              ) : null
            ) : (
              <Field label="Tags" optional error={err("tags")} className="lg:col-span-2">
                <TagsEditor rows={tags} onChange={setTags} />
              </Field>
            )}
          </div>
        </Section>

        <Section
          title="Definition"
          description="Task states can invoke Lambda functions (function ARN or arn:aws:states:::lambda:invoke), send SQS messages and publish to SNS topics."
          actions={
            <Select value="" onValueChange={applyTemplate}>
              <SelectTrigger size="sm" className="h-8 w-56" aria-label="Start from a template">
                <SelectValue placeholder="Start from a template" />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="choice">Pass, Choice, Succeed / Fail</SelectItem>
                <SelectItem value="parallel">Parallel and Map</SelectItem>
                <SelectGroup>
                  <SelectLabel>Invoke a Lambda function</SelectLabel>
                  {fnList.length ? (
                    fnList.map((f) => (
                      <SelectItem key={f.arn} value={`fn:${f.arn}`}>
                        {f.name}
                      </SelectItem>
                    ))
                  ) : (
                    <SelectItem value="none" disabled>
                      {fns.data ? "No functions" : "Loading..."}
                    </SelectItem>
                  )}
                </SelectGroup>
              </SelectContent>
            </Select>
          }
        >
          {editing && existing.isLoading ? (
            <div className="grid grid-cols-1 gap-4 lg:grid-cols-2">
              <Skeleton className="h-96" />
              <Skeleton className="h-96" />
            </div>
          ) : (
            <div className="grid grid-cols-1 items-start gap-4 lg:grid-cols-2">
              <div className="flex min-w-0 flex-col gap-3">
                <JsonEditor id="sm-definition" value={text} onChange={setText} rows={22} />
                <ValidationResult validation={validation} jsonInvalid={!!localError} />
              </div>
              <div className="flex min-w-0 flex-col gap-2 lg:sticky lg:top-4">
                <p className="text-muted-foreground text-xs font-medium">Graph preview{localError ? " (last valid JSON)" : ""}</p>
                <StateMachineGraph definition={preview} className="min-h-64" />
              </div>
            </div>
          )}
        </Section>

        <div className="flex flex-wrap items-center justify-end gap-2">
          {submitted && !valid && <p className="text-destructive mr-auto text-xs">Some settings need attention. Check the highlighted fields.</p>}
          <Button type="button" variant="outline" asChild>
            <Link href={cancelHref}>Cancel</Link>
          </Button>
          <Button type="submit" disabled={pending || (editing && !existing.data)}>
            {pending ? <Loader2 className="animate-spin" /> : <Save />}
            {editing ? "Save changes" : "Create state machine"}
          </Button>
        </div>
      </form>
    </div>
  )
}

function ValidationResult({ validation, jsonInvalid }: { validation: Validation; jsonInvalid: boolean }) {
  if (jsonInvalid) return null
  if (validation.state === "error") {
    return (
      <p className="text-destructive flex items-center gap-1.5 text-sm">
        <AlertCircle className="size-4" /> Could not validate: {validation.message}
      </p>
    )
  }
  if (validation.state !== "done") {
    return (
      <p className="text-muted-foreground flex items-center gap-1.5 text-sm">
        <Loader2 className="size-4 animate-spin" /> Validating definition...
      </p>
    )
  }
  const { result } = validation
  if (result.valid) {
    return (
      <p className="flex items-center gap-1.5 text-sm font-medium text-emerald-700 dark:text-emerald-400">
        <CheckCircle2 className="size-4" /> Valid Amazon States Language definition
      </p>
    )
  }
  return (
    <div className="border-destructive/30 bg-destructive/5 text-destructive rounded-md border p-3 text-sm">
      <p className="flex items-center gap-1.5 font-medium">
        <AlertCircle className="size-4" /> {result.errors.length} validation error{result.errors.length === 1 ? "" : "s"}
      </p>
      <ul className="mt-1.5 list-disc space-y-0.5 pl-6 font-mono text-[12.5px] break-words">
        {[...result.errors].sort().map((e, i) => (
          <li key={i}>{e}</li>
        ))}
      </ul>
    </div>
  )
}
