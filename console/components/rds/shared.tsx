"use client"

import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select"
import { formatMemoryMB } from "@/lib/format"
import { useApi } from "@/lib/hooks"
import type { DbClass, DbEngine, DbEngines, DbInstance, DbKind } from "@/lib/types"
import { cn } from "@/lib/utils"

export const RDS_PATH = "/api/v1/rds"
export const DB_INSTANCES_PATH = "/api/v1/rds/instances"
export const DB_SNAPSHOTS_PATH = "/api/v1/rds/snapshots"

/** The console has two faces over the same RDS API: RDS/DocumentDB databases and ElastiCache clusters. */
export type Family = "rds" | "elasticache"

export interface FamilyConfig {
  family: Family
  service: string
  kinds: DbKind[]
  classKind: "db" | "cache"
  base: string
  detailPath: string
  createPath: string
  snapshotsPath: string
  /** "database" / "cluster" */
  noun: string
  Noun: string
  nouns: string
  Nouns: string
  /** "snapshot" / "backup" */
  snap: string
  Snap: string
  snaps: string
  Snaps: string
  idLabel: string
  namespace: string
  dimension: string
}

export const FAMILIES: Record<Family, FamilyConfig> = {
  rds: {
    family: "rds",
    service: "RDS",
    kinds: ["relational", "document"],
    classKind: "db",
    base: "/rds/",
    detailPath: "/rds/instance/",
    createPath: "/rds/create/",
    snapshotsPath: "/rds/snapshots/",
    noun: "database",
    Noun: "Database",
    nouns: "databases",
    Nouns: "Databases",
    snap: "snapshot",
    Snap: "Snapshot",
    snaps: "snapshots",
    Snaps: "Snapshots",
    idLabel: "DB identifier",
    namespace: "HC/RDS",
    dimension: "DBInstanceIdentifier",
  },
  elasticache: {
    family: "elasticache",
    service: "ElastiCache",
    kinds: ["cache"],
    classKind: "cache",
    base: "/elasticache/",
    detailPath: "/elasticache/cluster/",
    createPath: "/elasticache/create/",
    snapshotsPath: "/elasticache/backups/",
    noun: "cluster",
    Noun: "Cluster",
    nouns: "clusters",
    Nouns: "Clusters",
    snap: "backup",
    Snap: "Backup",
    snaps: "backups",
    Snaps: "Backups",
    idLabel: "Cluster ID",
    namespace: "HC/ElastiCache",
    dimension: "CacheClusterId",
  },
}

export const familyOfKind = (kind: string): Family => (kind === "cache" ? "elasticache" : "rds")

export const inFamily = (cfg: FamilyConfig, kind: string) => (cfg.kinds as string[]).includes(kind)

export const dbHref = (kind: string, id: string, tab?: string) =>
  `${FAMILIES[familyOfKind(kind)].detailPath}?id=${encodeURIComponent(id)}${tab ? `&tab=${tab}` : ""}`

/** Detail tab ids differ in wording per family ("snapshots" vs "backups", "query" vs "console"). */
export const snapTab = (kind: string) => (kind === "cache" ? "backups" : "snapshots")
export const queryTab = (kind: string) => (kind === "cache" ? "console" : "query")

export const TRANSITIONAL = ["creating", "starting", "stopping", "rebooting", "restoring", "backing-up", "modifying", "deleting"]

export const isTransitional = (s: string) => TRANSITIONAL.includes(s)

/** Poll fast while anything is changing state, slowly otherwise. */
export function pollInterval(states: string[]): number {
  return states.some(isTransitional) ? 2000 : 15_000
}

export const ID_RE = /^[a-z][a-z0-9-]{0,62}$/

export function idError(id: string, what = "Identifier"): string | undefined {
  if (!id) return `Enter ${what === "Identifier" ? "an identifier" : `a ${what.toLowerCase()}`}`
  if (!ID_RE.test(id)) return `${what} must start with a lowercase letter and contain only lowercase letters, digits and hyphens (max 63)`
  return undefined
}

