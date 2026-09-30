import { badRequest, err, getState, type DemoService, type Router } from "../engine"
import { DAY, ago, epoch, hex, stableId } from "../util"
import { INSTANCE, NAMES, VPC_DEFAULT, VPC_SHOP } from "../ids"
import type { DnsRecord, HostedZone, HostedZoneSummary, RecordChange } from "@/lib/types"

interface R53State {
  zones: HostedZone[]
}
const st = () => getState().route53 as R53State

const zid = (name: string) => "Z" + stableId("", name, 13).toUpperCase()
const rec = (name: string, type: string, values: string[], ttl = 300, alias?: string): DnsRecord => ({ name, type, ttl, ...(alias ? { alias } : { values }) })
const alias = (name: string, target: string): DnsRecord => ({ name, type: "A", ttl: 60, alias: target })

function seed(): R53State {
  const zones: HostedZone[] = [
    {
      id: zid("shop.example.com."), name: "shop.example.com.", private: false, comment: "Public zone for the Shop storefront", serial: epoch() - 3 * 86400, created_at: ago(280 * DAY),
      records: [
        alias("@", NAMES.alb),
        rec("www", "CNAME", ["shop.example.com."], 300),
        rec("api", "CNAME", ["a1b2c3demo.execute-api.us-east-1.example.internal."], 300),
        rec("assets", "CNAME", [`${NAMES.bucketAssets}.s3-website-us-east-1.example.internal.`], 3600),
        alias("admin", NAMES.alb),
        rec("bastion", "A", [], 60, INSTANCE.bastion),
        rec("db", "CNAME", ["shop-db.c8x2demo.us-east-1.rds.internal."], 60),
        rec("ipv6", "AAAA", ["2001:db8:85a3::8a2e:370:7334"], 300),
        rec("@", "MX", ["10 mx1.mail.example.com.", "20 mx2.mail.example.com."], 3600),
        rec("@", "TXT", ["v=spf1 include:_spf.mail.example.com ~all", "google-site-verification=demo-not-a-real-token"], 3600),
        rec("_dmarc", "TXT", ["v=DMARC1; p=quarantine; rua=mailto:dmarc@example.com"], 3600),
        rec("_a79c1demo0e2b.shop.example.com.", "CNAME", ["_3f8e2demo1c9a.acm-validations.example.internal."], 300),
        rec("@", "CAA", ['0 issue "amazon.com"'], 3600),
      ],
    },
    {
      id: zid("shop.internal."), name: "shop.internal.", private: true, vpc_ids: [VPC_SHOP], comment: "Private names inside shop-vpc", serial: epoch() - 20 * 86400, created_at: ago(260 * DAY),
      records: [
        rec("db", "CNAME", ["shop-db.c8x2demo.us-east-1.rds.internal."], 60),
        rec("cache", "CNAME", ["shop-cache.demo.0001.use1.cache.internal."], 60),
        alias("web", NAMES.alb),
        rec("queue", "TXT", ["orders queue: shop-orders"], 300),
        rec("batch", "A", ["10.0.11.24"], 60),
      ],
    },
    {
      id: zid("dev.example.com."), name: "dev.example.com.", private: false, comment: "Developer sandbox names", serial: epoch() - 40 * 86400, created_at: ago(120 * DAY),
      records: [rec("sandbox", "A", ["203.0.113.45"], 300), rec("preview", "CNAME", ["sandbox.dev.example.com."], 300)],
    },
  ]
  return { zones }
}

const zone = (id: string): HostedZone => st().zones.find((z) => z.id === id) ?? (() => { throw err(404, "NoSuchHostedZone", `hosted zone "${id}" does not exist`) })()
const fqdn = (name: string, z: string) => (name === "" || name === "@" ? z : name.endsWith(".") ? name : (name + ".").endsWith(z) ? name + "." : `${name}.${z}`)
const LABEL_RE = /^([a-zA-Z0-9_]([a-zA-Z0-9_-]{0,61}[a-zA-Z0-9_])?\.)+$/
const IPV4 = /^(\d{1,3})\.(\d{1,3})\.(\d{1,3})\.(\d{1,3})$/

