"use client"

import { useEffect, useState } from "react"
import Link from "next/link"
import { AlertCircle, AlertTriangle, ArrowLeft, Download, FileText, Info, Loader2, Pencil, Play, Plus, RefreshCw, Trash2 } from "lucide-react"
import { toast } from "sonner"

import { Alert, AlertDescription } from "@/components/ui/alert"
import { Button } from "@/components/ui/button"
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog"
import { Input } from "@/components/ui/input"
import { Skeleton } from "@/components/ui/skeleton"
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select"
import { ActionsMenu } from "@/components/console/actions-menu"
import { CodeBlock } from "@/components/console/code-block"
import { ConfirmDialog } from "@/components/console/confirm-dialog"
import { CopyableText } from "@/components/console/copy-button"
import { CellText, DataTable, type Column } from "@/components/console/data-table"
import { EmptyState } from "@/components/console/empty-state"
import { ErrorState } from "@/components/console/error-state"
import { Field } from "@/components/console/form-field"
import { KeyValueGrid } from "@/components/console/key-value"
import { DetailSkeleton } from "@/components/console/loading"
import { PageHeader } from "@/components/console/page-header"
import { Section } from "@/components/console/section"
import { Tag, recordTypeAccent } from "@/components/console/tag"
import { TimeAgo } from "@/components/console/time-ago"
import { ApiError, api, errorMessage, seg } from "@/lib/api"
import { formatDate } from "@/lib/format"
import { revalidate, useApi, useQueryParam } from "@/lib/hooks"
import type { DnsRecord, DnsTestResult, HostedZone, HostedZoneDetail, Vpc } from "@/lib/types"
import { cn } from "@/lib/utils"

import { DeleteZoneDialog } from "./delete-zone-dialog"
import { RecordDialog } from "./record-dialog"
import { AliasTargetLink, R53_PATH, ZONES_PATH, ZoneTypeBadge, fqdn, useAliasTargets, zoneLabel, type AliasTarget } from "./shared"

interface Row {
  key: string
  record: DnsRecord
  /** generated SOA/NS rows cannot be edited */
  generated?: boolean
}

function BackButton() {
  return (
    <Button variant="outline" size="sm" asChild>
      <Link href="/route53/">
        <ArrowLeft /> Back to hosted zones
      </Link>
    </Button>
  )
}

