"use client"

import Link from "next/link"

import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select"
import { ApiError, errorMessage } from "@/lib/api"
import { useApi } from "@/lib/hooks"
import type { SecurityGroupRule, Vpc } from "@/lib/types"
import { cn } from "@/lib/utils"

// ---- IPv4 helpers ----

export interface Cidr {
  /** network address as an unsigned 32-bit number */
  base: number
  bits: number
  /** canonical form ("10.0.0.0/16") */
  text: string
}

export function parseIPv4(s: string): number | null {
  const m = /^(\d{1,3})\.(\d{1,3})\.(\d{1,3})\.(\d{1,3})$/.exec(s.trim())
  if (!m) return null
  const parts = m.slice(1).map(Number)
  if (parts.some((p) => p > 255)) return null
  return ((parts[0] << 24) | (parts[1] << 16) | (parts[2] << 8) | parts[3]) >>> 0
}

export function ipToString(n: number): string {
  return [n >>> 24, (n >>> 16) & 255, (n >>> 8) & 255, n & 255].join(".")
}

function mask(bits: number): number {
  return bits === 0 ? 0 : (0xffffffff << (32 - bits)) >>> 0
}

export function parseCidr(s: string): Cidr | null {
  const [ip, b, ...rest] = s.trim().split("/")
  if (rest.length || b === undefined || !/^\d{1,2}$/.test(b)) return null
  const bits = Number(b)
  const addr = parseIPv4(ip)
  if (addr === null || bits > 32) return null
  const base = (addr & mask(bits)) >>> 0
  return { base, bits, text: `${ipToString(base)}/${bits}` }
}

export function cidrContains(outer: Cidr, inner: Cidr): boolean {
  return inner.bits >= outer.bits && ((inner.base & mask(outer.bits)) >>> 0) === outer.base
}

export function cidrOverlaps(a: Cidr, b: Cidr): boolean {
  return cidrContains(a, b) || cidrContains(b, a)
}

export function cidrSize(c: Cidr): number {
  return 2 ** (32 - c.bits)
}

/** validateVpcCidr returns an error message, or null when s is a valid /16-/28 IPv4 block. */
export function validateVpcCidr(s: string): string | null {
  if (!s.trim()) return "Enter an IPv4 CIDR block, e.g. 10.100.0.0/16"
  const c = parseCidr(s)
  if (!c) return "Not a valid IPv4 CIDR block (a.b.c.d/n)"
  if (c.bits < 16 || c.bits > 28) return "The block size must be between /16 and /28"
  return null
}

export function validateSubnetCidr(s: string, vpc?: Vpc): string | null {
  if (!s.trim()) return "Enter an IPv4 CIDR block"
  const c = parseCidr(s)
  if (!c) return "Not a valid IPv4 CIDR block (a.b.c.d/n)"
  if (c.bits > 28) return "Subnets can be no smaller than /28"
  if (vpc) {
    const v = parseCidr(vpc.cidr)
    if (v && !cidrContains(v, c)) return `Must be inside the VPC range ${vpc.cidr}`
    for (const sn of vpc.subnets ?? []) {
      const o = parseCidr(sn.cidr)
      if (o && cidrOverlaps(o, c)) return `Overlaps subnet ${sn.name || sn.id} (${sn.cidr})`
    }
  }
  return null
}

/** validateSourceCidr accepts any IPv4 CIDR (0.0.0.0/0 = anywhere). */
export function validateSourceCidr(s: string): string | null {
  if (!s.trim()) return "Enter a source CIDR, e.g. 0.0.0.0/0"
  return parseCidr(s) ? null : "Not a valid IPv4 CIDR block (a.b.c.d/n)"
}

