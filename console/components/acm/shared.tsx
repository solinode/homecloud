"use client"

import { AlertTriangle } from "lucide-react"

import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select"
import { StatusBadge } from "@/components/console/status-badge"
import { formatDate } from "@/lib/format"
import { useApi } from "@/lib/hooks"
import type { Certificate, LoadBalancer } from "@/lib/types"
import { cn } from "@/lib/utils"

export const ACM_PATH = "/api/v1/acm"
export const CERTS_PATH = `${ACM_PATH}/certificates`

export const certHref = (id: string) => `/acm/certificate/?id=${encodeURIComponent(id)}`

/** certIdFromArn extracts the certificate id from "arn:aws:acm:...:certificate/<id>". */
export const certIdFromArn = (arn: string) => arn.slice(arn.lastIndexOf("/") + 1)

export const EXPIRY_WARNING_DAYS = 30

export function useCertificates() {
  return useApi<Certificate[]>(CERTS_PATH, { refreshInterval: 60_000 })
}

/** Whole days until the certificate expires, rounded up (negative once expired). */
export function daysLeft(c: Pick<Certificate, "not_after">, now = Date.now()): number {
  const d = (new Date(c.not_after).getTime() - now) / 86_400_000
  return d >= 0 ? Math.ceil(d) : Math.floor(d)
}

export const isExpired = (c: Pick<Certificate, "status" | "not_after">) => c.status === "EXPIRED" || daysLeft(c) < 0

export function CertStatusBadge({ cert }: { cert: Pick<Certificate, "status" | "not_after"> }) {
  if (isExpired(cert)) return <StatusBadge status="failed" label="Expired" tone="danger" />
  if (daysLeft(cert) < EXPIRY_WARNING_DAYS) return <StatusBadge status="issued" label="Issued, expiring soon" tone="warning" />
  return <StatusBadge status="issued" label="Issued" tone="success" />
}

export function CertTypeLabel({ type }: { type: string }) {
  return <span>{type === "PRIVATE" ? "HomeCloud private CA" : type === "IMPORTED" ? "Imported" : type}</span>
}

/** ExpiryCell shows the expiry date with a warning under 30 days. */
export function ExpiryCell({ cert, withDate = true }: { cert: Pick<Certificate, "status" | "not_after">; withDate?: boolean }) {
  const d = daysLeft(cert)
  const expired = isExpired(cert)
  const soon = !expired && d < EXPIRY_WARNING_DAYS
  return (
    <span className={cn("inline-flex flex-wrap items-center gap-1", expired && "text-destructive", soon && "text-amber-700 dark:text-amber-400")}>
      {(expired || soon) && <AlertTriangle className="size-3.5 shrink-0" />}
      {withDate && <span className="whitespace-nowrap">{formatDate(cert.not_after, false)}</span>}
      <span className={cn("text-xs whitespace-nowrap", !expired && !soon && "text-muted-foreground")}>
        {expired ? `(expired ${-d} day${-d === 1 ? "" : "s"} ago)` : d === 0 ? "(expires today)" : `(in ${d} day${d === 1 ? "" : "s"})`}
      </span>
    </span>
  )
}

/** Load balancer listeners that use a certificate ARN. */
export function listenersUsing(arn: string, lbs: LoadBalancer[] | undefined) {
  const out: { lb: string; port: number }[] = []
  for (const lb of lbs ?? []) for (const l of lb.listeners ?? []) if (l.certificate_arn === arn) out.push({ lb: lb.name, port: l.port })
  return out
}

/** All names on a certificate: the domain first, then SANs that differ from it. */
export function certNames(c: Pick<Certificate, "domain_name" | "subject_alternative_names">): string[] {
  return [c.domain_name, ...(c.subject_alternative_names ?? []).filter((n) => n !== c.domain_name)]
}

/** CertificatePicker selects an ACM certificate ARN for an HTTPS listener. Expired certificates are disabled. */
export function CertificatePicker({ id, value, onChange, invalid }: { id?: string; value: string; onChange: (arn: string) => void; invalid?: boolean }) {
  const { data, isLoading, error } = useCertificates()
  const certs = [...(data ?? [])].sort((a, b) => a.domain_name.localeCompare(b.domain_name))
  return (
    <Select value={value} onValueChange={(v) => v && onChange(v)} disabled={isLoading && !data}>
      <SelectTrigger id={id} className="w-full" aria-invalid={invalid}>
        <SelectValue placeholder={error ? "Could not load certificates" : isLoading && !data ? "Loading certificates..." : certs.length ? "Choose a certificate" : "No certificates in ACM"} />
      </SelectTrigger>
      <SelectContent>
        {certs.map((c) => (
          <SelectItem key={c.arn} value={c.arn} disabled={isExpired(c)}>
            <span className="font-mono text-[13px]">{c.domain_name}</span>
            <span className="text-muted-foreground text-xs">
              {c.type === "PRIVATE" ? "private CA" : "imported"} · {c.id.slice(0, 8)} · {isExpired(c) ? "expired" : `expires ${formatDate(c.not_after, false)}`}
            </span>
          </SelectItem>
        ))}
      </SelectContent>
    </Select>
  )
}