function validate(z: HostedZone, r: DnsRecord): DnsRecord {
  const type = String(r.type).toUpperCase()
  if (!["A", "AAAA", "CNAME", "TXT", "MX", "SRV", "NS", "CAA", "PTR"].includes(type)) throw badRequest(`unsupported record type "${r.type}"`)
  const name = (r.name || "@").toLowerCase().trim()
  const full = fqdn(name, z.name)
  if (!full.endsWith(z.name) || (name !== "@" && !LABEL_RE.test(full.replace(/^\*\./, "")))) throw badRequest(`record name "${name}" is not inside zone ${z.name}`)
  const ttl = r.ttl === 0 || r.ttl === undefined ? 300 : r.ttl
  if (ttl < 0 || ttl > 604800) throw badRequest("ttl must be 0-604800")
  if (r.alias) {
    if (type !== "A" || (r.values ?? []).length) throw badRequest("alias records are type A and have no values")
    return { name, type, ttl, alias: r.alias }
  }
  const values = r.values ?? []
  if (!values.length) throw badRequest("a record needs at least one value")
  if (type === "CNAME" && (values.length > 1 || full === z.name)) throw badRequest("a CNAME has exactly one value and cannot be at the zone apex")
  for (const v of values) {
    if (/[\n\r]/.test(v) || (type !== "TXT" && v.includes(";") && type !== "CAA")) throw badRequest("record values may not contain newlines or semicolons")
    if (type === "A" && !(IPV4.test(v) && v.split(".").every((n) => Number(n) <= 255))) throw badRequest(`"${v}" is not an IPv4 address`)
    if (type === "AAAA" && !(v.includes(":") && /^[0-9a-fA-F:]+$/.test(v))) throw badRequest(`"${v}" is not an IPv6 address`)
    if ((type === "CNAME" || type === "NS" || type === "PTR") && !LABEL_RE.test(v.replace(/\.?$/, "."))) throw badRequest(`"${v}" is not a domain name`)
  }
  return { name, type, ttl, values }
}

function zoneFile(z: HostedZone): string {
  const lines = [`$ORIGIN ${z.name}`, "$TTL 300", `@ 3600 IN SOA ns.${z.name} hostmaster.${z.name} ${z.serial} 7200 3600 1209600 60`, `@ 3600 IN NS ns.${z.name}`, `ns.${z.name} 3600 IN A 10.0.0.2`]
  for (const r of z.records) {
    const n = fqdn(r.name, z.name)
    if (r.alias) lines.push(`${n} ${r.ttl} IN A 10.0.1.${(r.alias.length * 7) % 200 + 10} ; alias ${r.alias}`)
    else
      for (const v of r.values ?? []) lines.push(`${n} ${r.ttl} IN ${r.type} ${r.type === "TXT" ? JSON.stringify(v) : v}`)
  }
  return lines.join("\n") + "\n"
}