export function ZoneDetail() {
  const id = useQueryParam("id")
  const { data, error, isLoading, isValidating, mutate } = useApi<HostedZoneDetail>(id ? `${ZONES_PATH}/${seg(id)}` : null, { refreshInterval: 30_000 })
  const vpcs = useApi<Vpc[]>("/api/v1/vpc/vpcs")
  const { targets, loading: targetsLoading } = useAliasTargets(!!data)
  const [editing, setEditing] = useState<DnsRecord | null>(null)
  const [creating, setCreating] = useState(false)
  const [deletingRecord, setDeletingRecord] = useState<DnsRecord | null>(null)
  const [deletingZone, setDeletingZone] = useState(false)
  const [exporting, setExporting] = useState(false)

  const zone = data?.zone
  const crumbs = [{ label: "Route 53", href: "/route53/" }, { label: "Hosted zones", href: "/route53/" }, { label: zone ? zoneLabel(zone.name) : id || "Zone" }]

  if (!id) {
    return (
      <>
        <PageHeader title="Hosted zone" breadcrumbs={crumbs} />
        <EmptyState title="No hosted zone selected" description="Open a zone from the hosted zones list." action={<BackButton />} />
      </>
    )
  }
  if (error && !data) {
    const notFound = error instanceof ApiError && error.status === 404
    return (
      <div className="flex flex-col gap-4">
        <PageHeader title={id} breadcrumbs={crumbs} />
        {notFound ? (
          <Section>
            <EmptyState icon={AlertCircle} title="Hosted zone not found" description={`Hosted zone ${id} does not exist. It may have been deleted.`} action={<BackButton />} />
          </Section>
        ) : (
          <ErrorState error={error} onRetry={() => mutate()} />
        )}
      </div>
    )
  }
  if (isLoading || !data || !zone) return <DetailSkeleton />

  return (
    <div className="flex flex-col gap-4">
      <PageHeader
        title={<span className="font-mono break-all">{zoneLabel(zone.name)}</span>}
        badge={<ZoneTypeBadge isPrivate={zone.private} />}
        description={zone.comment || undefined}
        breadcrumbs={crumbs}
        actions={
          <>
            <Button variant="outline" size="sm" onClick={() => mutate()} aria-label="Refresh">
              <RefreshCw className={cn(isValidating && "animate-spin")} />
            </Button>
            <Button variant="outline" size="sm" onClick={() => setExporting(true)}>
              <FileText /> Export zone file
            </Button>
            <ActionsMenu items={[{ label: "Delete hosted zone", icon: <Trash2 />, destructive: true, onSelect: () => setDeletingZone(true) }]} />
            <Button size="sm" onClick={() => setCreating(true)}>
              <Plus /> Create record
            </Button>
          </>
        }
      />

      <Section title="Hosted zone details">
        <KeyValueGrid
          columns={3}
          items={[
            { label: "Hosted zone ID", value: <CopyableText value={zone.id} /> },
            { label: "Domain name", value: <CopyableText value={zone.name} /> },
            { label: "Type", value: <ZoneTypeBadge isPrivate={zone.private} /> },
            { label: "Record count", value: String(zone.records.length + 2) },
            { label: "SOA serial", value: <span className="font-mono text-[13px]">{zone.serial}</span> },
            { label: "Created", value: <span>{formatDate(zone.created_at)} (<TimeAgo value={zone.created_at} />)</span> },
            ...(zone.private
              ? [
                  {
                    label: "Associated VPCs",
                    wide: true,
                    value: zone.vpc_ids?.length ? (
                      <span className="flex flex-wrap gap-x-3">
                        {zone.vpc_ids.map((v) => (
                          <Link key={v} href={`/vpc/?id=${encodeURIComponent(v)}`} className="text-primary font-mono text-[13px] whitespace-nowrap hover:underline">
                            {vpcs.data?.find((x) => x.id === v)?.name || v}
                          </Link>
                        ))}
                      </span>
                    ) : (
                      "All VPCs"
                    ),
                  },
                ]
              : []),
          ]}
        />
      </Section>

      <RecordsTable
        zone={zone}
        targets={targets}
        targetsLoading={targetsLoading}
        onCreate={() => setCreating(true)}
        onEdit={setEditing}
        onDelete={setDeletingRecord}
        refreshing={isValidating}
        onRefresh={() => mutate()}
      />

      <div className="grid grid-cols-1 gap-4 xl:grid-cols-2">
        <NameServers zone={zone} nameServers={data.name_servers} vpcs={vpcs.data} />
        <DnsTest zone={zone} />
      </div>

      <RecordDialog
        zone={zone}
        record={editing}
        open={creating || !!editing}
        onClose={() => {
          setCreating(false)
          setEditing(null)
        }}
        targets={targets}
        targetsLoading={targetsLoading}
      />
      <ConfirmDialog
        open={!!deletingRecord}
        onOpenChange={(o) => !o && setDeletingRecord(null)}
        title="Delete record?"
        description={
          deletingRecord && (
            <p>
              Delete the <span className="text-foreground font-mono">{deletingRecord.type}</span> record{" "}
              <span className="text-foreground font-mono">{zoneLabel(fqdn(deletingRecord.name, zone.name))}</span>? Clients stop getting an answer once cached copies
              expire (TTL {deletingRecord.ttl}s).
            </p>
          )
        }
        onConfirm={async () => {
          if (!deletingRecord) return
          await api.post(`${ZONES_PATH}/${seg(zone.id)}/changes`, { changes: [{ action: "DELETE", record: deletingRecord }] })
          toast.success(`Deleted ${deletingRecord.type} record ${zoneLabel(fqdn(deletingRecord.name, zone.name))}`)
          await revalidate(R53_PATH)
        }}
      />
      <DeleteZoneDialog zone={deletingZone ? { id: zone.id, name: zone.name, records: zone.records.length } : null} onClose={() => setDeletingZone(false)} redirect />
      <ZoneFileDialog zone={exporting ? zone : null} onClose={() => setExporting(false)} />
    </div>
  )
}

