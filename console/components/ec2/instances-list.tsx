"use client"

import { useMemo, useState } from "react"
import Link from "next/link"
import { useRouter } from "next/navigation"
import { Cpu, Plus } from "lucide-react"

import { Button } from "@/components/ui/button"
import { Label } from "@/components/ui/label"
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select"
import { Switch } from "@/components/ui/switch"
import { ActionsMenu, type ActionItem } from "@/components/console/actions-menu"
import { DataTable, type Column } from "@/components/console/data-table"
import { EmptyState } from "@/components/console/empty-state"
import { PageHeader } from "@/components/console/page-header"
import { StatusBadge } from "@/components/console/status-badge"
import { TimeAgo } from "@/components/console/time-ago"
import { useApi } from "@/lib/hooks"
import type { Instance } from "@/lib/types"
import { INSTANCES_PATH, PublicPorts, instanceHref, pollInterval, useInstanceActions } from "./instance-actions"

const STATES = ["all", "pending", "running", "stopping", "stopped", "shutting-down", "terminated"] as const

const columns: Column<Instance>[] = [
  {
    id: "name",
    header: "Name",
    cell: (i) => (i.name ? <span className="font-medium">{i.name}</span> : <span className="text-muted-foreground">-</span>),
    value: (i) => i.name,
  },
  {
    id: "id",
    header: "Instance ID",
    cell: (i) => (
      <Link href={instanceHref(i.id)} onClick={(e) => e.stopPropagation()} className="text-primary font-mono text-[13px] font-medium whitespace-nowrap hover:underline">
        {i.id}
      </Link>
    ),
    value: (i) => i.id,
  },
  { id: "state", header: "Instance state", cell: (i) => <StatusBadge status={i.state} />, value: (i) => i.state },
  { id: "type", header: "Instance type", cell: (i) => <span className="font-mono text-[13px]">{i.instance_type}</span>, value: (i) => i.instance_type },
  {
    id: "image",
    header: "Image",
    cell: (i) => (
      <span className="font-mono text-[13px]" title={i.image_id}>
        {i.image_ref}
      </span>
    ),
    value: (i) => `${i.image_ref} ${i.image_id}`,
    hideBelow: "md",
  },
  {
    id: "ip",
    header: "Private IPv4",
    cell: (i) => <span className="font-mono text-[13px]">{i.private_ip || "-"}</span>,
    value: (i) => i.private_ip,
    hideBelow: "sm",
  },
  {
    id: "ports",
    header: "Public ports",
    cell: (i) => <PublicPorts instance={i} compact />,
    value: (i) => Object.keys(i.public_ports ?? {}).join(" "),
    sortable: false,
    hideBelow: "md",
  },
  { id: "az", header: "Availability zone", cell: (i) => i.availability_zone, value: (i) => i.availability_zone, hideBelow: "lg" },
  {
    id: "launch",
    header: "Launch time",
    cell: (i) => <TimeAgo value={i.launch_time} />,
    value: (i) => i.launch_time,
    hideBelow: "lg",
  },
]

