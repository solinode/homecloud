"use client"

import { useMemo, useState } from "react"
import Link from "next/link"
import { AlertCircle, ArrowLeft, ChevronDown, ChevronRight, Loader2, Play, RefreshCw, Square } from "lucide-react"
import { toast } from "sonner"

import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert"
import { Button } from "@/components/ui/button"
import { ConfirmDialog } from "@/components/console/confirm-dialog"
import { CopyButton, CopyableText } from "@/components/console/copy-button"
import { DataTable, cellLinkClass, type Column } from "@/components/console/data-table"
import { EmptyState } from "@/components/console/empty-state"
import { ErrorState } from "@/components/console/error-state"
import { KeyValueGrid } from "@/components/console/key-value"
import { DetailSkeleton } from "@/components/console/loading"
import { PageHeader } from "@/components/console/page-header"
import { Section } from "@/components/console/section"
import { ApiError, api } from "@/lib/api"
import { formatDate } from "@/lib/format"
import { revalidate, useApi, useNow, useQueryParam } from "@/lib/hooks"
import type { SfnExecution, SfnHistoryEvent, StateMachineDetail } from "@/lib/types"
import { cn } from "@/lib/utils"
import { ExecStatusBadge, SFN_PATH, execDuration, executionPath, machineHref, machinePath, pretty } from "./common"
import { StateMachineGraph, statusesFromHistory } from "./graph"
import { StartExecutionDialog } from "./state-machine-detail"

