"use client"

import { useEffect, useState } from "react"
import Link from "next/link"
import { roleHref } from "@/components/iam/role-common"
import { useRouter } from "next/navigation"
import { AlertCircle, ArrowLeft, Loader2, Pencil, Play, PlayCircle, Trash2 } from "lucide-react"
import { toast } from "sonner"

import { Button } from "@/components/ui/button"
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog"
import { Input } from "@/components/ui/input"
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select"
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs"
import { CopyButton, CopyableText } from "@/components/console/copy-button"
import { DataTable, cellLinkClass, type Column } from "@/components/console/data-table"
import { EmptyState } from "@/components/console/empty-state"
import { ErrorState } from "@/components/console/error-state"
import { Field } from "@/components/console/form-field"
import { JsonEditor, jsonError } from "@/components/console/json-editor"
import { KeyValueGrid } from "@/components/console/key-value"
import { DetailSkeleton } from "@/components/console/loading"
import { PageHeader } from "@/components/console/page-header"
import { Section } from "@/components/console/section"
import { StatusBadge } from "@/components/console/status-badge"
import { TagList } from "@/components/console/tags-editor"
import { TimeAgo } from "@/components/console/time-ago"
import { ApiError, api, errorMessage } from "@/lib/api"
import { formatDate } from "@/lib/format"
import { revalidate, useApi, useNow, useQueryParam, useSetQueryParam } from "@/lib/hooks"
import { EXECUTION_STATUSES, type SfnExecutionSummary, type StartExecutionResult, type StateMachineDetail } from "@/lib/types"
import {
  ExecStatusBadge,
  ExecutionCountsView,
  SFN_PATH,
  editMachineHref,
  execDuration,
  execStatusLabel,
  executionHref,
  machinePath,
  nameError,
  pretty,
  useDeleteStateMachine,
} from "./common"
import { StateMachineGraph } from "./graph"

const TABS = ["executions", "definition"] as const
type Tab = (typeof TABS)[number]

export function StateMachineDetailPage() {
  const router = useRouter()
  const name = useQueryParam("name")
  const tabParam = useQueryParam("tab")
  const startParam = useQueryParam("start")
  const setParam = useSetQueryParam()
  const tab: Tab = (TABS as readonly string[]).includes(tabParam) ? (tabParam as Tab) : "executions"

  const { data, error, isLoading, mutate } = useApi<StateMachineDetail>(name ? machinePath(name) : null, {
    refreshInterval: (d) => ((d?.executions.RUNNING ?? 0) > 0 ? 3000 : 15000),
  })
  const del = useDeleteStateMachine({ onDeleted: () => router.push("/sfn/") })
  const [starting, setStarting] = useState(false)

  // ?start=1 (from the list's Actions menu) opens the start dialog once.
  useEffect(() => {
    if (startParam && data) {
      setStarting(true)
      setParam("start", null)
    }
  }, [startParam, data, setParam])

  const crumbs = [{ label: "Step Functions", href: "/sfn/" }, { label: "State machines", href: "/sfn/" }, { label: name || "State machine" }]

  if (!name) {
    return (
      <>
        <PageHeader title="State machine" breadcrumbs={crumbs} />
        <EmptyState title="No state machine selected" description="Open a state machine from the list." action={<BackButton />} />
      </>
    )
  }
  if (error && !data) {
    const notFound = error instanceof ApiError && error.status === 404
    return (
      <div className="flex flex-col gap-4">
        <PageHeader title={name} breadcrumbs={crumbs} />
        {notFound ? (
          <Section>
            <EmptyState icon={AlertCircle} title="State machine not found" description={`State machine ${name} does not exist.`} action={<BackButton />} />
          </Section>
        ) : (
          <ErrorState error={error} onRetry={() => mutate()} />
        )}
      </div>
    )
  }
  if (isLoading || !data) return <DetailSkeleton />

  const sm = data.state_machine
  return (
    <div className="flex flex-col gap-4">
      <PageHeader
        title={sm.name}
        badge={<StatusBadge status={sm.status.toLowerCase()} />}
        description={sm.definition.Comment ? String(sm.definition.Comment) : "Standard state machine"}
        breadcrumbs={crumbs}
        actions={
          <>
            <Button variant="outline" size="sm" asChild>
              <Link href={editMachineHref(sm.name)}>
                <Pencil /> Edit
              </Link>
            </Button>
            <Button variant="outline" size="sm" onClick={() => del.remove(sm.name)}>
              <Trash2 /> Delete
            </Button>
            <Button size="sm" onClick={() => setStarting(true)}>
              <Play /> Start execution
            </Button>
          </>
        }
      />

      <Section title="Details">
        <KeyValueGrid
          columns={3}
          items={[
            { label: "Name", value: sm.name },
            { label: "Status", value: <StatusBadge status={sm.status.toLowerCase()} /> },
            { label: "Type", value: sm.type === "EXPRESS" ? "Express" : "Standard" },
            {
              label: "Execution role",
              value: sm.role_arn ? (
                <Link href={roleHref(sm.role_arn.split("/").pop() ?? "")} className="text-primary hover:underline">
                  {sm.role_arn.split("/").pop()}
                </Link>
              ) : (
                ""
              ),
            },
            { label: "Created", value: <span>{formatDate(sm.created_at)} (<TimeAgo value={sm.created_at} />)</span> },
            { label: "Last updated", value: <span>{formatDate(sm.updated_at)} (<TimeAgo value={sm.updated_at} />)</span> },
            { label: "Executions", value: <ExecutionCountsView counts={data.executions} /> },
            { label: "ARN", value: <CopyableText value={sm.arn} />, wide: true },
            ...(sm.tags && Object.keys(sm.tags).length ? [{ label: "Tags", value: <TagList tags={sm.tags} />, wide: true }] : []),
          ]}
        />
      </Section>

      <Tabs value={tab} onValueChange={(v) => setParam("tab", v === "executions" ? null : v)}>
        <TabsList>
          <TabsTrigger value="executions">Executions</TabsTrigger>
          <TabsTrigger value="definition">Definition</TabsTrigger>
        </TabsList>
        <TabsContent value="executions">
          <ExecutionsTab machine={sm.name} onStart={() => setStarting(true)} />
        </TabsContent>
        <TabsContent value="definition">
          <DefinitionTab detail={data} />
        </TabsContent>
      </Tabs>

      <StartExecutionDialog machine={starting ? sm.name : null} onClose={() => setStarting(false)} />
      {del.dialog}
    </div>
  )
}