function routes(r: Router) {
  const P = "/api/v1/route53"
  r.get(`${P}/zones`, (): HostedZoneSummary[] =>
    st().zones.map((z) => ({ id: z.id, name: z.name, private: z.private, vpc_ids: z.vpc_ids ?? [], comment: z.comment ?? "", record_count: z.records.length + 2, created_at: z.created_at })),
  )
  r.post(`${P}/zones`, ({ body }) => {
    let name = String(body?.name ?? "").toLowerCase().trim()
    if (!name.endsWith(".")) name += "."
    if (!LABEL_RE.test(name) || name.length > 254) throw badRequest(`"${body?.name}" is not a valid domain name`)
    const dup = st().zones.find((z) => z.name === name)
    if (dup) throw err(409, "HostedZoneAlreadyExists", `zone ${name} already exists (${dup.id})`)
    if (body?.private && !(body.vpc_ids ?? []).length) throw badRequest("a private hosted zone needs at least one VPC")
    const z: HostedZone = {
      id: "Z" + hex(13).toUpperCase(), name, private: !!body?.private, vpc_ids: body?.vpc_ids ?? [], comment: body?.comment ?? "", records: [], serial: epoch(), created_at: new Date().toISOString(),
    }
    st().zones.push(z)
    return z
  })
  r.get(`${P}/zones/:id`, ({ params }) => {
    const z = zone(params.id)
    const ns: string[] = []
    if (!z.private || (z.vpc_ids ?? []).includes(VPC_SHOP)) ns.push("10.0.0.2 (shop-vpc VPC)")
    if (!z.private || (z.vpc_ids ?? []).includes(VPC_DEFAULT)) ns.push("172.31.0.2 (default VPC)")
    if (!z.private) ns.push("192.168.1.20:5353 (LAN)")
    return { zone: z, name_servers: ns }
  })
  r.del(`${P}/zones/:id`, ({ params, query }) => {
    const z = zone(params.id)
    if (z.records.length && query.force !== "true") throw err(409, "HostedZoneNotEmpty", `zone has ${z.records.length} records; delete them first or pass force=true`)
    st().zones = st().zones.filter((x) => x !== z)
  })
  r.post(`${P}/zones/:id/changes`, ({ params, body }) => {
    const z = zone(params.id)
    const changes = (body?.changes ?? []) as RecordChange[]
    if (!changes.length) throw badRequest("no changes")
    // atomic: work on a copy
    const recs = z.records.map((x) => ({ ...x }))
    for (const ch of changes) {
      const rc = validate(z, ch.record)
      const idx = recs.findIndex((x) => fqdn(x.name, z.name) === fqdn(rc.name, z.name) && x.type === rc.type)
      switch (String(ch.action).toUpperCase()) {
        case "CREATE":
          if (idx >= 0) throw err(400, "InvalidChangeBatch", `${rc.type} ${fqdn(rc.name, z.name)} already exists`)
          recs.push(rc)
          break
        case "UPSERT":
          if (idx >= 0) recs[idx] = rc
          else recs.push(rc)
          break
        case "DELETE":
          if (idx < 0) throw err(400, "InvalidChangeBatch", `${rc.type} ${fqdn(rc.name, z.name)} does not exist`)
          recs.splice(idx, 1)
          break
        default:
          throw badRequest("action must be CREATE, UPSERT or DELETE")
      }
    }
    const names = new Map<string, string[]>()
    for (const x of recs) names.set(fqdn(x.name, z.name), [...(names.get(fqdn(x.name, z.name)) ?? []), x.type])
    for (const [n, ts] of names) if (ts.includes("CNAME") && ts.length > 1) throw err(400, "InvalidChangeBatch", `${n} has a CNAME and other records`)
    z.records = recs
    z.serial = epoch()
    return { status: "INSYNC", records: recs.length }
  })
  r.get(`${P}/zones/:id/zone-file`, ({ params }) => ({ zone_file: zoneFile(zone(params.id)) }))
  r.post(`${P}/test-dns`, ({ body }) => {
    const name = String(body?.name ?? "").toLowerCase().replace(/\.$/, "") + "."
    const type = String(body?.type || "A").toUpperCase()
    if (!["A", "AAAA", "CNAME", "TXT", "MX"].includes(type)) throw badRequest("test supports A, AAAA, CNAME, TXT and MX")
    const answers: string[] = []
    for (const z of st().zones) {
      if (z.private) continue
      for (const x of z.records) {
        if (fqdn(x.name, z.name) !== name) continue
        if (x.alias && type === "A") answers.push(`203.0.113.${(x.alias.length * 11) % 200 + 20}`)
        else if (x.type === type) answers.push(...(x.values ?? []))
      }
    }
    const note = "queried the public view on the host port; private zones answer only inside their VPCs"
    if (!answers.length) return { name: body?.name, type, answers: [], error: `lookup ${body?.name}: no such host` }
    return { name: body?.name, type, answers, note }
  })
}

const service: DemoService = { name: "route53", seed, routes }
export default service
