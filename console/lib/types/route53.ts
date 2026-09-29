// Types for Route 53 hosted zones (cli/internal/svc/route53/route53.go).

export type DnsRecordType = "A" | "AAAA" | "CNAME" | "TXT" | "MX" | "SRV" | "NS" | "CAA" | "PTR"

export interface DnsRecord {
  /** relative to the zone ("www", "@") or fully qualified with a trailing dot */
  name: string
  type: DnsRecordType | string
  ttl: number
  values?: string[] | null
  /** HomeCloud resource (instance id, ECS task id, RDS id, load balancer name); A records only */
  alias?: string
}

/** GET /route53/zones row. */
export interface HostedZoneSummary {
  id: string
  /** fully qualified, with trailing dot */
  name: string
  private: boolean
  vpc_ids?: string[] | null
  comment?: string
  /** includes the generated SOA and NS records */
  record_count: number
  created_at: string
}

export interface HostedZone {
  id: string
  name: string
  private: boolean
  vpc_ids?: string[] | null
  comment?: string
  records: DnsRecord[]
  serial: number
  created_at: string
}

/** GET /route53/zones/{id} */
export interface HostedZoneDetail {
  zone: HostedZone
  /** e.g. "10.88.0.2 (default VPC)", "host:5353 (LAN)" */
  name_servers: string[]
}

export interface CreateHostedZoneInput {
  name: string
  private?: boolean
  vpc_ids?: string[]
  comment?: string
}

export type ChangeAction = "CREATE" | "UPSERT" | "DELETE"

export interface RecordChange {
  action: ChangeAction
  record: DnsRecord
}

export interface ChangeResult {
  status: string
  records: number
}

export interface DnsTestResult {
  name: string
  type: string
  answers: string[] | null
  error?: string
  note?: string
}
