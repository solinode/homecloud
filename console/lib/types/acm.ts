// Types for ACM certificates (cli/internal/svc/acm/acm.go).
import type { Tags } from "./common"

export type CertificateType = "PRIVATE" | "IMPORTED"
export type CertificateStatus = "ISSUED" | "EXPIRED"

export interface Certificate {
  arn: string
  id: string
  domain_name: string
  subject_alternative_names?: string[] | null
  type: CertificateType | string
  status: CertificateStatus | string
  issuer: string
  not_before: string
  not_after: string
  serial: string
  /** PEM; empty in list responses */
  certificate: string
  certificate_chain?: string
  created_at: string
  tags?: Tags | null
}

/** GET /acm/certificates/{id} */
export interface CertificateDetail {
  certificate: Certificate
  fingerprint_sha256: string
  /** used by a load balancer listener (cannot be deleted) */
  in_use: boolean
}

export interface RequestCertificateInput {
  domain_name: string
  subject_alternative_names?: string[]
  valid_days?: number
  tags?: Tags
}

export interface ImportCertificateInput {
  certificate: string
  private_key: string
  certificate_chain?: string
  tags?: Tags
}

/** GET /acm/ca */
export interface PrivateCa {
  subject: string
  not_after: string
  certificate: string
  hint: string
}
