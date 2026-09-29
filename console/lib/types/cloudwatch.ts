export interface MetricSeries {
  namespace: string
  name: string
  dimensions: Record<string, string> | null
  unit: string
}

export interface Datapoint {
  timestamp: string
  average: number
  sum: number
  minimum: number
  maximum: number
  sample_count: number
}

export type Statistic = "Average" | "Sum" | "Minimum" | "Maximum" | "SampleCount"

export interface MetricQuery {
  namespace: string
  name: string
  dimensions?: Record<string, string>
  start?: string
  end?: string
  /** seconds */
  period?: number
  stat?: Statistic
}

export interface MetricQueryResult {
  namespace: string
  name: string
  dimensions: Record<string, string> | null
  unit: string
  datapoints: Datapoint[]
}

export type AlarmState = "OK" | "ALARM" | "INSUFFICIENT_DATA"

export type ComparisonOperator =
  | "GreaterThanThreshold"
  | "GreaterThanOrEqualToThreshold"
  | "LessThanThreshold"
  | "LessThanOrEqualToThreshold"

export interface Alarm {
  name: string
  arn: string
  description: string
  namespace: string
  metric: string
  dimensions: Record<string, string> | null
  statistic: Statistic
  period: number
  evaluation_periods: number
  threshold: number
  comparison_operator: ComparisonOperator
  alarm_actions: string[]
  ok_actions: string[]
  state: AlarmState
  state_reason: string
  state_updated_at: string
  created_at: string
}

export interface LogGroup {
  name: string
  arn: string
  /** 0 = never expire */
  retention_days: number
  created_at: string
  source: "stored" | "container"
  stored_bytes: number
}

export interface LogStream {
  name: string
  last_event_time: string
  stored_bytes: number
}

export interface LogEvent {
  timestamp: string
  message: string
  stream: string
}