function BackButton() {
  return (
    <Button variant="outline" size="sm" asChild>
      <Link href="/sfn/">
        <ArrowLeft /> Back to state machines
      </Link>
    </Button>
  )
}

// ---- executions ----

function ExecutionsTab({ machine, onStart }: { machine: string; onStart: () => void }) {
  const [status, setStatus] = useState("all")
  const { data, error, isLoading, isValidating, mutate } = useApi<SfnExecutionSummary[]>(`${machinePath(machine)}/executions`, {
    query: { status: status === "all" ? undefined : status },
    refreshInterval: (d) => ((d ?? []).some((x) => x.status === "RUNNING") ? 2000 : 15000),
  })
  const anyRunning = (data ?? []).some((x) => x.status === "RUNNING")
  const now = useNow(anyRunning ? 1000 : 60_000)

  const columns: Column<SfnExecutionSummary>[] = [
    {
      id: "name",
      header: "Name",
      cell: (x) => (
        <Link href={executionHref(x.id)} className={`${cellLinkClass()} font-mono text-[13px]`}>
          {x.name}
        </Link>
      ),
      value: (x) => x.name,
    },
    { id: "status", header: "Status", cell: (x) => <ExecStatusBadge status={x.status} />, value: (x) => x.status },
    {
      id: "start",
      header: "Started",
      cell: (x) => <span className="whitespace-nowrap">{formatDate(x.start_date)}</span>,
      value: (x) => x.start_date,
      hideBelow: "sm",
    },
    {
      id: "stop",
      header: "Stopped",
      cell: (x) => (x.stop_date ? <span className="whitespace-nowrap">{formatDate(x.stop_date)}</span> : <span className="text-muted-foreground">-</span>),
      value: (x) => x.stop_date ?? "",
      hideBelow: "md",
    },
    {
      id: "duration",
      header: "Duration",
      cell: (x) => <span className="tabular-nums whitespace-nowrap">{execDuration(x.start_date, x.stop_date, now)}</span>,
      value: (x) => (x.stop_date ? new Date(x.stop_date).getTime() : now) - new Date(x.start_date).getTime(),
    },
  ]

  return (
    <DataTable
      title="Executions"
      data={data}
      columns={columns}
      rowId={(x) => x.id}
      loading={isLoading}
      error={error}
      onRefresh={() => mutate()}
      refreshing={isValidating}
      searchPlaceholder="Find executions by name"
      defaultSort={{ id: "start", desc: true }}
      actions={
        <Button size="sm" onClick={onStart}>
          <Play /> Start execution
        </Button>
      }
      filters={
        <Select value={status} onValueChange={(v) => v && setStatus(v)}>
          <SelectTrigger size="sm" className="h-8 w-40" aria-label="Status filter">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="all">All statuses</SelectItem>
            {EXECUTION_STATUSES.map((s) => (
              <SelectItem key={s} value={s}>
                {execStatusLabel(s)}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
      }
      empty={
        status !== "all" ? (
          <EmptyState
            icon={PlayCircle}
            title={`No ${execStatusLabel(status).toLowerCase()} executions`}
            action={
              <Button variant="outline" size="sm" onClick={() => setStatus("all")}>
                Show all executions
              </Button>
            }
          />
        ) : (
          <EmptyState
            icon={PlayCircle}
            title="No executions"
            description="Start an execution with a JSON input. Executions also start from EventBridge rules that target this state machine."
            action={
              <Button size="sm" onClick={onStart}>
                <Play /> Start execution
              </Button>
            }
          />
        )
      }
    />
  )
}

// ---- definition ----

function DefinitionTab({ detail }: { detail: StateMachineDetail }) {
  const sm = detail.state_machine
  const json = pretty(sm.definition)
  return (
    <div className="grid grid-cols-1 items-start gap-4 xl:grid-cols-2">
      <Section title="Graph" bodyClassName="p-3">
        <StateMachineGraph definition={sm.definition} />
      </Section>
      <Section
        title="Definition"
        actions={
          <>
            <CopyButton value={json} size="sm" label="Copy" toastMessage="Definition copied" />
            <Button variant="outline" size="sm" asChild>
              <Link href={editMachineHref(sm.name)}>
                <Pencil /> Edit
              </Link>
            </Button>
          </>
        }
        bodyClassName="p-3"
      >
        <pre className="bg-muted/30 max-h-[36rem] overflow-auto rounded-md border p-3 font-mono text-[12.5px] leading-5">{json}</pre>
      </Section>
    </div>
  )
}

// ---- start execution ----

export function StartExecutionDialog({ machine, onClose, initialInput }: { machine: string | null; onClose: () => void; initialInput?: string }) {
  const router = useRouter()
  const [name, setName] = useState("")
  const [input, setInput] = useState("{}")
  const [pending, setPending] = useState(false)
  const [submitted, setSubmitted] = useState(false)

  useEffect(() => {
    if (machine) {
      setName("")
      setSubmitted(false)
      if (initialInput !== undefined) setInput(initialInput)
    }
  }, [machine, initialInput])

  const nameErr = name ? nameError(name, "Execution names") : null
  const inputErr = jsonError(input)

  const submit = async (e: React.FormEvent) => {
    e.preventDefault()
    setSubmitted(true)
    if (!machine || nameErr || inputErr) return
    setPending(true)
    try {
      const r = await api.post<StartExecutionResult>(`${machinePath(machine)}/executions`, { name: name || undefined, input: JSON.parse(input) })
      toast.success(`Started execution ${r.name}`)
      await revalidate(SFN_PATH)
      onClose()
      router.push(executionHref(r.id))
    } catch (err) {
      toast.error(errorMessage(err))
    } finally {
      setPending(false)
    }
  }

  return (
    <Dialog open={!!machine} onOpenChange={(o) => !o && !pending && onClose()}>
      <DialogContent className="sm:max-w-2xl">
        <form onSubmit={submit} className="flex min-w-0 flex-col gap-4">
          <DialogHeader>
            <DialogTitle>Start execution</DialogTitle>
            <DialogDescription>
              Run <span className="font-mono">{machine}</span> with a JSON input. You are taken to the execution to follow it.
            </DialogDescription>
          </DialogHeader>
          <Field label="Name" optional htmlFor="exec-name" error={submitted || name ? nameErr : undefined} help="Unique within this state machine. Defaults to a random ID.">
            <Input id="exec-name" autoComplete="off" spellCheck={false} value={name} onChange={(e) => setName(e.target.value)} className="font-mono text-[13px]" />
          </Field>
          <Field label="Input">
            <div className="max-h-[50vh] overflow-auto">
              <JsonEditor value={input} onChange={setInput} rows={8} />
            </div>
          </Field>
          <DialogFooter>
            <Button type="button" variant="outline" onClick={onClose} disabled={pending}>
              Cancel
            </Button>
            <Button type="submit" disabled={pending || !!inputErr || !!nameErr}>
              {pending ? <Loader2 className="animate-spin" /> : <Play />}
              Start execution
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}
