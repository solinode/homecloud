"use client"

import { useState } from "react"
import Link from "next/link"
import { AlertCircle, ArrowLeft, Loader2, Pause, Pencil, Play, RefreshCw, Trash2 } from "lucide-react"
import { toast } from "sonner"

import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert"
import { Button } from "@/components/ui/button"
import { ActionsMenu } from "@/components/console/actions-menu"
import { CodeBlock } from "@/components/console/code-block"
import { CopyableText } from "@/components/console/copy-button"
import { CellLink, CellText, DataTable, type Column } from "@/components/console/data-table"
import { EmptyState } from "@/components/console/empty-state"
import { ErrorState } from "@/components/console/error-state"
import { KeyValueGrid } from "@/components/console/key-value"
import { DetailSkeleton } from "@/components/console/loading"
import { PageHeader } from "@/components/console/page-header"
import { Section } from "@/components/console/section"
import { StatTile } from "@/components/console/stat-tile"
import { StatusBadge } from "@/components/console/status-badge"
import { Tag } from "@/components/console/tag"
import { TimeAgo } from "@/components/console/time-ago"
import { instanceHref } from "@/components/ec2/instance-actions"
import { tgHref } from "@/components/elb/shared"
import { fileSystemHref } from "@/components/efs/common"
import { ApiError, api, errorMessage } from "@/lib/api"
import { formatDate, pluralize } from "@/lib/format"
import { revalidate, useApi, useQueryParam } from "@/lib/hooks"
import type { AsgInstance, AutoScalingGroup, ScalingActivity } from "@/lib/types"
import { cn } from "@/lib/utils"

import { ASG_PREFIX, ActivityStatusBadge, DeleteGroupDialog, EditCapacityDialog, EditPoliciesDialog, GroupStatusBadge, groupPath, groupPoll, metricLabel } from "./shared"

function BackButton() {
  return (
    <Button variant="outline" size="sm" asChild>
      <Link href="/ec2/autoscaling/">
        <ArrowLeft /> Back to Auto Scaling groups
      </Link>
    </Button>
  )
}

const instanceColumns: Column<AsgInstance>[] = [
  {
    id: "id",
    header: "Instance ID",
    cell: (i) => (
      <CellLink href={instanceHref(i.id)} mono>
        {i.id}
      </CellLink>
    ),
    value: (i) => i.id,
  },
  { id: "state", header: "State", cell: (i) => <StatusBadge status={i.state} />, value: (i) => i.state },
  { id: "ip", header: "Private IP", cell: (i) => <CellText mono>{i.private_ip}</CellText>, value: (i) => i.private_ip, hideBelow: "sm" },
  { id: "subnet", header: "Subnet", cell: (i) => <CellText mono>{i.subnet_id}</CellText>, value: (i) => i.subnet_id, hideBelow: "md" },
  { id: "launched", header: "Launched", cell: (i) => <TimeAgo value={i.launch_time} />, value: (i) => i.launch_time },
]

const activityColumns: Column<ScalingActivity & { key: string }>[] = [
  { id: "status", header: "Status", cell: (a) => <ActivityStatusBadge status={a.status} />, value: (a) => a.status },
  { id: "desc", header: "Description", cell: (a) => <span className="break-words">{a.description}</span>, value: (a) => a.description },
  { id: "cause", header: "Cause", cell: (a) => <span className="text-muted-foreground text-xs break-words">{a.cause}</span>, value: (a) => a.cause, hideBelow: "md" },
  {
    id: "time",
    header: "Time",
    cell: (a) => (
      <span className="whitespace-nowrap" title={formatDate(a.time)}>
        <TimeAgo value={a.time} />
      </span>
    ),
    value: (a) => a.time,
  },
]

