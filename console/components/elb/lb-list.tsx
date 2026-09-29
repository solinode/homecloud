"use client"

import { useState } from "react"
import Link from "next/link"
import { useRouter } from "next/navigation"
import { Network, Plus } from "lucide-react"
import { toast } from "sonner"

import { Button } from "@/components/ui/button"
import { ActionsMenu, type ActionItem } from "@/components/console/actions-menu"
import { ConfirmDialog } from "@/components/console/confirm-dialog"
import { CopyableText } from "@/components/console/copy-button"
import { DataTable, type Column } from "@/components/console/data-table"
import { EmptyState } from "@/components/console/empty-state"
import { PageHeader } from "@/components/console/page-header"
import { TimeAgo } from "@/components/console/time-ago"
import { api, seg } from "@/lib/api"
import { pluralize } from "@/lib/format"
import { revalidate } from "@/lib/hooks"
import type { LoadBalancer } from "@/lib/types"
import { ELB_PREFIX, LBS_PATH, LbPublicPorts, LbStateBadge, lbHref, useLoadBalancers, vpcHref } from "./shared"

const columns: Column<LoadBalancer>[] = [
  {
    id: "name",
    header: "Name",
    cell: (lb) => (
      <Link href={lbHref(lb.name)} onClick={(e) => e.stopPropagation()} className="text-primary font-medium whitespace-nowrap hover:underline">
        {lb.name}
      </Link>
    ),
    value: (lb) => lb.name,
  },
  { id: "state", header: "State", cell: (lb) => <LbStateBadge state={lb.state} />, value: (lb) => lb.state },
  {
    id: "scheme",
    header: "Scheme",
    cell: (lb) => <span className="whitespace-nowrap">{lb.scheme === "internal" ? "Internal" : "Internet-facing"}</span>,
    value: (lb) => lb.scheme,
    hideBelow: "sm",
  },
  {
    id: "dns",
    header: "DNS name",
    cell: (lb) => <CopyableText value={lb.dns_name} />,
    value: (lb) => lb.dns_name,
    hideBelow: "md",
  },
  {
    id: "ports",
    header: "Public endpoints",
    cell: (lb) => <LbPublicPorts lb={lb} compact empty={lb.scheme === "internal" ? "Internal only" : "-"} />,
    value: (lb) => Object.values(lb.public_ports ?? {}).join(" "),
    sortable: false,
    hideBelow: "sm",
  },
  {
    id: "listeners",
    header: "Listeners",
    cell: (lb) => (
      <span className="whitespace-nowrap" title={(lb.listeners ?? []).map((l) => `${l.protocol}:${l.port}`).join(", ")}>
        {(lb.listeners ?? []).map((l) => `${l.protocol}:${l.port}`).join(", ") || "-"}
      </span>
    ),
    value: (lb) => lb.listeners?.length ?? 0,
    hideBelow: "lg",
  },
  {
    id: "vpc",
    header: "VPC",
    cell: (lb) => (
      <Link href={vpcHref(lb.vpc_id)} onClick={(e) => e.stopPropagation()} className="text-primary font-mono text-[13px] whitespace-nowrap hover:underline">
        {lb.vpc_id}
      </Link>
    ),
    value: (lb) => `${lb.vpc_id} ${lb.subnet_id}`,
    hideBelow: "lg",
  },
  { id: "created", header: "Created", cell: (lb) => <TimeAgo value={lb.created_at} />, value: (lb) => lb.created_at, hideBelow: "lg" },
]

export function LoadBalancersList() {
  const router = useRouter()
  const { data, error, isLoading, isValidating, mutate } = useLoadBalancers()
  const [selected, setSelected] = useState<string[]>([])
  const [deleting, setDeleting] = useState<LoadBalancer | null>(null)

  const sel = (data ?? []).find((lb) => lb.name === selected[0]) ?? null
  const items: ActionItem[] = [
    { label: "View details", onSelect: () => sel && router.push(lbHref(sel.name)), disabled: !sel },
    { label: "View generated config", onSelect: () => sel && router.push(lbHref(sel.name, "config")), disabled: !sel },
    { separator: true },
    { label: "Delete load balancer", destructive: true, onSelect: () => setDeleting(sel), disabled: !sel },
  ]

  return (
    <div className="flex flex-col gap-4">
      <PageHeader
        title="Load balancers"
        description="Application load balancers route HTTP traffic from listeners to target groups of instances and ECS tasks. Each one is a managed nginx in your VPC."
        breadcrumbs={[{ label: "ELB", href: "/elb/" }, { label: "Load balancers" }]}
      />
      <DataTable
        title="Load balancers"
        data={data}
        columns={columns}
        rowId={(lb) => lb.name}
        loading={isLoading}
        error={error}
        onRefresh={() => mutate()}
        refreshing={isValidating}
        selection="single"
        selected={selected}
        onSelectedChange={setSelected}
        searchPlaceholder="Find load balancer by name, DNS name or VPC"
        defaultSort={{ id: "name" }}
        actions={
          <>
            <ActionsMenu items={items} disabled={!sel} />
            <Button size="sm" asChild>
              <Link href="/elb/create/">
                <Plus /> Create load balancer
              </Link>
            </Button>
          </>
        }
        empty={
          <EmptyState
            icon={Network}
            title="No load balancers"
            description="Create a target group with your instances first, then a load balancer with a listener that forwards to it."
            action={
              <div className="flex flex-wrap justify-center gap-2">
                <Button size="sm" variant="outline" asChild>
                  <Link href="/elb/target-groups/">Target groups</Link>
                </Button>
                <Button size="sm" asChild>
                  <Link href="/elb/create/">
                    <Plus /> Create load balancer
                  </Link>
                </Button>
              </div>
            }
          />
        }
      />
      <DeleteLoadBalancerDialog lb={deleting} onClose={() => setDeleting(null)} onDeleted={() => setSelected([])} />
    </div>
  )
}

export function DeleteLoadBalancerDialog({ lb, onClose, onDeleted }: { lb: LoadBalancer | null; onClose: () => void; onDeleted?: () => void }) {
  return (
    <ConfirmDialog
      open={!!lb}
      onOpenChange={(o) => !o && onClose()}
      title={`Delete load balancer ${lb?.name ?? ""}?`}
      description={
        <>
          The load balancer container is removed and its {pluralize(lb?.listeners?.length ?? 0, "listener")} stop accepting traffic immediately. Target groups
          and their targets are kept.
        </>
      }
      confirmText={lb?.name}
      onConfirm={async () => {
        if (!lb) return
        await api.del(`${LBS_PATH}/${seg(lb.name)}`)
        toast.success(`Deleted load balancer ${lb.name}`)
        await revalidate(ELB_PREFIX)
        onDeleted?.()
      }}
    />
  )
}
