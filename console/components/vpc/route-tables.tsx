"use client"

import { useState } from "react"
import { Plus, Route as RouteIcon, Trash2 } from "lucide-react"
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
import { Section } from "@/components/console/section"
import { api, seg } from "@/lib/api"
import { revalidate, useApi } from "@/lib/hooks"
import type { InternetGateway, RouteTable, Subnet } from "@/lib/types"

import { useVpcs, VpcLink, VpcSelect } from "./common"
import { IGW_PATH } from "./internet-gateways"

const RTB_PATH = "/api/v1/ec2/route-tables"

export function RouteTables() {
  const { data, error, isLoading, isValidating, mutate } = useApi<RouteTable[]>(RTB_PATH)
  const vpcs = useVpcs()
  const igws = useApi<InternetGateway[]>(IGW_PATH)
  const subnets = useApi<Subnet[]>("/api/v1/vpc/subnets")
  const [selected, setSelected] = useState<string[]>([])
  const [dialog, setDialog] = useState<"create" | "delete" | "route" | "assoc" | null>(null)
  const [vpc, setVpc] = useState("")
  const [name, setName] = useState("")
  const [dest, setDest] = useState("0.0.0.0/0")
  const [target, setTarget] = useState("")
  const [subnet, setSubnet] = useState("")
  const [removeRoute, setRemoveRoute] = useState<string | null>(null)
  const sel = data?.find((t) => t.id === selected[0])
  const open = (d: typeof dialog) => {
    setName("")
    setVpc("")
    setDest("0.0.0.0/0")
    setTarget("")
    setSubnet("")
    setDialog(d)
  }
  const done = async () => {
    await revalidate("/api/v1/ec2/")
    await revalidate("/api/v1/vpc")
  }
  const base = sel ? `${RTB_PATH}/${seg(sel.id)}` : ""
  const vpcIgws = (igws.data ?? []).filter((g) => g.vpc_id === sel?.vpc_id)
  const freeSubnets = (subnets.data ?? []).filter((s) => s.vpc_id === sel?.vpc_id && !sel?.associations?.some((a) => a.subnet_id === s.id))

  const columns: Column<RouteTable>[] = [
    { id: "name", header: "Name", cell: (t) => t.tags?.Name || <span className="text-muted-foreground">-</span>, value: (t) => t.tags?.Name },
    { id: "id", header: "Route table ID", cell: (t) => <span className="font-mono text-[13px] font-medium">{t.id}</span>, value: (t) => t.id },
    { id: "vpc", header: "VPC", cell: (t) => <span onClick={(e) => e.stopPropagation()}><VpcLink id={t.vpc_id} vpcs={vpcs.data} /></span>, value: (t) => t.vpc_id },
    { id: "routes", header: "Routes", cell: (t) => (t.routes ?? []).length + 1, value: (t) => (t.routes ?? []).length + 1, hideBelow: "sm" },
    { id: "assoc", header: "Subnet associations", cell: (t) => (t.associations ?? []).filter((a) => a.subnet_id).length, value: (t) => (t.associations ?? []).length, hideBelow: "sm" },
    { id: "main", header: "Main", cell: (t) => ((t.associations ?? []).some((a) => a.main) ? "Yes" : "No"), value: (t) => ((t.associations ?? []).some((a) => a.main) ? 1 : 0), hideBelow: "md" },
  ]

  return (
    <div className="flex flex-col gap-4">
      <PageHeader
        title="Route tables"
        description="A route table decides where a subnet's traffic goes. Subnets without an association use the VPC's main route table."
        breadcrumbs={[{ label: "VPC", href: "/vpc/" }, { label: "Route tables" }]}
      />
      <DataTable
        title="Route tables"
        data={data}
        columns={columns}
        rowId={(t) => t.id}
        loading={isLoading}
        error={error}
        onRefresh={() => mutate()}
        refreshing={isValidating}
        selection="single"
        selected={selected}
        onSelectedChange={setSelected}
        searchPlaceholder="Find route table"
        defaultSort={{ id: "id" }}
        actions={
          <>
            <ActionsMenu
              disabled={!sel}
              items={[
                { label: "Add route", onSelect: () => open("route") },
                { label: "Associate subnet", onSelect: () => open("assoc") },
                { label: "Delete route table", destructive: true, onSelect: () => open("delete"), disabled: !!sel?.associations?.some((a) => a.main), hint: "The main route table cannot be deleted" },
              ]}
            />
            <Button size="sm" onClick={() => open("create")}>
              <Plus /> Create route table
            </Button>
          </>
        }
        empty={<EmptyState icon={RouteIcon} title="No route tables" description="Create a route table for a VPC." />}
      />
      {sel && (
        <div className="grid gap-4 lg:grid-cols-2">
          <Section title={`Routes of ${sel.id}`} flush>
            <table className="w-full text-sm">
              <thead className="text-muted-foreground border-b text-left">
                <tr>
                  <th className="px-4 py-2 font-medium">Destination</th>
                  <th className="px-4 py-2 font-medium">Target</th>
                  <th className="px-4 py-2 font-medium">Origin</th>
                  <th />
                </tr>
              </thead>
              <tbody>
                <tr className="border-b">
                  <td className="px-4 py-2 font-mono text-[13px]">{vpcs.data?.find((v) => v.id === sel.vpc_id)?.cidr ?? "VPC range"}</td>
                  <td className="px-4 py-2">local</td>
                  <td className="text-muted-foreground px-4 py-2">CreateRouteTable</td>
                  <td />
                </tr>
                {(sel.routes ?? []).map((r) => (
                  <tr key={r.destination} className="border-b last:border-0">
                    <td className="px-4 py-2 font-mono text-[13px]">{r.destination}</td>
                    <td className="px-4 py-2 font-mono text-[13px]">{r.gateway_id || r.target}</td>
                    <td className="text-muted-foreground px-4 py-2">{r.origin}</td>
                    <td className="px-2 py-1 text-right">
                      <Button variant="ghost" size="icon" aria-label={`Remove route ${r.destination}`} onClick={() => setRemoveRoute(r.destination)}>
                        <Trash2 />
                      </Button>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </Section>
          <Section title="Subnet associations" flush>
            {(sel.associations ?? []).length === 0 ? (
              <p className="text-muted-foreground p-4 text-sm">No associations.</p>
            ) : (
              <ul>
                {(sel.associations ?? []).map((a) => (
                  <li key={a.id} className="flex items-center justify-between border-b px-4 py-2 text-sm last:border-0">
                    <span>
                      <span className="font-mono text-[13px]">{a.subnet_id ?? "Main (all subnets without an association)"}</span>
                      <span className="text-muted-foreground ml-2 font-mono text-xs">{a.id}</span>
                    </span>
                    {!a.main && (
                      <Button
                        variant="ghost"
                        size="sm"
                        onClick={async () => {
                          try {
                            await api.del(`${base}/associations/${seg(a.id)}`)
                            toast.success("Subnet disassociated")
                            await done()
                          } catch (err) {
                            toast.error(err instanceof Error ? err.message : String(err))
                          }
                        }}
                      >
                        Disassociate
                      </Button>
                    )}
                  </li>
                ))}
              </ul>
            )}
          </Section>
        </div>
      )}
      {dialog === "create" && (
        <FormDialog
          open
          onOpenChange={(o) => !o && setDialog(null)}
          title="Create route table"
          submitLabel="Create route table"
          disabled={!vpc}
          onSubmit={async () => {
            const t = await api.post<RouteTable>(RTB_PATH, { vpc_id: vpc, tags: name.trim() ? { Name: name.trim() } : undefined })
            toast.success(`Created ${t.id}`)
            await done()
          }}
        >
          <Field label="Name tag" htmlFor="rtb-name" optional>
            <Input id="rtb-name" value={name} onChange={(e) => setName(e.target.value)} autoFocus />
          </Field>
          <Field label="VPC" htmlFor="rtb-vpc">
            <VpcSelect id="rtb-vpc" value={vpc} vpcs={vpcs.data} onChange={setVpc} />
          </Field>
        </FormDialog>
      )}
      {dialog === "route" && sel && (
        <FormDialog
          open
          onOpenChange={(o) => !o && setDialog(null)}
          title={`Add route to ${sel.id}`}
          submitLabel="Add route"
          disabled={!dest.trim() || !target}
          onSubmit={async () => {
            await api.post(`${base}/routes`, { destination: dest.trim(), gateway_id: target })
            toast.success(`Added route ${dest.trim()}`)
            await done()
          }}
        >
          <Field label="Destination CIDR" htmlFor="rt-dest">
            <Input id="rt-dest" value={dest} onChange={(e) => setDest(e.target.value)} className="font-mono" autoFocus />
          </Field>
          <Field label="Target internet gateway" htmlFor="rt-target" help="Only internet gateways attached to this table's VPC can be targets.">
            <Select value={target} onValueChange={setTarget}>
              <SelectTrigger id="rt-target" className="w-full">
                <SelectValue placeholder="Select an internet gateway" />
              </SelectTrigger>
              <SelectContent>
                {vpcIgws.map((g) => (
                  <SelectItem key={g.id} value={g.id}>
                    {g.id} {g.tags?.Name ? `(${g.tags.Name})` : ""}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </Field>
        </FormDialog>
      )}
      {dialog === "assoc" && sel && (
        <FormDialog
          open
          onOpenChange={(o) => !o && setDialog(null)}
          title={`Associate a subnet with ${sel.id}`}
          submitLabel="Associate"
          disabled={!subnet}
          onSubmit={async () => {
            await api.post(`${base}/associations`, { subnet_id: subnet })
            toast.success(`Associated ${subnet}`)
            await done()
          }}
        >
          <Field label="Subnet" htmlFor="rt-subnet">
            <Select value={subnet} onValueChange={setSubnet}>
              <SelectTrigger id="rt-subnet" className="w-full">
                <SelectValue placeholder="Select a subnet" />
              </SelectTrigger>
              <SelectContent>
                {freeSubnets.map((s) => (
                  <SelectItem key={s.id} value={s.id}>
                    {s.id} {s.name ? `| ${s.name}` : ""} ({s.cidr})
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </Field>
        </FormDialog>
      )}
      {sel && (
        <ConfirmDialog
          open={dialog === "delete"}
          onOpenChange={(o) => !o && setDialog(null)}
          title={`Delete ${sel.id}?`}
          onConfirm={async () => {
            await api.del(base)
            toast.success(`Deleted ${sel.id}`)
            setSelected([])
            await done()
          }}
        />
      )}
      {sel && (
        <ConfirmDialog
          open={!!removeRoute}
          onOpenChange={(o) => !o && setRemoveRoute(null)}
          title={`Remove route ${removeRoute}?`}
          description="Traffic for this destination falls back to the remaining routes."
          actionLabel="Remove"
          onConfirm={async () => {
            await api.del(`${base}/routes`, { destination: removeRoute })
            toast.success("Route removed")
            await done()
          }}
        />
      )}
    </div>
  )
}