/** suggestSubnetCidr proposes the first free /24 inside the VPC. */
export function suggestSubnetCidr(vpc: Vpc | undefined): string {
  const v = vpc && parseCidr(vpc.cidr)
  if (!v) return ""
  const bits = Math.min(28, Math.max(v.bits, 24))
  const step = 2 ** (32 - bits)
  const used = (vpc.subnets ?? []).map((s) => parseCidr(s.cidr)).filter((c): c is Cidr => !!c)
  for (let base = v.base; base < v.base + cidrSize(v); base += step) {
    const c: Cidr = { base: base >>> 0, bits, text: `${ipToString(base >>> 0)}/${bits}` }
    if (!used.some((u) => cidrOverlaps(u, c))) return c.text
  }
  return ""
}

// ---- security group rules ----

export const WELL_KNOWN_PORTS: Record<number, string> = {
  20: "FTP data",
  21: "FTP",
  22: "SSH",
  25: "SMTP",
  53: "DNS",
  80: "HTTP",
  110: "POP3",
  143: "IMAP",
  443: "HTTPS",
  1433: "MSSQL",
  1883: "MQTT",
  2049: "NFS",
  3000: "Custom HTTP",
  3306: "MySQL/Aurora",
  3389: "RDP",
  5432: "PostgreSQL",
  5672: "AMQP",
  6379: "Redis",
  8000: "Custom HTTP",
  8080: "Custom HTTP",
  8443: "Custom HTTPS",
  9000: "Custom HTTP",
  9200: "Elasticsearch",
  11211: "Memcached",
  27017: "MongoDB",
}

export function ruleType(r: Pick<SecurityGroupRule, "protocol" | "from_port" | "to_port">): string {
  if (r.from_port === r.to_port) {
    const n = WELL_KNOWN_PORTS[r.from_port]
    if (n && !(r.protocol === "udp" && r.from_port !== 53)) return n
  }
  return r.protocol === "udp" ? "Custom UDP" : "Custom TCP"
}

export function portRange(r: Pick<SecurityGroupRule, "from_port" | "to_port">): string {
  return r.from_port === r.to_port ? String(r.from_port) : `${r.from_port}-${r.to_port}`
}

/** rulesSummary renders "tcp 80, tcp 443" (first few rules). */
export function rulesSummary(rules: SecurityGroupRule[], max = 4): string {
  if (!rules.length) return ""
  const parts = rules.slice(0, max).map((r) => `${r.protocol} ${portRange(r)}`)
  if (rules.length > max) parts.push(`+${rules.length - max} more`)
  return parts.join(", ")
}

export interface RulePreset {
  label: string
  protocol: "tcp" | "udp"
  port?: number
}

export const RULE_PRESETS: RulePreset[] = [
  { label: "Custom TCP", protocol: "tcp" },
  { label: "Custom UDP", protocol: "udp" },
  { label: "SSH", protocol: "tcp", port: 22 },
  { label: "HTTP", protocol: "tcp", port: 80 },
  { label: "HTTPS", protocol: "tcp", port: 443 },
  { label: "Custom HTTP (8080)", protocol: "tcp", port: 8080 },
  { label: "PostgreSQL", protocol: "tcp", port: 5432 },
  { label: "MySQL/Aurora", protocol: "tcp", port: 3306 },
  { label: "Redis", protocol: "tcp", port: 6379 },
  { label: "MongoDB", protocol: "tcp", port: 27017 },
  { label: "DNS (UDP)", protocol: "udp", port: 53 },
  { label: "RDP", protocol: "tcp", port: 3389 },
]

export const MAX_PORT_RANGE = 32

export interface RuleDraft {
  key: string
  preset: string
  protocol: "tcp" | "udp"
  from: string
  to: string
  cidr: string
  description: string
}

let draftSeq = 0
export function newRuleDraft(preset = "Custom TCP"): RuleDraft {
  const p = RULE_PRESETS.find((x) => x.label === preset) ?? RULE_PRESETS[0]
  return {
    key: `r${++draftSeq}`,
    preset: p.label,
    protocol: p.protocol,
    from: p.port ? String(p.port) : "",
    to: p.port ? String(p.port) : "",
    cidr: "0.0.0.0/0",
    description: "",
  }
}

