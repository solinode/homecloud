import { badRequest, conflict, err, getState, notFound, type DemoService, type Router, type State } from "../engine"
import { AZ_A, AZ_B, DAY, REGION, ago, arn, stableId, uid } from "../util"
import { IGW_DEFAULT, IGW_SHOP, SG, SUBNET, VPC_DEFAULT, VPC_SHOP } from "../ids"
import type { InternetGateway, RouteEntry, RouteTable, SecurityGroup, SecurityGroupRule, Subnet, Tags, Vpc } from "@/lib/types"

// VPCs, subnets, security groups (/api/v1/vpc) plus internet gateways and route
// tables (/api/v1/ec2/internet-gateways, /api/v1/ec2/route-tables).

export const VPC_DEV = stableId("vpc-", "dev-vpc")
export const SUBNET_DEV = stableId("subnet-", "dev-a")
export const SG_DEV = stableId("sg-", "dev-default")
export const NAT_GATEWAY = stableId("nat-", "shop-nat")

type SubnetRec = Omit<Subnet, "available_ips" | "used_ips">
type VpcRec = Omit<Vpc, "subnets">

export interface VpcState {
  vpcs: VpcRec[]
  subnets: SubnetRec[]
  sgs: SecurityGroup[]
  igws: InternetGateway[]
  rtbs: RouteTable[]
  /** addresses held by resources of other services (RDS, ECS, ...), per subnet */
  extraUsed: Record<string, number>
}

export const vpcState = (): VpcState => getState().vpc as VpcState

// ---- IPv4 helpers ----

const ipToInt = (s: string) => s.split(".").reduce((a, o) => a * 256 + Number(o), 0)
const intToIp = (n: number) => [24, 16, 8, 0].map((sh) => Math.floor(n / 2 ** sh) % 256).join(".")

export interface Cidr {
  base: number
  bits: number
  size: number
  text: string
}

export function parseCidr(s: string): Cidr | null {
  const m = /^(\d{1,3})\.(\d{1,3})\.(\d{1,3})\.(\d{1,3})\/(\d{1,2})$/.exec((s ?? "").trim())
  if (!m) return null
  const o = [m[1], m[2], m[3], m[4]].map(Number)
  const bits = Number(m[5])
  if (o.some((x) => x > 255) || bits > 32) return null
  const size = 2 ** (32 - bits)
  const base = Math.floor(ipToInt(o.join(".")) / size) * size
  return { base, bits, size, text: `${intToIp(base)}/${bits}` }
}
const overlaps = (a: Cidr, b: Cidr) => a.base < b.base + b.size && b.base < a.base + a.size

// ---- shared helpers (used by ec2, elb, autoscaling) ----

export const nameTag = (tags?: Tags | null) => tags?.Name

/** subnetUsed counts the addresses held in a subnet by instances, load balancers and other services. */
export function subnetUsed(subnetId: string): number {
  const st = getState()
  const inst = ((st.ec2?.instances ?? []) as { subnet_id: string; state: string }[]).filter((i) => i.subnet_id === subnetId && i.state !== "terminated").length
  const lbs = ((st.elb?.lbs ?? []) as { subnet_id: string }[]).filter((l) => l.subnet_id === subnetId).length
  return inst + lbs + (vpcState().extraUsed[subnetId] ?? 0)
}

/** allocIp picks a free private address in the subnet. */
export function allocIp(subnetId: string): string {
  const sn = vpcState().subnets.find((s) => s.id === subnetId)
  const c = sn && parseCidr(sn.cidr)
  if (!c) return "10.0.0.10"
  const taken = new Set<string>()
  const st = getState()
  for (const i of (st.ec2?.instances ?? []) as { subnet_id: string; private_ip: string; state: string }[]) if (i.subnet_id === subnetId && i.state !== "terminated") taken.add(i.private_ip)
  for (const l of (st.elb?.lbs ?? []) as { subnet_id: string; private_ip: string }[]) if (l.subnet_id === subnetId) taken.add(l.private_ip)
  for (let n = 10 + Math.floor(Math.random() * 40); n < c.size - 2; n++) {
    const ip = intToIp(c.base + n)
    if (!taken.has(ip)) return ip
  }
  throw conflict(`subnet ${subnetId} has no free addresses`)
}

