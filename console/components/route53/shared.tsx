"use client"

import Link from "next/link"
import { Globe, Lock } from "lucide-react"

import { dbHref } from "@/components/rds/shared"
import { Tag } from "@/components/console/tag"
import { useApi } from "@/lib/hooks"
import type { DbInstance, DnsRecord, EcsTask, HostedZoneSummary, Instance, LoadBalancer } from "@/lib/types"

export const R53_PATH = "/api/v1/route53"
export const ZONES_PATH = `${R53_PATH}/zones`

export const zoneHref = (id: string) => `/route53/zone/?id=${encodeURIComponent(id)}`

/** zoneLabel drops the trailing dot of a fully qualified zone name. */
export const zoneLabel = (name: string) => name.replace(/\.$/, "")

export function useZones() {
  return useApi<HostedZoneSummary[]>(ZONES_PATH, { refreshInterval: 30_000 })
}

/** Mirrors labelRe in route53.go (every label followed by a dot). */
export const LABEL_RE = /^([a-zA-Z0-9_]([a-zA-Z0-9_-]{0,61}[a-zA-Z0-9_])?\.)+$/

export function zoneNameError(input: string): string | null {
  let n = input.trim().toLowerCase()
  if (!n) return "Enter a domain name"
  if (!n.endsWith(".")) n += "."
  if (!LABEL_RE.test(n) || n.length > 254) return "Use labels of letters, digits, hyphens and underscores separated by dots, e.g. example.internal"
  return null
}

/** fqdn mirrors route53.go: "@" or "" is the apex, names ending in "." are absolute. */
export function fqdn(name: string, zone: string): string {
  if (!name || name === "@") return zone
  if (name.endsWith(".")) return name
  if (`${name}.`.endsWith(zone)) return `${name}.`
  return `${name}.${zone}`
}

export const RECORD_TYPES = ["A", "AAAA", "CNAME", "TXT", "MX", "SRV", "NS", "CAA", "PTR"] as const

export const RECORD_HELP: Record<string, { placeholder: string; help: string }> = {
  A: { placeholder: "192.168.1.20", help: "IPv4 addresses, one per line." },
  AAAA: { placeholder: "fd00::20", help: "IPv6 addresses, one per line." },
  CNAME: { placeholder: "www.example.com.", help: "Exactly one domain name. A CNAME cannot share its name with other records or sit at the zone apex." },
  TXT: { placeholder: "v=spf1 -all", help: "One string per line, up to 255 characters each. Quotes are added for you." },
  MX: { placeholder: "10 mail.example.com.", help: "Priority and mail server, one per line." },
  SRV: { placeholder: "10 5 5060 sip.example.com.", help: "Priority, weight, port and target, one per line." },
  NS: { placeholder: "ns1.example.com.", help: "Name servers for a delegated subdomain, one per line." },
  CAA: { placeholder: '0 issue "letsencrypt.org"', help: "Flags, tag and value, one per line." },
  PTR: { placeholder: "host.example.com.", help: "Domain names, one per line (reverse DNS)." },
}

const DOMAIN = (v: string) => LABEL_RE.test(`${v.replace(/\.$/, "")}.`)
const IPV4 = /^(25[0-5]|2[0-4]\d|1\d\d|[1-9]?\d)(\.(25[0-5]|2[0-4]\d|1\d\d|[1-9]?\d)){3}$/

function isIPv6(v: string) {
  if (!v.includes(":") || IPV4.test(v)) return false
  try {
    new URL(`http://[${v}]/`)
    return true
  } catch {
    return false
  }
}

/** valueError mirrors validate() in route53.go for a single value. */
export function valueError(type: string, v: string): string | null {
  if (/[\n\r;]/.test(v)) return "Values may not contain semicolons"
  switch (type) {
    case "A":
      return IPV4.test(v) ? null : `${v} is not an IPv4 address`
    case "AAAA":
      return isIPv6(v) ? null : `${v} is not an IPv6 address`
    case "CNAME":
    case "NS":
    case "PTR":
      return DOMAIN(v) ? null : `${v} is not a domain name`
    case "TXT":
      return v.length > 255 ? "TXT strings are at most 255 characters" : null
    case "MX":
      return /^\d+\s+\S+$/.test(v) ? null : "Use the form: priority mail-server"
    case "SRV":
      return /^\d+\s+\d+\s+\d+\s+\S+$/.test(v) ? null : "Use the form: priority weight port target"
    case "CAA":
      return /^\d+\s+[a-zA-Z0-9]+\s+\S.*$/.test(v) ? null : 'Use the form: flags tag "value"'
  }
  return null
}

