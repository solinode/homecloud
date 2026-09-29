// Types for the Step Functions API (cli/internal/svc/sfn).

import type { Tags } from "./common"

export type ExecutionStatus = "RUNNING" | "SUCCEEDED" | "FAILED" | "TIMED_OUT" | "ABORTED"

export const EXECUTION_STATUSES: ExecutionStatus[] = ["RUNNING", "SUCCEEDED", "FAILED", "TIMED_OUT", "ABORTED"]

/** Execution counts by status; statuses without executions are omitted. */
export type ExecutionCounts = Partial<Record<ExecutionStatus, number>>

// ---- Amazon States Language (loosely typed: definitions may be partial or invalid) ----

export interface AslCatcher {
  ErrorEquals?: string[]
  Next?: string
  ResultPath?: string | null
}

export interface AslRetrier {
  ErrorEquals?: string[]
  IntervalSeconds?: number
  MaxAttempts?: number
  BackoffRate?: number
}

export interface AslState {
  Type?: string
  Comment?: string
  Next?: string
  End?: boolean
  Resource?: string
  Choices?: Record<string, unknown>[]
  Default?: string
  Catch?: AslCatcher[]
  Retry?: AslRetrier[]
  Branches?: AslMachine[]
  Iterator?: AslMachine
  ItemProcessor?: AslMachine
  Error?: string
  Cause?: string
  [key: string]: unknown
}

export interface AslMachine {
  Comment?: string
  StartAt?: string
  States?: Record<string, AslState>
  TimeoutSeconds?: number
  [key: string]: unknown
}

// ---- API shapes ----

/** GET /api/v1/sfn/state-machines rows. */
export interface StateMachineSummary {
  name: string
  arn: string
  status: string
  created_at: string
  updated_at: string
  executions: ExecutionCounts
}

export interface StateMachine {
  name: string
  arn: string
  definition: AslMachine
  status: string
  created_at: string
  updated_at: string
  tags?: Tags
}

/** GET /api/v1/sfn/state-machines/{name}. */
export interface StateMachineDetail {
  state_machine: StateMachine
  executions: ExecutionCounts
}

export interface CreateStateMachineInput {
  name: string
  /** A JSON object, or a string holding the JSON. */
  definition: AslMachine | string
  tags?: Tags
}

/** GET /api/v1/sfn/state-machines/{name}/executions rows (newest first; ?status= filters). */
export interface SfnExecutionSummary {
  id: string
  arn: string
  name: string
  status: ExecutionStatus
  start_date: string
  stop_date?: string | null
}

export interface SfnHistoryEvent {
  id: number
  timestamp: string
  /** ExecutionStarted, <Type>StateEntered/Exited/Failed, TaskScheduled/Succeeded/Failed/Retrying, WaitStateWaiting, Execution<Status>. */
  type: string
  state?: string
  details?: unknown
}

/** GET /api/v1/sfn/executions/{id}. */
export interface SfnExecution extends SfnExecutionSummary {
  state_machine: string
  input: unknown
  output?: unknown
  error?: string
  cause?: string
  history?: SfnHistoryEvent[]
}

export interface StartExecutionInput {
  name?: string
  input?: unknown
}

export interface StartExecutionResult {
  id: string
  arn: string
  name: string
  start_date: string
}

export interface ValidateDefinitionResult {
  valid: boolean
  errors: string[]
}
