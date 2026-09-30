import { badRequest, err, getState, unavailable, type DemoService, type Router } from "../engine"
import { DAY, ago, ahead, stableId, uuid } from "../util"
import { NAMES } from "../ids"
import type { Certificate, PrivateCa } from "@/lib/types"

interface AcmState {
  certs: Certificate[]
}
const st = () => getState().acm as AcmState

const CA_NAME = "HomeCloud Demo Private CA"
const pemOf = (label: string, seedText: string) => {
  const b = btoa(`demo-not-a-real-${label.toLowerCase()}:${seedText}`).padEnd(256, "A")
  return `-----BEGIN ${label}-----\n${(b.match(/.{1,64}/g) ?? []).join("\n")}\n-----END ${label}-----\n`
}
const CA_PEM = pemOf("CERTIFICATE", "private-ca-root")
const serial = (s: string) => stableId("", s, 32)
const DOMAIN_RE = /^(\*\.)?([a-zA-Z0-9]([a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?\.)+[a-zA-Z]{2,63}$/

function mkCert(o: { domain: string; sans?: string[]; type?: string; issuer?: string; issuedAgo: number; days: number; tags?: Certificate["tags"]; status?: string }): Certificate {
  const id = stableId("", o.domain + (o.sans ?? []).join(","), 8) + "-" + stableId("", o.domain, 4) + "-4" + stableId("", o.domain, 3) + "-a" + stableId("", o.domain + "x", 3) + "-" + stableId("", o.domain + "y", 12)
  return {
    arn: `arn:aws:acm:us-east-1:123456789012:certificate/${id}`, id, domain_name: o.domain, subject_alternative_names: o.sans ?? [], type: o.type ?? "PRIVATE", status: o.status ?? "ISSUED",
    issuer: o.issuer ?? CA_NAME, not_before: ago(o.issuedAgo), not_after: ahead(o.days * DAY - o.issuedAgo), serial: serial(o.domain), certificate: pemOf("CERTIFICATE", o.domain),
    certificate_chain: o.type === "IMPORTED" ? pemOf("CERTIFICATE", "intermediate-" + o.domain) : CA_PEM, created_at: ago(o.issuedAgo), tags: o.tags ?? {},
  }
}

function seed(): AcmState {
  const certs = [
    mkCert({ domain: NAMES.zone, sans: [`*.${NAMES.zone}`], issuedAgo: 60 * DAY, days: 395, tags: { app: "shop", env: "prod" } }),
    mkCert({ domain: `admin.${NAMES.zone}`, issuedAgo: 380 * DAY, days: 395, tags: { app: "shop", env: "prod" } }),
    mkCert({ domain: `api.${NAMES.zone}`, sans: [`api-internal.${NAMES.zone}`], type: "IMPORTED", issuer: "Demo Public CA R3", issuedAgo: 45 * DAY, days: 90, tags: { app: "shop", source: "imported" } }),
    mkCert({ domain: "staging.example.com", sans: ["*.staging.example.com"], issuedAgo: 100 * DAY, days: 365, tags: { env: "staging" } }),
    mkCert({ domain: "dev.example.com", issuedAgo: 500 * DAY, days: 395, status: "EXPIRED", tags: { env: "dev" } }),
  ]
  return { certs }
}

const find = (id: string): Certificate => {
  const c = st().certs.find((x) => x.id === id || x.arn === id)
  if (!c) throw err(404, "ResourceNotFoundException", `certificate "${id}" does not exist`)
  return c
}
/** in use when any load balancer state mentions the certificate ARN. */
const inUse = (c: Certificate) => JSON.stringify((getState() as Record<string, unknown>).elb ?? {}).includes(c.arn)

function routes(r: Router) {
  const P = "/api/v1/acm"
  r.get(`${P}/certificates`, () => st().certs.map((c) => ({ ...c, certificate: "", certificate_chain: "" })))
  r.post(`${P}/certificates`, ({ body }) => {
    const domain = String(body?.domain_name ?? "").trim()
    const sans = ((body?.subject_alternative_names ?? []) as string[]).map((s) => s.trim()).filter(Boolean)
    for (const d of [domain, ...sans]) if (!DOMAIN_RE.test(d) && !/^\d+\.\d+\.\d+\.\d+$/.test(d)) throw badRequest(`"${d}" is not a valid domain name or IP address`)
    const days = Number(body?.valid_days) || 395
    if (days < 1 || days > 825) throw badRequest("valid_days must be 1-825")
    const id = uuid()
    const c: Certificate = {
      arn: `arn:aws:acm:us-east-1:123456789012:certificate/${id}`, id, domain_name: domain, subject_alternative_names: sans, type: "PRIVATE", status: "ISSUED", issuer: CA_NAME,
      not_before: ago(3600_000), not_after: ahead(days * DAY), serial: serial(id), certificate: pemOf("CERTIFICATE", domain), certificate_chain: CA_PEM, created_at: new Date().toISOString(), tags: body?.tags ?? {},
    }
    st().certs.push(c)
    return c
  })
  r.post(`${P}/certificates/import`, ({ body }) => {
    const cert = String(body?.certificate ?? "")
    const key = String(body?.private_key ?? "")
    if (!/-----BEGIN CERTIFICATE-----[\s\S]+-----END CERTIFICATE-----/.test(cert)) throw badRequest("certificate and private key do not form a valid pair: no PEM certificate found")
    if (!/-----BEGIN [A-Z ]*PRIVATE KEY-----[\s\S]+-----END [A-Z ]*PRIVATE KEY-----/.test(key)) throw badRequest("certificate and private key do not form a valid pair: no PEM private key found")
    const id = uuid()
    const c: Certificate = {
      arn: `arn:aws:acm:us-east-1:123456789012:certificate/${id}`, id, domain_name: "imported.example.com", subject_alternative_names: ["www.imported.example.com"], type: "IMPORTED", status: "ISSUED",
      issuer: "Imported CA", not_before: ago(DAY), not_after: ahead(364 * DAY), serial: serial(id), certificate: cert.trim() + "\n", certificate_chain: body?.certificate_chain ? String(body.certificate_chain) : "",
      created_at: new Date().toISOString(), tags: body?.tags ?? {},
    }
    st().certs.push(c)
    return c
  })
  r.get(`${P}/certificates/:id`, ({ params }) => {
    const c = find(params.id)
    return { certificate: c, fingerprint_sha256: stableId("", c.arn, 64), in_use: inUse(c) }
  })
  r.post(`${P}/certificates/:id/renew`, ({ params }) => {
    const c = find(params.id)
    if (c.type !== "PRIVATE") throw badRequest("imported certificates are renewed by importing a new one")
    const span = new Date(c.not_after).getTime() - new Date(c.not_before).getTime()
    c.not_before = ago(3600_000)
    c.not_after = new Date(Date.now() + span).toISOString()
    c.serial = serial(c.id + Date.now())
    c.status = "ISSUED"
    return c
  })
  r.del(`${P}/certificates/:id`, ({ params }) => {
    const c = find(params.id)
    if (inUse(c)) throw err(409, "ResourceInUseException", "certificate is used by a load balancer listener")
    st().certs = st().certs.filter((x) => x !== c)
  })
  r.get(`${P}/ca`, ({ query }): PrivateCa => {
    if (query.format === "pem") throw unavailable("Downloading the CA certificate")
    return {
      subject: CA_NAME, not_after: ahead(3000 * DAY), certificate: CA_PEM,
      hint: `Trust this CA on your devices to accept certificates issued by HomeCloud (${CA_NAME}).`,
    }
  })
}

const service: DemoService = { name: "acm", seed, routes }
export default service
