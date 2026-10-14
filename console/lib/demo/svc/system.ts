import type { Health, Subsystem, SystemStatus, WhoAmI } from "@/lib/types"
import { unavailable, type DemoService } from "../engine"
import { ACCOUNT, REGION, DAY, HOUR, MIN, hashStr } from "../util"

const VERSION = "0.4.1"
const LOADED = Date.now()
const BASE_UPTIME = 12 * 24 * 3600 + 5 * 3600 + 1234

const service: DemoService = {
  name: "system",
  seed: () => ({}),
  routes: (r) => {
    r.get("/api/v1/health", (): Health => ({
      status: "ok",
      version: VERSION,
      region: REGION,
      uptime_seconds: BASE_UPTIME + Math.floor((Date.now() - LOADED) / 1000),
    }))
    r.get("/api/v1/auth/whoami", (): WhoAmI => ({
      account_id: ACCOUNT,
      user_name: "demo-admin",
      arn: `arn:aws:iam::${ACCOUNT}:user/demo-admin`,
      root: false,
      region: REGION,
    }))
    r.post("/api/v1/auth/logout", () => ({ ok: true }))
    r.get("/api/v1/system/status", systemStatus)
    r.get("/api/v1/system/info", () => ({ version: VERSION, region: REGION, account_id: ACCOUNT, started_at: new Date(LOADED - BASE_UPTIME * 1000 - DAY * 0).toISOString() }))
    // Backups of the server's data directory need a real server.
    r.get("/api/v1/system/backup", () => {
      throw unavailable("Downloading a backup")
    })
    r.post("/api/v1/system/backup", () => {
      throw unavailable("Creating a backup")
    })
  },
}

// ---- GET /api/v1/system/status ----

const GiB = 1024 ** 3
const MiB = 1024 ** 2

/** Background loop name -> interval in seconds. */
const LOOPS: [string, number][] = [
  ["cloudwatch", 30],
  ["ecs", 5],
  ["elb", 5],
  ["sqs", 2],
  ["lambda-reaper", 30],
  ["lambda-pollers", 1],
  ["events", 60],
  ["autoscaling", 10],
  ["route53", 10],
  ["dynamodb", 30],
  ["cloudtrail", 60],
  ["firewall", 5],
  ["imds", 30],
  ["disk-usage", 300],
  ["maintenance", 3600],
]

/** goDuration formats seconds like Go's time.Duration ("5s", "5m0s", "1h0m0s"). */
function goDuration(sec: number): string {
  const h = Math.floor(sec / 3600)
  const m = Math.floor((sec % 3600) / 60)
  const s = sec % 60
  if (h) return `${h}h${m}m${s}s`
  if (m) return `${m}m${s}s`
  return `${s}s`
}

function systemStatus(): SystemStatus {
  const now = Date.now()
  const startedAt = LOADED - BASE_UPTIME * 1000
  const uptime = Math.floor((now - startedAt) / 1000)
  const tick = Math.floor((now - LOADED) / 15_000)
  const iso = (ms: number) => new Date(ms).toISOString()
  const loops: Subsystem[] = LOOPS.map(([name, every]) => {
    // Ages wander inside the interval so the page looks alive between polls.
    const age = Math.floor(((now / 1000) % every) + (hashStr(name) % 3 === 0 ? 0.4 : 0.1))
    return {
      name: `loop:${name}`,
      kind: "loop",
      status: "ok",
      detail: `every ${goDuration(every)}`,
      last_run: iso(now - age * 1000),
      last_run_age_seconds: age,
    }
  })
  const fsTotal = 500107862016
  const dataDir = 734003200 + (tick % 40) * 96 * 1024
  return {
    status: "ok",
    version: VERSION,
    commit: "fd10aa6",
    go_version: "go1.25.1",
    region: REGION,
    account_id: ACCOUNT,
    started_at: iso(startedAt),
    uptime_seconds: uptime,
    host: { os: "linux", arch: "amd64", cpus: 8 },
    process: { pid: 1187, goroutines: 204 + (tick % 9), heap_bytes: 52 * MiB + (tick % 13) * 640 * 1024, sys_bytes: 118 * MiB },
    docker: {
      reachable: true,
      server_version: "27.4.1",
      os: "Ubuntu 24.04.1 LTS",
      kernel_version: "6.8.0-45-generic",
      architecture: "x86_64",
      cpus: 8,
      memory_bytes: 33554432000,
      storage_driver: "overlay2",
      containers: 17,
      containers_running: 14,
      images: 23,
      error: "",
    },
    subsystems: [
      { name: "api", kind: "core", status: "ok", detail: "listening on 0.0.0.0:8080" },
      { name: "docker", kind: "core", status: "ok", detail: "Docker 27.4.1" },
      { name: "minio", kind: "helper", status: "ok", detail: "running" },
      { name: "ecr", kind: "helper", status: "ok", detail: "running" },
      { name: "dns", kind: "helper", status: "warning", detail: "restarting" },
      { name: "imds", kind: "helper", status: "ok", detail: "running" },
      { name: "data-dir-disk", kind: "disk", status: "ok", detail: `${Math.round(((312345678848 - dataDir) / fsTotal) * 100)}% free` },
      ...loops,
    ],
    disk: {
      data_dir: { path: "/var/lib/homecloud", used_bytes: dataDir, fs_total_bytes: fsTotal, fs_free_bytes: 312345678848 - dataDir },
      volumes: { count: 9, size_bytes: Math.round(4.2 * GiB) },
      measured_at: iso(now - ((now / 1000) % 300) * 1000),
    },
    containers: [
      { service: "ec2", state: "running", count: 3 },
      { service: "ec2", state: "exited", count: 1 },
      { service: "rds", state: "running", count: 2 },
      { service: "elasticache", state: "running", count: 1 },
      { service: "ecs", state: "running", count: 2 },
      { service: "lambda", state: "running", count: 3 },
      { service: "elb", state: "running", count: 1 },
      { service: "s3", state: "running", count: 1 },
      { service: "ecr", state: "running", count: 1 },
      { service: "route53", state: "restarting", count: 1 },
      { service: "imds", state: "running", count: 1 },
    ],
    lambda: {
      environments_idle: 2,
      environments_busy: tick % 3 === 0 ? 1 : 0,
      environments_starting: 0,
      cold_starts: 41,
      warm_starts: 1290 + tick,
    },
    recent_errors: [
      {
        time: iso(now - 7 * MIN),
        source: "route53",
        message: "dns server: container homecloud-dns exited (code 1), restarting (attempt 2)",
      },
      {
        time: iso(now - 2 * HOUR - 14 * MIN),
        source: "lambda",
        message: "invoke thumbnail-generator: runtime exited with error: signal: killed (memory limit 128 MiB)",
      },
      {
        time: iso(now - 9 * HOUR),
        source: "ecs",
        message: "service web-api: task 3f2a9c1e stopped: essential container exited (code 137)",
      },
    ],
    metrics: { path: "/metrics", action: "homecloud:GetMetrics", listen_addr: "" },
  }
}

export default service
