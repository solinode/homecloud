"use client"

import { useEffect, useMemo, useState } from "react"
import Link from "next/link"
import { useRouter } from "next/navigation"
import { Network, Plus, Trash2 } from "lucide-react"
import { toast } from "sonner"

import { Button } from "@/components/ui/button"
import { ActionsMenu } from "@/components/console/actions-menu"
import { ConfirmDialog } from "@/components/console/confirm-dialog"
import { CopyableText } from "@/components/console/copy-button"
import { CellText, cellLinkClass, DataTable, type Column } from "@/components/console/data-table"
import { EmptyState } from "@/components/console/empty-state"
import { KeyValueGrid } from "@/components/console/key-value"
import { PageHeader } from "@/components/console/page-header"
import { StatusBadge } from "@/components/console/status-badge"
import { Tag } from "@/components/console/tag"
import { TimeAgo } from "@/components/console/time-ago"
import { api } from "@/lib/api"
import { formatDate, formatNumber } from "@/lib/format"
import { revalidate, useQueryParam } from "@/lib/hooks"
import type { Subnet, Vpc } from "@/lib/types"
import { cn } from "@/lib/utils"

import { cidrContains, cidrSize, deleteErrorMessage, parseCidr, subnetsHref, useVpcs } from "./common"
import { CreateSubnetDialog, CreateVpcDialog } from "./dialogs"

const SUBNET_COLORS = ["var(--chart-1)", "var(--chart-2)", "var(--chart-3)", "var(--chart-4)", "var(--chart-5)"]

/** CidrMap draws the VPC's address range as a bar with each subnet's slice of it. */
function CidrMap({ vpcCidr, subnets }: { vpcCidr: string; subnets: Subnet[] }) {
  const v = parseCidr(vpcCidr)
  if (!v) return null
  const total = cidrSize(v)
  const used = subnets.reduce((n, s) => n + (parseCidr(s.cidr) ? cidrSize(parseCidr(s.cidr)!) : 0), 0)
  return (
    <div className="flex flex-col gap-1.5">
      <div className="text-muted-foreground flex items-center justify-between gap-2 text-xs">
        <span className="font-mono">{v.text}</span>
        <span className="tabular-nums">{Math.round((used / total) * 100)}% of the range allocated to subnets</span>
      </div>
      <div className="bg-muted relative h-2.5 overflow-hidden rounded-full border" role="img" aria-label={`Subnet allocation of ${v.text}`}>
        {subnets.map((s, i) => {
          const c = parseCidr(s.cidr)
          if (!c || !cidrContains(v, c)) return null
          const left = ((c.base - v.base) / total) * 100
          const width = Math.max((cidrSize(c) / total) * 100, 0.6)
          return (
            <span
              key={s.id}
              title={`${s.name || s.id} · ${c.text}`}
              className="absolute inset-y-0 border-card border-r"
              style={{ left: `${left}%`, width: `${width}%`, background: SUBNET_COLORS[i % SUBNET_COLORS.length] }}
            />
          )
        })}
      </div>
    </div>
  )
}

