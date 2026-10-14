// GET /api/v1/system/status (IAM action homecloud:GetSystemStatus).

export type SystemHealth = "ok" | "degraded" | "down"
export type SubsystemStatus = "ok" | "warning" | "error" | "unknown"

export interface Subsystem {
  name: string
  /** core | helper | disk | loop */
  kind: string
  status: SubsystemStatus
  detail: string
  last_run?: string
  last_run_age_seconds?: number
}

export interface SystemStatus {
  status: SystemHealth
  version: string
  commit: string
  go_version: string
  region: string
  account_id: string
  started_at: string
  uptime_seconds: number
  host: { os: string; arch: string; cpus: number }
  process: { pid: number; goroutines: number; heap_bytes: number; sys_bytes: number }
  docker: {
    reachable: boolean
    server_version: string
    os: string
    kernel_version: string
    architecture: string
    cpus: number
    memory_bytes: number
    storage_driver: string
    containers: number
    containers_running: number
    images: number
    error: string
  }
  subsystems: Subsystem[]
  disk: {
    /** used_bytes is null until the first background measurement */
    data_dir: { path: string; used_bytes: number | null; fs_total_bytes: number; fs_free_bytes: number }
    volumes: { count: number; size_bytes: number; error?: string }
    measured_at: string | null
  }
  containers: { service: string; state: string; count: number }[]
  lambda?: {
    environments_idle: number
    environments_busy: number
    environments_starting: number
    cold_starts: number
    warm_starts: number
  }
  recent_errors: { time: string; source: string; message: string }[]
  metrics: { path: string; action: string; listen_addr: string }
}
