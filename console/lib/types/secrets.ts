import type { Tags } from "./common"

export interface SecretVersion {
  id: string
  stages: string[]
  created_at: string
}

export interface Secret {
  name: string
  arn: string
  description: string
  managed_by?: string
  versions: SecretVersion[]
  created_at: string
  updated_at: string
  last_accessed?: string
  deletion_date?: string
  tags?: Tags | null
}

export interface SecretValue {
  name: string
  value: string
  version_id: string
  stages: string[]
  created_at: string
}
