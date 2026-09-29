"use client"

import { useState } from "react"
import { Globe, Plus } from "lucide-react"
import { toast } from "sonner"

import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { ActionsMenu } from "@/components/console/actions-menu"
import { ConfirmDialog } from "@/components/console/confirm-dialog"
import { DataTable, type Column } from "@/components/console/data-table"
import { EmptyState } from "@/components/console/empty-state"
import { Field } from "@/components/console/form-field"
import { FormDialog } from "@/components/console/form-dialog"
import { PageHeader } from "@/components/console/page-header"
import { StatusBadge } from "@/components/console/status-badge"
import { api, seg } from "@/lib/api"
import { revalidate, useApi } from "@/lib/hooks"
import type { InternetGateway } from "@/lib/types"

import { useVpcs, VpcLink, VpcSelect } from "./common"

export const IGW_PATH = "/api/v1/ec2/internet-gateways"

export function InternetGateways() {
  const { data, error, isLoading, isValidating, mutate } = useApi<InternetGateway[]>(IGW_PATH)
  const vpcs = useVpcs()
  const [selected, setSelected] = useState<string[]>([])
  const [dialog, setDialog] = useState<"create" | "attach" | "detach" | "delete" | null>(null)
  const [name, setName] = useState("")
  const [vpc, setVpc] = useState("")
  const sel = data?.find((g) => g.id === selected[0])
  const open = (d: typeof dialog) => {
    setName("")
    setVpc("")
    setDialog(d)
  }
  const done = async () => {
    await revalidate("/api/v1/ec2/")
    await revalidate("/api/v1/vpc")
  }

  const columns: Column<InternetGateway>[] = [
    { id: "name", header: "Name", cell: (g) => g.tags?.Name || <span className="text-muted-foreground">-</span>, value: (g) => g.tags?.Name },
    { id: "id", header: "Internet gateway ID", cell: (g) => <span className="font-mono text-[13px] font-medium">{g.id}</span>, value: (g) => g.id },
    { id: "state", header: "State", cell: (g) => <StatusBadge status={g.vpc_id ? "attached" : "detached"} />, value: (g) => (g.vpc_id ? "attached" : "detached") },
    {
      id: "vpc",
      header: "VPC ID",
      cell: (g) => (g.vpc_id ? <span onClick={(e) => e.stopPropagation()}><VpcLink id={g.vpc_id} vpcs={vpcs.data} /></span> : <span className="text-muted-foreground">-</span>),
      value: (g) => g.vpc_id,
    },
  ]

  return (
    <div className="flex flex-col gap-4">
      <PageHeader
        title="Internet gateways"
        description="A VPC reaches the internet when it has an attached internet gateway and a route table sending 0.0.0.0/0 to it."
        breadcrumbs={[{ label: "VPC", href: "/vpc/" }, { label: "Internet gateways" }]}
      />
      <DataTable
        title="Internet gateways"
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
        searchPlaceholder="Find internet gateway"
        defaultSort={{ id: "id" }}
        actions={
          <>
            <ActionsMenu
              disabled={!sel}
              items={[
                { label: "Attach to VPC", onSelect: () => open("attach"), disabled: !!sel?.vpc_id, hint: "Already attached" },
                { label: "Detach from VPC", onSelect: () => open("detach"), disabled: !sel?.vpc_id, hint: "Not attached" },
                { label: "Delete internet gateway", destructive: true, onSelect: () => open("delete"), disabled: !!sel?.vpc_id, hint: "Detach it first" },
              ]}
            />
            <Button size="sm" onClick={() => open("create")}>
              <Plus /> Create internet gateway
            </Button>
          </>
        }
        empty={<EmptyState icon={Globe} title="No internet gateways" description="Create one and attach it to a VPC." />}
      />
      {dialog === "create" && (
        <FormDialog
          open
          onOpenChange={(o) => !o && setDialog(null)}
          title="Create internet gateway"
          submitLabel="Create internet gateway"
          onSubmit={async () => {
            const g = await api.post<InternetGateway>(IGW_PATH, { tags: name.trim() ? { Name: name.trim() } : undefined })
            toast.success(`Created ${g.id}`)
            await done()
          }}
        >
          <Field label="Name tag" htmlFor="igw-name" optional>
            <Input id="igw-name" value={name} onChange={(e) => setName(e.target.value)} autoFocus />
          </Field>
        </FormDialog>
      )}
      {dialog === "attach" && sel && (
        <FormDialog
          open
          onOpenChange={(o) => !o && setDialog(null)}
          title={`Attach ${sel.id}`}
          submitLabel="Attach"
          disabled={!vpc}
          onSubmit={async () => {
            await api.post(`${IGW_PATH}/${seg(sel.id)}/attach`, { vpc_id: vpc })
            toast.success(`Attached ${sel.id} to ${vpc}`)
            await done()
          }}
        >
          <Field label="VPC" htmlFor="igw-vpc">
            <VpcSelect id="igw-vpc" value={vpc} vpcs={vpcs.data} onChange={setVpc} />
          </Field>
        </FormDialog>
      )}
      {sel && (
        <ConfirmDialog
          open={dialog === "detach"}
          onOpenChange={(o) => !o && setDialog(null)}
          title={`Detach ${sel.id} from ${sel.vpc_id}?`}
          description="The VPC loses internet access if this is its only gateway."
          actionLabel="Detach"
          onConfirm={async () => {
            await api.post(`${IGW_PATH}/${seg(sel.id)}/detach`, { vpc_id: sel.vpc_id })
            toast.success(`Detached ${sel.id}`)
            await done()
          }}
        />
      )}
      {sel && (
        <ConfirmDialog
          open={dialog === "delete"}
          onOpenChange={(o) => !o && setDialog(null)}
          title={`Delete ${sel.id}?`}
          onConfirm={async () => {
            await api.del(`${IGW_PATH}/${seg(sel.id)}`)
            toast.success(`Deleted ${sel.id}`)
            setSelected([])
            await done()
          }}
        />
      )}
    </div>
  )
}
