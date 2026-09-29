import type { Tags } from "./common"

export interface EcrRepository {
  name: string
  arn: string
  /** "localhost:5500/<name>" */
  uri: string
  tag_mutable: boolean
  created_at: string
  description?: string
  tags?: Tags
}

export interface EcrImage {
  /** "linux/arm64"; empty when the manifest has no config */
  platform?: string
  tags: string[]
  digest: string
  size_bytes: number
  media_type: string
  /** The image config's "created" time (zero time when unknown). */
  pushed_at?: string
  /** "<registry>/<repo>@sha256:..." */
  uri: string
}

export interface EcrRepositoryDetail {
  repository: EcrRepository
  images: EcrImage[]
  push_commands: string[]
}

export interface EcrStatus {
  /** "starting" | "available" | error text */
  status: string
  /** "localhost:5500" */
  registry: string
  push_example: string
}
