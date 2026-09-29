import type { Tags } from "./common"

export type KeyType = "S" | "N"

export interface KeyDef {
  name: string
  type: KeyType
}

export interface TableIndex {
  name: string
  partition_key: KeyDef
  sort_key?: KeyDef | null
}

export interface DynamoTable {
  name: string
  arn: string
  partition_key: KeyDef
  sort_key?: KeyDef | null
  global_secondary_indexes: TableIndex[] | null
  ttl_attribute?: string
  status: string
  billing_mode: string
  created_at: string
  tags?: Tags | null
  item_count: number
  size_bytes: number
}

export interface CreateTableInput {
  name: string
  partition_key: KeyDef
  sort_key?: KeyDef
  global_secondary_indexes?: TableIndex[]
  ttl_attribute?: string
  tags?: Tags
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
