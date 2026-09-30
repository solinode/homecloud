"use client"

import { useState } from "react"
import Link from "next/link"
import { MapPin, Plus } from "lucide-react"
import { toast } from "sonner"

import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select"
import { ActionsMenu } from "@/components/console/actions-menu"
import { ConfirmDialog } from "@/components/console/confirm-dialog"
import { DataTable, type Column } from "@/components/console/data-table"
import { EmptyState } from "@/components/console/empty-state"
import { Field } from "@/components/console/form-field"
import { FormDialog } from "@/components/console/form-dialog"
import { PageHeader } from "@/components/console/page-header"
import { api, seg } from "@/lib/api"
import { revalidate, useApi } from "@/lib/hooks"
import type { ElasticIP, Instance } from "@/lib/types"

import { INSTANCES_PATH, instanceHref, instanceLabel } from "./instance-actions"

export const ADDRESSES_PATH = "/api/v1/ec2/addresses"

export function ElasticIPsList() {
  const { data, error, isLoading, isValidating, mutate } = useApi<ElasticIP[]>(ADDRESSES_PATH)
  const instances = useApi<Instance[]>(INSTANCES_PATH)
  const [selected, setSelected] = useState<string[]>([])
  const [dialog, setDialog] = useState<"allocate" | "associate" | "disassociate" | "release" | null>(null)
  const [name, setName] = useState("")
  const [instance, setInstance] = useState("")
  const sel = data?.find((a) => a.allocation_id === selected[0])
  const open = (d: typeof dialog) => {
    setName("")
    setInstance("")
    setDialog(d)
  }
  const done = async () => {
    await revalidate(ADDRESSES_PATH)
    await revalidate(INSTANCES_PATH)
  }
  const live = (instances.data ?? []).filter((i) => i.state !== "terminated" && i.state !== "shutting-down")

  const columns: Column<ElasticIP>[] = [
    { id: "name", header: "Name", cell: (a) => a.tags?.Name || <span className="text-muted-foreground">-</span>, value: (a) => a.tags?.Name },
    { id: "ip", header: "Allocated IPv4 address", cell: (a) => <span className="font-mono text-[13px] font-medium">{a.public_ip}</span>, value: (a) => a.public_ip },
    { id: "id", header: "Allocation ID", cell: (a) => <span className="font-mono text-[13px]">{a.allocation_id}</span>, value: (a) => a.allocation_id },
    {
      id: "instance",
      header: "Associated instance",
      cell: (a) =>
        a.instance_id ? (
          <Link href={instanceHref(a.instance_id)} onClick={(e) => e.stopPropagation()} className="text-primary font-mono text-[13px] hover:underline">
            {a.instance_id}
          </Link>
        ) : (
          <span className="text-muted-foreground">-</span>
        ),
      value: (a) => a.instance_id,
    },
    { id: "private", header: "Private IP", cell: (a) => a.private_ip || <span className="text-muted-foreground">-</span>, value: (a) => a.private_ip },
    { id: "assoc", header: "Association ID", cell: (a) => (a.association_id ? <span className="font-mono text-[13px]">{a.association_id}</span> : <span className="text-muted-foreground">-</span>), value: (a) => a.association_id },
  ]

  return (
    <div className="flex flex-col gap-4">
      <PageHeader
        title="Elastic IP addresses"
        description="A stable public address that stays with your account and follows an instance across stop and start. On HomeCloud it is a reserved address record; traffic still reaches an instance through the ports its security groups publish on the host."
        breadcrumbs={[{ label: "EC2", href: "/ec2/" }, { label: "Elastic IPs" }]}
      />
      <DataTable
        title="Elastic IP addresses"
        data={data}
        columns={columns}
        rowId={(a) => a.allocation_id}
        loading={isLoading}
        error={error}
        onRefresh={() => mutate()}
        refreshing={isValidating}
        selection="single"
        selected={selected}
        onSelectedChange={setSelected}
        searchPlaceholder="Find address by IP, ID or instance"
        defaultSort={{ id: "ip" }}
        actions={
          <>
            <ActionsMenu
              disabled={!sel}
              items={[
                { label: "Associate address", onSelect: () => open("associate") },
                { label: "Disassociate address", onSelect: () => open("disassociate"), disabled: !sel?.association_id, hint: "Not associated" },
                { label: "Release address", destructive: true, onSelect: () => open("release"), disabled: !!sel?.association_id, hint: "Disassociate it first" },
              ]}
            />
            <Button size="sm" onClick={() => open("allocate")}>
              <Plus /> Allocate Elastic IP address
            </Button>
          </>
        }
        empty={<EmptyState icon={MapPin} title="No Elastic IP addresses" description="Allocate one and associate it with an instance." />}
      />
      {dialog === "allocate" && (
        <FormDialog
          open
          onOpenChange={(o) => !o && setDialog(null)}
          title="Allocate Elastic IP address"
          submitLabel="Allocate"
          onSubmit={async () => {
            const a = await api.post<ElasticIP>(ADDRESSES_PATH, { tags: name.trim() ? { Name: name.trim() } : undefined })
            toast.success(`Allocated ${a.public_ip}`)
            await done()
          }}
        >
          <Field label="Name tag" htmlFor="eip-name" optional>
            <Input id="eip-name" value={name} onChange={(e) => setName(e.target.value)} autoFocus />
          </Field>
        </FormDialog>
      )}
      {dialog === "associate" && sel && (
        <FormDialog
          open
          onOpenChange={(o) => !o && setDialog(null)}
          title={`Associate ${sel.public_ip}`}
          submitLabel="Associate"
          disabled={!instance}
          onSubmit={async () => {
            await api.post(`${ADDRESSES_PATH}/${seg(sel.allocation_id)}/associate`, { instance_id: instance, reassociate: true })
            toast.success(`Associated ${sel.public_ip} with ${instance}`)
            await done()
          }}
        >
          <Field label="Instance" htmlFor="eip-instance" help={sel.instance_id ? `Currently associated with ${sel.instance_id}; it will be moved.` : undefined}>
            <Select value={instance} onValueChange={setInstance}>
              <SelectTrigger id="eip-instance" className="w-full">
                <SelectValue placeholder={instances.data ? "Select an instance" : "Loading instances..."} />
              </SelectTrigger>
              <SelectContent>
                {live.map((i) => (
                  <SelectItem key={i.id} value={i.id}>
                    <span className="font-mono text-[13px]">{i.id}</span>
                    {i.name ? <span className="text-muted-foreground"> | {instanceLabel(i)}</span> : null}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </Field>
        </FormDialog>
      )}
      {sel && (
        <ConfirmDialog
          open={dialog === "disassociate"}
          onOpenChange={(o) => !o && setDialog(null)}
          title={`Disassociate ${sel.public_ip} from ${sel.instance_id}?`}
          description="The instance loses this public IP address; the allocation stays with your account."
          actionLabel="Disassociate"
          onConfirm={async () => {
            await api.post(`${ADDRESSES_PATH}/${seg(sel.allocation_id)}/disassociate`)
            toast.success(`Disassociated ${sel.public_ip}`)
            await done()
          }}
        />
      )}
      {sel && (
        <ConfirmDialog
          open={dialog === "release"}
          onOpenChange={(o) => !o && setDialog(null)}
          title={`Release ${sel.public_ip}?`}
          description="The address returns to the pool and may be handed out again."
          actionLabel="Release"
          onConfirm={async () => {
            await api.del(`${ADDRESSES_PATH}/${seg(sel.allocation_id)}`)
            toast.success(`Released ${sel.public_ip}`)
            setSelected([])
            await done()
          }}
        />
      )}
    </div>
  )
}
