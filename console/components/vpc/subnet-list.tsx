"use client"

import { useMemo, useState } from "react"
import { Plus, Split, Trash2 } from "lucide-react"
import { toast } from "sonner"

import { Button } from "@/components/ui/button"
import { Progress } from "@/components/ui/progress"
import { ActionsMenu } from "@/components/console/actions-menu"
import { ConfirmDialog } from "@/components/console/confirm-dialog"
import { CellText, DataTable, type Column } from "@/components/console/data-table"
import { EmptyState } from "@/components/console/empty-state"
import { PageHeader } from "@/components/console/page-header"
import { Tag } from "@/components/console/tag"
import { TimeAgo } from "@/components/console/time-ago"
import { api } from "@/lib/api"
import { formatNumber } from "@/lib/format"
import { revalidate, useApi, useQueryParam, useSetQueryParam } from "@/lib/hooks"
import type { Subnet } from "@/lib/types"

import { deleteErrorMessage, useVpcs, VpcLink, VpcSelect } from "./common"
import { CreateSubnetDialog } from "./dialogs"

export function SubnetList() {
  const vpcId = useQueryParam("vpc_id")
  const setParam = useSetQueryParam()
  const vpcs = useVpcs()
  const { data, error, isLoading, isValidating, mutate } = useApi<Subnet[]>("/api/v1/vpc/subnets", { query: { vpc_id: vpcId || undefined } })
  const [selected, setSelected] = useState<string[]>([])
  const [createOpen, setCreateOpen] = useState(false)
  const [deleteOpen, setDeleteOpen] = useState(false)

  const subnet = data?.find((s) => s.id === selected[0])

  const columns = useMemo<Column<Subnet>[]>(
    () => [
      { id: "name", header: "Name", value: (s) => s.name, cell: (s) => <CellText className="font-medium">{s.name}</CellText> },
      { id: "id", header: "Subnet ID", value: (s) => s.id, cell: (s) => <CellText mono>{s.id}</CellText> },
      {
        id: "vpc",
        header: "VPC",
        value: (s) => s.vpc_id,
        cell: (s) => (
          <span onClick={(e) => e.stopPropagation()}>
            <VpcLink id={s.vpc_id} vpcs={vpcs.data} />
          </span>
        ),
        hideBelow: "md",
      },
      { id: "cidr", header: "IPv4 CIDR", value: (s) => s.cidr, cell: (s) => <span className="font-mono text-[13px] whitespace-nowrap">{s.cidr}</span> },
      { id: "az", header: "Availability Zone", value: (s) => s.availability_zone, cell: (s) => <span className="whitespace-nowrap">{s.availability_zone}</span>, hideBelow: "sm" },
      {
        id: "ips",
        header: "Available IPv4",
        value: (s) => s.available_ips,
        cell: (s) => {
          const total = s.available_ips + s.used_ips
          return (
            <div className="flex min-w-32 flex-col gap-1">
              <span className="tabular-nums whitespace-nowrap">
                {formatNumber(s.available_ips, 0)}
                <span className="text-muted-foreground"> ({formatNumber(s.used_ips, 0)} used)</span>
              </span>
              <Progress value={total ? (s.used_ips / total) * 100 : 0} className="h-1" />
            </div>
          )
        },
      },
      { id: "default", header: "Default subnet", value: (s) => (s.default ? "Yes" : "No"), cell: (s) =>
          s.default ? (
            <Tag accent="brand" mono={false}>
              Default
            </Tag>
          ) : (
            <span className="text-muted-foreground">No</span>
          ),
        hideBelow: "lg",
      },
      { id: "created", header: "Created", value: (s) => s.created_at, cell: (s) => <TimeAgo value={s.created_at} />, hideBelow: "lg" },
    ],
    [vpcs.data],
  )

  const onDelete = async () => {
    if (!subnet) return
    try {
      await api.del(`/api/v1/vpc/subnets/${encodeURIComponent(subnet.id)}`)
    } catch (err) {
      throw new Error(deleteErrorMessage(err, "subnet"))
    } finally {
      revalidate("/api/v1/vpc")
    }
    toast.success(`Subnet ${subnet.name || subnet.id} deleted`)
    setSelected([])
  }

  return (
    <div className="flex flex-col gap-4">
      <PageHeader
        breadcrumbs={[{ label: "VPC", href: "/vpc/" }, { label: "Subnets" }]}
        title="Subnets"
        description="A subnet is a range of addresses in a VPC. Instances launched into a subnet get a static private IP from its range."
      />
      <DataTable
        title="Subnets"
        data={data}
        columns={columns}
        rowId={(s) => s.id}
        loading={isLoading}
        error={error}
        onRefresh={() => mutate()}
        refreshing={isValidating}
        selection="single"
        selected={selected}
        onSelectedChange={setSelected}
        searchPlaceholder="Filter subnets"
        defaultSort={{ id: "cidr" }}
        filters={
          <VpcSelect size="sm" className="w-full md:w-72" allowAll value={vpcId} vpcs={vpcs.data} onChange={(v) => setParam("vpc_id", v || null)} />
        }
        actions={
          <>
            <ActionsMenu
              disabled={!subnet}
              items={[
                {
                  label: "Delete subnet",
                  destructive: true,
                  icon: <Trash2 />,
                  disabled: !!subnet && subnet.used_ips > 0,
                  hint: subnet && subnet.used_ips > 0 ? "The subnet still has resources" : undefined,
                  onSelect: () => setDeleteOpen(true),
                },
              ]}
            />
            <Button size="sm" onClick={() => setCreateOpen(true)}>
              <Plus /> Create subnet
            </Button>
          </>
        }
        empty={
          <EmptyState
            icon={Split}
            title={vpcId ? "No subnets in this VPC" : "No subnets"}
            description="Create a subnet to launch instances into a VPC."
            action={
              <Button size="sm" onClick={() => setCreateOpen(true)}>
                <Plus /> Create subnet
              </Button>
            }
          />
        }
      />
      <CreateSubnetDialog open={createOpen} onOpenChange={setCreateOpen} vpcs={vpcs.data} defaultVpcId={vpcId || undefined} />
      {subnet && (
        <ConfirmDialog
          open={deleteOpen}
          onOpenChange={setDeleteOpen}
          title={`Delete subnet ${subnet.name || subnet.id}?`}
          description={
            <>
              Subnet <span className="font-mono">{subnet.id}</span> ({subnet.cidr}) will be deleted. Subnets with running resources cannot be deleted.
              {subnet.default && " This is a default subnet of the default VPC."}
            </>
          }
          confirmText={subnet.default ? subnet.id : undefined}
          actionLabel="Delete subnet"
          onConfirm={onDelete}
        />
      )}
    </div>
  )
}
