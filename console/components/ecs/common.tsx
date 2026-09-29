"use client"

import { useEffect, useMemo } from "react"
import Link from "next/link"

import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select"
import { StatusBadge } from "@/components/console/status-badge"
import { useApi } from "@/lib/hooks"
import type { EcsService, EcsTask, EcsTaskDefinition } from "@/lib/types"

export const ECS_PATH = "/api/v1/ecs"
export const SERVICES_PATH = `${ECS_PATH}/services`
export const TASK_DEFS_PATH = `${ECS_PATH}/task-definitions`
export const TASKS_PATH = `${ECS_PATH}/tasks`
export const TARGET_GROUPS_PATH = "/api/v1/elb/target-groups"

// ---- links ----

export const serviceHref = (name: string, tab?: string) => `/ecs/service/?name=${encodeURIComponent(name)}${tab ? `&tab=${tab}` : ""}`

export const taskHref = (id: string) => `/ecs/task/?id=${encodeURIComponent(id)}`

export const taskDefHref = (family: string, revision: number | string) =>
  `/ecs/task-definition/?family=${encodeURIComponent(family)}&revision=${encodeURIComponent(String(revision))}`

/** Register form, optionally prefilled from "family:revision" (create new revision). */
export const registerHref = (from?: string) => `/ecs/task-definitions/register/${from ? `?from=${encodeURIComponent(from)}` : ""}`

export const targetGroupHref = (name: string) => `/elb/target-group/?name=${encodeURIComponent(name)}`

export const secretHref = (name: string) => `/secrets/secret/?name=${encodeURIComponent(name)}`

// ---- helpers ----

export const tdKey = (td: Pick<EcsTaskDefinition, "family" | "revision">) => `${td.family}:${td.revision}`

/**
 * parseTdKey splits "family:revision". A forced redeploy suffixes the key of
 * tasks from the old deployment with "(redeploy)", which is stripped here.
 */
export function parseTdKey(key: string): { family: string; revision: number; redeploy: boolean } {
  const redeploy = key.endsWith("(redeploy)")
  const clean = key.replace(/(\(redeploy\))+$/, "")
  const i = clean.lastIndexOf(":")
  if (i < 0) return { family: clean, revision: 0, redeploy }
  return { family: clean.slice(0, i), revision: Number(clean.slice(i + 1)) || 0, redeploy }
}

/** splitValueFrom splits a secret reference "name:json-key" (the backend cuts at the first ":"). */
export function splitValueFrom(v: string): [string, string] {
  const i = v.indexOf(":")
  return i < 0 ? [v, ""] : [v.slice(0, i), v.slice(i + 1)]
}

export const shortId = (id: string) => id.slice(0, 12)

export function formatCpu(v: number | null | undefined) {
  if (v === null || v === undefined) return "-"
  return `${Number(v.toFixed(3))} vCPU`
}

/** Families sorted by name, each with its revisions newest first. */
export function groupFamilies(list: EcsTaskDefinition[]): [string, EcsTaskDefinition[]][] {
  const m = new Map<string, EcsTaskDefinition[]>()
  for (const td of list) m.set(td.family, [...(m.get(td.family) ?? []), td])
  return [...m.entries()].sort((a, b) => a[0].localeCompare(b[0])).map(([f, revs]) => [f, revs.sort((a, b) => b.revision - a.revision)])
}