export function defaultSubnet(): SubnetRec {
  const v = vpcState()
  const s = v.subnets.find((x) => x.default) ?? v.subnets[0]
  if (!s) throw conflict("no subnet available; create a VPC and subnet first")
  return s
}

export const defaultSg = (vpcId: string) => vpcState().sgs.find((g) => g.vpc_id === vpcId && g.name === "default")?.id ?? ""

const subnetView = (sn: SubnetRec): Subnet => {
  const c = parseCidr(sn.cidr)
  const used = subnetUsed(sn.id)
  return { ...sn, used_ips: used, available_ips: (c ? c.size : 256) - 5 - used }
}

function vpcView(v: VpcRec): Vpc {
  return { ...v, subnets: vpcState().subnets.filter((s) => s.vpc_id === v.id).map(subnetView) }
}

/** wantsInternet: an attached gateway plus a 0.0.0.0/0 route to it. */
function applyInternet(vpcId: string) {
  const s = vpcState()
  const v = s.vpcs.find((x) => x.id === vpcId)
  if (!v) return
  const attached = new Set(s.igws.filter((g) => g.vpc_id === vpcId).map((g) => g.id))
  v.internet_access = s.rtbs.some((t) => t.vpc_id === vpcId && (t.routes ?? []).some((r) => r.destination === "0.0.0.0/0" && !!r.gateway_id && attached.has(r.gateway_id)))
}

const rtbId = () => uid("rtb-")
const sgrId = () => uid("sgr-")

function rule(protocol: "tcp" | "udp", from: number, to: number, cidr: string, description?: string): SecurityGroupRule {
  return { id: sgrId(), protocol, from_port: from, to_port: to, cidr, description }
}

/** fromGroup is a rule whose source is another security group of the VPC (cidr stays empty, as in the API). */
function fromGroup(protocol: "tcp" | "udp", from: number, to: number, group: string, description?: string): SecurityGroupRule {
  return { id: sgrId(), protocol, from_port: from, to_port: to, source_group: group, description }
}

/** selfRule is the rule every default group starts with: all inbound traffic from the group's own members. */
const selfRule = (groupId: string): SecurityGroupRule => ({ id: stableId("sgr-", `${groupId}-self`), protocol: "-1", from_port: -1, to_port: -1, source_group: groupId })

// A rule from 0.0.0.0/0 or 127.0.0.1/32 is also published as host ports, which limits its range (the API's maxPublishedRange).
const MAX_PUBLISHED_RANGE = 32

/** normalizeRule mirrors POST /api/v1/vpc/security-groups/:id/ingress: any IPv4 CIDR, or a source_group, but not both. */
function normalizeRule(r: Partial<SecurityGroupRule>): SecurityGroupRule {
  const protocol = String(r.protocol || "tcp").toLowerCase()
  if (protocol !== "tcp" && protocol !== "udp") throw badRequest("protocol must be tcp or udp (the EC2 API takes other protocols)")
  const from = Number(r.from_port)
  const to = Number(r.to_port || r.from_port)
  if (!Number.isInteger(from) || !Number.isInteger(to) || from < 1 || to > 65535 || from > to) throw badRequest(`invalid port range ${from}-${to}`)
  const description = r.description || undefined
  if (r.source_group) {
    if (r.cidr) throw badRequest("a rule has one source: a cidr or a source_group")
    return { id: sgrId(), protocol, from_port: from, to_port: to, source_group: r.source_group, description }
  }
  const cidr = r.cidr || "0.0.0.0/0"
  if (!parseCidr(cidr)) throw badRequest(`invalid cidr "${cidr}"`)
  if ((cidr === "0.0.0.0/0" || cidr === "127.0.0.1/32") && to - from >= MAX_PUBLISHED_RANGE) throw badRequest(`port ranges from ${cidr} are limited to ${MAX_PUBLISHED_RANGE} ports`)
  return { id: sgrId(), protocol, from_port: from, to_port: to, cidr, description }
}