function VpcDetail({ vpc }: { vpc: Vpc }) {
  const subnets = [...(vpc.subnets ?? [])].sort((a, b) => a.cidr.localeCompare(b.cidr, undefined, { numeric: true }))
  return (
    <div className="flex flex-col gap-4">
      <KeyValueGrid
        columns={4}
        items={[
          { label: "ARN", value: <CopyableText value={vpc.arn} />, wide: true },
          { label: "Docker network", value: <CopyableText value={vpc.network} /> },
          { label: "Outbound internet", value: vpc.internet_access ? "Allowed" : "Blocked (isolated)" },
          { label: "Created", value: formatDate(vpc.created_at) },
          {
            label: "Security groups",
            value: (
              <Link className="text-primary hover:underline" href={`/vpc/security-groups/?vpc_id=${encodeURIComponent(vpc.id)}`}>
                View groups
              </Link>
            ),
          },
        ]}
      />
      <div>
        <div className="mb-2 flex items-center justify-between gap-2">
          <h3 className="text-sm font-semibold">Subnets ({subnets.length})</h3>
          <Link href={subnetsHref(vpc.id)} className="text-primary text-sm hover:underline">
            Manage subnets
          </Link>
        </div>
        {subnets.length === 0 ? (
          <p className="text-muted-foreground text-sm">This VPC has no subnets yet. Create one to launch instances into it.</p>
        ) : (
          <div className="flex flex-col gap-3">
          <CidrMap vpcCidr={vpc.cidr} subnets={subnets} />
          <div className="bg-card overflow-x-auto rounded-lg border">
            <table className="w-full text-sm">
              <thead>
                <tr className="bg-muted/40 text-muted-foreground border-b text-left text-xs">
                  <th className="px-3 py-1.5 font-medium">Name</th>
                  <th className="px-3 py-1.5 font-medium">Subnet ID</th>
                  <th className="px-3 py-1.5 font-medium">CIDR</th>
                  <th className="px-3 py-1.5 font-medium">AZ</th>
                  <th className="px-3 py-1.5 text-right font-medium">Available IPs</th>
                </tr>
              </thead>
              <tbody>
                {subnets.map((s, i) => (
                  <tr key={s.id} className="border-b last:border-0">
                    <td className="px-3 py-1.5 whitespace-nowrap">
                      <span className="inline-flex items-center gap-2">
                        <span aria-hidden className="size-2 shrink-0 rounded-sm" style={{ background: SUBNET_COLORS[i % SUBNET_COLORS.length] }} />
                        {s.name || <span className="text-muted-foreground">-</span>}
                      </span>
                    </td>
                    <td className="px-3 py-1.5 font-mono text-[13px] whitespace-nowrap">{s.id}</td>
                    <td className="px-3 py-1.5 font-mono text-[13px] whitespace-nowrap">{s.cidr}</td>
                    <td className="px-3 py-1.5 whitespace-nowrap">{s.availability_zone}</td>
                    <td className="px-3 py-1.5 text-right tabular-nums">
                      {formatNumber(s.available_ips, 0)}
                      {s.used_ips > 0 && <span className="text-muted-foreground"> ({s.used_ips} used)</span>}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
          </div>
        )}
      </div>
    </div>
  )
}

export function VpcList() {
  const router = useRouter()
  const { data, error, isLoading, isValidating, mutate } = useVpcs()
  const initialId = useQueryParam("id")
  const [selected, setSelected] = useState<string[]>([])
  const [createOpen, setCreateOpen] = useState(false)
  const [subnetOpen, setSubnetOpen] = useState(false)
  const [deleteOpen, setDeleteOpen] = useState(false)

  useEffect(() => {
    if (initialId) setSelected([initialId])
  }, [initialId])

  const vpc = data?.find((v) => v.id === selected[0])

  const columns = useMemo<Column<Vpc>[]>(
    () => [
      {
        id: "name",
        header: "Name",
        value: (v) => v.name,
        cell: (v) => (
          <button type="button" className={cn(cellLinkClass(), "block max-w-[16rem] truncate whitespace-nowrap")} title={v.name || undefined} onClick={(e) => (e.stopPropagation(), setSelected(selected[0] === v.id ? [] : [v.id]))}>
            {v.name || <span className="text-muted-foreground font-normal">-</span>}
          </button>
        ),
      },
      { id: "id", header: "VPC ID", value: (v) => v.id, cell: (v) => <CellText mono>{v.id}</CellText> },
      { id: "state", header: "State", value: (v) => v.state, cell: (v) => <StatusBadge status={v.state} /> },
      { id: "cidr", header: "IPv4 CIDR", value: (v) => v.cidr, cell: (v) => <span className="font-mono text-[13px]">{v.cidr}</span> },
      { id: "default", header: "Default VPC", value: (v) => (v.default ? "Yes" : "No"), cell: (v) =>
          v.default ? (
            <Tag accent="brand" mono={false}>
              Default
            </Tag>
          ) : (
            <span className="text-muted-foreground">No</span>
          ),
        hideBelow: "md",
      },
      {
        id: "internet",
        header: "Internet access",
        value: (v) => (v.internet_access ? "Enabled" : "Isolated"),
        cell: (v) =>
          v.internet_access ? <StatusBadge status="enabled" label="Enabled" /> : <StatusBadge status="isolated" tone="warning" label="Isolated" />,
        hideBelow: "sm",
      },
      {
        id: "subnets",
        header: "Subnets",
        value: (v) => v.subnets?.length ?? 0,
        cell: (v) => (
          <Link href={subnetsHref(v.id)} className="text-primary tabular-nums hover:underline" onClick={(e) => e.stopPropagation()}>
            {v.subnets?.length ?? 0}
          </Link>
        ),
      },
      { id: "network", header: "Docker network", value: (v) => v.network, cell: (v) => <CellText mono muted>{v.network}</CellText>, hideBelow: "lg" },
      { id: "created", header: "Created", value: (v) => v.created_at, cell: (v) => <TimeAgo value={v.created_at} />, hideBelow: "lg" },
    ],
    [selected],
  )

  const onDelete = async () => {
    if (!vpc) return
    try {
      await api.del(`/api/v1/vpc/vpcs/${encodeURIComponent(vpc.id)}`)
    } catch (err) {
      throw new Error(deleteErrorMessage(err, "VPC"))
    } finally {
      revalidate("/api/v1/vpc")
    }
    toast.success(`VPC ${vpc.name || vpc.id} deleted`)
    setSelected([])
  }

  return (
    <div className="flex flex-col gap-4">
      <PageHeader
        breadcrumbs={[{ label: "VPC", href: "/vpc/" }, { label: "Your VPCs" }]}
        title="Your VPCs"
        description={
          <>
            Each VPC is a Docker bridge network. Resources in the same VPC reach each other by private IP or DNS name; security group inbound rules
            decide which ports are published on the host.
          </>
        }
      />
      <DataTable
        title="VPCs"
        data={data}
        columns={columns}
        rowId={(v) => v.id}
        loading={isLoading}
        error={error}
        onRefresh={() => mutate()}
        refreshing={isValidating}
        selection="single"
        selected={selected}
        onSelectedChange={setSelected}
        searchPlaceholder="Filter VPCs"
        defaultSort={{ id: "created" }}
        expanded={(v) => (v.id === selected[0] ? <VpcDetail vpc={v} /> : null)}
        actions={
          <>
            <ActionsMenu
              disabled={!vpc}
              items={[
                { label: "Create subnet", onSelect: () => setSubnetOpen(true) },
                { label: "View subnets", onSelect: () => vpc && router.push(subnetsHref(vpc.id)) },
                { separator: true },
                {
                  label: "Delete VPC",
                  destructive: true,
                  icon: <Trash2 />,
                  disabled: vpc?.default,
                  hint: vpc?.default ? "The default VPC cannot be deleted" : undefined,
                  onSelect: () => setDeleteOpen(true),
                },
              ]}
            />
            <Button size="sm" onClick={() => setCreateOpen(true)}>
              <Plus /> Create VPC
            </Button>
          </>
        }
        empty={
          <EmptyState
            icon={Network}
            title="No VPCs"
            description="Create a VPC to get an isolated network for your instances."
            action={
              <Button size="sm" onClick={() => setCreateOpen(true)}>
                <Plus /> Create VPC
              </Button>
            }
          />
        }
      />
      <CreateVpcDialog open={createOpen} onOpenChange={setCreateOpen} vpcs={data} onCreated={(v) => setSelected([v.id])} />
      <CreateSubnetDialog open={subnetOpen} onOpenChange={setSubnetOpen} vpcs={data} defaultVpcId={vpc?.id} />
      {vpc && (
        <ConfirmDialog
          open={deleteOpen}
          onOpenChange={setDeleteOpen}
          title={`Delete VPC ${vpc.name || vpc.id}?`}
          description={
            <>
              This removes the Docker network <span className="font-mono">{vpc.network}</span> together with the VPC&apos;s {vpc.subnets?.length ?? 0} subnet(s)
              and security groups. Instances and other resources in the VPC must be terminated first.
            </>
          }
          confirmText={vpc.id}
          actionLabel="Delete VPC"
          onConfirm={onDelete}
        />
      )}
    </div>
  )
}
