"use client"

import { useEffect, useState } from "react"
import Link from "next/link"
import { useRouter } from "next/navigation"
import { Plus, Target } from "lucide-react"

import { Button } from "@/components/ui/button"
import { ActionsMenu, type ActionItem } from "@/components/console/actions-menu"
import { DataTable, type Column } from "@/components/console/data-table"
import { EmptyState } from "@/components/console/empty-state"
import { PageHeader } from "@/components/console/page-header"
import { TimeAgo } from "@/components/console/time-ago"
import { useQueryParam, useSetQueryParam } from "@/lib/hooks"
import type { LoadBalancer, TargetGroup } from "@/lib/types"
import { CreateTargetGroupDialog, DeleteTargetGroupDialog, EditHealthCheckDialog, RegisterTargetsDialog } from "./tg-dialogs"
import { LbLink, healthCounts, lbsUsing, tgHref, useLoadBalancers, useTargetGroups, vpcHref } from "./shared"

function TargetSummary({ tg }: { tg: TargetGroup }) {
  const c = healthCounts(tg.targets)
  if (!c.total) return <span className="text-muted-foreground">0</span>
  return (
    <span className="whitespace-nowrap">
      {c.total}
      <span className="text-muted-foreground"> · </span>
      <span className={c.healthy ? "text-emerald-700 dark:text-emerald-400" : "text-muted-foreground"}>{c.healthy} healthy</span>
      {c.unhealthy > 0 && <span className="text-red-700 dark:text-red-400">, {c.unhealthy} unhealthy</span>}
    </span>
  )
}

function makeColumns(lbs: LoadBalancer[] | undefined): Column<TargetGroup>[] {
  return [
    {
      id: "name",
      header: "Name",
      cell: (tg) => (
        <Link href={tgHref(tg.name)} onClick={(e) => e.stopPropagation()} className="text-primary font-medium whitespace-nowrap hover:underline">
          {tg.name}
        </Link>
      ),
      value: (tg) => tg.name,
    },
    { id: "port", header: "Protocol : Port", cell: (tg) => <span className="font-mono text-[13px]">{tg.protocol}:{tg.port}</span>, value: (tg) => tg.port },
    { id: "targets", header: "Targets", cell: (tg) => <TargetSummary tg={tg} />, value: (tg) => tg.targets.length },
    {
      id: "lbs",
      header: "Load balancers",
      cell: (tg) => {
        const used = lbsUsing(tg.name, lbs)
        return used.length ? (
          <span className="flex flex-wrap gap-x-2">
            {used.map((n) => (
              <LbLink key={n} name={n} />
            ))}
          </span>
        ) : (
          <span className="text-muted-foreground">None</span>
        )
      },
      value: (tg) => lbsUsing(tg.name, lbs).join(" "),
      hideBelow: "sm",
    },
    {
      id: "hc",
      header: "Health check",
      cell: (tg) => (
        <span className="font-mono text-[13px] whitespace-nowrap">
          {tg.health_check.path} <span className="text-muted-foreground">every {tg.health_check.interval_seconds}s</span>
        </span>
      ),
      value: (tg) => tg.health_check.path,
      hideBelow: "md",
    },
    {
      id: "vpc",
      header: "VPC",
      cell: (tg) => (
        <Link href={vpcHref(tg.vpc_id)} onClick={(e) => e.stopPropagation()} className="text-primary font-mono text-[13px] whitespace-nowrap hover:underline">
          {tg.vpc_id}
        </Link>
      ),
      value: (tg) => tg.vpc_id,
      hideBelow: "lg",
    },
    { id: "created", header: "Created", cell: (tg) => <TimeAgo value={tg.created_at} />, value: (tg) => tg.created_at, hideBelow: "lg" },
  ]
}

export function TargetGroupsList() {
  const router = useRouter()
  const createParam = useQueryParam("create")
  const setParam = useSetQueryParam()
  const { data, error, isLoading, isValidating, mutate } = useTargetGroups(10_000)
  const lbs = useLoadBalancers(false)
  const [selected, setSelected] = useState<string[]>([])
  const [creating, setCreating] = useState(false)
  const [editing, setEditing] = useState<TargetGroup | null>(null)
  const [registering, setRegistering] = useState<TargetGroup | null>(null)
  const [deleting, setDeleting] = useState<TargetGroup | null>(null)

  // /elb/target-groups/?create=1 opens the create dialog (linked from the load balancer wizard).
  useEffect(() => {
    if (createParam) {
      setCreating(true)
      setParam("create", null)
    }
  }, [createParam, setParam])

  const sel = (data ?? []).find((tg) => tg.name === selected[0]) ?? null
  const items: ActionItem[] = [
    { label: "Register targets", onSelect: () => setRegistering(sel), disabled: !sel },
    { label: "Edit health check settings", onSelect: () => setEditing(sel), disabled: !sel },
    { label: "View targets", onSelect: () => sel && router.push(tgHref(sel.name)), disabled: !sel },
    { separator: true },
    { label: "Delete target group", destructive: true, onSelect: () => setDeleting(sel), disabled: !sel },
  ]

  return (
    <div className="flex flex-col gap-4">
      <PageHeader
        title="Target groups"
        description="Groups of EC2 instances and ECS tasks that load balancers route to. HomeCloud health-checks every target from inside the VPC."
        breadcrumbs={[{ label: "ELB", href: "/elb/" }, { label: "Target groups" }]}
      />
      <DataTable
        title="Target groups"
        data={data}
        columns={makeColumns(lbs.data)}
        rowId={(tg) => tg.name}
        loading={isLoading}
        error={error}
        onRefresh={() => (mutate(), lbs.mutate())}
        refreshing={isValidating}
        selection="single"
        selected={selected}
        onSelectedChange={setSelected}
        searchPlaceholder="Find target group by name, port or load balancer"
        defaultSort={{ id: "name" }}
        actions={
          <>
            <ActionsMenu items={items} disabled={!sel} />
            <Button size="sm" onClick={() => setCreating(true)}>
              <Plus /> Create target group
            </Button>
          </>
        }
        empty={
          <EmptyState
            icon={Target}
            title="No target groups"
            description="A target group is a set of instances or ECS tasks, a port and a health check. Load balancer listeners and rules forward to target groups."
            action={
              <Button size="sm" onClick={() => setCreating(true)}>
                <Plus /> Create target group
              </Button>
            }
          />
        }
      />
      <CreateTargetGroupDialog open={creating} onOpenChange={setCreating} />
      <EditHealthCheckDialog tg={editing} onClose={() => setEditing(null)} />
      <RegisterTargetsDialog tg={registering} onClose={() => setRegistering(null)} />
      <DeleteTargetGroupDialog
        tg={deleting}
        usedBy={deleting ? lbsUsing(deleting.name, lbs.data) : []}
        onClose={() => setDeleting(null)}
        onDeleted={() => setSelected([])}
      />
    </div>
  )
}