export function GroupDetail() {
  const name = useQueryParam("name")
  const { data: g, error, isLoading, isValidating, mutate } = useApi<AutoScalingGroup>(name ? groupPath(name) : null, {
    refreshInterval: (d) => groupPoll(d ? [d] : undefined),
  })
  const [capacity, setCapacity] = useState(false)
  const [policies, setPolicies] = useState(false)
  const [deleting, setDeleting] = useState(false)
  const [suspending, setSuspending] = useState(false)

  const crumbs = [{ label: "EC2", href: "/ec2/" }, { label: "Auto Scaling groups", href: "/ec2/autoscaling/" }, { label: name || "Group" }]

  if (!name) {
    return (
      <>
        <PageHeader title="Auto Scaling group" breadcrumbs={crumbs} />
        <EmptyState title="No group selected" description="Open a group from the Auto Scaling groups list." action={<BackButton />} />
      </>
    )
  }
  if (error && !g) {
    const notFound = error instanceof ApiError && error.status === 404
    return (
      <div className="flex flex-col gap-4">
        <PageHeader title={name} breadcrumbs={crumbs} />
        {notFound ? (
          <Section>
            <EmptyState icon={AlertCircle} title="Auto Scaling group not found" description={`Group ${name} does not exist. It may have been deleted.`} action={<BackButton />} />
          </Section>
        ) : (
          <ErrorState error={error} onRetry={() => mutate()} />
        )}
      </div>
    )
  }
  if (isLoading || !g) return <DetailSkeleton />

  const deletingState = g.status !== "Active"
  const instances = g.instances ?? []
  const activities = (g.activities ?? []).map((a, i) => ({ ...a, key: `${a.time}-${i}` }))
  const launch = g.launch

  const toggleSuspend = async () => {
    setSuspending(true)
    try {
      await api.patch(groupPath(g.name), { suspended: !g.suspended })
      toast.success(g.suspended ? "Resumed dynamic scaling" : "Suspended dynamic scaling; the group keeps its desired capacity")
      await revalidate(ASG_PREFIX)
    } catch (e) {
      toast.error(errorMessage(e))
    } finally {
      setSuspending(false)
    }
  }

  return (
    <div className="flex flex-col gap-4">
      <PageHeader
        title={g.name}
        badge={<GroupStatusBadge g={g} />}
        breadcrumbs={crumbs}
        actions={
          <>
            <Button variant="outline" size="icon-sm" onClick={() => mutate()} aria-label="Refresh">
              <RefreshCw className={cn(isValidating && "animate-spin")} />
            </Button>
            <Button variant="outline" size="sm" onClick={toggleSuspend} disabled={deletingState || suspending}>
              {suspending ? <Loader2 className="animate-spin" /> : g.suspended ? <Play /> : <Pause />}
              {g.suspended ? "Resume scaling" : "Suspend scaling"}
            </Button>
            <ActionsMenu
              disabled={deletingState}
              items={[
                { label: "Edit capacity", onSelect: () => setCapacity(true) },
                { label: "Edit scaling policies", onSelect: () => setPolicies(true) },
                { separator: true },
                { label: "Delete group", destructive: true, icon: <Trash2 />, onSelect: () => setDeleting(true) },
              ]}
            />
            <Button size="sm" onClick={() => setCapacity(true)} disabled={deletingState}>
              <Pencil /> Edit capacity
            </Button>
          </>
        }
      />

      <div className="grid grid-cols-2 gap-4 lg:grid-cols-4">
        <StatTile label="Desired" value={g.desired_capacity} caption="Target instance count" />
        <StatTile label="Minimum" value={g.min_size} caption="Lower bound" />
        <StatTile label="Maximum" value={g.max_size} caption="Upper bound" />
        <StatTile
          label="Instances"
          value={instances.length}
          tone={instances.length === g.desired_capacity ? "success" : "warning"}
          caption={instances.length === g.desired_capacity ? "At desired capacity" : "Converging"}
        />
      </div>

      {deletingState && (
        <Alert>
          <Loader2 className="animate-spin" />
          <AlertTitle>Deleting</AlertTitle>
          <AlertDescription>The group&apos;s instances are being terminated. The group disappears once they are gone.</AlertDescription>
        </Alert>
      )}
      {g.suspended && !deletingState && (
        <Alert>
          <Pause />
          <AlertTitle>Dynamic scaling is suspended</AlertTitle>
          <AlertDescription>Scaling policies are not evaluated. The group still replaces stopped instances and keeps the desired capacity.</AlertDescription>
        </Alert>
      )}

      <Section title="Group details">
        <KeyValueGrid
          columns={4}
          items={[
            { label: "Health check grace period", value: `${g.health_check_grace_seconds} seconds` },
            { label: "Last scaling", value: g.last_scaling ? <TimeAgo value={g.last_scaling} /> : "Never" },
            { label: "Created", value: <span>{formatDate(g.created_at)}</span> },
            {
              label: "Target groups",
              value: (g.target_groups ?? []).length ? (
                <span className="flex flex-wrap gap-x-2">
                  {(g.target_groups ?? []).map((t) => (
                    <Link key={t} href={tgHref(t)} className="text-primary hover:underline">
                      {t}
                    </Link>
                  ))}
                </span>
              ) : (
                "None"
              ),
            },
            {
              label: "Subnets",
              wide: true,
              value: (g.subnet_ids ?? []).length ? (
                <span className="flex flex-wrap gap-x-3">
                  {(g.subnet_ids ?? []).map((s) => (
                    <Link key={s} href="/vpc/subnets/" className="text-primary font-mono text-[13px] hover:underline">
                      {s}
                    </Link>
                  ))}
                </span>
              ) : (
                "Default subnet"
              ),
            },
            { label: "ARN", value: <CopyableText value={g.arn} />, wide: true },
          ]}
        />
      </Section>

      <Section title="Launch configuration" description="New instances use this configuration.">
        <KeyValueGrid
          columns={3}
          items={[
            { label: "Image", value: <span className="font-mono text-[13px]">{launch.image_id}</span> },
            { label: "Instance type", value: <span className="font-mono text-[13px]">{launch.instance_type || "default"}</span> },
            {
              label: "Security groups",
              value: (launch.security_group_ids ?? []).length ? (
                <span className="flex flex-wrap gap-x-2">
                  {(launch.security_group_ids ?? []).map((s) => (
                    <Link key={s} href={`/vpc/security-group/?id=${encodeURIComponent(s)}`} className="text-primary font-mono text-[13px] hover:underline">
                      {s}
                    </Link>
                  ))}
                </span>
              ) : (
                "VPC default"
              ),
            },
            {
              label: "File systems",
              value: (launch.file_systems ?? []).length ? (
                <span className="flex flex-col gap-0.5">
                  {(launch.file_systems ?? []).map((f) => (
                    <span key={f.mount_path}>
                      <Link href={fileSystemHref(f.file_system_id)} className="text-primary font-mono text-[13px] hover:underline">
                        {f.file_system_id}
                      </Link>{" "}
                      <span className="text-muted-foreground font-mono text-xs">
                        {f.mount_path}
                        {f.read_only ? " (ro)" : ""}
                      </span>
                    </span>
                  ))}
                </span>
              ) : (
                ""
              ),
            },
            {
              label: "User data",
              wide: true,
              value: launch.user_data ? <CodeBlock code={launch.user_data} title="user-data" wrap maxHeight="14rem" /> : "",
            },
          ]}
        />
      </Section>

      <Section
        title="Dynamic scaling policies"
        actions={
          <Button size="sm" variant="outline" onClick={() => setPolicies(true)} disabled={deletingState}>
            <Pencil /> Edit policies
          </Button>
        }
      >
        {(g.policies ?? []).length === 0 ? (
          <p className="text-muted-foreground text-sm">No policies. The group keeps the desired capacity you set.</p>
        ) : (
          <ul className="flex flex-col divide-y text-sm">
            {(g.policies ?? []).map((p) => (
              <li key={p.name} className="flex flex-wrap items-center gap-x-3 gap-y-1 py-2 first:pt-0 last:pb-0">
                <Tag mono={false} accent="info">
                  Target tracking
                </Tag>
                <span className="font-medium">{p.name}</span>
                <span className="text-muted-foreground">
                  {metricLabel(p.metric)} at {p.target_value}%, cooldown {p.cooldown_seconds}s
                </span>
              </li>
            ))}
          </ul>
        )}
      </Section>

      <DataTable
        title="Instances"
        description={`${pluralize(instances.length, "instance")} running or pending. Stopped members are terminated and replaced.`}
        data={instances}
        columns={instanceColumns}
        rowId={(i) => i.id}
        noSearch
        defaultSort={{ id: "launched" }}
        empty={<EmptyState title="No instances" description={g.desired_capacity ? "Launching instances..." : "Desired capacity is 0."} />}
      />

      <DataTable
        title="Activity history"
        data={activities}
        columns={activityColumns}
        rowId={(a) => a.key}
        noSearch={activities.length < 10}
        pageSize={15}
        empty={<EmptyState title="No activity yet" />}
      />

      <EditCapacityDialog group={capacity ? g : null} onClose={() => setCapacity(false)} />
      <EditPoliciesDialog group={policies ? g : null} onClose={() => setPolicies(false)} />
      <DeleteGroupDialog group={deleting ? g : null} onClose={() => setDeleting(false)} redirect />
    </div>
  )
}