// ---- seed ----

function seed(): VpcState {
  const sn = (id: string, vpc_id: string, name: string, cidr: string, az: string, def: boolean, age: number): SubnetRec => ({
    id, vpc_id, name, cidr, availability_zone: az, default: def, created_at: ago(age),
  })
  const mainAssoc = (n: string) => ({ id: stableId("rtbassoc-", n), main: true })
  const shopTags = (name: string): Tags => ({ Name: name, Environment: "production", Project: "shop" })
  const rtbDefault = stableId("rtb-", "default-main")
  const rtbShopMain = stableId("rtb-", "shop-main")
  const rtbPublic = stableId("rtb-", "shop-public")
  const rtbPrivate = stableId("rtb-", "shop-private")
  const rtbDev = stableId("rtb-", "dev-main")
  return {
    vpcs: [
      { id: VPC_DEFAULT, name: "default", cidr: "172.31.0.0/16", network: "homecloud-default", default: true, internet_access: true, state: "available", created_at: ago(140 * DAY), tags: null, arn: arn("ec2", `vpc/${VPC_DEFAULT}`) },
      { id: VPC_SHOP, name: "shop-vpc", cidr: "10.0.0.0/16", network: "homecloud-shop-vpc", default: false, internet_access: true, state: "available", created_at: ago(96 * DAY), tags: shopTags("shop-vpc"), arn: arn("ec2", `vpc/${VPC_SHOP}`) },
      { id: VPC_DEV, name: "dev-vpc", cidr: "10.20.0.0/20", network: "homecloud-dev-vpc", default: false, internet_access: false, state: "available", created_at: ago(23 * DAY), tags: { Name: "dev-vpc", Environment: "dev" }, arn: arn("ec2", `vpc/${VPC_DEV}`) },
    ],
    subnets: [
      sn(SUBNET.defaultA, VPC_DEFAULT, "default-a", "172.31.0.0/20", AZ_A, true, 140 * DAY),
      sn(SUBNET.defaultB, VPC_DEFAULT, "default-b", "172.31.16.0/20", AZ_B, true, 140 * DAY),
      sn(SUBNET.publicA, VPC_SHOP, "shop-public-a", "10.0.1.0/24", AZ_A, false, 96 * DAY),
      sn(SUBNET.publicB, VPC_SHOP, "shop-public-b", "10.0.2.0/24", AZ_B, false, 96 * DAY),
      sn(SUBNET.privateA, VPC_SHOP, "shop-private-a", "10.0.11.0/24", AZ_A, false, 96 * DAY),
      sn(SUBNET.privateB, VPC_SHOP, "shop-private-b", "10.0.12.0/24", AZ_B, false, 96 * DAY),
      sn(SUBNET_DEV, VPC_DEV, "dev-a", "10.20.0.0/24", AZ_A, false, 23 * DAY),
    ],
    sgs: [
      { id: SG.defaultVpc, vpc_id: VPC_DEFAULT, name: "default", description: "default VPC security group", ingress: [selfRule(SG.defaultVpc)], self_rule: true, created_at: ago(140 * DAY) },
      { id: SG.shopDefault, vpc_id: VPC_SHOP, name: "default", description: "default VPC security group", ingress: [selfRule(SG.shopDefault)], self_rule: true, created_at: ago(96 * DAY) },
      { id: SG_DEV, vpc_id: VPC_DEV, name: "default", description: "default VPC security group", ingress: [selfRule(SG_DEV)], self_rule: true, created_at: ago(23 * DAY) },
      {
        id: SG.alb, vpc_id: VPC_SHOP, name: "shop-alb", description: "Public HTTP/HTTPS to the application load balancer", created_at: ago(95 * DAY),
        ingress: [rule("tcp", 80, 80, "0.0.0.0/0", "HTTP from anywhere"), rule("tcp", 443, 443, "0.0.0.0/0", "HTTPS from anywhere")],
      },
      {
        id: SG.web, vpc_id: VPC_SHOP, name: "shop-web", description: "Web tier instances behind the ALB", created_at: ago(95 * DAY),
        ingress: [fromGroup("tcp", 80, 80, SG.alb, "HTTP from the load balancer"), rule("tcp", 8080, 8080, "10.0.0.0/16", "Application port"), fromGroup("tcp", 22, 22, SG.bastion, "SSH from the bastion group")],
      },
      { id: SG.db, vpc_id: VPC_SHOP, name: "shop-db", description: "Postgres access from the web tier", created_at: ago(94 * DAY), ingress: [fromGroup("tcp", 5432, 5432, SG.web, "Postgres from the web tier"), rule("tcp", 5432, 5432, "10.0.12.0/24", "Postgres from private-b")] },
      { id: SG.cache, vpc_id: VPC_SHOP, name: "shop-cache", description: "Redis access from the web tier", created_at: ago(94 * DAY), ingress: [rule("tcp", 6379, 6379, "10.0.0.0/16", "Redis from the VPC")] },
      { id: SG.bastion, vpc_id: VPC_SHOP, name: "shop-bastion", description: "SSH entry point for operators", created_at: ago(70 * DAY), ingress: [rule("tcp", 22, 22, "203.0.113.0/24", "SSH from the office network")] },
    ],
    igws: [
      { id: IGW_DEFAULT, vpc_id: VPC_DEFAULT, auto: true },
      { id: IGW_SHOP, vpc_id: VPC_SHOP, tags: { Name: "shop-igw", Project: "shop" } },
    ],
    rtbs: [
      { id: rtbDefault, vpc_id: VPC_DEFAULT, routes: [{ destination: "0.0.0.0/0", gateway_id: IGW_DEFAULT, origin: "CreateRoute" }], associations: [mainAssoc("default")] },
      { id: rtbShopMain, vpc_id: VPC_SHOP, routes: [], associations: [mainAssoc("shop-main")], tags: { Name: "shop-main" } },
      {
        id: rtbPublic, vpc_id: VPC_SHOP, routes: [{ destination: "0.0.0.0/0", gateway_id: IGW_SHOP, origin: "CreateRoute" }], tags: { Name: "shop-public-rt" },
        associations: [{ id: stableId("rtbassoc-", "pub-a"), subnet_id: SUBNET.publicA }, { id: stableId("rtbassoc-", "pub-b"), subnet_id: SUBNET.publicB }],
      },
      {
        id: rtbPrivate, vpc_id: VPC_SHOP, routes: [{ destination: "0.0.0.0/0", target: NAT_GATEWAY, target_kind: "nat-gateway", origin: "CreateRoute" }], tags: { Name: "shop-private-rt" },
        associations: [{ id: stableId("rtbassoc-", "priv-a"), subnet_id: SUBNET.privateA }, { id: stableId("rtbassoc-", "priv-b"), subnet_id: SUBNET.privateB }],
      },
      { id: rtbDev, vpc_id: VPC_DEV, routes: [], associations: [mainAssoc("dev-main"), { id: stableId("rtbassoc-", "dev-a"), subnet_id: SUBNET_DEV }], tags: { Name: "dev-main" } },
    ],
    extraUsed: {
      [SUBNET.privateA]: 6, // RDS, ElastiCache, ECS tasks
      [SUBNET.privateB]: 5,
      [SUBNET.publicB]: 1,
      [SUBNET_DEV]: 2,
    },
  }
}

