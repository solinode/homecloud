"use client"

import { useEffect, useMemo, useState } from "react"
import Link from "next/link"
import { ExternalLink } from "lucide-react"

import { Checkbox } from "@/components/ui/checkbox"
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select"
import { Skeleton } from "@/components/ui/skeleton"
import { ErrorState } from "@/components/console/error-state"
import { Field } from "@/components/console/form-field"
import { Tag } from "@/components/console/tag"
import { formatNumber } from "@/lib/format"
import { useApi } from "@/lib/hooks"
import { portRange, ruleProtocol, ruleSource } from "@/components/vpc/common"
import type { SecurityGroup, SecurityGroupRule, Subnet } from "@/lib/types"
import { cn } from "@/lib/utils"

function ruleSummary(r: SecurityGroupRule) {
  return `${ruleProtocol(r)} ${portRange(r)} from ${ruleSource(r)}`
}

function sortSubnets(list: Subnet[]) {
  return [...list].sort((a, b) => a.vpc_id.localeCompare(b.vpc_id) || a.availability_zone.localeCompare(b.availability_zone) || a.name.localeCompare(b.name))
}

/**
 * useNetworkSelection holds a subnet + security group selection: it defaults
 * to the default VPC's subnet in zone "a" and resets the groups to the VPC's
 * default group whenever the VPC changes.
 */
export function useNetworkSelection() {
  const subnets = useApi<Subnet[]>("/api/v1/vpc/subnets", { revalidateOnFocus: false })
  const [subnetId, setSubnetId] = useState("")
  const [sgIds, setSgIds] = useState<string[]>([])
  const [sgVpc, setSgVpc] = useState("")

  const sorted = useMemo(() => sortSubnets(subnets.data ?? []), [subnets.data])
  useEffect(() => {
    if (subnetId || !sorted.length) return
    const pick = sorted.find((s) => s.default && s.availability_zone.endsWith("a")) ?? sorted.find((s) => s.default) ?? sorted[0]
    setSubnetId(pick.id)
  }, [sorted, subnetId])

  const subnet = sorted.find((s) => s.id === subnetId)
  const vpcId = subnet?.vpc_id ?? ""
  const sgs = useApi<SecurityGroup[]>(vpcId ? "/api/v1/vpc/security-groups" : null, { query: { vpc_id: vpcId } })
  useEffect(() => {
    if (!vpcId || !sgs.data || sgVpc === vpcId) return
    const def = sgs.data.find((g) => g.vpc_id === vpcId && g.name === "default")
    setSgIds(def ? [def.id] : [])
    setSgVpc(vpcId)
  }, [vpcId, sgs.data, sgVpc])

  const groups = (sgs.data ?? []).filter((g) => g.vpc_id === vpcId)
  return { subnets, sorted, subnet, subnetId, setSubnetId, vpcId, sgs, groups, sgIds, setSgIds }
}

export type NetworkSelection = ReturnType<typeof useNetworkSelection>

export function NetworkFields({ net, idPrefix = "net", error, compact }: { net: NetworkSelection; idPrefix?: string; error?: string; compact?: boolean }) {
  const { subnets, sorted, subnet, subnetId, setSubnetId, sgs, groups, sgIds, setSgIds, vpcId } = net
  return (
    <div className="flex flex-col gap-4">
      <Field
        label="Subnet"
        htmlFor={`${idPrefix}-subnet`}
        error={error}
        help={
          subnet ? (
            <>
              VPC <span className="font-mono">{subnet.vpc_id}</span> · {subnet.cidr} · {subnet.availability_zone} · {formatNumber(subnet.available_ips)} available IPs
            </>
          ) : undefined
        }
      >
        {subnets.error ? (
          <ErrorState error={subnets.error} onRetry={() => subnets.mutate()} />
        ) : (
          <Select value={subnetId} onValueChange={(v) => v && setSubnetId(v)} disabled={!subnets.data}>
            <SelectTrigger id={`${idPrefix}-subnet`} className="w-full">
              <SelectValue placeholder={subnets.data ? "Choose a subnet" : "Loading subnets..."} />
            </SelectTrigger>
            <SelectContent>
              {sorted.map((s) => (
                <SelectItem key={s.id} value={s.id} disabled={s.available_ips <= 0}>
                  <span className="font-medium">{s.name || s.id}</span>
                  <span className="text-muted-foreground font-mono text-xs">
                    {s.id} · {s.cidr} · {s.availability_zone}
                  </span>
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        )}
      </Field>

      <div className="flex flex-col gap-2">
        <div className="flex flex-wrap items-baseline justify-between gap-2">
          <span className="text-[13px] font-medium">Security groups</span>
          {!compact && (
            <Link href="/vpc/security-groups/" className="text-primary inline-flex items-center gap-1 text-sm hover:underline">
              Manage security groups <ExternalLink className="size-3.5" />
            </Link>
          )}
        </div>
        {sgs.error ? (
          <ErrorState error={sgs.error} onRetry={() => sgs.mutate()} />
        ) : !sgs.data || !vpcId ? (
          <Skeleton className="h-16 rounded-md" />
        ) : groups.length === 0 ? (
          <p className="text-muted-foreground text-sm">This VPC has no security groups.</p>
        ) : (
          <div className={cn("divide-y overflow-y-auto rounded-lg border", compact ? "max-h-44" : "max-h-80")} role="group" aria-label="Security groups">
            {groups.map((g) => {
              const checked = sgIds.includes(g.id)
              return (
                <label key={g.id} className={cn("flex cursor-pointer items-start gap-3 px-3 py-2 transition-colors", checked ? "bg-brand-soft" : "hover:bg-muted/50")}>
                  <Checkbox className="mt-0.5" checked={checked} onCheckedChange={(v) => setSgIds(v ? [...sgIds, g.id] : sgIds.filter((x) => x !== g.id))} />
                  <span className="flex min-w-0 flex-col gap-0.5">
                    <span className="text-sm">
                      <span className="font-medium">{g.name}</span> <span className="text-muted-foreground font-mono text-xs">{g.id}</span>
                    </span>
                    {!compact && (
                      <span className="flex flex-wrap gap-1 pt-0.5">
                        {g.ingress.length === 0 ? (
                          <span className="text-muted-foreground text-xs">No inbound rules</span>
                        ) : (
                          g.ingress.map((r) => <Tag key={r.id}>{ruleSummary(r)}</Tag>)
                        )}
                      </span>
                    )}
                  </span>
                </label>
              )
            })}
          </div>
        )}
      </div>
    </div>
  )
}
