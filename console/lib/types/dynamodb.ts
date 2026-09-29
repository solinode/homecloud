import type { Tags } from "./common"

export type KeyType = "S" | "N"

export interface KeyDef {
  name: string
  type: KeyType
}

export type ProjectionType = "ALL" | "KEYS_ONLY" | "INCLUDE"

export interface IndexProjection {
  type: ProjectionType
  non_key_attributes?: string[]
}

export interface TableIndex {
  name: string
  partition_key: KeyDef
  sort_key?: KeyDef | null
  /** Omitted means ALL. */
  projection?: IndexProjection | null
  status?: string
}

export type StreamViewType = "KEYS_ONLY" | "NEW_IMAGE" | "OLD_IMAGE" | "NEW_AND_OLD_IMAGES"

/** StreamInfo is one of a table's streams; disabled streams stay readable for 24 hours. */
export interface StreamInfo {
  label: string
  view_type: StreamViewType
  created: string
  disabled?: string | null
}

export interface DynamoTable {
  name: string
  arn: string
  partition_key: KeyDef
  sort_key?: KeyDef | null
  global_secondary_indexes: TableIndex[] | null
  local_secondary_indexes?: TableIndex[] | null
  ttl_attribute?: string
  status: string
  billing_mode: string
  created_at: string
  tags?: Tags | null
  item_count: number
  size_bytes: number
  /** Set while a stream is enabled. */
  stream_arn?: string
  stream_view_type?: StreamViewType
  streams?: StreamInfo[] | null
  deletion_protection?: boolean
  table_class?: string
  point_in_time_recovery?: boolean
  sse_enabled?: boolean
}

export interface CreateTableInput {
  name: string
  partition_key: KeyDef
  sort_key?: KeyDef
  global_secondary_indexes?: TableIndex[]
  local_secondary_indexes?: TableIndex[]
  ttl_attribute?: string
  stream_view_type?: StreamViewType
  deletion_protection?: boolean
  tags?: Tags
}

export interface UpdateTableInput {
  ttl_attribute?: string
  add_index?: TableIndex
  remove_index?: string
  tags?: Tags
  /** "" disables the stream. */
  stream_view_type?: StreamViewType | ""
  deletion_protection?: boolean
}

export type DynamoItem = Record<string, unknown>

export type ConditionOp = "eq" | "ne" | "lt" | "le" | "gt" | "ge" | "begins_with" | "contains" | "exists" | "not_exists" | "between"

export interface Condition {
  attr?: string
  op: ConditionOp
  value?: unknown
  value2?: unknown
}

export interface PageInput {
  limit?: number
  start_key?: DynamoItem
  filter?: Condition[]
  projection?: string[]
}

export interface QueryInput extends PageInput {
  index?: string
  partition_value: unknown
  sort_condition?: Condition
  forward?: boolean
}

export interface PageResult {
  items: DynamoItem[]
  count: number
  scanned_count: number
  last_evaluated_key?: DynamoItem
}
