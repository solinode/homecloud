"use client"

import { useState } from "react"
import Link from "next/link"
import { AlertCircle, AlertTriangle, ArrowLeft, Download, Loader2, RefreshCw, RotateCcw, Trash2 } from "lucide-react"
import { toast } from "sonner"

import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert"
import { Button } from "@/components/ui/button"
import { ActionsMenu } from "@/components/console/actions-menu"
import { CodeBlock } from "@/components/console/code-block"
import { CopyableText } from "@/components/console/copy-button"
import { EmptyState } from "@/components/console/empty-state"
import { ErrorState } from "@/components/console/error-state"
import { KeyValueGrid } from "@/components/console/key-value"
import { DetailSkeleton } from "@/components/console/loading"
import { PageHeader } from "@/components/console/page-header"
import { Section } from "@/components/console/section"
import { StatusBadge } from "@/components/console/status-badge"
import { Tag } from "@/components/console/tag"
import { TagList } from "@/components/console/tags-editor"
import { TimeAgo } from "@/components/console/time-ago"
import { ApiError, api, errorMessage, seg } from "@/lib/api"
import { formatDate } from "@/lib/format"
import { revalidate, useApi, useQueryParam } from "@/lib/hooks"
import type { CertificateDetail as CertDetail, LoadBalancer } from "@/lib/types"
import { cn } from "@/lib/utils"

import { PrivateCaPanel } from "./ca-panel"
import { DeleteCertificateDialog } from "./certificate-dialogs"
import { ACM_PATH, CERTS_PATH, CertStatusBadge, CertTypeLabel, EXPIRY_WARNING_DAYS, ExpiryCell, daysLeft, isExpired, listenersUsing } from "./shared"

function BackButton() {
  return (
    <Button variant="outline" size="sm" asChild>
      <Link href="/acm/">
        <ArrowLeft /> Back to certificates
      </Link>
    </Button>
  )
}

/** Colon-separated upper-case hex, like browsers show fingerprints. */
const colonHex = (hex: string) => hex.toUpperCase().match(/.{1,2}/g)?.join(":") ?? hex

function PemBlock({ title, pem, filename }: { title: string; pem: string; filename: string }) {
  const download = () => {
    const url = URL.createObjectURL(new Blob([pem], { type: "application/x-pem-file" }))
    const a = document.createElement("a")
    a.href = url
    a.download = filename
    a.click()
    URL.revokeObjectURL(url)
  }
  return (
    <Section
      title={title}
      actions={
        <>
          <Button variant="outline" size="sm" onClick={download}>
            <Download /> Download
          </Button>
        </>
      }
    >
      <CodeBlock code={pem} title={filename} wrap maxHeight="18rem" copyLabel="Copy PEM" />
    </Section>
  )
}

