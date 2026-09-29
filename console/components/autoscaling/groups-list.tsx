"use client"

import { useState } from "react"
import Link from "next/link"
import { useRouter } from "next/navigation"
import { Plus, Scaling } from "lucide-react"
import { toast } from "sonner"

import { Button } from "@/components/ui/button"
import { ActionsMenu, type ActionItem } from "@/components/console/actions-menu"
import { DataTable, cellLinkClass, type Column } from "@/components/console/data-table"
import { EmptyState } from "@/components/console/empty-state"
import { PageHeader } from "@/components/console/page-header"
import { TimeAgo } from "@/components/console/time-ago"
import { api, errorMessage } from "@/lib/api"
import { pluralize } from "@/lib/format"
import { revalidate } from "@/lib/hooks"
import type { AutoScalingGroup } from "@/lib/types"

import { ASG_PREFIX, DeleteGroupDialog, EditCapacityDialog, EditPoliciesDialog, GroupStatusBadge, groupHref, groupPath, useGroups } from "./shared"

const columns: Column<AutoScalingGroup>[] = [
  {
    id: "name",
    header: "Name",
    cell: (g) => (
      <Link href={groupHref(g.name)} onClick={(e) => e.stopPropagation()} className={cellLinkClass()}>
        {g.name}
      </Link>
    ),
    value: (g) => g.name,
  },
  {
    id: "instances",
    header: "Instances",
    cell: (g) => <span className="tabular-nums">{(g.instances ?? []).length}</span>,
    value: (g) => (g.instances ?? []).length,
  },
  { id: "status", header: "Status", cell: (g) => <GroupStatusBadge g={g} />, value: (g) => g.status },
  { id: "desired", header: "Desired", cell: (g) => <span className="tabular-nums">{g.desired_capacity}</span>, value: (g) => g.desired_capacity },
  { id: "min", header: "Min", cell: (g) => <span className="tabular-nums">{g.min_size}</span>, value: (g) => g.min_size, hideBelow: "sm" },
  { id: "max", header: "Max", cell: (g) => <span className="tabular-nums">{g.max_size}</span>, value: (g) => g.max_size, hideBelow: "sm" },
  {
    id: "launch",
    header: "Launch configuration",
    cell: (g) => (
      <span className="font-mono text-[13px] whitespace-nowrap">
        {g.launch.image_id} · {g.launch.instance_type || "default"}
      </span>
    ),
    value: (g) => `${g.launch.image_id} ${g.launch.instance_type}`,
    hideBelow: "lg",
  },
  {
    id: "policies",
    header: "Scaling",
    cell: (g) => ((g.policies ?? []).length ? `Target tracking (${pluralize((g.policies ?? []).length, "policy", "policies")})` : <span className="text-muted-foreground">Manual</span>),
    value: (g) => (g.policies ?? []).length,
    hideBelow: "lg",
  },
  { id: "created", header: "Created", cell: (g) => <TimeAgo value={g.created_at} />, value: (g) => g.created_at, hideBelow: "md" },
]

export function GroupsList() {
  const router = useRouter()
  const { data, error, isLoading, isValidating, mutate } = useGroups()
  const [selected, setSelected] = useState<string[]>([])
  const [capacity, setCapacity] = useState<AutoScalingGroup | null>(null)
  const [policies, setPolicies] = useState<AutoScalingGroup | null>(null)
  const [deleting, setDeleting] = useState<AutoScalingGroup | null>(null)
  const sel = (data ?? []).find((g) => selected.includes(g.name)) ?? null
  const deletingState = sel?.status !== "Active"

  const setSuspended = async (g: AutoScalingGroup, suspended: boolean) => {
    try {
      await api.patch(groupPath(g.name), { suspended })
      toast.success(suspended ? `Suspended dynamic scaling of ${g.name}` : `Resumed dynamic scaling of ${g.name}`)
      await revalidate(ASG_PREFIX)
    } catch (e) {
      toast.error(errorMessage(e))
    }
  }

  const items: ActionItem[] = [
    { label: "View details", onSelect: () => sel && router.push(groupHref(sel.name)), disabled: !sel },
    { label: "Edit capacity", onSelect: () => sel && setCapacity(sel), disabled: !sel || deletingState },
    { label: "Edit scaling policies", onSelect: () => sel && setPolicies(sel), disabled: !sel || deletingState },
    sel?.suspended
      ? { label: "Resume dynamic scaling", onSelect: () => sel && setSuspended(sel, false), disabled: deletingState }
      : { label: "Suspend dynamic scaling", onSelect: () => sel && setSuspended(sel, true), disabled: !sel || deletingState },
    { separator: true },
    { label: "Delete", destructive: true, onSelect: () => sel && setDeleting(sel), disabled: !sel || deletingState },
  ]

  return (
    <div className="flex flex-col gap-4">
      <PageHeader
        title="Auto Scaling groups"
        description="Keep a fleet of identical instances at the desired capacity across subnets: stopped instances are replaced, members are registered with target groups, and target tracking scales on CPU or memory."
        breadcrumbs={[{ label: "EC2", href: "/ec2/" }, { label: "Auto Scaling groups" }]}
      />
      <DataTable
        title="Auto Scaling groups"
        data={data}
        columns={columns}
        rowId={(g) => g.name}
        loading={isLoading}
        error={error}
        onRefresh={() => mutate()}
        refreshing={isValidating}
        selection="single"
        selected={selected}
        onSelectedChange={setSelected}
        searchPlaceholder="Filter by name or image"
        defaultSort={{ id: "name" }}
        actions={
          <>
            <ActionsMenu items={items} disabled={!sel} />
            <Button size="sm" asChild>
              <Link href="/ec2/autoscaling/create/">
                <Plus /> Create Auto Scaling group
              </Link>
            </Button>
          </>
        }
        empty={
          <EmptyState
            icon={Scaling}
            title="No Auto Scaling groups"
            description="Create a group to run a fleet of instances from one launch configuration, optionally behind a load balancer target group."
            action={
              <Button size="sm" asChild>
                <Link href="/ec2/autoscaling/create/">
                  <Plus /> Create Auto Scaling group
                </Link>
              </Button>
            }
          />
        }
      />
      <EditCapacityDialog group={capacity} onClose={() => setCapacity(null)} />
      <EditPoliciesDialog group={policies} onClose={() => setPolicies(null)} />
      <DeleteGroupDialog group={deleting} onClose={() => setDeleting(null)} />
    </div>
  )
}
