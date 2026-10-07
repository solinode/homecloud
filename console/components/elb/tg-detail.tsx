"use client"

import { useMemo, useState } from "react"
import Link from "next/link"
import { useRouter } from "next/navigation"
import { AlertCircle, ArrowLeft, Info, Loader2, Pencil, Plus, RefreshCw, Target } from "lucide-react"
import { toast } from "sonner"

import { Alert, AlertDescription } from "@/components/ui/alert"
import { Button } from "@/components/ui/button"
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs"
import { ActionsMenu, type ActionItem } from "@/components/console/actions-menu"
import { ConfirmDialog } from "@/components/console/confirm-dialog"
import { CopyableText } from "@/components/console/copy-button"
import { DataTable, type Column } from "@/components/console/data-table"
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
import { ApiError, api, seg } from "@/lib/api"
import { formatDate, pluralize } from "@/lib/format"
import { revalidate, useApi, useQueryParam, useSetQueryParam } from "@/lib/hooks"
import type { ElbTarget, Instance, TargetGroup } from "@/lib/types"
import { DeleteTargetGroupDialog, EditHealthCheckDialog, RegisterTargetsDialog } from "./tg-dialogs"
import {
  ELB_PREFIX,
  HealthBadge,
  LbLink,
  TGS_PATH,
  TargetLink,
  ecsServiceHref,
  healthCounts,
  lbsUsing,
  targetKind,
  useInstances,
  useLoadBalancers,
  vpcHref,
} from "./shared"

const TABS = ["targets", "health"] as const
type Tab = (typeof TABS)[number]

interface EcsServiceLite {
  name: string
  load_balancer?: { target_group: string; container_port: number } | null
}

// Row id: the API keys a registration on (id, port), but deregistration removes every port of an id.
const rowKey = (t: ElbTarget) => `${t.id}:${t.port}`