// ---- routes ----

function findVpc(id: string): VpcRec {
  const v = vpcState().vpcs.find((x) => x.id === id)
  if (!v) throw notFound("vpc", id)
  return v
}
function findSubnet(id: string): SubnetRec {
  const v = vpcState().subnets.find((x) => x.id === id)
  if (!v) throw notFound("subnet", id)
  return v
}
function findSg(id: string): SecurityGroup {
  const g = vpcState().sgs.find((x) => x.id === id)
  if (!g) throw notFound("security group", id)
  return g
}
const igwErr = (id: string) => err(400, "InvalidInternetGatewayID.NotFound", `The internetGateway ID '${id}' does not exist`)
const rtbErr = (id: string) => err(400, "InvalidRouteTableID.NotFound", `The routeTable ID '${id}' does not exist`)
function findRtb(id: string): RouteTable {
  const t = vpcState().rtbs.find((x) => x.id === id)
  if (!t) throw rtbErr(id)
  return t
}

/** sgInUse: referenced by an instance, an Auto Scaling launch configuration or a load balancer. */
function sgInUse(id: string, st: State): boolean {
  if ((st.ec2?.instances ?? []).some((i: { state: string; security_groups: string[] }) => i.state !== "terminated" && i.security_groups.includes(id))) return true
  return (st.autoscaling?.groups ?? []).some((g: { launch: { security_group_ids: string[] | null } }) => (g.launch.security_group_ids ?? []).includes(id))
}

