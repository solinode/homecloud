import type { Tags } from "./common"

export interface Bucket {
  name: string
  arn: string
  created_at: string
  region: string
  website: boolean
  public: boolean
}

export interface LifecycleRule {
  id: string
  prefix: string
  expiration_days: number
  status?: string
}

export interface BucketDetail {
  name: string
  arn: string
  region: string
  /** "Enabled" | "Suspended" | "" */
  versioning: string
  public: boolean
  policy: string
  object_count: number
  size_bytes: number
  stats_truncated: boolean
  website: boolean
  index_document: string
  error_document: string
  lifecycle_rules: LifecycleRule[] | null
  tags?: Tags | null
  website_url: string
}

export interface S3Object {
  key: string
  size: number
  last_modified: string
  etag: string
  storage_class: string
  version_id: string
  is_latest: boolean
  delete_marker: boolean
}

export interface ObjectListing {
  bucket: string
  prefix: string
  prefixes: string[]
  objects: S3Object[]
  truncated: boolean
}

export interface ObjectMeta {
  key: string
  size: number
  content_type: string
  etag: string
  last_modified: string
  version_id: string
  metadata: Record<string, string> | null
  arn: string
  url: string
}

export interface S3Credentials {
  endpoint: string
  region: string
  access_key_id: string
  secret_access_key: string
}

export interface S3Status {
  status: string
  endpoint: string
  region: string
  console_url: string
}

export interface PresignResult {
  url: string
  expires_at: string
}
