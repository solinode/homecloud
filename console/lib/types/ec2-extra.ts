import type { Tags } from "./common"

export interface KeyPair {
  id: string
  name: string
  type: "rsa" | "ed25519" | string
  fingerprint: string
  public_key: string
  created_at: string
  tags?: Tags
}

export interface Snapshot {
  id: string
  volume_id: string
  volume_size: number
  state: "pending" | "completed" | "error" | string
  message?: string
  description?: string
  start_time: string
  completed_at?: string
  encrypted?: boolean
  tags?: Tags
}

export interface InternetGateway {
  id: string
  vpc_id?: string
  tags?: Tags
  auto?: boolean
}

export interface RouteEntry {
  destination: string
  gateway_id?: string
  target?: string
  target_kind?: string
  origin: string
}

export interface RouteTable {
  id: string
  vpc_id: string
  routes: RouteEntry[] | null
  associations: { id: string; subnet_id?: string; main?: boolean }[] | null
  tags?: Tags
}

// ---- CloudWatch Logs / alarms ----

export interface MetricFilter {
  filterName: string
  filterPattern: string
  metricTransformations: {
    metricName: string
    metricNamespace: string
    metricValue: string
    defaultValue?: number
    unit?: string
  }[]
  creationTime: number
  logGroupName: string
}

export interface SubscriptionFilter {
  filterName: string
  logGroupName: string
  filterPattern: string
  destinationArn: string
  roleArn?: string
  distribution: string
  creationTime: number
}

export interface InsightsResults {
  status: string
  results: { field: string; value: string }[][]
  statistics: { recordsMatched: number; recordsScanned: number; bytesScanned: number }
}

export interface AlarmHistoryItem {
  alarm_name: string
  timestamp: string
  type: string
  summary: string
  data: string
}

// ---- EventBridge buses and Scheduler ----

export interface EventBus {
  name: string
  arn: string
  description?: string
  dead_letter_arn?: string
  created_at?: string
}

export interface ScheduleGroup {
  Name: string
  Arn: string
  State: string
}

export interface Schedule {
  Name: string
  GroupName: string
  Arn: string
  Description?: string
  ScheduleExpression: string
  ScheduleExpressionTimezone?: string
  StartDate?: number
  EndDate?: number
  State: string
  FlexibleTimeWindow: { Mode: string; MaximumWindowInMinutes?: number }
  Target: { Arn: string; RoleArn: string; Input?: string }
  ActionAfterCompletion?: string
}