export function ExecutionDetail() {
  const id = useQueryParam("id")
  const { data: x, error, isLoading, isValidating, mutate } = useApi<SfnExecution>(id ? executionPath(id) : null, {
    refreshInterval: (d) => (d?.status === "RUNNING" ? 1000 : 0),
  })
  const running = x?.status === "RUNNING"
  const machine = useApi<StateMachineDetail>(x ? machinePath(x.state_machine) : null, { revalidateOnFocus: false })
  const now = useNow(running ? 1000 : 60_000)
  const [stopping, setStopping] = useState(false)
  const [rerun, setRerun] = useState(false)

  const statuses = useMemo(() => statusesFromHistory(x?.history, running), [x?.history, running])

  const crumbs = [
    { label: "Step Functions", href: "/sfn/" },
    { label: "State machines", href: "/sfn/" },
    ...(x ? [{ label: x.state_machine, href: machineHref(x.state_machine) }] : []),
    { label: x?.name ?? "Execution" },
  ]

  if (!id) {
    return (
      <>
        <PageHeader title="Execution" breadcrumbs={crumbs} />
        <EmptyState title="No execution selected" description="Open an execution from a state machine's Executions tab." action={<BackButton />} />
      </>
    )
  }
  if (error && !x) {
    const notFound = error instanceof ApiError && error.status === 404
    return (
      <div className="flex flex-col gap-4">
        <PageHeader title="Execution" breadcrumbs={crumbs} />
        {notFound ? (
          <Section>
            <EmptyState
              icon={AlertCircle}
              title="Execution not found"
              description={`Execution ${id} does not exist. Only the 200 most recent finished executions of each state machine are kept.`}
              action={<BackButton />}
            />
          </Section>
        ) : (
          <ErrorState error={error} onRetry={() => mutate()} />
        )}
      </div>
    )
  }
  if (isLoading || !x) return <DetailSkeleton />

  const failed = x.status === "FAILED" || x.status === "TIMED_OUT" || x.status === "ABORTED"
  const inputJson = pretty(x.input)

  return (
    <div className="flex flex-col gap-4">
      <PageHeader
        title={x.name}
        badge={<ExecStatusBadge status={x.status} />}
        description={
          <>
            Execution of{" "}
            <Link href={machineHref(x.state_machine)} className="text-primary hover:underline">
              {x.state_machine}
            </Link>
          </>
        }
        breadcrumbs={crumbs}
        actions={
          <>
            <Button variant="outline" size="sm" onClick={() => mutate()} disabled={isValidating} aria-label="Refresh">
              {isValidating ? <Loader2 className="animate-spin" /> : <RefreshCw />}
              Refresh
            </Button>
            <Button variant="outline" size="sm" onClick={() => setRerun(true)}>
              <Play /> New execution
            </Button>
            {running && (
              <Button variant="destructive" size="sm" onClick={() => setStopping(true)}>
                <Square /> Stop execution
              </Button>
            )}
          </>
        }
      />

      {running && (
        <p className="text-muted-foreground flex items-center gap-2 text-sm">
          <Loader2 className="size-4 animate-spin" /> The execution is running. This page refreshes every second.
        </p>
      )}
      {failed && (x.error || x.cause) && (
        <Alert variant={x.status === "ABORTED" ? "default" : "destructive"}>
          <AlertCircle />
          <AlertTitle className="font-mono text-[13px]">{x.error || "Error"}</AlertTitle>
          {x.cause && (
            <AlertDescription>
              <span className="font-mono text-[13px] break-words whitespace-pre-wrap">{x.cause}</span>
            </AlertDescription>
          )}
        </Alert>
      )}

      <Section title="Details">
        <KeyValueGrid
          columns={3}
          items={[
            { label: "Status", value: <ExecStatusBadge status={x.status} /> },
            {
              label: "State machine",
              value: (
                <Link href={machineHref(x.state_machine)} className={cellLinkClass()}>
                  {x.state_machine}
                </Link>
              ),
            },
            { label: "Execution ID", value: <CopyableText value={x.id} display={`${x.id.slice(0, 16)}…`} /> },
            { label: "Started", value: formatDate(x.start_date) },
            { label: "Stopped", value: x.stop_date ? formatDate(x.stop_date) : "" },
            { label: "Duration", value: <span className="tabular-nums">{execDuration(x.start_date, x.stop_date, now)}</span> },
            { label: "Execution ARN", value: <CopyableText value={x.arn} />, wide: true },
          ]}
        />
      </Section>

      <Section title="Graph" description="State colors come from this execution's event history. The graph shows the state machine's current definition." bodyClassName="p-3">
        {machine.data ? (
          <StateMachineGraph definition={machine.data.state_machine.definition} statuses={statuses} />
        ) : machine.error ? (
          <ErrorState error={machine.error} onRetry={() => machine.mutate()} />
        ) : (
          <div className="text-muted-foreground flex h-40 items-center justify-center gap-2 text-sm">
            <Loader2 className="size-4 animate-spin" /> Loading definition...
          </div>
        )}
      </Section>

      <div className="grid grid-cols-1 gap-4 lg:grid-cols-2">
        <JsonSection title="Input" value={x.input} />
        {running ? (
          <Section title="Output">
            <p className="text-muted-foreground text-sm">The output is available when the execution succeeds.</p>
          </Section>
        ) : x.status === "SUCCEEDED" ? (
          <JsonSection title="Output" value={x.output ?? null} />
        ) : (
          <Section title="Output">
            <p className="text-muted-foreground text-sm">No output: the execution {x.status === "ABORTED" ? "was stopped" : x.status === "TIMED_OUT" ? "timed out" : "failed"}.</p>
          </Section>
        )}
      </div>

      <EventHistory history={x.history ?? []} start={x.start_date} />

      <ConfirmDialog
        open={stopping}
        onOpenChange={setStopping}
        title={`Stop execution ${x.name}?`}
        description="The execution is aborted at its current state (States.Aborted). Work already done by Task states is not undone."
        actionLabel="Stop execution"
        onConfirm={async () => {
          await api.post(`${executionPath(x.id)}/stop`)
          toast.success(`Stopping execution ${x.name}`)
          await revalidate(SFN_PATH)
        }}
      />
      <StartExecutionDialog machine={rerun ? x.state_machine : null} onClose={() => setRerun(false)} initialInput={inputJson} />
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

function JsonSection({ title, value }: { title: string; value: unknown }) {
  const json = pretty(value)
  return (
    <Section title={title} actions={<CopyButton value={json} size="sm" toastMessage={`${title} copied`} />} bodyClassName="p-3">
      <pre className="bg-muted/30 max-h-96 overflow-auto rounded-md border p-3 font-mono text-[12.5px] leading-5">{json}</pre>
    </Section>
  )
}

// ---- event history ----

function eventTone(e: SfnHistoryEvent): string {
  const t = e.type
  if (t.endsWith("StateExited") && e.details && typeof e.details === "object" && "caught" in e.details) return "text-orange-600 dark:text-orange-400"
  if (/(Failed|TimedOut|Aborted)$/.test(t)) return "text-red-600 dark:text-red-400"
  if (/(Succeeded|Exited)$/.test(t)) return "text-emerald-700 dark:text-emerald-400"
  if (/(Retrying|Waiting)$/.test(t)) return "text-amber-700 dark:text-amber-400"
  return "text-blue-700 dark:text-blue-400"
}

function elapsed(ts: string, start: number): string {
  const ms = new Date(ts).getTime() - start
  if (Number.isNaN(ms)) return "-"
  if (ms < 1000) return `${Math.max(0, ms)} ms`
  if (ms < 60_000) return `${(ms / 1000).toFixed(3)} s`
  return `${(ms / 60_000).toFixed(1)} min`
}

function EventHistory({ history, start }: { history: SfnHistoryEvent[]; start: string }) {
  const [open, setOpen] = useState<Set<number>>(new Set())
  const t0 = history.length ? new Date(history[0].timestamp).getTime() : new Date(start).getTime()
  const toggle = (id: number) =>
    setOpen((s) => {
      const n = new Set(s)
      if (n.has(id)) n.delete(id)
      else n.add(id)
      return n
    })

  const columns: Column<SfnHistoryEvent>[] = [
    {
      id: "id",
      header: "ID",
      cell: (e) => (
        <span className="inline-flex items-center gap-1 tabular-nums">
          {e.details !== undefined ? (
            open.has(e.id) ? (
              <ChevronDown className="text-muted-foreground size-3.5" />
            ) : (
              <ChevronRight className="text-muted-foreground size-3.5" />
            )
          ) : (
            <span className="inline-block size-3.5" />
          )}
          {e.id}
        </span>
      ),
      value: (e) => e.id,
    },
    { id: "type", header: "Type", cell: (e) => <span className={cn("font-medium whitespace-nowrap", eventTone(e))}>{e.type}</span>, value: (e) => e.type },
    { id: "state", header: "State", cell: (e) => e.state ?? <span className="text-muted-foreground">-</span>, value: (e) => e.state ?? "" },
    {
      id: "elapsed",
      header: "Elapsed",
      cell: (e) => <span className="tabular-nums whitespace-nowrap">{elapsed(e.timestamp, t0)}</span>,
      value: (e) => new Date(e.timestamp).getTime(),
      hideBelow: "sm",
    },
    {
      id: "time",
      header: "Timestamp",
      cell: (e) => <span className="whitespace-nowrap">{formatDate(e.timestamp)}</span>,
      value: (e) => e.timestamp,
      hideBelow: "md",
    },
  ]

  return (
    <DataTable
      title="Event history"
      description="Click an event to see its details."
      data={history}
      columns={columns}
      rowId={(e) => String(e.id)}
      searchPlaceholder="Filter events by type or state"
      defaultSort={{ id: "id" }}
      pageSize={100}
      onRowClick={(e) => e.details !== undefined && toggle(e.id)}
      expanded={(e) =>
        open.has(e.id) && e.details !== undefined ? (
          <div className="flex min-w-0 flex-col gap-2">
            <div className="flex items-center justify-between gap-2">
              <span className="text-muted-foreground text-xs font-medium">Details</span>
              <CopyButton value={pretty(e.details)} label="Copy details" />
            </div>
            <pre className="bg-card max-h-80 max-w-[calc(100vw-4rem)] overflow-auto rounded-md border p-3 font-mono text-[12.5px] leading-5">{pretty(e.details)}</pre>
          </div>
        ) : null
      }
      empty={<p className="text-muted-foreground p-8 text-center text-sm">No events recorded yet.</p>}
    />
  )
}
