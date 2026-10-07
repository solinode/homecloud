"use client"

import { useState } from "react"
import { useRouter } from "next/navigation"
import { BadgeCheck, FileUp, Plus } from "lucide-react"
import { toast } from "sonner"

import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { ActionsMenu, type ActionItem } from "@/components/console/actions-menu"
import { CellLink, CellText, DataTable, type Column } from "@/components/console/data-table"
import { EmptyState } from "@/components/console/empty-state"
import { PageHeader } from "@/components/console/page-header"
import { StatusBadge } from "@/components/console/status-badge"
import { TimeAgo } from "@/components/console/time-ago"
import { api, errorMessage, seg } from "@/lib/api"
import { revalidate, useApi, useQueryParam } from "@/lib/hooks"
import type { Certificate, LoadBalancer } from "@/lib/types"

import { PrivateCaPanel } from "./ca-panel"
import { DeleteCertificateDialog, ImportCertificateDialog, RequestCertificateDialog } from "./certificate-dialogs"
import { ACM_PATH, CERTS_PATH, CertStatusBadge, CertTypeLabel, ExpiryCell, certHref, certNames, listenersUsing, useCertificates } from "./shared"

export function CertificatesList() {
  const router = useRouter()
  const { data, error, isLoading, isValidating, mutate } = useCertificates()
  const lbs = useApi<LoadBalancer[]>("/api/v1/elb/load-balancers", { refreshInterval: 60_000 })
  const [selected, setSelected] = useState<string[]>([])
  const [requesting, setRequesting] = useState(useQueryParam("request") === "1")
  const [importing, setImporting] = useState(false)
  const [deleting, setDeleting] = useState<Certificate | null>(null)
  const [renewing, setRenewing] = useState(false)

  const sel = (data ?? []).find((c) => selected.includes(c.id)) ?? null
  const inUse = (c: Certificate) => listenersUsing(c.arn, lbs.data)

  const columns: Column<Certificate>[] = [
    {
      id: "domain",
      header: "Domain name",
      cell: (c) => {
        const extra = certNames(c).length - 1
        return (
          <span className="flex items-center gap-1.5">
            <CellLink href={certHref(c.id)} mono max="18rem">
              {c.domain_name}
            </CellLink>
            {extra > 0 && (
              <Badge variant="secondary" className="font-normal" title={certNames(c).slice(1).join(", ")}>
                +{extra}
              </Badge>
            )}
          </span>
        )
      },
      value: (c) => certNames(c).join(" "),
    },
    { id: "type", header: "Type", cell: (c) => <CertTypeLabel type={c.type} />, value: (c) => c.type, hideBelow: "md" },
    { id: "status", header: "Status", cell: (c) => <CertStatusBadge cert={c} />, value: (c) => c.status },
    {
      id: "inuse",
      header: "In use",
      cell: (c) => {
        const u = inUse(c)
        return u.length ? (
          <span title={u.map((x) => `${x.lb}:${x.port}`).join(", ")}>
            <StatusBadge status="in-use" label={u.length === 1 ? `In use (${u[0].lb}:${u[0].port})` : `In use (${u.length} listeners)`} />
          </span>
        ) : (
          <span className="text-muted-foreground">No</span>
        )
      },
      value: (c) => inUse(c).length,
      hideBelow: "lg",
    },
    { id: "expires", header: "Expires", cell: (c) => <ExpiryCell cert={c} />, value: (c) => c.not_after },
    { id: "issuer", header: "Issuer", cell: (c) => <CellText muted max="14rem">{c.issuer}</CellText>, value: (c) => c.issuer, hideBelow: "lg" },
    { id: "created", header: "Created", cell: (c) => <TimeAgo value={c.created_at} />, value: (c) => c.created_at, hideBelow: "sm" },
  ]

  const renew = async (c: Certificate) => {
    setRenewing(true)
    try {
      await api.post(`${CERTS_PATH}/${seg(c.id)}/renew`)
      toast.success(`Renewed the certificate for ${c.domain_name}; load balancers using it reload automatically`)
      await revalidate(ACM_PATH)
    } catch (e) {
      toast.error(errorMessage(e))
    } finally {
      setRenewing(false)
    }
  }

  const selInUse = sel ? inUse(sel).length > 0 : false
  const items: ActionItem[] = [
    { label: "View details", onSelect: () => sel && router.push(certHref(sel.id)), disabled: !sel },
    {
      label: "Renew now",
      onSelect: () => sel && renew(sel),
      disabled: !sel || sel.type !== "PRIVATE" || renewing,
      hint: sel && sel.type !== "PRIVATE" ? "Imported certificates are renewed by importing a new one" : undefined,
    },
    { separator: true },
    {
      label: "Delete",
      destructive: true,
      onSelect: () => sel && setDeleting(sel),
      disabled: !sel || selInUse,
      hint: selInUse ? "Used by a load balancer listener: remove the listener first" : undefined,
    },
  ]

  return (
    <div className="flex flex-col gap-4">
      <PageHeader
        title="Certificates"
        description="TLS certificates for load balancer HTTPS listeners: issued by this installation's private CA, or imported from another authority such as Let's Encrypt."
        breadcrumbs={[{ label: "Certificate Manager", href: "/acm/" }, { label: "Certificates" }]}
      />
      <DataTable
        title="Certificates"
        data={data}
        columns={columns}
        rowId={(c) => c.id}
        loading={isLoading}
        error={error}
        onRefresh={() => mutate()}
        refreshing={isValidating}
        selection="single"
        selected={selected}
        onSelectedChange={setSelected}
        searchPlaceholder="Filter by domain, type or issuer"
        defaultSort={{ id: "domain" }}
        actions={
          <>
            <ActionsMenu items={items} disabled={!sel} />
            <Button size="sm" variant="outline" onClick={() => setImporting(true)}>
              <FileUp /> Import
            </Button>
            <Button size="sm" onClick={() => setRequesting(true)}>
              <Plus /> Request
            </Button>
          </>
        }
        empty={
          <EmptyState
            icon={BadgeCheck}
            title="No certificates"
            description="Request a certificate from the private CA for names like app.home.arpa or an IP address, then use it on an HTTPS load balancer listener."
            action={
              <Button size="sm" onClick={() => setRequesting(true)}>
                <Plus /> Request a certificate
              </Button>
            }
          />
        }
      />
      <PrivateCaPanel />
      <RequestCertificateDialog open={requesting} onOpenChange={setRequesting} />
      <ImportCertificateDialog open={importing} onOpenChange={setImporting} />
      <DeleteCertificateDialog cert={deleting} onClose={() => setDeleting(null)} />
    </div>
  )
}
