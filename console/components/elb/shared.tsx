"use client"

import type { ReactNode } from "react"
import Link from "next/link"

import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select"
import { StatusBadge } from "@/components/console/status-badge"
import { useApi } from "@/lib/hooks"
import type { ElbListener, ElbTarget, Instance, LoadBalancer, TargetGroup } from "@/lib/types"

export const LBS_PATH = "/api/v1/elb/load-balancers"
export const TGS_PATH = "/api/v1/elb/target-groups"
export const ELB_PREFIX = "/api/v1/elb"

export const lbHref = (name: string, tab?: string) => `/elb/load-balancer/?name=${encodeURIComponent(name)}${tab ? `&tab=${tab}` : ""}`
export const tgHref = (name: string, tab?: string) => `/elb/target-group/?name=${encodeURIComponent(name)}${tab ? `&tab=${tab}` : ""}`
export const instanceHref = (id: string) => `/ec2/instance/?id=${encodeURIComponent(id)}`
export const taskHref = (id: string) => `/ecs/task/?id=${encodeURIComponent(id)}`
export const ecsServiceHref = (name: string) => `/ecs/service/?name=${encodeURIComponent(name)}`
export const vpcHref = (id: string) => `/vpc/?id=${encodeURIComponent(id)}`
export const subnetsHref = (vpcId: string) => `/vpc/subnets/?vpc_id=${encodeURIComponent(vpcId)}`

/** Load balancer and target group names: up to 32 letters, digits and hyphens, no leading/trailing hyphen. */
export const NAME_RE = /^[a-zA-Z0-9](?:[a-zA-Z0-9-]{0,30}[a-zA-Z0-9])?$/

export const isLbTransitional = (state: string) => state === "provisioning"

/** Poll fast while a load balancer is being (re)provisioned, slowly otherwise. */
export function lbPollInterval(states: string[]): number {
  return states.some(isLbTransitional) ? 2000 : 15_000
}

export function LbStateBadge({ state }: { state: string }) {
  if (state === "provisioning") return <StatusBadge status="pending" label="Provisioning" tone="warning" />
  return <StatusBadge status={state} />
}

const HEALTH_TONE = {
  healthy: "success",
  unhealthy: "danger",
  unavailable: "warning",
  initial: "warning",
  unused: "neutral",
} as const

export function HealthBadge({ health }: { health: string }) {
  const tone = HEALTH_TONE[health as keyof typeof HEALTH_TONE] ?? "neutral"
  return <StatusBadge status={health} tone={tone} />
}

/** ECS task ids are 32 hex characters; EC2 instance ids start with "i-". */
export const isEcsTaskId = (id: string) => /^[0-9a-f]{32}$/.test(id)
export const isInstanceId = (id: string) => id.startsWith("i-")

export function targetKind(id: string): string {
  if (isInstanceId(id)) return "Instance"
  if (isEcsTaskId(id)) return "ECS task"
  return "Target"
}

/** TargetLink links an instance or ECS task target to its console page. */
export function TargetLink({ id, className }: { id: string; className?: string }) {
  const href = isInstanceId(id) ? instanceHref(id) : isEcsTaskId(id) ? taskHref(id) : null
  const text = isEcsTaskId(id) ? id.slice(0, 12) : id
  const cls = `font-mono text-[13px] whitespace-nowrap ${className ?? ""}`
  if (!href) return <span className={cls}>{id}</span>
  return (
    <Link href={href} title={id} onClick={(e) => e.stopPropagation()} className={`text-primary hover:underline ${cls}`}>
      {text}
    </Link>
  )
}

export function TgLink({ name }: { name: string }) {
  return (
    <Link href={tgHref(name)} onClick={(e) => e.stopPropagation()} className="text-primary font-medium whitespace-nowrap hover:underline">
      {name}
    </Link>
  )
}

export function LbLink({ name }: { name: string }) {
  return (
    <Link href={lbHref(name)} onClick={(e) => e.stopPropagation()} className="text-primary font-medium whitespace-nowrap hover:underline">
      {name}
    </Link>
  )
}

/** Host port a listener is published on, if any. */
export function listenerPublicPort(lb: LoadBalancer, l: Pick<ElbListener, "port">): number | undefined {
  return lb.public_ports?.[`${l.port}/tcp`]
}