export function CertificateDetail() {
  const id = useQueryParam("id")
  const { data, error, isLoading, isValidating, mutate } = useApi<CertDetail>(id ? `${CERTS_PATH}/${seg(id)}` : null, { refreshInterval: 60_000 })
  const lbs = useApi<LoadBalancer[]>("/api/v1/elb/load-balancers", { refreshInterval: 60_000 })
  const [deleting, setDeleting] = useState(false)
  const [renewing, setRenewing] = useState(false)

  const c = data?.certificate
  const crumbs = [{ label: "Certificate Manager", href: "/acm/" }, { label: "Certificates", href: "/acm/" }, { label: c?.domain_name || id || "Certificate" }]

  if (!id) {
    return (
      <>
        <PageHeader title="Certificate" breadcrumbs={crumbs} />
        <EmptyState title="No certificate selected" description="Open a certificate from the certificates list." action={<BackButton />} />
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
            <EmptyState icon={AlertCircle} title="Certificate not found" description={`Certificate ${id} does not exist. It may have been deleted.`} action={<BackButton />} />
          </Section>
        ) : (
          <ErrorState error={error} onRetry={() => mutate()} />
        )}
      </div>
    )
  }
  if (isLoading || !data || !c) return <DetailSkeleton />

  const users = listenersUsing(c.arn, lbs.data)
  const inUse = data.in_use || users.length > 0
  const expired = isExpired(c)
  const left = daysLeft(c)

  const renew = async () => {
    setRenewing(true)
    try {
      await api.post(`${CERTS_PATH}/${seg(c.id)}/renew`)
      toast.success("Certificate renewed with the same ARN; load balancers using it reload automatically")
      await revalidate(ACM_PATH)
    } catch (e) {
      toast.error(errorMessage(e))
    } finally {
      setRenewing(false)
    }
  }


  return (
    <div className="flex flex-col gap-4">
      <PageHeader
        title={<span className="font-mono break-all">{c.domain_name}</span>}
        badge={
          <>
            <CertStatusBadge cert={c} />
            {inUse && <StatusBadge status="in-use" label="In use" />}
          </>
        }
        description={<span className="font-mono text-[13px] break-all">{c.id}</span>}
        breadcrumbs={crumbs}
        actions={
          <>
            <Button variant="outline" size="sm" onClick={() => mutate()} aria-label="Refresh">
              <RefreshCw className={cn(isValidating && "animate-spin")} />
            </Button>
            <ActionsMenu
              items={[
                {
                  label: "Delete certificate",
                  icon: <Trash2 />,
                  destructive: true,
                  disabled: inUse,
                  hint: inUse ? "In use by a load balancer listener. Remove the listener or switch it to another certificate first." : undefined,
                  onSelect: () => setDeleting(true),
                },
              ]}
            />
            {c.type === "PRIVATE" && (
              <Button size="sm" onClick={renew} disabled={renewing}>
                {renewing ? <Loader2 className="animate-spin" /> : <RotateCcw />} Renew
              </Button>
            )}
          </>
        }
      />

      {(expired || left < EXPIRY_WARNING_DAYS) && (
        <Alert variant={expired ? "destructive" : "warning"}>
          <AlertTriangle />
          <AlertTitle>{expired ? "This certificate has expired" : `This certificate expires in ${left} day${left === 1 ? "" : "s"}`}</AlertTitle>
          <AlertDescription>
            {c.type === "PRIVATE"
              ? "Renew it to issue fresh certificate material under the same ARN; listeners using it pick it up automatically."
              : "Import a renewed certificate and switch the listeners to it."}
            {expired && " New HTTPS listeners cannot use an expired certificate."}
          </AlertDescription>
        </Alert>
      )}

      <Section title="Certificate status">
        <KeyValueGrid
          columns={3}
          items={[
            { label: "Identifier", value: <CopyableText value={c.id} /> },
            { label: "Status", value: <CertStatusBadge cert={c} /> },
            { label: "Type", value: <CertTypeLabel type={c.type} /> },
            { label: "Domain name", value: <CopyableText value={c.domain_name} /> },
            {
              label: "Subject alternative names",
              value: c.subject_alternative_names?.length ? (
                <span className="flex flex-col gap-0.5">
                  {c.subject_alternative_names.map((n) => (
                    <span key={n} className="font-mono text-[13px] break-all">
                      {n}
                    </span>
                  ))}
                </span>
              ) : (
                ""
              ),
            },
            { label: "Issuer", value: c.issuer },
            { label: "Not before", value: formatDate(c.not_before) },
            { label: "Not after", value: <ExpiryCell cert={c} /> },
            { label: "Requested / imported", value: <span>{formatDate(c.created_at)} (<TimeAgo value={c.created_at} />)</span> },
            { label: "Serial number", value: <CopyableText value={colonHex(c.serial)} /> },
            { label: "SHA-256 fingerprint", value: <CopyableText value={colonHex(data.fingerprint_sha256)} />, wide: true },
            { label: "ARN", value: <CopyableText value={c.arn} />, wide: true },
          ]}
        />
      </Section>

      <Section title="Associated resources" description="Load balancer listeners that serve this certificate. A certificate in use cannot be deleted.">
        {users.length ? (
          <ul className="flex flex-col gap-1 text-sm">
            {users.map((u) => (
              <li key={`${u.lb}:${u.port}`}>
              <span className="inline-flex items-center gap-2">
                <Link href={`/elb/load-balancer/?name=${encodeURIComponent(u.lb)}&tab=listeners`} className="text-primary font-medium whitespace-nowrap hover:underline">
                  {u.lb}
                </Link>
                <Tag accent="success">HTTPS:{u.port}</Tag>
              </span>
              </li>
            ))}
          </ul>
        ) : data.in_use ? (
          <p className="text-sm">In use by a load balancer listener.</p>
        ) : (
          <p className="text-muted-foreground text-sm">
            Not in use. Add an HTTPS listener on a{" "}
            <Link href="/elb/" className="text-primary hover:underline">
              load balancer
            </Link>{" "}
            to serve it.
          </p>
        )}
      </Section>

      <PemBlock title="Certificate body" pem={c.certificate} filename={`${c.domain_name.replace(/^\*\./, "wildcard.")}.pem`} />
      {c.certificate_chain && <PemBlock title="Certificate chain" pem={c.certificate_chain} filename="chain.pem" />}

      {c.type === "PRIVATE" && <PrivateCaPanel />}

      <Section title="Tags">
        <TagList tags={c.tags} />
      </Section>

      <DeleteCertificateDialog cert={deleting ? c : null} onClose={() => setDeleting(false)} redirect />
    </div>
  )
}