/** Same rule as the backend: at least 8 characters, no quotes, slashes, @ or spaces. */
export function passwordError(p: string): string | undefined {
  if (p.length < 8) return "At least 8 characters"
  if (/["'/@\s\\]/.test(p)) return "May not contain quotes, slashes, @ or spaces"
  return undefined
}

// ---- engine capabilities (mirrors cli/internal/svc/rds/engines.go) ----

export const supportsSnapshots = (engine: string) => engine !== "memcached"
export const supportsQuery = (engine: string) => engine !== "memcached"
export const supportsPasswordReset = (engine: string) => ["postgres", "mysql", "mariadb", "mongodb"].includes(engine)
export const isRedisLike = (engine: string) => engine === "redis" || engine === "valkey"
export const isSqlEngine = (engine: string) => ["postgres", "mysql", "mariadb"].includes(engine)

export function useEngines() {
  return useApi<DbEngines>(`${RDS_PATH}/engines`, { revalidateOnFocus: false })
}

export function engineLabel(engines: DbEngine[] | undefined, name: string) {
  return engines?.find((e) => e.name === name)?.label ?? ENGINE_STYLE[name]?.label ?? name
}

export function formatVcpu(n: number) {
  return `${n} vCPU${n === 1 ? "" : "s"}`
}

export function classSummary(c: Pick<DbClass, "vcpus" | "memory_mb">) {
  return `${formatVcpu(c.vcpus)}, ${formatMemoryMB(c.memory_mb)}`
}

export function endpointText(i: DbInstance) {
  return i.endpoint.address ? `${i.endpoint.address}:${i.endpoint.port}` : ""
}

export function publicText(i: DbInstance) {
  if (!i.publicly_accessible || !i.endpoint.public_host || !i.endpoint.public_port) return ""
  return `${i.endpoint.public_host}:${i.endpoint.public_port}`
}

// ---- engine "logos" ----

const ENGINE_STYLE: Record<string, { mono: string; cls: string; label: string }> = {
  postgres: { mono: "Pg", cls: "bg-sky-700 text-white", label: "PostgreSQL" },
  mysql: { mono: "My", cls: "bg-orange-500 text-white", label: "MySQL" },
  mariadb: { mono: "Ma", cls: "bg-amber-800 text-white", label: "MariaDB" },
  mongodb: { mono: "Mo", cls: "bg-emerald-600 text-white", label: "MongoDB" },
  redis: { mono: "R", cls: "bg-red-600 text-white", label: "Redis" },
  valkey: { mono: "Vk", cls: "bg-indigo-600 text-white", label: "Valkey" },
  memcached: { mono: "Mc", cls: "bg-teal-600 text-white", label: "Memcached" },
}

/** EngineLogo is a small colored tile with the engine's monogram. */
export function EngineLogo({ engine, size = "sm", className }: { engine: string; size?: "xs" | "sm" | "lg"; className?: string }) {
  const s = ENGINE_STYLE[engine] ?? { mono: engine.slice(0, 2), cls: "bg-slate-500 text-white" }
  const dim = { xs: "size-5 text-[9px] rounded", sm: "size-6 text-[10px] rounded-md", lg: "size-10 text-sm rounded-lg" }[size]
  return (
    <span aria-hidden className={cn("inline-flex shrink-0 items-center justify-center font-bold tracking-tight select-none", dim, s.cls, className)}>
      {s.mono}
    </span>
  )
}

/** EngineCell renders "[logo] PostgreSQL 16". */
export function EngineCell({ engine, version, engines }: { engine: string; version?: string; engines?: DbEngine[] }) {
  return (
    <span className="inline-flex items-center gap-2 whitespace-nowrap">
      <EngineLogo engine={engine} size="xs" />
      <span>
        {engineLabel(engines, engine).replace(" (DocumentDB compatible)", "")}
        {version && <span className="text-muted-foreground"> {version}</span>}
      </span>
    </span>
  )
}

/** ClassSelect picks an instance/node class of one kind (db.* or cache.*). */
export function ClassSelect({
  value,
  onChange,
  kind,
  id,
  className,
}: {
  value: string
  onChange: (v: string) => void
  kind: "db" | "cache"
  id?: string
  className?: string
}) {
  const { data, isLoading } = useEngines()
  const classes = (data?.classes ?? []).filter((c) => c.kind === kind)
  return (
    <Select value={value} onValueChange={onChange} disabled={isLoading && !data}>
      <SelectTrigger id={id} className={cn("w-full", className)}>
        <SelectValue placeholder={isLoading ? "Loading classes..." : "Choose a class"} />
      </SelectTrigger>
      <SelectContent>
        {classes.map((c) => (
          <SelectItem key={c.name} value={c.name}>
            <span className="font-mono text-[13px]">{c.name}</span>
            <span className="text-muted-foreground text-xs">{classSummary(c)}</span>
          </SelectItem>
        ))}
      </SelectContent>
    </Select>
  )
}