function RecordsTable({
  zone,
  targets,
  targetsLoading,
  onCreate,
  onEdit,
  onDelete,
  refreshing,
  onRefresh,
}: {
  zone: HostedZone
  targets: AliasTarget[]
  targetsLoading: boolean
  onCreate: () => void
  onEdit: (r: DnsRecord) => void
  onDelete: (r: DnsRecord) => void
  refreshing: boolean
  onRefresh: () => void
}) {
  const ns = `ns.${zone.name}`
  const rows: Row[] = [
    { key: "@SOA", generated: true, record: { name: "@", type: "SOA", ttl: 3600, values: [`${ns} hostmaster.${zone.name} ${zone.serial} 7200 3600 1209600 60`] } },
    { key: "@NS", generated: true, record: { name: "@", type: "NS", ttl: 3600, values: [ns] } },
    ...zone.records.map((r) => ({ key: `${fqdn(r.name, zone.name)} ${r.type}`, record: r })),
  ]

  const columns: Column<Row>[] = [
    {
      id: "name",
      header: "Record name",
      cell: (r) => <CellText mono>{zoneLabel(fqdn(r.record.name, zone.name))}</CellText>,
      value: (r) => fqdn(r.record.name, zone.name),
    },
    { id: "type", header: "Type", cell: (r) => <Tag accent={recordTypeAccent(r.record.type)}>{r.record.type}</Tag>, value: (r) => r.record.type },
    {
      id: "routing",
      header: "Routing",
      cell: (r) =>
        r.record.alias ? (
          <Tag accent="brand" mono={false}>
            Alias
          </Tag>
        ) : r.generated ? (
          <Tag mono={false} className="text-faint">
            Generated
          </Tag>
        ) : (
          <Tag mono={false}>Simple</Tag>
        ),
      value: (r) => (r.record.alias ? "Alias" : "Simple"),
      hideBelow: "md",
    },
    {
      id: "value",
      header: "Value / route traffic to",
      cell: (r) =>
        r.record.alias ? (
          <AliasTargetLink alias={r.record.alias} targets={targets} loading={targetsLoading} />
        ) : (
          <span className="flex flex-col gap-0.5">
            {(r.record.values ?? []).map((v, i) => (
              <span key={i} className="font-mono text-[13px] break-all">
                {r.record.type === "TXT" ? JSON.stringify(v) : v}
              </span>
            ))}
          </span>
        ),
      value: (r) => r.record.alias ?? (r.record.values ?? []).join(" "),
    },
    { id: "ttl", header: "TTL", cell: (r) => <span className="tabular-nums">{r.record.ttl}</span>, value: (r) => r.record.ttl, hideBelow: "sm" },
    {
      id: "actions",
      header: "",
      className: "w-24 text-right",
      cell: (r) =>
        r.generated ? null : (
          <span className="flex justify-end gap-1">
            <Button variant="ghost" size="icon" className="size-8" onClick={() => onEdit(r.record)} aria-label={`Edit ${r.record.type} ${r.record.name}`}>
              <Pencil />
            </Button>
            <Button
              variant="ghost"
              size="icon"
              className="text-destructive hover:text-destructive size-8"
              onClick={() => onDelete(r.record)}
              aria-label={`Delete ${r.record.type} ${r.record.name}`}
            >
              <Trash2 />
            </Button>
          </span>
        ),
    },
  ]

  return (
    <DataTable
      title="Records"
      description="SOA and NS records are generated by HomeCloud. Alias records follow the private IP of an instance, task, database or load balancer."
      data={rows}
      count={rows.length}
      columns={columns}
      rowId={(r) => r.key}
      onRefresh={onRefresh}
      refreshing={refreshing}
      searchPlaceholder="Filter by name, type or value"
      rowClassName={(r) => (r.generated ? "bg-muted/20" : undefined)}
      actions={
        <Button size="sm" onClick={onCreate}>
          <Plus /> Create record
        </Button>
      }
    />
  )
}

function NameServers({ zone, nameServers, vpcs }: { zone: HostedZone; nameServers: string[]; vpcs?: Vpc[] }) {
  const lan = nameServers.find((n) => n.endsWith("(LAN)"))
  const lanAddr = lan?.replace(/\s*\(LAN\)$/, "")
  const [lanHost, lanPort] = lanAddr ? [lanAddr.slice(0, lanAddr.lastIndexOf(":")), lanAddr.slice(lanAddr.lastIndexOf(":") + 1)] : ["", ""]
  const sample = zone.records.find((r) => r.type === "A") ?? zone.records[0]
  const sampleName = zoneLabel(sample ? fqdn(sample.name, zone.name) : zone.name)
  return (
    <Section title="Name servers" description="Where clients send queries for this zone.">
      <div className="flex flex-col gap-4 text-sm">
        <ul className="flex flex-col gap-1">
          {nameServers.map((n) => (
            <li key={n}>
              <CopyableText value={n.replace(/\s*\(.*\)$/, "")} display={n} />
            </li>
          ))}
        </ul>
        <div className="text-muted-foreground flex gap-2 text-xs">
          <Info className="mt-px size-3.5 shrink-0" />
          <div className="flex flex-col gap-2">
            <p>
              Inside a VPC, the resolver is the VPC&apos;s <span className="text-foreground font-mono">.2</span> address
              {vpcs?.length ? (
                <>
                  {" "}
                  (for example <span className="text-foreground font-mono">{nameServers[0]?.split(" ")[0]}</span> in {vpcs[0].name || vpcs[0].id})
                </>
              ) : null}
              . New instances, ECS tasks and Lambda functions use it automatically; it also forwards other names (container names, the internet) upstream.
            </p>
            {zone.private ? (
              <p>
                This is a private zone: it answers only for clients inside {zone.vpc_ids?.length ? "the associated VPCs" : "your VPCs"}. It is not served on the LAN
                DNS port.
              </p>
            ) : lanAddr ? (
              <p>
                Public zones are also served to your LAN on{" "}
                <span className="text-foreground font-mono">
                  {lanHost} port {lanPort}
                </span>{" "}
                (UDP and TCP). Point a router&apos;s conditional forwarder at it, or test with{" "}
                <span className="bg-muted text-foreground rounded px-1 font-mono">
                  dig @{lanHost} -p {lanPort} {sampleName}
                </span>
                .
              </p>
            ) : null}
          </div>
        </div>
      </div>
    </Section>
  )
}