export function InstancesList() {
  const router = useRouter()
  const { data, error, isLoading, isValidating, mutate } = useApi<Instance[]>(INSTANCES_PATH, {
    refreshInterval: (d) => pollInterval((d ?? []).map((i) => i.state)),
  })
  const [selected, setSelected] = useState<string[]>([])
  const [state, setState] = useState<string>("all")
  const [hideTerminated, setHideTerminated] = useState(true)
  const actions = useInstanceActions()

  const rows = useMemo(() => {
    const all = data ?? []
    if (state !== "all") return all.filter((i) => i.state === state)
    return hideTerminated ? all.filter((i) => i.state !== "terminated") : all
  }, [data, state, hideTerminated])

  const sel = rows.filter((i) => selected.includes(i.id))
  const single = sel.length === 1 ? sel[0] : null
  const ids = sel.map((i) => i.id)
  const all = (s: string) => sel.length > 0 && sel.every((i) => i.state === s)
  const disabled = actions.busy

  const stateItems: ActionItem[] = [
    { label: "Start instance", onSelect: () => actions.start(ids), disabled: disabled || !all("stopped") },
    { label: "Stop instance", onSelect: () => actions.stop(ids), disabled: disabled || !all("running") },
    { label: "Reboot instance", onSelect: () => actions.reboot(ids), disabled: disabled || !all("running") },
    { separator: true },
    {
      label: "Terminate instance",
      destructive: true,
      onSelect: () => actions.terminate(sel),
      disabled: disabled || !sel.length || sel.some((i) => i.state === "terminated" || i.state === "shutting-down"),
    },
  ]
  const actionItems: ActionItem[] = [
    { label: "Connect", onSelect: () => single && router.push(instanceHref(single.id, "connect")), disabled: !single || single.state !== "running" },
    { separator: true },
    { heading: "Instance settings" },
    {
      label: "Change instance type",
      onSelect: () => single && actions.changeType(single),
      disabled: !single || single.state !== "stopped",
      hint: "Select a single stopped instance",
    },
    { heading: "Image" },
    {
      label: "Create image",
      onSelect: () => single && actions.createImage(single),
      disabled: !single || !single.container_id || single.state === "terminated",
    },
  ]

  const counts = useMemo(() => {
    const c: Record<string, number> = {}
    for (const i of data ?? []) c[i.state] = (c[i.state] ?? 0) + 1
    return c
  }, [data])

  return (
    <div className="flex flex-col gap-4">
      <PageHeader
        title="Instances"
        description="Virtual servers running as resource-limited containers in your VPC subnets."
        breadcrumbs={[{ label: "EC2", href: "/ec2/" }, { label: "Instances" }]}
      />
      <DataTable
        title="Instances"
        data={data ? rows : undefined}
        columns={columns}
        rowId={(i) => i.id}
        loading={isLoading}
        error={error}
        onRefresh={() => mutate()}
        refreshing={isValidating}
        selection="multi"
        selected={selected}
        onSelectedChange={setSelected}
        searchPlaceholder="Find instance by name, ID, IP or type"
        defaultSort={{ id: "launch", desc: true }}
        actions={
          <>
            <ActionsMenu label="Instance state" items={stateItems} disabled={!sel.length} />
            <ActionsMenu items={actionItems} disabled={!sel.length} />
            <Button size="sm" asChild>
              <Link href="/ec2/launch/">
                <Plus /> Launch instances
              </Link>
            </Button>
          </>
        }
        filters={
          <>
            <Select value={state} onValueChange={setState}>
              <SelectTrigger size="sm" className="h-8 w-44" aria-label="Instance state filter">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {STATES.map((s) => (
                  <SelectItem key={s} value={s}>
                    <span className="capitalize">{s === "all" ? "All states" : s}</span>
                    {s !== "all" && counts[s] ? <span className="text-muted-foreground text-xs">({counts[s]})</span> : null}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
            {state === "all" && (
              <div className="flex items-center gap-2">
                <Switch id="hide-terminated" checked={hideTerminated} onCheckedChange={setHideTerminated} />
                <Label htmlFor="hide-terminated" className="text-sm font-normal whitespace-nowrap">
                  Hide terminated{counts.terminated ? ` (${counts.terminated})` : ""}
                </Label>
              </div>
            )}
          </>
        }
        empty={
          (data?.length ?? 0) > 0 ? (
            <EmptyState
              icon={Cpu}
              title="No instances match this filter"
              description={state !== "all" ? `There are no ${state} instances.` : "All of your instances are terminated."}
              action={
                <Button variant="outline" size="sm" onClick={() => (setState("all"), setHideTerminated(false))}>
                  Show all instances
                </Button>
              }
            />
          ) : (
            <EmptyState
              icon={Cpu}
              title="No instances"
              description="Launch an instance from Ubuntu, Debian, Alpine or your own image. It gets a private IP in your VPC and can publish ports on this host."
              action={
                <Button size="sm" asChild>
                  <Link href="/ec2/launch/">
                    <Plus /> Launch instances
                  </Link>
                </Button>
              }
            />
          )
        }
      />
      {actions.dialogs}
    </div>
  )
}
