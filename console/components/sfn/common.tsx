"use client"

import { useState, type ReactNode } from "react"
import { toast } from "sonner"

import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip"
import { ConfirmDialog } from "@/components/console/confirm-dialog"
import { StatusBadge, TONES, type Tone } from "@/components/console/status-badge"
import { api, seg } from "@/lib/api"
import { formatDuration } from "@/lib/format"
import { revalidate } from "@/lib/hooks"
import { EXECUTION_STATUSES, type AslMachine, type ExecutionCounts, type ExecutionStatus } from "@/lib/types"
import { cn } from "@/lib/utils"

export const SFN_PATH = "/api/v1/sfn"
export const MACHINES_PATH = "/api/v1/sfn/state-machines"
export const EXECUTIONS_PATH = "/api/v1/sfn/executions"

export const machinePath = (name: string) => `${MACHINES_PATH}/${seg(name)}`
export const executionPath = (id: string) => `${EXECUTIONS_PATH}/${seg(id)}`

export const machineHref = (name: string, tab?: string) => `/sfn/state-machine/?name=${encodeURIComponent(name)}${tab ? `&tab=${tab}` : ""}`
export const editMachineHref = (name: string) => `/sfn/create/?name=${encodeURIComponent(name)}`
export const executionHref = (id: string) => `/sfn/execution/?id=${encodeURIComponent(id)}`

export const NAME_RE = /^[a-zA-Z0-9_-]{1,80}$/

export function nameError(name: string, what = "Names"): string | null {
  if (!name) return "Enter a name"
  if (name.length > 80) return `${what} are at most 80 characters`
  if (!NAME_RE.test(name)) return "Use only letters, digits, hyphens (-) and underscores (_)"
  return null
}

// ---- execution status ----

const EXEC_TONE: Record<ExecutionStatus, Tone> = {
  RUNNING: "info",
  SUCCEEDED: "success",
  FAILED: "danger",
  TIMED_OUT: "danger",
  ABORTED: "neutral",
}

export const execStatusLabel = (s: string) => (s === "TIMED_OUT" ? "Timed out" : s.charAt(0) + s.slice(1).toLowerCase())

export function ExecStatusBadge({ status, className }: { status: string; className?: string }) {
  return <StatusBadge status={status} tone={EXEC_TONE[status as ExecutionStatus] ?? "neutral"} label={execStatusLabel(status)} className={className} />
}

/** Tone per status for the execution counters (timed out reads as a warning next to failed). */
const COUNT_TONE: Record<ExecutionStatus, Tone> = { ...EXEC_TONE, TIMED_OUT: "warning" }

/** Fill and text classes for the per-status execution counters. */
export const EXEC_COUNT_CLASS: Record<ExecutionStatus, string> = Object.fromEntries(
  EXECUTION_STATUSES.map((s) => [s, TONES[COUNT_TONE[s]]]),
) as Record<ExecutionStatus, string>

/** ExecutionCountsView renders "2 running · 5 succeeded ..." as small colored counters. */
export function ExecutionCountsView({ counts, className }: { counts?: ExecutionCounts; className?: string }) {
  const present = EXECUTION_STATUSES.filter((s) => (counts?.[s] ?? 0) > 0)
  if (!present.length) return <span className="text-muted-foreground text-sm">None</span>
  return (
    <span className={cn("flex flex-wrap items-center gap-1", className)}>
      {present.map((s) => (
        <Tooltip key={s}>
          <TooltipTrigger asChild>
            <span className={cn("inline-flex items-center rounded border px-1.5 py-0.5 text-xs font-medium whitespace-nowrap tabular-nums", EXEC_COUNT_CLASS[s])}>
              {counts![s]} {execStatusLabel(s).toLowerCase()}
            </span>
          </TooltipTrigger>
          <TooltipContent>
            {counts![s]} {execStatusLabel(s).toLowerCase()} execution{counts![s] === 1 ? "" : "s"}
          </TooltipContent>
        </Tooltip>
      ))}
    </span>
  )
}

export const totalExecutions = (c?: ExecutionCounts) => Object.values(c ?? {}).reduce((a, b) => a + (b ?? 0), 0)

