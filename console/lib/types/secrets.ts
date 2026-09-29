import type { Tags } from "./common"

export interface SecretVersion {
  id: string
  stages: string[]
  /** True when the value is SecretBinary. */
  binary?: boolean
  /** ARN of the customer KMS key that sealed this version; absent for the default key. */
  kms_key?: string
  created_at: string
  last_accessed?: string
}

/** Rotation schedule as stored by Secrets Manager (AWS field names). */
export interface SecretRotationRules {
  AutomaticallyAfterDays?: number
  Duration?: string
  ScheduleExpression?: string
}

export interface Secret {
  name: string
  arn: string
  description: string
  managed_by?: string
  /** Customer KMS key ARN; absent when the default key (alias/aws/secretsmanager) is used. */
  kms_key_id?: string
  versions: SecretVersion[]
  created_at: string
  updated_at: string
  last_accessed?: string
  deleted_at?: string
  deletion_date?: string
  tags?: Tags | null
  /** Resource-based policy JSON; absent when none is attached. */
  resource_policy?: string
  rotation_enabled?: boolean
  rotation_lambda_arn?: string
  rotation_rules?: SecretRotationRules | null
  last_rotated?: string
  next_rotation?: string
  /** Why the last rotation failed. */
  rotation_error?: string
}

export interface SecretValue {
  name: string
  value: string
  binary?: boolean
  version_id: string
  stages: string[]
  created_at: string
}
