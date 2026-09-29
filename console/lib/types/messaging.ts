import type { Tags } from "./common"

// ---- SQS ----

export interface RedrivePolicy {
  /** name of the dead-letter queue */
  dead_letter_queue: string
  max_receive_count: number
}

export interface Queue {
  name: string
  arn: string
  url: string
  fifo: boolean
  content_based_deduplication: boolean
  visibility_timeout: number
  message_retention_seconds: number
  delay_seconds: number
  receive_wait_time_seconds: number
  max_message_size: number
  redrive_policy?: RedrivePolicy | null
  created_at: string
  last_modified: string
  tags?: Tags | null
  approximate_number_of_messages: number
  approximate_number_of_messages_not_visible: number
  approximate_number_of_messages_delayed: number
  messages_sent: number
  messages_received: number
  messages_deleted: number
  /** queues whose redrive policy points at this queue (this queue is their DLQ) */
  dead_letter_source_queues: string[] | null
}

export interface QueueAttributesInput {
  visibility_timeout?: number
  message_retention_seconds?: number
  delay_seconds?: number
  receive_wait_time_seconds?: number
  max_message_size?: number
  content_based_deduplication?: boolean
  /** null/omitted leaves it; {dead_letter_queue:""} removes it */
  redrive_policy?: RedrivePolicy | null
  tags?: Tags
}

export interface CreateQueueInput extends QueueAttributesInput {
  name: string
  fifo: boolean
}

export interface MessageAttribute {
  /** String | Number | Binary */
  data_type: string
  string_value: string
}

export interface SendMessageInput {
  body: string
  delay_seconds?: number
  message_attributes?: Record<string, MessageAttribute>
  group_id?: string
  dedup_id?: string
}

export interface SendMessageResult {
  message_id: string
  md5_of_body: string
  sequence_number?: string
  duplicate?: boolean
}

export interface ReceivedMessage {
  message_id: string
  receipt_handle: string
  body: string
  md5_of_body: string
  /** SentTimestamp, ApproximateReceiveCount, ApproximateFirstReceiveTimestamp, MessageGroupId ... */
  attributes: Record<string, string>
  message_attributes?: Record<string, MessageAttribute> | null
}

export interface PeekedMessage {
  message_id: string
  body: string
  sent_at: string
  receive_count: number
  state: "available" | "in-flight" | "delayed"
  group_id: string
  message_attributes: Record<string, MessageAttribute> | null
  size: number
  source_queue: string
}

// ---- SNS ----

export interface Topic {
  name: string
  arn: string
  display_name: string
  fifo: boolean
  created_at: string
  tags?: Tags | null
  messages_published: number
  subscriptions: number
}

export type SubscriptionProtocol = "sqs" | "lambda" | "http" | "https"

export interface Subscription {
  arn: string
  topic_arn: string
  topic_name: string
  protocol: SubscriptionProtocol
  endpoint: string
  raw_message_delivery: boolean
  filter_policy?: Record<string, string[]> | null
  status: string
  delivered: number
  failed: number
  last_error?: string
  last_delivery?: string
  created_at: string
}

export interface TopicDetail extends Topic {
  subscription_list: Subscription[] | null
}

// ---- EventBridge ----

export interface RuleTarget {
  id: string
  /** Lambda function, SQS queue or SNS topic ARN */
  arn: string
  /** constant JSON sent instead of the event */
  input?: string
}

export interface EventRule {
  name: string
  arn: string
  description: string
  event_bus: string
  schedule_expression?: string
  event_pattern?: Record<string, unknown> | null
  state: "ENABLED" | "DISABLED"
  targets: RuleTarget[] | null
  last_triggered?: string
  next_run?: string
  invocations: number
  failed_invocations: number
  last_error?: string
  created_at: string
}

export interface PutRuleInput {
  description?: string
  schedule_expression?: string
  event_pattern?: Record<string, unknown>
  state?: "ENABLED" | "DISABLED"
  targets?: { id?: string; arn: string; input?: string }[]
}

export interface EventEntry {
  source: string
  detail_type: string
  detail?: unknown
  resources?: string[]
}

export interface PutEventsResult {
  entries: { event_id: string; matched_rules: number }[]
  failed_entry_count: number
}
