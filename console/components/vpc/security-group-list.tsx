"use client"

import { useMemo, useState } from "react"
import Link from "next/link"
import { useRouter } from "next/navigation"
import { Plus, Shield, Trash2 } from "lucide-react"
import { toast } from "sonner"

import { Button } from "@/components/ui/button"
import { ActionsMenu } from "@/components/console/actions-menu"
import { ConfirmDialog } from "@/components/console/confirm-dialog"
import { cellLinkClass, DataTable, type Column } from "@/components/console/data-table"
import { EmptyState } from "@/components/console/empty-state"
import { PageHeader } from "@/components/console/page-header"
import { TimeAgo } from "@/components/console/time-ago"
import { api } from "@/lib/api"
import { revalidate, useApi, useQueryParam, useSetQueryParam } from "@/lib/hooks"
import type { SecurityGroup } from "@/lib/types"

import { deleteErrorMessage, rulesSummary, sgHref, useVpcs, VpcLink, VpcSelect } from "./common"
import { CreateSecurityGroupDialog } from "./dialogs"

export function SecurityGroupList() {
  const router = useRouter()
  const vpcId = useQueryParam("vpc_id")
  const setParam = useSetQueryParam()
  const vpcs = useVpcs()
  const { data, error, isLoading, isValidating, mutate } = useApi<SecurityGroup[]>("/api/v1/vpc/security-groups", { query: { vpc_id: vpcId || undefined } })
  const [selected, setSelected] = useState<string[]>([])
  const [createOpen, setCreateOpen] = useState(false)
  const [deleteOpen, setDeleteOpen] = useState(false)

  const group = data?.find((g) => g.id === selected[0])

  const columns = useMemo<Column<SecurityGroup>[]>(
    () => [
      {
        id: "name",
        header: "Name",
        value: (g) => g.name,
        cell: (g) => (
          <Link href={sgHref(g.id)} className={cellLinkClass()} onClick={(e) => e.stopPropagation()}>
            {g.name}
          </Link>
        ),
      },
      { id: "id", header: "Group ID", value: (g) => g.id, cell: (g) => <span className="font-mono text-[13px]">{g.id}</span> },
      {
        id: "vpc",
        header: "VPC",
        value: (g) => g.vpc_id,
        cell: (g) => (
          <span onClick={(e) => e.stopPropagation()}>
            <VpcLink id={g.vpc_id} vpcs={vpcs.data} />
          </span>
        ),
        hideBelow: "md",
      },
      {
        id: "description",
        header: "Description",
        value: (g) => g.description,
        cell: (g) => (
          <span className="text-muted-foreground block max-w-64 truncate" title={g.description}>
            {g.description || "-"}
          </span>
        ),
        hideBelow: "lg",
      },
      {
        id: "rules",
        header: "Inbound rules",
        value: (g) => g.ingress.length,
        cell: (g) =>
          g.ingress.length ? (
            <span className="flex items-baseline gap-2">
              <span className="tabular-nums">{g.ingress.length}</span>
              <span className="text-muted-foreground max-w-56 truncate font-mono text-xs" title={rulesSummary(g.ingress, 50)}>
                {rulesSummary(g.ingress)}
              </span>
            </span>
          ) : (
            <span className="text-muted-foreground">0</span>
          ),
      },
      { id: "created", header: "Created", value: (g) => g.created_at, cell: (g) => <TimeAgo value={g.created_at} />, hideBelow: "lg" },
    ],
    [vpcs.data],
  )

  const onDelete = async () => {
    if (!group) return
    try {
      await api.del(`/api/v1/vpc/security-groups/${encodeURIComponent(group.id)}`)
    } catch (err) {
      throw new Error(deleteErrorMessage(err, "security group"))
    } finally {
      revalidate("/api/v1/vpc")
    }
    toast.success(`Security group ${group.name} deleted`)
    setSelected([])
  }

  return (
    <div className="flex flex-col gap-4">
      <PageHeader
        breadcrumbs={[{ label: "VPC", href: "/vpc/" }, { label: "Security groups" }]}
        title="Security groups"
        description="A security group acts as a virtual firewall. In HomeCloud its inbound rules decide which ports of an instance are published on the host."
      />
      <DataTable
        title="Security groups"
        data={data}
        columns={columns}
        rowId={(g) => g.id}
        loading={isLoading}
        error={error}
        onRefresh={() => mutate()}
        refreshing={isValidating}
        selection="single"
        selected={selected}
        onSelectedChange={setSelected}
        searchPlaceholder="Filter security groups"
        defaultSort={{ id: "created" }}
        filters={
          <VpcSelect size="sm" className="w-full md:w-72" allowAll value={vpcId} vpcs={vpcs.data} onChange={(v) => setParam("vpc_id", v || null)} />
        }
        actions={
          <>
            <ActionsMenu
              disabled={!group}
              items={[
                { label: "View details", onSelect: () => group && router.push(sgHref(group.id)) },
                { label: "Edit inbound rules", onSelect: () => group && router.push(`${sgHref(group.id)}&add=1`) },
                { separator: true },
                {
                  label: "Delete security group",
                  destructive: true,
                  icon: <Trash2 />,
                  disabled: group?.name === "default",
                  hint: group?.name === "default" ? "The default security group cannot be deleted" : undefined,
                  onSelect: () => setDeleteOpen(true),
                },
              ]}
            />
            <Button size="sm" onClick={() => setCreateOpen(true)}>
              <Plus /> Create security group
            </Button>
          </>
        }
        empty={
          <EmptyState
            icon={Shield}
            title="No security groups"
            description="Create a security group to control which ports of your instances are reachable."
            action={
              <Button size="sm" onClick={() => setCreateOpen(true)}>
                <Plus /> Create security group
              </Button>
            }
          />
        }
      />
      <CreateSecurityGroupDialog open={createOpen} onOpenChange={setCreateOpen} vpcs={vpcs.data} groups={data} onCreated={(g) => router.push(sgHref(g.id))} />
      {group && (
        <ConfirmDialog
          open={deleteOpen}
          onOpenChange={setDeleteOpen}
          title={`Delete security group ${group.name}?`}
          description={
            <>
              Security group <span className="font-mono">{group.id}</span> and its {group.ingress.length} inbound rule(s) will be deleted. Groups used by
              instances cannot be deleted.
            </>
          }
          actionLabel="Delete"
          onConfirm={onDelete}
        />
      )}
    </div>
  )
}
