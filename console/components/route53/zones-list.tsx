"use client"

import { useEffect, useState } from "react"
import Link from "next/link"
import { useRouter } from "next/navigation"
import { Globe, Loader2, Lock, Plus } from "lucide-react"
import { toast } from "sonner"

import { Button } from "@/components/ui/button"
import { Checkbox } from "@/components/ui/checkbox"
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog"
import { Input } from "@/components/ui/input"
import { Skeleton } from "@/components/ui/skeleton"
import { Textarea } from "@/components/ui/textarea"
import { ActionsMenu, type ActionItem } from "@/components/console/actions-menu"
import { CopyButton } from "@/components/console/copy-button"
import { CellLink, CellText, DataTable, type Column } from "@/components/console/data-table"
import { EmptyState } from "@/components/console/empty-state"
import { Field } from "@/components/console/form-field"
import { OptionCard, OptionGroup } from "@/components/console/option-card"
import { PageHeader } from "@/components/console/page-header"
import { TimeAgo } from "@/components/console/time-ago"
import { api, errorMessage } from "@/lib/api"
import { revalidate, useApi, useQueryParam } from "@/lib/hooks"
import type { CreateHostedZoneInput, HostedZone, HostedZoneSummary, Vpc } from "@/lib/types"
import { cn } from "@/lib/utils"

import { DeleteZoneDialog } from "./delete-zone-dialog"
import { R53_PATH, ZONES_PATH, ZoneTypeBadge, useZones, zoneHref, zoneLabel, zoneNameError } from "./shared"

function VpcList({ ids, vpcs }: { ids?: string[] | null; vpcs?: Vpc[] }) {
  if (!ids?.length) return <span className="text-muted-foreground whitespace-nowrap">All VPCs</span>
  return (
    <span className="flex flex-wrap gap-x-2">
      {ids.map((id) => (
        <Link key={id} href={`/vpc/?id=${encodeURIComponent(id)}`} onClick={(e) => e.stopPropagation()} className="text-primary font-mono text-[13px] whitespace-nowrap hover:underline">
          {vpcs?.find((v) => v.id === id)?.name || id}
        </Link>
      ))}
    </span>
  )
}

export function ZonesList() {
  const router = useRouter()
  const { data, error, isLoading, isValidating, mutate } = useZones()
  const vpcs = useApi<Vpc[]>("/api/v1/vpc/vpcs")
  const [selected, setSelected] = useState<string[]>([])
  const [creating, setCreating] = useState(useQueryParam("create") === "1")
  const [deleting, setDeleting] = useState<HostedZoneSummary | null>(null)

  const sel = (data ?? []).find((z) => selected.includes(z.id)) ?? null

  const columns: Column<HostedZoneSummary>[] = [
    {
      id: "name",
      header: "Hosted zone name",
      cell: (z) => (
        <CellLink href={zoneHref(z.id)} mono>
          {zoneLabel(z.name)}
        </CellLink>
      ),
      value: (z) => z.name,
    },
    { id: "type", header: "Type", cell: (z) => <ZoneTypeBadge isPrivate={z.private} />, value: (z) => (z.private ? "Private" : "Public") },
    { id: "records", header: "Record count", cell: (z) => <span className="tabular-nums">{z.record_count}</span>, value: (z) => z.record_count },
    {
      id: "vpcs",
      header: "VPCs",
      cell: (z) => (z.private ? <VpcList ids={z.vpc_ids} vpcs={vpcs.data} /> : <span className="text-muted-foreground">-</span>),
      value: (z) => (z.vpc_ids ?? []).join(" "),
      hideBelow: "md",
    },
    {
      id: "id",
      header: "Hosted zone ID",
      cell: (z) => (
        <span className="inline-flex items-center gap-1 font-mono text-[13px] whitespace-nowrap">
          {z.id}
          <CopyButton value={z.id} label="Copy zone ID" />
        </span>
      ),
      value: (z) => z.id,
      hideBelow: "lg",
    },
    { id: "comment", header: "Description", cell: (z) => (
        <CellText muted max="16rem">
          {z.comment}
        </CellText>
      ), value: (z) => z.comment, hideBelow: "lg" },
    { id: "created", header: "Created", cell: (z) => <TimeAgo value={z.created_at} />, value: (z) => z.created_at, hideBelow: "sm" },
  ]

  const items: ActionItem[] = [
    { label: "View details", onSelect: () => sel && router.push(zoneHref(sel.id)), disabled: !sel },
    { separator: true },
    { label: "Delete hosted zone", destructive: true, onSelect: () => sel && setDeleting(sel), disabled: !sel },
  ]

  return (
    <div className="flex flex-col gap-4">
      <PageHeader
        title="Hosted zones"
        description="DNS zones served by HomeCloud's managed DNS server. Private zones answer only inside their VPCs; public zones also answer clients on your LAN."
        breadcrumbs={[{ label: "Route 53", href: "/route53/" }, { label: "Hosted zones" }]}
      />
      <DataTable
        title="Hosted zones"
        data={data}
        columns={columns}
        rowId={(z) => z.id}
        loading={isLoading}
        error={error}
        onRefresh={() => mutate()}
        refreshing={isValidating}
        selection="single"
        selected={selected}
        onSelectedChange={setSelected}
        searchPlaceholder="Filter by name, ID or description"
        defaultSort={{ id: "name" }}
        actions={
          <>
            <ActionsMenu items={items} disabled={!sel} />
            <Button size="sm" onClick={() => setCreating(true)}>
              <Plus /> Create hosted zone
            </Button>
          </>
        }
        empty={
          <EmptyState
            icon={Globe}
            title="No hosted zones"
            description="Create a zone such as corp.internal to give instances, databases and load balancers friendly DNS names."
            action={
              <Button size="sm" onClick={() => setCreating(true)}>
                <Plus /> Create hosted zone
              </Button>
            }
          />
        }
      />
      <CreateZoneDialog open={creating} onOpenChange={setCreating} vpcs={vpcs.data} />
      <DeleteZoneDialog zone={deleting ? { id: deleting.id, name: deleting.name, records: deleting.record_count } : null} onClose={() => setDeleting(null)} />
    </div>
  )
}