const TEST_TYPES = ["A", "AAAA", "CNAME", "TXT", "MX"]

function DnsTest({ zone }: { zone: HostedZone }) {
  const [name, setName] = useState(zoneLabel(zone.name))
  const [type, setType] = useState("A")
  const [pending, setPending] = useState(false)
  const [result, setResult] = useState<DnsTestResult | null>(null)

  useEffect(() => {
    setName(zoneLabel(zone.name))
    setResult(null)
  }, [zone.name])

  const run = async (e: React.FormEvent) => {
    e.preventDefault()
    if (!name.trim()) return
    setPending(true)
    try {
      setResult(await api.post<DnsTestResult>(`${R53_PATH}/test-dns`, { name: name.trim(), type }))
    } catch (err) {
      toast.error(errorMessage(err))
    } finally {
      setPending(false)
    }
  }

  const answers = result?.answers ?? []

  return (
    <Section title="Test record" description="Queries HomeCloud's DNS server like dig and shows the answer.">
      <form onSubmit={run} className="flex flex-col gap-3">
        <div className="grid grid-cols-1 gap-2 sm:grid-cols-[minmax(0,1fr)_7rem_auto] sm:items-end">
          <Field label="Record name" htmlFor="dns-test-name">
            <Input id="dns-test-name" value={name} onChange={(e) => setName(e.target.value)} className="font-mono text-[13px]" spellCheck={false} />
          </Field>
          <Field label="Type" htmlFor="dns-test-type">
            <Select value={type} onValueChange={setType}>
              <SelectTrigger id="dns-test-type" className="w-full font-mono">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {TEST_TYPES.map((t) => (
                  <SelectItem key={t} value={t} className="font-mono">
                    {t}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </Field>
          <Button type="submit" disabled={pending || !name.trim()}>
            {pending ? <Loader2 className="animate-spin" /> : <Play />} Test
          </Button>
        </div>
        {zone.private && (
          <Alert variant="warning">
            <AlertTriangle />
            <AlertDescription>
              The test queries the public view (the LAN DNS port), so records in this private zone do not answer here. Query them from inside the VPC instead, e.g.
              with Run command on an instance.
            </AlertDescription>
          </Alert>
        )}
        {result && (
          <div className="flex flex-col gap-1.5" aria-live="polite">
            <CodeBlock
              title={`${result.type || "A"} ${result.name}`}
              code={result.error ? result.error : answers.length ? answers.join("\n") : "# No answer"}
              tone={result.error ? "danger" : undefined}
              wrap
            />
            {result.note && <p className="text-muted-foreground text-xs">Note: {result.note}.</p>}
          </div>
        )}
      </form>
    </Section>
  )
}

function ZoneFileDialog({ zone, onClose }: { zone: HostedZone | null; onClose: () => void }) {
  const { data, error, isLoading } = useApi<{ zone_file: string }>(zone ? `${ZONES_PATH}/${seg(zone.id)}/zone-file` : null, { revalidateOnFocus: false })
  const text = data?.zone_file ?? ""
  const download = () => {
    if (!zone) return
    const url = URL.createObjectURL(new Blob([text], { type: "text/dns" }))
    const a = document.createElement("a")
    a.href = url
    a.download = `${zoneLabel(zone.name)}.zone`
    a.click()
    URL.revokeObjectURL(url)
  }
  return (
    <Dialog open={!!zone} onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="sm:max-w-3xl">
        <DialogHeader>
          <DialogTitle>Zone file for {zone ? zoneLabel(zone.name) : ""}</DialogTitle>
          <DialogDescription>BIND format, as served by the DNS server. Alias records appear with their target&apos;s current IP.</DialogDescription>
        </DialogHeader>
        {error ? (
          <ErrorState error={error} />
        ) : isLoading ? (
          <Skeleton className="h-64 w-full rounded-lg" />
        ) : (
          <CodeBlock code={text} title={zone ? `${zoneLabel(zone.name)}.zone` : "zone file"} maxHeight="55vh" />
        )}
        <DialogFooter>
          <Button variant="outline" onClick={onClose}>
            Close
          </Button>
          <Button onClick={download} disabled={!text}>
            <Download /> Download
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