export interface RuleErrors {
  from?: string
  to?: string
  cidr?: string
}

export function validateRule(d: RuleDraft): RuleErrors {
  const e: RuleErrors = {}
  const from = Number(d.from)
  const to = d.to.trim() === "" ? from : Number(d.to)
  if (!d.from.trim() || !Number.isInteger(from) || from < 1 || from > 65535) e.from = "Port 1-65535"
  if (!Number.isInteger(to) || to < 1 || to > 65535) e.to = "Port 1-65535"
  else if (!e.from && to < from) e.to = "Must be >= from port"
  else if (!e.from && to - from >= MAX_PORT_RANGE) e.to = `At most ${MAX_PORT_RANGE} ports per rule`
  const c = validateSourceCidr(d.cidr)
  if (c) e.cidr = c
  return e
}

export function ruleBody(d: RuleDraft) {
  const from = Number(d.from)
  const to = d.to.trim() === "" ? from : Number(d.to)
  return { protocol: d.protocol, from_port: from, to_port: to, cidr: parseCidr(d.cidr)?.text ?? d.cidr, description: d.description.trim() || undefined }
}

// ---- shared data + UI ----

export function useVpcs() {
  return useApi<Vpc[]>("/api/v1/vpc/vpcs")
}

export const vpcHref = (id: string) => `/vpc/?id=${encodeURIComponent(id)}`
export const sgHref = (id: string) => `/vpc/security-group/?id=${encodeURIComponent(id)}`
export const subnetsHref = (vpcId: string) => `/vpc/subnets/?vpc_id=${encodeURIComponent(vpcId)}`

export function vpcLabel(v: Vpc | undefined, id: string): string {
  if (!v) return id
  return v.name ? `${v.name} (${v.id})` : v.id
}

/** VpcLink shows "vpc-123 | name" linking to the VPC list. */
export function VpcLink({ id, vpcs, className }: { id: string; vpcs?: Vpc[]; className?: string }) {
  const v = vpcs?.find((x) => x.id === id)
  return (
    <Link href={vpcHref(id)} className={cn("text-primary hover:underline", className)} title={v?.cidr}>
      <span className="font-mono text-[13px]">{id}</span>
      {v?.name && <span className="text-muted-foreground"> | {v.name}</span>}
    </Link>
  )
}

export function VpcSelect({
  value,
  onChange,
  vpcs,
  allowAll,
  id,
  className,
  size,
}: {
  value: string
  onChange: (v: string) => void
  vpcs: Vpc[] | undefined
  allowAll?: boolean
  id?: string
  className?: string
  size?: "sm" | "default"
}) {
  return (
    <Select value={value || (allowAll ? "__all" : "")} onValueChange={(v) => onChange(v === "__all" ? "" : v)}>
      <SelectTrigger id={id} size={size} className={cn("w-full", className)}>
        <SelectValue placeholder={vpcs ? "Select a VPC" : "Loading VPCs..."} />
      </SelectTrigger>
      <SelectContent>
        {allowAll && <SelectItem value="__all">All VPCs</SelectItem>}
        {(vpcs ?? []).map((v) => (
          <SelectItem key={v.id} value={v.id}>
            <span className="font-mono text-[13px]">{v.id}</span>
            <span className="text-muted-foreground">
              {v.name ? ` | ${v.name}` : ""} ({v.cidr})
            </span>
          </SelectItem>
        ))}
      </SelectContent>
    </Select>
  )
}

/** dependencyMessage explains a DependencyViolation in plain words. */
export function deleteErrorMessage(err: unknown, what: string): string {
  if (err instanceof ApiError && err.code === "DependencyViolation") {
    return `This ${what} is still in use: ${err.message}. Terminate or move the resources that use it, then try again.`
  }
  return errorMessage(err)
}
