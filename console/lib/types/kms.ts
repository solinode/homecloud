import type { Tags } from "./common"

export type KmsKeyState = "Enabled" | "Disabled" | "PendingDeletion"

/** A KMS key as returned by GET /api/v1/kms/keys and /keys/{id}. */
export interface KmsKey {
  id: string
  arn: string
  description: string
  key_spec: string
  key_usage: string
  state: KmsKeyState | string
  /** Service-managed (alias/hc/*) keys cannot be changed. */
  managed: boolean
  rotation_enabled: boolean
  last_rotated?: string | null
  deletion_date?: string | null
  created_at: string
  key_versions: number
  aliases: string[]
  tags?: Tags | null
}

export interface KmsAlias {
  name: string
  key_id: string
}

export interface CreateKmsKeyInput {
  description?: string
  alias?: string
  rotation_enabled?: boolean
  tags?: Tags
}

export interface KmsEncryptResult {
  ciphertext_blob: string
  /** Key ARN. */
  key_id: string
}

export interface KmsDecryptResult {
  /** base64 */
  plaintext: string
  /** Key ARN. */
  key_id: string
}