/** recordNameError checks a record name relative to the zone ("" or "@" is the apex, "*" wildcards are allowed). */
export function recordNameError(name: string, zone: string): string | null {
  const n = name.trim().toLowerCase()
  if (!n || n === "@") return null
  const full = fqdn(n, zone)
  if (!full.endsWith(zone)) return `The name must be inside ${zoneLabel(zone)}`
  if (!LABEL_RE.test(full.replace(/^\*\./, ""))) return "Use letters, digits, hyphens and underscores; a leading * is a wildcard"
  return null
}

export function ZoneTypeBadge({ isPrivate, className }: { isPrivate: boolean; className?: string }) {
  const Icon = isPrivate ? Lock : Globe
  return (
    <Tag accent={isPrivate ? "neutral" : "violet"} mono={false} className={className}>
      <Icon /> {isPrivate ? "Private" : "Public"}
    </Tag>
  )
}

// ---- alias targets ----

export type AliasKind = "instance" | "task" | "database" | "load-balancer"

export interface AliasTarget {
  id: string
  kind: AliasKind
  label: string
  detail: string
  href: string
  /** the target currently has a private IP the DNS server can answer with */
  live: boolean
}

const KIND_LABEL: Record<AliasKind, string> = {
  instance: "EC2 instance",
  task: "ECS task",
  database: "Database",
  "load-balancer": "Load balancer",
}

export const aliasKindLabel = (k: AliasKind) => KIND_LABEL[k]

/**
 * useAliasTargets lists the resources an alias record can follow: EC2
 * instances and ECS tasks (by id), RDS/ElastiCache databases (by id) and
 * load balancers (by name).
 */
export function useAliasTargets(enabled = true) {
  const instances = useApi<Instance[]>(enabled ? "/api/v1/ec2/instances" : null, { refreshInterval: 30_000 })
  const tasks = useApi<EcsTask[]>(enabled ? "/api/v1/ecs/tasks" : null, { refreshInterval: 30_000 })
  const dbs = useApi<DbInstance[]>(enabled ? "/api/v1/rds/instances" : null, { refreshInterval: 30_000 })
  const lbs = useApi<LoadBalancer[]>(enabled ? "/api/v1/elb/load-balancers" : null, { refreshInterval: 30_000 })
  const out: AliasTarget[] = []
  for (const i of instances.data ?? []) {
    if (i.state === "terminated") continue
    out.push({
      id: i.id,
      kind: "instance",
      label: i.name ? `${i.name} (${i.id})` : i.id,
      detail: `${i.state}${i.private_ip ? ` · ${i.private_ip}` : ""}`,
      href: `/ec2/instance/?id=${encodeURIComponent(i.id)}`,
      live: i.state === "running" || i.state === "pending",
    })
  }
  for (const t of tasks.data ?? []) {
    if (t.last_status === "STOPPED") continue
    out.push({
      id: t.id,
      kind: "task",
      label: `${t.id.slice(0, 12)}${t.service ? ` (${t.service})` : ""}`,
      detail: `${t.last_status}${t.private_ip ? ` · ${t.private_ip}` : ""}`,
      href: `/ecs/task/?id=${encodeURIComponent(t.id)}`,
      live: t.last_status === "RUNNING",
    })
  }
  for (const d of dbs.data ?? []) {
    out.push({
      id: d.id,
      kind: "database",
      label: d.id,
      detail: `${d.engine} · ${d.status}`,
      href: dbHref(d.kind, d.id),
      live: d.status !== "stopped" && d.status !== "failed" && d.status !== "deleting",
    })
  }
  for (const lb of lbs.data ?? []) {
    out.push({
      id: lb.name,
      kind: "load-balancer",
      label: lb.name,
      detail: `${lb.scheme} · ${lb.private_ip || lb.state}`,
      href: `/elb/load-balancer/?name=${encodeURIComponent(lb.name)}`,
      live: !!lb.private_ip,
    })
  }
  const loading = !instances.data || !tasks.data || !dbs.data || !lbs.data
  return { targets: out, loading }
}

export function AliasTargetLink({ alias, targets, loading }: { alias: string; targets: AliasTarget[]; loading: boolean }) {
  const t = targets.find((x) => x.id === alias)
  if (!t) {
    return (
      <span className="inline-flex flex-wrap items-center gap-1.5">
        <span className="font-mono text-[13px]">{alias}</span>
        {!loading && <span className="text-destructive text-xs">(not found: no answer)</span>}
      </span>
    )
  }
  return (
    <span className="inline-flex flex-wrap items-center gap-1.5">
      <span className="text-muted-foreground text-xs">{aliasKindLabel(t.kind)}</span>
      <Link href={t.href} className="text-primary font-mono text-[13px] hover:underline" onClick={(e) => e.stopPropagation()}>
        {t.kind === "task" ? t.id.slice(0, 12) : t.id}
      </Link>
      {!t.live && <span className="text-warning text-xs">(not running: no answer)</span>}
    </span>
  )
}

/** Values to display for a record (alias records show their target separately). */
export const recordValues = (r: DnsRecord) => r.values ?? []