function CreateZoneDialog({ open, onOpenChange, vpcs }: { open: boolean; onOpenChange: (o: boolean) => void; vpcs?: Vpc[] }) {
  const router = useRouter()
  const [name, setName] = useState("")
  const [isPrivate, setPrivate] = useState(false)
  const [vpcIds, setVpcIds] = useState<string[]>([])
  const [comment, setComment] = useState("")
  const [touched, setTouched] = useState(false)
  const [pending, setPending] = useState(false)

  useEffect(() => {
    if (!open) return
    setName("")
    setPrivate(false)
    setVpcIds([])
    setComment("")
    setTouched(false)
  }, [open])

  const nameErr = zoneNameError(name)

  const submit = async (e: React.FormEvent) => {
    e.preventDefault()
    setTouched(true)
    if (nameErr) return
    const body: CreateHostedZoneInput = { name: name.trim(), private: isPrivate, vpc_ids: isPrivate && vpcIds.length ? vpcIds : undefined, comment: comment.trim() || undefined }
    setPending(true)
    try {
      const z = await api.post<HostedZone>(ZONES_PATH, body)
      toast.success(`Created hosted zone ${zoneLabel(z.name)}`)
      await revalidate(R53_PATH)
      onOpenChange(false)
      router.push(zoneHref(z.id))
    } catch (err) {
      toast.error(errorMessage(err))
    } finally {
      setPending(false)
    }
  }

  const TYPES = [
    { value: false, label: "Public hosted zone", icon: Globe, blurb: "Answers inside every VPC and on the LAN DNS port of this host." },
    { value: true, label: "Private hosted zone", icon: Lock, blurb: "Answers only for clients inside the chosen VPCs." },
  ]

  return (
    <Dialog open={open} onOpenChange={(o) => !pending && onOpenChange(o)}>
      <DialogContent className="max-h-[90vh] overflow-y-auto sm:max-w-xl">
        <form onSubmit={submit} className="flex flex-col gap-5">
          <DialogHeader>
            <DialogTitle>Create hosted zone</DialogTitle>
            <DialogDescription>A hosted zone holds the DNS records of one domain and its subdomains.</DialogDescription>
          </DialogHeader>
          <Field label="Domain name" htmlFor="zone-name" error={touched || name ? nameErr : undefined} help="e.g. corp.internal or home.arpa">
            <Input id="zone-name" autoFocus value={name} onChange={(e) => setName(e.target.value)} placeholder="corp.internal" className="font-mono" spellCheck={false} autoComplete="off" />
          </Field>
          <Field label="Type">
            <OptionGroup label="Zone type">
              {TYPES.map((t) => (
                <OptionCard key={t.label} selected={isPrivate === t.value} onSelect={() => setPrivate(t.value)} icon={t.icon} title={t.label} description={t.blurb} />
              ))}
            </OptionGroup>
          </Field>
          {isPrivate && (
            <Field label="VPCs to associate" optional help="Leave all unchecked to answer in every VPC.">
              {!vpcs ? (
                <Skeleton className="h-20 w-full rounded-lg" />
              ) : (
                <div className="divide-y rounded-lg border">
                  {vpcs.map((v) => (
                    <label key={v.id} className={cn("flex cursor-pointer items-center gap-3 px-3 py-2 text-sm", vpcIds.includes(v.id) ? "bg-brand-soft" : "hover:bg-muted/50")}>
                      <Checkbox checked={vpcIds.includes(v.id)} onCheckedChange={(c) => setVpcIds(c ? [...vpcIds, v.id] : vpcIds.filter((x) => x !== v.id))} />
                      <span className="truncate font-medium whitespace-nowrap">{v.name || v.id}</span>
                      <span className="text-muted-foreground font-mono text-xs">
                        {v.id} · {v.cidr}
                      </span>
                    </label>
                  ))}
                </div>
              )}
            </Field>
          )}
          <Field label="Description" htmlFor="zone-comment" optional>
            <Textarea id="zone-comment" rows={2} value={comment} onChange={(e) => setComment(e.target.value)} placeholder="Internal names for the lab" />
          </Field>
          <DialogFooter>
            <Button type="button" variant="outline" onClick={() => onOpenChange(false)} disabled={pending}>
              Cancel
            </Button>
            <Button type="submit" disabled={pending || (touched && !!nameErr)}>
              {pending && <Loader2 className="animate-spin" />}
              Create hosted zone
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}