function routes(r: Router) {
  // ---- VPCs ----
  r.get("/api/v1/vpc/vpcs", () => vpcState().vpcs.map(vpcView))
  r.get("/api/v1/vpc/vpcs/:id", ({ params }) => vpcView(findVpc(params.id)))
  r.post("/api/v1/vpc/vpcs", ({ body }) => {
    const s = vpcState()
    const c = parseCidr(String(body?.cidr ?? ""))
    if (!c) throw badRequest(`cidr "${body?.cidr ?? ""}" is not a valid IPv4 CIDR block`)
    if (c.bits < 16 || c.bits > 28) throw badRequest("VPC CIDR must be between /16 and /28")
    for (const v of s.vpcs) {
      const o = parseCidr(v.cidr)
      if (o && overlaps(c, o)) throw conflict(`cidr ${c.text} overlaps existing network ${v.network}`)
    }
    const internet = body?.internet_access !== false
    const name = String(body?.name ?? "").trim()
    const id = uid("vpc-")
    const v: VpcRec = {
      id, name, cidr: c.text, network: `homecloud-${name || id}`, default: false, internet_access: false, state: "available", created_at: new Date().toISOString(),
      tags: name ? { Name: name } : null, arn: arn("ec2", `vpc/${id}`),
    }
    s.vpcs.push(v)
    const dsg = uid("sg-")
    s.sgs.push({ id: dsg, vpc_id: id, name: "default", description: "default VPC security group", ingress: [selfRule(dsg)], self_rule: true, created_at: v.created_at })
    const t: RouteTable = { id: rtbId(), vpc_id: id, routes: [], associations: [{ id: uid("rtbassoc-"), main: true }] }
    if (internet) {
      const g: InternetGateway = { id: uid("igw-"), vpc_id: id, auto: true }
      s.igws.push(g)
      t.routes = [{ destination: "0.0.0.0/0", gateway_id: g.id, origin: "CreateRoute" }]
    }
    s.rtbs.push(t)
    applyInternet(id)
    return vpcView(v)
  })
  r.del("/api/v1/vpc/vpcs/:id", ({ params }) => {
    const s = vpcState()
    const v = findVpc(params.id)
    for (const sn of s.subnets.filter((x) => x.vpc_id === v.id)) {
      if (subnetUsed(sn.id) > 0) throw err(409, "DependencyViolation", `vpc ${v.id} has resources in subnet ${sn.id}; terminate them first`)
    }
    s.subnets = s.subnets.filter((x) => x.vpc_id !== v.id)
    s.sgs = s.sgs.filter((x) => x.vpc_id !== v.id)
    s.rtbs = s.rtbs.filter((x) => x.vpc_id !== v.id)
    s.igws = s.igws.filter((g) => !(g.vpc_id === v.id && g.auto))
    for (const g of s.igws) if (g.vpc_id === v.id) delete g.vpc_id
    s.vpcs = s.vpcs.filter((x) => x.id !== v.id)
    return null
  })

  // ---- subnets ----
  r.get("/api/v1/vpc/subnets", ({ query }) =>
    vpcState()
      .subnets.filter((s) => !query.vpc_id || s.vpc_id === query.vpc_id)
      .map(subnetView),
  )
  r.post("/api/v1/vpc/subnets", ({ body }) => {
    const s = vpcState()
    const v = s.vpcs.find((x) => x.id === body?.vpc_id)
    if (!v) throw notFound("vpc", String(body?.vpc_id ?? ""))
    const c = parseCidr(String(body?.cidr ?? ""))
    if (!c) throw badRequest(`cidr "${body?.cidr ?? ""}" is not a valid IPv4 CIDR block`)
    const vp = parseCidr(v.cidr)!
    if (c.base < vp.base || c.base + c.size > vp.base + vp.size || c.bits > 28) throw badRequest(`subnet ${c.text} must be inside ${vp.text} and no smaller than /28`)
    for (const o of s.subnets) {
      const oc = parseCidr(o.cidr)
      if (o.vpc_id === v.id && oc && overlaps(c, oc)) throw conflict(`cidr ${c.text} conflicts with subnet ${o.id} (${o.cidr})`)
    }
    const sn: SubnetRec = {
      id: uid("subnet-"), vpc_id: v.id, name: String(body?.name ?? ""), cidr: c.text, availability_zone: body?.availability_zone || `${REGION}a`, default: false, created_at: new Date().toISOString(),
    }
    s.subnets.push(sn)
    return subnetView(sn)
  })
  r.del("/api/v1/vpc/subnets/:id", ({ params }) => {
    const s = vpcState()
    const sn = findSubnet(params.id)
    if (subnetUsed(sn.id) > 0) throw err(409, "DependencyViolation", `subnet ${sn.id} still has resources`)
    s.subnets = s.subnets.filter((x) => x.id !== sn.id)
    for (const t of s.rtbs) t.associations = (t.associations ?? []).filter((a) => a.subnet_id !== sn.id)
    return null
  })

  // ---- security groups ----
  r.get("/api/v1/vpc/security-groups", ({ query }) => vpcState().sgs.filter((g) => !query.vpc_id || g.vpc_id === query.vpc_id))
  r.get("/api/v1/vpc/security-groups/:id", ({ params }) => findSg(params.id))
  r.post("/api/v1/vpc/security-groups", ({ body }) => {
    const s = vpcState()
    const vpcId = body?.vpc_id || VPC_DEFAULT
    findVpc(vpcId)
    const name = String(body?.name ?? "")
    if (!name.trim() || name.startsWith("sg-") || name.length > 255) throw badRequest("name is required, may not start with sg- and must be at most 255 characters")
    if (s.sgs.some((g) => g.vpc_id === vpcId && g.name === name)) throw err(409, "InvalidGroup.Duplicate", `security group "${name}" already exists in ${vpcId}`)
    const g: SecurityGroup = {
      id: uid("sg-"), vpc_id: vpcId, name, description: String(body?.description ?? ""), created_at: new Date().toISOString(),
      ingress: ((body?.ingress ?? []) as Partial<SecurityGroupRule>[]).map(normalizeRule),
    }
    s.sgs.push(g)
    return g
  })
  r.post("/api/v1/vpc/security-groups/:id/ingress", ({ params, body }) => {
    const g = findSg(params.id)
    const r = normalizeRule(body ?? {})
    if (r.source_group) findSg(r.source_group)
    g.ingress.push(r)
    return g
  })
  r.del("/api/v1/vpc/security-groups/:id/ingress/:rule", ({ params }) => {
    const g = findSg(params.id)
    const n = g.ingress.length
    g.ingress = g.ingress.filter((x) => x.id !== params.rule)
    if (g.ingress.length === n) throw notFound("rule", params.rule)
    return g
  })
  r.del("/api/v1/vpc/security-groups/:id", ({ params }) => {
    const s = vpcState()
    const g = findSg(params.id)
    if (g.name === "default") throw err(409, "CannotDelete", "the default security group cannot be deleted")
    if (sgInUse(g.id, getState())) throw err(409, "DependencyViolation", `security group ${g.id} is in use`)
    const ref = s.sgs.find((o) => o.id !== g.id && o.ingress.some((x) => x.source_group === g.id))
    if (ref) throw err(409, "DependencyViolation", `security group ${g.id} is referenced by a rule of ${ref.id}`)
    s.sgs = s.sgs.filter((x) => x.id !== g.id)
    return null
  })

  // ---- internet gateways ----
  const IGW = "/api/v1/ec2/internet-gateways"
  r.get(IGW, () => [...vpcState().igws].sort((a, b) => a.id.localeCompare(b.id)))
  r.post(IGW, ({ body }) => {
    const g: InternetGateway = { id: uid("igw-"), tags: body?.tags && Object.keys(body.tags).length ? body.tags : undefined }
    vpcState().igws.push(g)
    return g
  })
  r.post(`${IGW}/:id/attach`, ({ params, body }) => {
    const s = vpcState()
    const g = s.igws.find((x) => x.id === params.id)
    if (!g) throw igwErr(params.id)
    if (!s.vpcs.some((v) => v.id === body?.vpc_id)) throw err(400, "InvalidVpcID.NotFound", `The vpc ID '${body?.vpc_id}' does not exist`)
    if (s.igws.some((x) => x.vpc_id === body.vpc_id && x.id !== g.id)) throw err(400, "InvalidParameterValue", `Network ${body.vpc_id} already has an internet gateway attached`)
    if (g.vpc_id) throw err(400, "Resource.AlreadyAssociated", `resource ${g.id} is already attached to network ${g.vpc_id}`)
    g.vpc_id = body.vpc_id
    applyInternet(body.vpc_id)
    return null
  })
  r.post(`${IGW}/:id/detach`, ({ params, body }) => {
    const g = vpcState().igws.find((x) => x.id === params.id)
    if (!g) throw igwErr(params.id)
    if (!g.vpc_id || g.vpc_id !== body?.vpc_id) throw err(400, "Gateway.NotAttached", `resource ${g.id} is not attached to network ${body?.vpc_id}`)
    const vpc = g.vpc_id
    delete g.vpc_id
    applyInternet(vpc)
    return null
  })
  r.del(`${IGW}/:id`, ({ params }) => {
    const s = vpcState()
    const g = s.igws.find((x) => x.id === params.id)
    if (!g) throw igwErr(params.id)
    if (g.vpc_id) throw err(400, "DependencyViolation", `The internetGateway '${g.id}' has dependencies and cannot be deleted.`)
    s.igws = s.igws.filter((x) => x.id !== g.id)
    return null
  })

  // ---- route tables ----
  const RTB = "/api/v1/ec2/route-tables"
  r.get(RTB, () => [...vpcState().rtbs].sort((a, b) => a.id.localeCompare(b.id)))
  r.post(RTB, ({ body }) => {
    findVpc(String(body?.vpc_id ?? ""))
    const t: RouteTable = { id: rtbId(), vpc_id: body.vpc_id, routes: [], associations: [], tags: body?.tags && Object.keys(body.tags).length ? body.tags : undefined }
    vpcState().rtbs.push(t)
    return t
  })
  r.del(`${RTB}/:id`, ({ params }) => {
    const s = vpcState()
    const t = findRtb(params.id)
    if ((t.associations ?? []).length > 0) throw err(400, "DependencyViolation", `The routeTable '${t.id}' has dependencies and cannot be deleted.`)
    s.rtbs = s.rtbs.filter((x) => x.id !== t.id)
    applyInternet(t.vpc_id)
    return null
  })
  r.post(`${RTB}/:id/routes`, ({ params, body }) => {
    const s = vpcState()
    const t = findRtb(params.id)
    const c = parseCidr(String(body?.destination ?? ""))
    if (!c) throw err(400, "InvalidParameterValue", `Value (${body?.destination ?? ""}) for parameter destinationCidrBlock is invalid`)
    const route: RouteEntry = { destination: c.text, origin: "CreateRoute" }
    const gw = String(body?.gateway_id ?? "")
    if (gw.startsWith("igw-")) {
      const g = s.igws.find((x) => x.id === gw)
      if (!g) throw igwErr(gw)
      if (g.vpc_id !== t.vpc_id) throw err(400, "InvalidParameterValue", `route table and network gateway ${g.id} belong to different networks`)
      route.gateway_id = gw
    } else if (gw) {
      route.gateway_id = gw
    } else if (body?.target) {
      route.target = body.target
      if (body.target_kind) route.target_kind = body.target_kind
    } else throw err(400, "InvalidParameterValue", "a route needs a target")
    const rs = (t.routes ??= [])
    const n = rs.findIndex((x) => x.destination === route.destination)
    if (body?.replace) {
      if (n < 0) throw err(400, "InvalidRoute.NotFound", `no route with destination-cidr-block ${route.destination} in route table ${t.id}`)
      rs[n] = route
    } else if (n >= 0) throw err(400, "RouteAlreadyExists", `The route identified by ${route.destination} already exists.`)
    else rs.push(route)
    applyInternet(t.vpc_id)
    return null
  })
  r.del(`${RTB}/:id/routes`, ({ params, query }) => {
    const t = findRtb(params.id)
    const dest = parseCidr(query.destination ?? "")?.text ?? query.destination
    const before = (t.routes ?? []).length
    t.routes = (t.routes ?? []).filter((x) => x.destination !== dest)
    if (t.routes.length === before) throw err(400, "InvalidRoute.NotFound", `no route with destination-cidr-block ${dest} in route table ${t.id}`)
    applyInternet(t.vpc_id)
    return null
  })
  r.post(`${RTB}/:id/associations`, ({ params, body }) => {
    const s = vpcState()
    const t = findRtb(params.id)
    const sn = s.subnets.find((x) => x.id === body?.subnet_id)
    if (!sn) throw err(400, "InvalidSubnetID.NotFound", `The subnet ID '${body?.subnet_id ?? ""}' does not exist`)
    if (sn.vpc_id !== t.vpc_id) throw err(400, "InvalidParameterValue", `route table ${t.id} and subnet ${sn.id} belong to different networks`)
    const other = s.rtbs.find((o) => (o.associations ?? []).some((a) => a.subnet_id === sn.id))
    if (other) throw err(400, "Resource.AlreadyAssociated", `the specified association for route table ${other.id} conflicts with an existing association`)
    const a = { id: uid("rtbassoc-"), subnet_id: sn.id }
    ;(t.associations ??= []).push(a)
    return { association_id: a.id }
  })
  r.del(`${RTB}/:id/associations/:assoc`, ({ params }) => {
    const s = vpcState()
    const t = s.rtbs.find((x) => (x.associations ?? []).some((a) => a.id === params.assoc))
    const a = t?.associations?.find((x) => x.id === params.assoc)
    if (!t || !a) throw err(400, "InvalidAssociationID.NotFound", `The association ID '${params.assoc}' does not exist`)
    if (a.main) throw err(400, "InvalidParameterValue", `cannot disassociate the main route table association ${a.id}`)
    t.associations = (t.associations ?? []).filter((x) => x.id !== a.id)
    return null
  })
}

const service: DemoService = {
  name: "vpc",
  seed,
  routes,
}

export default service