/** URL of a published listener; HTTPS listeners get an https:// URL. */
export function publicUrl(lb: LoadBalancer, port: number, https = false) {
  return `${https ? "https" : "http"}://${lb.public_host || "localhost"}:${port}`
}

/** Whether the listener published as "443/tcp" is HTTPS. */
const isHttpsKey = (lb: LoadBalancer, key: string) => (lb.listeners ?? []).some((l) => `${l.port}/tcp` === key && l.protocol === "HTTPS")

/** Clickable http://host:port links for every published listener. */
export function LbPublicPorts({ lb, empty = "-", compact }: { lb: LoadBalancer; empty?: ReactNode; compact?: boolean }) {
  const entries = Object.entries(lb.public_ports ?? {}).sort((a, b) => parseInt(a[0]) - parseInt(b[0]))
  if (!entries.length) return <span className="text-muted-foreground">{empty}</span>
  const host = lb.public_host || "localhost"
  return (
    <span className="flex flex-wrap gap-x-3 gap-y-1">
      {entries.map(([k, port]) => (
        <a
          key={k}
          href={publicUrl(lb, port, isHttpsKey(lb, k))}
          target="_blank"
          rel="noreferrer"
          onClick={(e) => e.stopPropagation()}
          className="text-primary font-mono text-[13px] whitespace-nowrap hover:underline"
        >
          {compact ? `${k.split("/")[0]} → ${port}` : `${isHttpsKey(lb, k) ? "https://" : ""}${host}:${port}`}
        </a>
      ))}
    </span>
  )
}

export function useLoadBalancers(poll = true) {
  return useApi<LoadBalancer[]>(LBS_PATH, { refreshInterval: poll ? (d) => lbPollInterval((d ?? []).map((l) => l.state)) : 0 })
}

export function useTargetGroups(refreshInterval = 15_000) {
  return useApi<TargetGroup[]>(TGS_PATH, { refreshInterval })
}

export function useInstances() {
  return useApi<Instance[]>("/api/v1/ec2/instances", { refreshInterval: 30_000 })
}

/** Target groups referenced by a load balancer's listeners and rules. */
export function lbTargetGroups(lb: LoadBalancer): string[] {
  const s = new Set<string>()
  for (const l of lb.listeners ?? []) {
    if (l.default_target_group) s.add(l.default_target_group)
    for (const r of l.rules ?? []) s.add(r.target_group)
  }
  return [...s]
}

/** Names of load balancers routing to a target group. */
export function lbsUsing(tg: string, lbs: LoadBalancer[] | undefined): string[] {
  return (lbs ?? []).filter((lb) => lbTargetGroups(lb).includes(tg)).map((lb) => lb.name)
}

export function healthCounts(targets: ElbTarget[]) {
  const c = { total: targets.length, healthy: 0, unhealthy: 0, other: 0 }
  for (const t of targets) {
    if (t.health === "healthy") c.healthy++
    else if (t.health === "unhealthy") c.unhealthy++
    else c.other++
  }
  return c
}

/** TargetGroupSelect picks a target group (optionally limited to one VPC). */
export function TargetGroupSelect({
  id,
  value,
  onChange,
  vpcId,
  placeholder = "Choose a target group",
  invalid,
}: {
  id?: string
  value: string
  onChange: (v: string) => void
  vpcId?: string
  placeholder?: string
  invalid?: boolean
}) {
  const { data, isLoading } = useTargetGroups()
  const groups = (data ?? []).filter((g) => !vpcId || g.vpc_id === vpcId).sort((a, b) => a.name.localeCompare(b.name))
  return (
    <Select value={value} onValueChange={(v) => v && onChange(v)} disabled={isLoading && !data}>
      <SelectTrigger id={id} className="w-full" aria-invalid={invalid}>
        <SelectValue placeholder={isLoading && !data ? "Loading target groups..." : groups.length ? placeholder : "No target groups in this VPC"} />
      </SelectTrigger>
      <SelectContent>
        {groups.map((g) => (
          <SelectItem key={g.name} value={g.name}>
            <span className="font-medium">{g.name}</span>
            <span className="text-muted-foreground font-mono text-xs">
              {g.protocol}:{g.port} · {g.targets.length} target{g.targets.length === 1 ? "" : "s"}
            </span>
          </SelectItem>
        ))}
      </SelectContent>
    </Select>
  )
}
