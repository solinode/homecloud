import type { Tags } from "./common"

export type SsmParameterType = "String" | "StringList" | "SecureString"

/** Parameter summary from GET /api/v1/ssm/parameters (DescribeParameters). */
export interface SsmParameter {
  name: string
  arn: string
  type: SsmParameterType
  /** KMS key ARN for SecureString parameters; "" otherwise. */
  key_id: string
  description: string
  version: number
  last_modified: string
  last_modified_by: string
  data_type: string
  tags?: Tags | null
}

/** GET /api/v1/ssm/parameter?name=&with_decryption= (GetParameter). */
export interface SsmParameterValue {
  name: string
  arn: string
  type: SsmParameterType
  /** Ciphertext blob for SecureString unless with_decryption=true. */
  value: string
  version: number
  last_modified: string
  data_type: string
  labels?: string[] | null
}

/** One entry of GET /api/v1/ssm/parameter/history. */
export interface SsmParameterVersion {
  version: number
  /** "****" for SecureString unless with_decryption=true. */
  value: string
  last_modified: string
  modified_by: string
  labels?: string[] | null
}

export interface PutSsmParameterInput {
  name: string
  value: string
  type?: SsmParameterType
  key_id?: string
  description?: string
  overwrite?: boolean
  tags?: Tags
}

export interface PutSsmParameterResult {
  version: number
  tier: string
}
