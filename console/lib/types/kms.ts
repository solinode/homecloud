import type { Tags } from "./common"

export type KmsKeyState = "Enabled" | "Disabled" | "PendingDeletion"

export type KmsKeyUsage = "ENCRYPT_DECRYPT" | "SIGN_VERIFY" | "GENERATE_VERIFY_MAC"

/** A KMS key as returned by GET /api/v1/kms/keys and /keys/{id}. */
export interface KmsKey {
  id: string
  arn: string
  description: string
  /** SYMMETRIC_DEFAULT, RSA_2048/3072/4096, ECC_NIST_P256/P384/P521 or HMAC_224/256/384/512. */
  key_spec: string
  key_usage: KmsKeyUsage | string
  state: KmsKeyState | string
  /** Service-managed (alias/hc/*, alias/aws/*) keys cannot be changed. */
  managed: boolean
  rotation_enabled: boolean
  /** Automatic rotation interval (90-2560 days, default 365). */
  rotation_period_days?: number
  next_rotation?: string | null
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
  created_at?: string
  updated_at?: string
}

export interface CreateKmsKeyInput {
  description?: string
  alias?: string
  rotation_enabled?: boolean
  key_spec?: string
  key_usage?: string
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
  encryption_algorithm?: string
}

export interface KmsGrant {
  grant_id: string
  name?: string
  grantee_principal: string
  retiring_principal?: string
  operations: string[]
  constraints?: unknown
  created_at: string
  /** Only returned when the grant is created. */
  grant_token?: string
}

export interface KmsPublicKey {
  key_id: string
  /** base64 DER (SubjectPublicKeyInfo). */
  public_key: string
  pem: string
  key_spec: string
  key_usage: string
  encryption_algorithms?: string[] | null
  signing_algorithms?: string[] | null
}
