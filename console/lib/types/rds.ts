import type { Tags } from "./common"

/** Database family: RDS (relational), DocumentDB-style (document) or ElastiCache (cache). */
export type DbKind = "relational" | "document" | "cache"

export type DbStatus =
  | "creating"
  | "available"
  | "stopping"
  | "stopped"
  | "starting"
  | "rebooting"
  | "restoring"
  | "backing-up"
  | "modifying"
  | "deleting"
  | "failed"

export interface DbEngine {
  name: string
  label: string
  kind: DbKind
  versions: string[]
  default_port: number
  has_users: boolean
  has_database: boolean
  has_password: boolean
}

export interface DbClass {
  name: string
  vcpus: number
  memory_mb: number
  /** "db" for RDS/DocumentDB, "cache" for ElastiCache */
  kind: "db" | "cache"
}

export interface DbEngines {
  engines: DbEngine[]
  classes: DbClass[]
}

export interface DbEndpoint {
  /** private DNS name inside the VPC */
  address: string
  port: number
  /** host reachable from outside, when publicly accessible */
  public_host: string
  public_port: number
  private_ip: string
  connect_hint: string
}

export interface DbInstance {
  id: string
  arn: string
  kind: DbKind
  engine: string
  engine_version: string
  class: string
  vcpus: number
  memory_mb: number
  storage_gb: number
  status: DbStatus | string
  status_reason?: string
  master_username?: string
  db_name?: string
  /** Secrets Manager secret holding the master credentials ("rds!<id>") */
  secret_name?: string
  endpoint: DbEndpoint
  vpc_id: string
  subnet_id: string
  availability_zone: string
  publicly_accessible: boolean
  backup_retention_days: number
  latest_backup?: string
  deletion_protection: boolean
  container_id?: string
  restored_from?: string
  created_at: string
  tags?: Tags | null
}

export interface CreateDbInput {
  id: string
  engine: string
  engine_version?: string
  class?: string
  storage_gb?: number
  master_username?: string
  master_password?: string
  db_name?: string
  subnet_id?: string
  publicly_accessible?: boolean
  /** fixed host port when publicly accessible (0 = any) */
  port?: number
  backup_retention_days?: number
  deletion_protection?: boolean
  tags?: Tags
}

export interface DbSnapshot {
  id: string
  arn: string
  source_instance: string
  kind: DbKind
  engine: string
  engine_version: string
  type: "manual" | "automated"
  status: "creating" | "available" | "failed" | string
  status_reason?: string
  size_bytes: number
  master_username?: string
  db_name?: string
  storage_gb: number
  created_at: string
}

export interface DbQueryResult {
  exit_code: number
  /** raw stdout of the client (CSV for postgres, TSV for MySQL/MariaDB) */
  output: string
  /** stderr; set when the statement failed */
  error: string
  duration_ms: number
  /** parsed result grid (relational engines only, on success) */
  columns?: string[] | null
  rows?: string[][] | null
}

/** Master credentials stored in the rds!<id> secret. */
export interface DbCredentials {
  username?: string
  password: string
  engine: string
  host: string
  port: number
  dbname?: string
  dbInstanceIdentifier: string
}
