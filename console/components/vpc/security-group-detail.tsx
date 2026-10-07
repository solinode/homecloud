"use client"

import { useEffect, useState } from "react"
import { useRouter } from "next/navigation"
import { Info, Plus, Server, ShieldOff, Trash2 } from "lucide-react"
import { toast } from "sonner"

import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert"
import { Button } from "@/components/ui/button"
import { ActionsMenu } from "@/components/console/actions-menu"
import { ConfirmDialog } from "@/components/console/confirm-dialog"
import { CopyableText } from "@/components/console/copy-button"
import { CellLink, CellText, DataTable, type Column } from "@/components/console/data-table"
import { EmptyState } from "@/components/console/empty-state"
import { ErrorState } from "@/components/console/error-state"
import { KeyValueGrid } from "@/components/console/key-value"
import { DetailSkeleton } from "@/components/console/loading"
import { PageHeader } from "@/components/console/page-header"
import { Section } from "@/components/console/section"
import { StatusBadge } from "@/components/console/status-badge"
import { Tag } from "@/components/console/tag"
import { api } from "@/lib/api"
import { formatDate } from "@/lib/format"
import { revalidate, useApi, useQueryParam } from "@/lib/hooks"
import type { Instance, SecurityGroup, SecurityGroupRule } from "@/lib/types"

import { deleteErrorMessage, portRange, ruleProtocol, ruleSource, ruleType, sgHref, useVpcs, VpcLink } from "./common"
import { AddRulesDialog } from "./dialogs"