/** splitCommand parses a shell-style command line ('sh -c "echo hi"') or a JSON array into argv. */
export function splitCommand(input: string): { argv: string[]; error?: string } {
  const s = input.trim()
  if (!s) return { argv: [] }
  if (s.startsWith("[")) {
    try {
      const v = JSON.parse(s)
      if (Array.isArray(v) && v.every((x) => typeof x === "string")) return { argv: v }
    } catch {
      // fall through
    }
    return { argv: [], error: 'Enter a JSON array of strings, e.g. ["sh", "-c", "echo hi"]' }
  }
  const out: string[] = []
  let cur = ""
  let has = false
  let quote: '"' | "'" | null = null
  for (let i = 0; i < s.length; i++) {
    const c = s[i]
    if (quote) {
      if (c === quote) quote = null
      else if (c === "\\" && quote === '"' && i + 1 < s.length && /["\\$`]/.test(s[i + 1])) cur += s[++i]
      else cur += c
    } else if (c === '"' || c === "'") {
      quote = c
      has = true
    } else if (c === "\\" && i + 1 < s.length) {
      cur += s[++i]
      has = true
    } else if (/\s/.test(c)) {
      if (has || cur) out.push(cur)
      cur = ""
      has = false
    } else {
      cur += c
    }
  }
  if (quote) return { argv: [], error: "Unterminated quote" }
  if (has || cur) out.push(cur)
  return { argv: out }
}

/** joinCommand renders argv back as a shell-style command line. */
export function joinCommand(argv?: string[] | null): string {
  return (argv ?? []).map((a) => (a === "" ? "''" : /^[\w@%+=:,./-]+$/.test(a) ? a : `'${a.replace(/'/g, `'\\''`)}'`)).join(" ")
}

// ---- polling ----

export function serviceBusy(s: Pick<EcsService, "status" | "desired_count" | "running_count" | "pending_count">) {
  return s.status !== "ACTIVE" || s.pending_count > 0 || s.running_count !== s.desired_count
}

/** A deployment is in progress while tasks of another revision still run. */
export function deploymentInProgress(s: EcsService) {
  if (serviceBusy(s)) return true
  return (s.tasks ?? []).some((t) => t.last_status !== "STOPPED" && t.task_definition !== s.task_definition)
}

export const taskTransitional = (t: Pick<EcsTask, "last_status" | "desired_status">) =>
  t.last_status === "PROVISIONING" || (t.last_status === "RUNNING" && t.desired_status === "STOPPED")

// ---- badges ----

export function TaskStatusBadge({ status }: { status: string }) {
  if (status === "PROVISIONING") return <StatusBadge status="pending" label="Provisioning" />
  return <StatusBadge status={status.toLowerCase()} />
}

export function ServiceStatusBadge({ status }: { status: string }) {
  if (status === "DRAINING") return <StatusBadge status="deleting" label="Draining" />
  return <StatusBadge status={status.toLowerCase()} />
}

export function TaskDefLink({ value, className }: { value: string; className?: string }) {
  const { family, revision, redeploy } = parseTdKey(value)
  if (!revision) return <span className="font-mono text-[13px]">{value || "-"}</span>
  return (
    <span className="inline-flex items-center gap-1.5 whitespace-nowrap">
      <Link
        href={taskDefHref(family, revision)}
        onClick={(e) => e.stopPropagation()}
        className={className ?? "text-primary font-mono text-[13px] hover:underline"}
      >
        {family}:{revision}
      </Link>
      {redeploy && <span className="text-muted-foreground text-xs">(replacing)</span>}
    </span>
  )
}

// ---- related services (ELB / ECR); minimal shapes used by the ECS pages ----

export interface TargetGroupTarget {
  id: string
  port: number
  ip: string
  health: string
  reason?: string
}

export interface TargetGroupInfo {
  name: string
  arn: string
  protocol: string
  port: number
  vpc_id: string
  targets?: TargetGroupTarget[] | null
}

export interface EcrRepositoryInfo {
  name: string
  uri: string
}

export interface EcrImageInfo {
  tags?: string[] | null
  digest: string
  size_bytes: number
  pushed_at: string
  uri: string
}

export interface EcrRepositoryDetail {
  images: EcrImageInfo[] | null
  push_commands?: string[]
  repository: EcrRepositoryInfo
}

/** Target health keyed by task ID (tasks register as targets under their ID). */
export function useTargetHealth(targetGroup?: string | null) {
  const { data } = useApi<TargetGroupInfo>(targetGroup ? `${TARGET_GROUPS_PATH}/${encodeURIComponent(targetGroup)}` : null, { refreshInterval: 5000 })
  return useMemo(() => {
    const m = new Map<string, TargetGroupTarget>()
    for (const t of data?.targets ?? []) m.set(t.id, t)
    return m
  }, [data])
}

export function HealthBadge({ target }: { target?: TargetGroupTarget }) {
  if (!target) return <span className="text-muted-foreground">-</span>
  const h = target.health.toLowerCase()
  const tone = h === "healthy" ? "success" : h === "unhealthy" ? "danger" : h === "initial" ? "warning" : "neutral"
  return (
    <span title={target.reason}>
      <StatusBadge status={h} tone={tone} />
    </span>
  )
}

// ---- task definition picker ----

export function useTaskDefinitions(all = false) {
  return useApi<EcsTaskDefinition[]>(TASK_DEFS_PATH, { query: all ? { status: "all" } : undefined, refreshInterval: 30_000 })
}

/**
 * TaskDefinitionPicker chooses a family and one of its ACTIVE revisions.
 * value is "family:revision"; an empty value defaults to the first family's
 * latest revision (or `defaultFamily`'s).
 */
export function TaskDefinitionPicker({
  value,
  onChange,
  idPrefix = "td",
  defaultFamily,
  autoSelect = true,
}: {
  value: string
  onChange: (key: string) => void
  idPrefix?: string
  defaultFamily?: string
  autoSelect?: boolean
}) {
  const { data, isLoading } = useTaskDefinitions()
  const families = useMemo(() => groupFamilies(data ?? []), [data])
  const { family, revision } = parseTdKey(value)
  const revisions = families.find(([f]) => f === family)?.[1] ?? []

  useEffect(() => {
    if (!autoSelect || value || !families.length) return
    const pick = families.find(([f]) => f === defaultFamily) ?? families[0]
    onChange(tdKey(pick[1][0]))
  }, [autoSelect, value, families, defaultFamily, onChange])

  return (
    <div className="grid grid-cols-1 gap-2 sm:grid-cols-[minmax(0,1fr)_10rem]">
      <Select
        value={family}
        onValueChange={(f) => {
          const revs = families.find(([x]) => x === f)?.[1]
          if (f && revs?.length) onChange(tdKey(revs[0]))
        }}
        disabled={!families.length}
      >
        <SelectTrigger id={`${idPrefix}-family`} className="w-full" aria-label="Task definition family">
          <SelectValue placeholder={isLoading ? "Loading task definitions..." : families.length ? "Choose a family" : "No active task definitions"} />
        </SelectTrigger>
        <SelectContent>
          {families.map(([f, revs]) => (
            <SelectItem key={f} value={f}>
              <span className="font-mono text-[13px]">{f}</span>
              <span className="text-muted-foreground text-xs">latest {revs[0].revision}</span>
            </SelectItem>
          ))}
        </SelectContent>
      </Select>
      <Select value={revision ? String(revision) : ""} onValueChange={(r) => r && onChange(`${family}:${r}`)} disabled={!revisions.length}>
        <SelectTrigger id={`${idPrefix}-revision`} className="w-full" aria-label="Revision">
          <SelectValue placeholder="Revision" />
        </SelectTrigger>
        <SelectContent>
          {revisions.map((td, i) => (
            <SelectItem key={td.revision} value={String(td.revision)}>
              Revision {td.revision}
              {i === 0 && <span className="text-muted-foreground text-xs">(latest)</span>}
            </SelectItem>
          ))}
        </SelectContent>
      </Select>
    </div>
  )
}