/** execDuration renders the time between start and stop (or now while running). */
export function execDuration(start: string, stop?: string | null, now = Date.now()): string {
  const a = new Date(start).getTime()
  const b = stop ? new Date(stop).getTime() : now
  if (Number.isNaN(a) || Number.isNaN(b)) return "-"
  const ms = Math.max(0, b - a)
  if (ms < 1000) return `${ms} ms`
  if (ms < 60_000) return `${(ms / 1000).toFixed(ms < 10_000 ? 2 : 1)} s`
  return formatDuration(ms / 1000)
}

export const pretty = (v: unknown) => (v === undefined ? "" : JSON.stringify(v, null, 2))

// ---- samples ----

export function sampleDefinition(): AslMachine {
  return {
    Comment: "Check an order total and route it",
    StartAt: "Prepare",
    States: {
      Prepare: {
        Type: "Pass",
        Parameters: { "order.$": "$", "checkedAt.$": "$$.State.EnteredTime" },
        Next: "Is large order?",
      },
      "Is large order?": {
        Type: "Choice",
        Choices: [{ Variable: "$.order.total", NumericGreaterThan: 100, Next: "Needs review" }],
        Default: "Approved",
      },
      "Needs review": {
        Type: "Fail",
        Error: "Order.TooLarge",
        Cause: "Orders over 100 need a manual review",
      },
      Approved: { Type: "Succeed" },
    },
  }
}

export function lambdaSample(fnArn: string): AslMachine {
  return {
    Comment: "Invoke a Lambda function and catch its errors",
    StartAt: "Invoke",
    States: {
      Invoke: {
        Type: "Task",
        Resource: fnArn,
        ResultPath: "$.result",
        Retry: [{ ErrorEquals: ["States.TaskFailed"], IntervalSeconds: 1, MaxAttempts: 2 }],
        Catch: [{ ErrorEquals: ["States.ALL"], ResultPath: "$.error", Next: "Handle error" }],
        Next: "Done",
      },
      "Handle error": { Type: "Fail", Error: "InvokeFailed", Cause: "The function returned an error" },
      Done: { Type: "Succeed" },
    },
  }
}

export function parallelSample(): AslMachine {
  return {
    Comment: "Fan out: run two branches in parallel, then process each item",
    StartAt: "Fan out",
    States: {
      "Fan out": {
        Type: "Parallel",
        Branches: [
          { StartAt: "Price", States: { Price: { Type: "Pass", Result: { price: 42 }, End: true } } },
          { StartAt: "Stock", States: { Stock: { Type: "Pass", Result: { inStock: true }, End: true } } },
        ],
        ResultPath: "$.checks",
        Next: "Wait a moment",
      },
      "Wait a moment": { Type: "Wait", Seconds: 1, Next: "Each item" },
      "Each item": {
        Type: "Map",
        ItemsPath: "$.items",
        ItemProcessor: { StartAt: "Tag item", States: { "Tag item": { Type: "Pass", Parameters: { "item.$": "$", processed: true }, End: true } } },
        ResultPath: "$.processed",
        End: true,
      },
    },
  }
}

// ---- delete ----

export interface DeleteMachineAction {
  remove: (name: string) => void
  dialog: ReactNode
}

/** useDeleteStateMachine asks for the name before deleting a state machine and its executions. */
export function useDeleteStateMachine(opts: { onDeleted?: (name: string) => void } = {}): DeleteMachineAction {
  const [name, setName] = useState<string | null>(null)
  const dialog = (
    <ConfirmDialog
      open={!!name}
      onOpenChange={(o) => !o && setName(null)}
      title={`Delete state machine ${name ?? ""}?`}
      description="Running executions are stopped and the execution history of this state machine is deleted. EventBridge rules targeting it will fail to start executions."
      confirmText={name ?? undefined}
      onConfirm={async () => {
        if (!name) return
        await api.del(machinePath(name))
        await revalidate(SFN_PATH)
        toast.success(`Deleted state machine ${name}`)
        opts.onDeleted?.(name)
      }}
    />
  )
  return { remove: setName, dialog }
}