export function SecurityGroupDetail() {
  const id = useQueryParam("id")
  const wantAdd = useQueryParam("add") === "1"
  const router = useRouter()
  const { data: g, error, isLoading, mutate } = useApi<SecurityGroup>(id ? `/api/v1/vpc/security-groups/${encodeURIComponent(id)}` : null)
  const vpcs = useVpcs()
  const instances = useApi<Instance[]>("/api/v1/ec2/instances")
  const [addOpen, setAddOpen] = useState(false)
  const [removeRule, setRemoveRule] = useState<SecurityGroupRule | null>(null)
  const [deleteOpen, setDeleteOpen] = useState(false)

  useEffect(() => {
    if (wantAdd && g) setAddOpen(true)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [wantAdd, !!g])

  if (!id) return <ErrorState error={new Error("No security group ID given. Open a group from the security groups list.")} />
  if (isLoading && !g) return <DetailSkeleton />
  if (error || !g)
    return (
      <div className="flex flex-col gap-4">
        <PageHeader breadcrumbs={[{ label: "VPC", href: "/vpc/" }, { label: "Security groups", href: "/vpc/security-groups/" }, { label: id }]} title={id} />
        <ErrorState error={error ?? new Error("Security group not found")} onRetry={() => mutate()} />
      </div>
    )

  const vpc = vpcs.data?.find((v) => v.id === g.vpc_id)
  const users = (instances.data ?? []).filter((i) => i.state !== "terminated" && i.security_groups?.includes(g.id))

  const ruleColumns: Column<SecurityGroupRule>[] = [
    { id: "id", header: "Rule ID", cell: (r) => <CellText mono>{r.id}</CellText>, value: (r) => r.id, hideBelow: "md" },
    { id: "type", header: "Type", cell: (r) => <span className="whitespace-nowrap">{ruleType(r)}</span>, value: (r) => ruleType(r) },
    { id: "protocol", header: "Protocol", cell: (r) => <Tag accent={r.protocol === "-1" ? "brand" : r.protocol === "udp" ? "violet" : "info"}>{ruleProtocol(r)}</Tag>, value: (r) => r.protocol },
    { id: "ports", header: "Port range", cell: (r) => <span className="font-mono text-[13px] whitespace-nowrap">{portRange(r)}</span>, value: (r) => r.from_port },
    {
      id: "source",
      header: "Source",
      cell: (r) =>
        r.source_group ? (
          <CellLink href={sgHref(r.source_group)} mono>
            {r.source_group === g.id ? `${r.source_group} (this group)` : r.source_group}
          </CellLink>
        ) : (
          <span className="font-mono text-[13px] whitespace-nowrap">{r.cidr}</span>
        ),
      value: (r) => ruleSource(r),
    },
    { id: "description", header: "Description", cell: (r) => <CellText muted>{r.description}</CellText>, value: (r) => r.description, hideBelow: "md" },
    {
      id: "actions",
      header: "",
      className: "text-right",
      cell: (r) => (
        <Button variant="ghost" size="sm" className="text-destructive hover:text-destructive" onClick={(e) => (e.stopPropagation(), setRemoveRule(r))}>
          <Trash2 /> Remove
        </Button>
      ),
    },
  ]

  const instanceColumns: Column<Instance>[] = [
    {
      id: "id",
      header: "Instance",
      cell: (i) => (
        <CellLink href={`/ec2/instance/?id=${encodeURIComponent(i.id)}`} mono>
          {i.id}
        </CellLink>
      ),
      value: (i) => i.id,
    },
    { id: "name", header: "Name", cell: (i) => <CellText>{i.name}</CellText>, value: (i) => i.name },
    { id: "state", header: "State", cell: (i) => <StatusBadge status={i.state} />, value: (i) => i.state },
    { id: "ip", header: "Private IP", cell: (i) => <CellText mono>{i.private_ip}</CellText>, value: (i) => i.private_ip, hideBelow: "sm" },
    {
      id: "ports",
      header: "Published ports",
      cell: (i) => (
        <CellText mono>
          {Object.entries(i.public_ports ?? {})
            .map(([k, v]) => `${k} -> ${v}`)
            .join(", ")}
        </CellText>
      ),
      sortable: false,
      hideBelow: "md",
    },
  ]

  const onRemoveRule = async () => {
    if (!removeRule) return
    await api.del(`/api/v1/vpc/security-groups/${encodeURIComponent(g.id)}/ingress/${encodeURIComponent(removeRule.id)}`)
    toast.success("Inbound rule removed")
    mutate()
    revalidate("/api/v1/vpc/security-groups")
  }

  const onDelete = async () => {
    try {
      await api.del(`/api/v1/vpc/security-groups/${encodeURIComponent(g.id)}`)
    } catch (err) {
      throw new Error(deleteErrorMessage(err, "security group"))
    }
    toast.success(`Security group ${g.name} deleted`)
    revalidate("/api/v1/vpc")
    router.push("/vpc/security-groups/")
  }

  return (
    <div className="flex flex-col gap-5">
      <PageHeader
        breadcrumbs={[{ label: "VPC", href: "/vpc/" }, { label: "Security groups", href: "/vpc/security-groups/" }, { label: g.id }]}
        title={g.name}
        badge={<Tag title="Security group ID">{g.id}</Tag>}
        description={g.description || undefined}
        actions={
          <>
            <ActionsMenu
              items={[
                {
                  label: "Delete security group",
                  icon: <Trash2 />,
                  destructive: true,
                  disabled: g.name === "default",
                  hint: g.name === "default" ? "The default security group cannot be deleted" : undefined,
                  onSelect: () => setDeleteOpen(true),
                },
              ]}
            />
            <Button size="sm" onClick={() => setAddOpen(true)}>
              <Plus /> Add inbound rule
            </Button>
          </>
        }
      />

      <Section title="Details">
        <KeyValueGrid
          items={[
            { label: "Security group name", value: g.name },
            { label: "Security group ID", value: <CopyableText value={g.id} /> },
            { label: "VPC", value: <VpcLink id={g.vpc_id} vpcs={vpcs.data} /> },
            { label: "Description", value: g.description },
            { label: "Inbound rules", value: String(g.ingress.length) },
            { label: "Created", value: formatDate(g.created_at) },
          ]}
        />
      </Section>

      <Alert variant="info">
        <Info />
        <AlertTitle>How inbound rules work in HomeCloud</AlertTitle>
        <AlertDescription>
          <p>
            Inbound rules are enforced between the resources of a VPC: a rule allows traffic from an IPv4 CIDR or from every resource that has another security
            group. Changes apply to running resources within seconds.
          </p>
          <p>
            Rules from 0.0.0.0/0 (or 127.0.0.1/32) also publish their TCP/UDP ports on the host; Docker assigns each a host port, shown on the instance&apos;s
            details page.
          </p>
        </AlertDescription>
      </Alert>

      <DataTable
        title="Inbound rules"
        data={g.ingress}
        columns={ruleColumns}
        rowId={(r) => r.id}
        onRefresh={() => mutate()}
        noSearch={g.ingress.length < 6}
        actions={
          <Button size="sm" variant="outline" onClick={() => setAddOpen(true)}>
            <Plus /> Edit inbound rules
          </Button>
        }
        empty={
          <EmptyState
            icon={ShieldOff}
            title="No inbound rules"
            description="No ports are published for instances using this group."
            action={
              <Button size="sm" onClick={() => setAddOpen(true)}>
                <Plus /> Add rule
              </Button>
            }
          />
        }
      />

      <Section title="Outbound rules">
        {vpc && !vpc.internet_access ? (
          <p className="text-sm">
            <StatusBadge status="isolated" tone="warning" label="Internet blocked" />{" "}
            <span className="text-muted-foreground">
              VPC {vpc.name || vpc.id} is isolated: outbound traffic to the internet is blocked. Traffic inside the VPC is allowed.
            </span>
          </p>
        ) : (
          <p className="text-sm">
            <StatusBadge status="allowed" label="All traffic allowed" />{" "}
            <span className="text-muted-foreground">All outbound traffic is allowed (destination 0.0.0.0/0, all protocols and ports).</span>
          </p>
        )}
      </Section>

      <DataTable
        title="Instances using this group"
        data={instances.data ? users : undefined}
        columns={instanceColumns}
        rowId={(i) => i.id}
        loading={instances.isLoading}
        error={instances.error}
        onRetry={() => instances.mutate()}
        noSearch={users.length < 6}
        empty={<EmptyState icon={Server} title="No instances use this security group" description="Instances get security groups when they are launched." />}
      />

      <AddRulesDialog
        open={addOpen}
        onOpenChange={(o) => {
          setAddOpen(o)
          if (!o) mutate()
        }}
        group={g}
      />
      <ConfirmDialog
        open={!!removeRule}
        onOpenChange={(o) => !o && setRemoveRule(null)}
        title="Remove inbound rule?"
        description={
          removeRule && (
            <>
              {ruleType(removeRule)} ({ruleProtocol(removeRule)} {portRange(removeRule)}) from <span className="font-mono">{ruleSource(removeRule)}</span> will be removed.
              Traffic it allowed stops for new connections; a published port is closed when the instance is next recreated.
            </>
          )
        }
        actionLabel="Remove rule"
        onConfirm={onRemoveRule}
      />
      <ConfirmDialog
        open={deleteOpen}
        onOpenChange={setDeleteOpen}
        title={`Delete security group ${g.name}?`}
        description={users.length ? `${users.length} instance(s) still use this group, so deletion will fail until they are terminated.` : "This cannot be undone."}
        actionLabel="Delete"
        onConfirm={onDelete}
      />
    </div>
  )
}