export function TargetGroupDetail() {
  const router = useRouter()
  const name = useQueryParam("name")
  const tabParam = useQueryParam("tab")
  const setParam = useSetQueryParam()
  const tab: Tab = (TABS as readonly string[]).includes(tabParam) ? (tabParam as Tab) : "targets"

  const { data: tg, error, isLoading, isValidating, mutate } = useApi<TargetGroup>(name ? `${TGS_PATH}/${seg(name)}` : null, { refreshInterval: 5000 })
  const lbs = useLoadBalancers(false)
  const instances = useInstances()
  // ECS services register their tasks here; failure (e.g. no permission) just hides the note.
  const ecs = useApi<EcsServiceLite[]>(name ? "/api/v1/ecs/services" : null, { refreshInterval: 60_000 })

  const [selected, setSelected] = useState<string[]>([])
  const [editing, setEditing] = useState(false)
  const [registering, setRegistering] = useState(false)
  const [deleting, setDeleting] = useState(false)
  const [deregistering, setDeregistering] = useState<ElbTarget[] | null>(null)

  const byId = useMemo(() => new Map((instances.data ?? []).map((i) => [i.id, i])), [instances.data])

  const crumbs = [{ label: "ELB", href: "/elb/" }, { label: "Target groups", href: "/elb/target-groups/" }, { label: name || "Target group" }]

  if (!name) {
    return (
      <>
        <PageHeader title="Target group" breadcrumbs={crumbs} />
        <EmptyState title="No target group selected" description="Open a target group from the list." action={<BackButton />} />
      </>
    )
  }
  if (error && !tg) {
    const notFound = error instanceof ApiError && error.status === 404
    return (
      <div className="flex flex-col gap-4">
        <PageHeader title={name} breadcrumbs={crumbs} />
        {notFound ? (
          <Section>
            <EmptyState icon={AlertCircle} title="Target group not found" description={`Target group ${name} does not exist.`} action={<BackButton />} />
          </Section>
        ) : (
          <ErrorState error={error} onRetry={() => mutate()} />
        )}
      </div>
    )
  }
  if (isLoading || !tg) return <DetailSkeleton />

  const usedBy = lbsUsing(tg.name, lbs.data)
  const ecsServices = (ecs.data ?? []).filter((s) => s.load_balancer?.target_group === tg.name)
  const c = healthCounts(tg.targets)
  const selTargets = tg.targets.filter((t) => selected.includes(rowKey(t)))

  const columns: Column<ElbTarget>[] = [
    { id: "id", header: "Target", cell: (t) => <TargetLink id={t.id} />, value: (t) => t.id },
    {
      id: "name",
      header: "Name",
      cell: (t) => <TargetName target={t} instance={byId.get(t.id)} />,
      value: (t) => byId.get(t.id)?.name ?? targetKind(t.id),
      hideBelow: "md",
    },
    { id: "port", header: "Port", cell: (t) => <span className="font-mono text-[13px]">{t.port}</span>, value: (t) => t.port },
    {
      id: "ip",
      header: "Private IP",
      cell: (t) => <span className="font-mono text-[13px]">{t.ip || "-"}</span>,
      value: (t) => t.ip,
      hideBelow: "sm",
    },
    { id: "health", header: "Health status", cell: (t) => <HealthBadge health={t.health} />, value: (t) => t.health },
    {
      id: "reason",
      header: "Health status details",
      cell: (t) => <span className="text-muted-foreground text-xs break-words">{t.reason || "-"}</span>,
      value: (t) => t.reason,
      sortable: false,
      hideBelow: "lg",
    },
  ]

  const actionItems: ActionItem[] = [
    { label: "Edit health check settings", onSelect: () => setEditing(true) },
    { separator: true },
    { label: "Delete target group", destructive: true, onSelect: () => setDeleting(true) },
  ]

  return (
    <div className="flex flex-col gap-4">
      <PageHeader
        title={tg.name}
        badge={
          c.total ? (
            <StatusBadge
              status={c.unhealthy ? "unhealthy" : c.healthy === c.total ? "healthy" : "partial"}
              tone={c.unhealthy ? "danger" : c.healthy === c.total ? "success" : "neutral"}
              label={`${c.healthy}/${c.total} healthy`}
            />
          ) : (
            <StatusBadge status="empty" tone="neutral" label="No targets" />
          )
        }
        description={
          <span>
            Target group · <span className="font-mono text-[13px]">{tg.protocol}:{tg.port}</span> · {pluralize(c.total, "target")}
          </span>
        }
        breadcrumbs={crumbs}
        actions={
          <>
            <Button variant="outline" size="sm" onClick={() => mutate()} disabled={isValidating} aria-label="Refresh">
              {isValidating ? <Loader2 className="animate-spin" /> : <RefreshCw />}
              Refresh
            </Button>
            <ActionsMenu items={actionItems} />
            <Button size="sm" onClick={() => setRegistering(true)}>
              <Plus /> Register targets
            </Button>
          </>
        }
      />

      <div className="grid grid-cols-2 gap-3 lg:grid-cols-4">
        <StatTile label="Targets" value={c.total} icon={<Target />} />
        <StatTile label="Healthy" value={c.healthy} tone={c.healthy ? "success" : "neutral"} />
        <StatTile label="Unhealthy" value={c.unhealthy} tone={c.unhealthy ? "danger" : "neutral"} />
        <StatTile label="Other" value={c.other} tone={c.other ? "warning" : "neutral"} caption="Initial, unused or unavailable" />
      </div>

      <Section title="Details">
        <KeyValueGrid
          columns={4}
          items={[
            {
              label: "Protocol : Port",
              value: (
                <Tag accent={tg.protocol === "HTTPS" ? "success" : "info"}>
                  {tg.protocol}:{tg.port}
                </Tag>
              ),
            },
            {
              label: "VPC",
              value: (
                <Link href={vpcHref(tg.vpc_id)} className="text-primary font-mono text-[13px] whitespace-nowrap hover:underline">
                  {tg.vpc_id}
                </Link>
              ),
            },
            {
              label: "Load balancers",
              value: usedBy.length ? (
                <span className="flex flex-wrap gap-x-2">
                  {usedBy.map((n) => (
                    <LbLink key={n} name={n} />
                  ))}
                </span>
              ) : (
                "None"
              ),
            },
            { label: "Created", value: <span>{formatDate(tg.created_at)} (<TimeAgo value={tg.created_at} />)</span> },
            { label: "Health check", value: <span className="font-mono text-[13px]">{tg.health_check.path}</span> },
            { label: "ARN", value: <CopyableText value={tg.arn} />, wide: true },
          ]}
        />
      </Section>

      {ecsServices.length > 0 && (
        <Alert variant="info">
          <Info />
          <AlertDescription>
          <span>
            Targets are managed by ECS service{ecsServices.length > 1 ? "s" : ""}{" "}
            {ecsServices.map((s, i) => (
              <span key={s.name}>
                {i > 0 && ", "}
                <Link href={ecsServiceHref(s.name)} className="text-primary font-medium hover:underline">
                  {s.name}
                </Link>
              </span>
            ))}
            : running tasks are registered and stopped tasks deregistered automatically.
          </span>
          </AlertDescription>
        </Alert>
      )}
      {!usedBy.length && c.total > 0 && (
        <Alert>
          <Info />
          <AlertDescription>Targets are health-checked only while an active load balancer forwards to this group, so they show as Unused.</AlertDescription>
        </Alert>
      )}

      <Tabs value={tab} onValueChange={(v) => setParam("tab", v === "targets" ? null : v)}>
        <TabsList>
          <TabsTrigger value="targets">Targets ({c.total})</TabsTrigger>
          <TabsTrigger value="health">Health checks</TabsTrigger>
        </TabsList>
        <TabsContent value="targets">
          <DataTable
            title="Registered targets"
            data={tg.targets}
            columns={columns}
            rowId={rowKey}
            onRefresh={() => mutate()}
            refreshing={isValidating}
            selection="multi"
            selected={selected}
            onSelectedChange={setSelected}
            searchPlaceholder="Filter targets"
            defaultSort={{ id: "id" }}
            actions={
              <>
                <Button size="sm" variant="outline" disabled={!selTargets.length} onClick={() => setDeregistering(selTargets)}>
                  Deregister
                </Button>
                <Button size="sm" onClick={() => setRegistering(true)}>
                  <Plus /> Register targets
                </Button>
              </>
            }
            empty={
              <EmptyState
                icon={Target}
                title="No registered targets"
                description="Register running instances to start receiving traffic from load balancers that forward to this group."
                action={
                  <Button size="sm" onClick={() => setRegistering(true)}>
                    <Plus /> Register targets
                  </Button>
                }
              />
            }
          />
        </TabsContent>
        <TabsContent value="health">
          <Section
            title="Health check settings"
            actions={
              <Button size="sm" variant="outline" onClick={() => setEditing(true)}>
                <Pencil /> Edit
              </Button>
            }
          >
            <KeyValueGrid
              columns={3}
              items={[
                { label: "Protocol", value: "HTTP" },
                { label: "Path", value: <span className="font-mono text-[13px]">{tg.health_check.path}</span> },
                { label: "Port", value: "Traffic port" },
                { label: "Interval", value: `${tg.health_check.interval_seconds} seconds` },
                { label: "Healthy threshold", value: pluralize(tg.health_check.healthy_threshold, "consecutive success", "consecutive successes") },
                { label: "Unhealthy threshold", value: pluralize(tg.health_check.unhealthy_threshold, "consecutive failure") },
                { label: "Timeout", value: "4 seconds" },
                { label: "Success codes", value: "200-399" },
              ]}
            />
          </Section>
        </TabsContent>
      </Tabs>

      <EditHealthCheckDialog tg={editing ? tg : null} onClose={() => setEditing(false)} />
      <RegisterTargetsDialog tg={registering ? tg : null} onClose={() => setRegistering(false)} />
      <DeleteTargetGroupDialog tg={deleting ? tg : null} usedBy={usedBy} onClose={() => setDeleting(false)} onDeleted={() => router.push("/elb/target-groups/")} />
      <ConfirmDialog
        open={!!deregistering}
        onOpenChange={(o) => !o && setDeregistering(null)}
        title={`Deregister ${pluralize(deregistering?.length ?? 0, "target")}?`}
        actionLabel="Deregister"
        description={
          <div className="flex flex-col gap-2">
            <p>Load balancers stop sending new requests to these targets immediately. The instances and tasks keep running.</p>
            <ul className="flex flex-col gap-0.5">
              {(deregistering ?? []).map((t) => (
                <li key={rowKey(t)} className="text-foreground font-mono text-[13px]">
                  {t.id}:{t.port}
                </li>
              ))}
            </ul>
            {ecsServices.length > 0 && (deregistering ?? []).some((t) => !t.id.startsWith("i-")) && (
              <p className="text-warning">ECS re-registers running service tasks when they are replaced.</p>
            )}
          </div>
        }
        onConfirm={async () => {
          const list = deregistering ?? []
          const ids = [...new Set(list.map((t) => t.id))]
          for (const id of ids) await api.del(`${TGS_PATH}/${seg(tg.name)}/targets/${seg(id)}`)
          toast.success(`Deregistered ${pluralize(ids.length, "target")}`)
          setSelected([])
          await revalidate(ELB_PREFIX)
        }}
      />
    </div>
  )
}

function TargetName({ target, instance }: { target: ElbTarget; instance?: Instance }) {
  if (instance) {
    return (
      <span className="flex flex-col">
        <span className="max-w-[16rem] truncate font-medium whitespace-nowrap" title={instance.name}>{instance.name || "-"}</span>
        {instance.state !== "running" && <span className="text-muted-foreground text-xs">Instance {instance.state}</span>}
      </span>
    )
  }
  return <span className="text-muted-foreground">{targetKind(target.id)}</span>
}

function BackButton() {
  return (
    <Button variant="outline" size="sm" asChild>
      <Link href="/elb/target-groups/">
        <ArrowLeft /> Back to target groups
      </Link>
    </Button>
  )
}
