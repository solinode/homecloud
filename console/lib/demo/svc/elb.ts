import { badRequest, conflict, err, getState, later, notFound, type DemoService, type Router } from "../engine"
import { DAY, arn, ago, hashStr, hex, stableId } from "../util"
import { INSTANCE, NAMES, SUBNET, VPC_SHOP } from "../ids"
import { allocIp, defaultSubnet, vpcState } from "./vpc"
import { instanceHooks } from "./ec2"
import type { ElbListener, ElbRule, ElbTarget, Instance, LoadBalancer, TargetGroup } from "@/lib/types"

// Elastic Load Balancing: load balancers, listeners, rules, target groups and
// target health (/api/v1/elb). Health of instance targets follows the instance's
// real (demo) state so stopping a web server turns its target unhealthy.

export interface ElbState {
  lbs: LoadBalancer[]
  tgs: TargetGroup[]
}
export const elbState = () => getState().elb as ElbState

const uuidish = (n: string) => {
  const h = stableId("", n, 32)
  return `${h.slice(0, 8)}-${h.slice(8, 12)}-4${h.slice(13, 16)}-a${h.slice(17, 20)}-${h.slice(20, 32)}`
}
/** ACM certificate the ALB's HTTPS listener uses (same derivation as an "shop.example.com" certificate). */
export const SHOP_CERT_ARN = arn("acm", `certificate/${uuidish("shop.example.com")}`)

const lbArn = (name: string) => arn("elasticloadbalancing", `loadbalancer/app/${name}/${stableId("", "lb/" + name, 16)}`)
const tgArn = (name: string) => arn("elasticloadbalancing", `targetgroup/${name}/${stableId("", "tg/" + name, 16)}`)
const hostPort = (name: string, port: number) => 31000 + ((hashStr(`${name}:${port}`) + port) % 1500)
const TG_API = "shop-api-tg"

function seed(): ElbState {
  const tg = (name: string, port: number, path: string, age: number, targets: ElbTarget[], vpc = VPC_SHOP, hc: Partial<TargetGroup["health_check"]> = {}): TargetGroup => ({
    name, arn: tgArn(name), protocol: "HTTP", port, vpc_id: vpc, created_at: ago(age),
    health_check: { path, interval_seconds: 15, healthy_threshold: 2, unhealthy_threshold: 2, ...hc }, targets,
  })
  const t = (id: string, port: number, ip: string, health = "healthy", reason?: string): ElbTarget => ({ id, port, ip, health, reason })
  const rule = (priority: number, tg: string, o: { path_prefix?: string; host_header?: string }): ElbRule => ({ id: hex(12), priority, target_group: tg, ...o })
  const mkLb = (o: Partial<LoadBalancer> & { name: string }): LoadBalancer => ({
    arn: lbArn(o.name), dns_name: `${o.name}.elb.internal`, scheme: "internet-facing", vpc_id: VPC_SHOP, subnet_id: SUBNET.publicA, private_ip: "10.0.1.7", listeners: [], state: "active",
    container_id: hex(64), public_ports: {}, public_host: "localhost", created_at: ago(90 * DAY), tags: null, ...o,
  })
  return {
    tgs: [
      tg(NAMES.tgWeb, 80, "/healthz", 90 * DAY, [t(INSTANCE.web1, 80, "10.0.11.21"), t(INSTANCE.web2, 80, "10.0.12.34")], VPC_SHOP, { healthy_threshold: 3 }),
      tg(TG_API, 8080, "/health", 60 * DAY, [
        t(stableId("", "task-a", 32), 8080, "10.0.11.87"),
        t(stableId("", "task-b", 32), 8080, "10.0.12.91"),
        t(stableId("", "task-c", 32), 8080, "10.0.12.94", "unhealthy", "Health checks failed with these codes: [503]"),
      ]),
    ],
    lbs: [
      mkLb({
        name: NAMES.alb,
        public_ports: { "80/tcp": hostPort(NAMES.alb, 80), "443/tcp": hostPort(NAMES.alb, 443) },
        tags: { Environment: "production", Project: "shop" },
        listeners: [
          { id: hex(12), port: 80, protocol: "HTTP", redirect_https_port: 443, default_target_group: "", rules: [] },
          {
            id: hex(12), port: 443, protocol: "HTTPS", certificate_arn: SHOP_CERT_ARN, default_target_group: NAMES.tgWeb,
            rules: [rule(10, TG_API, { path_prefix: "/api/" }), rule(20, NAMES.tgWeb, { host_header: "admin.shop.example.com" })],
          },
        ],
      }),
      mkLb({
        name: "shop-internal-alb", scheme: "internal", subnet_id: SUBNET.privateB, private_ip: "10.0.12.15", created_at: ago(58 * DAY), tags: { Environment: "production", Project: "shop" },
        listeners: [{ id: hex(12), port: 8080, protocol: "HTTP", default_target_group: TG_API, rules: [] }],
      }),
    ],
  }
}

// ---- helpers ----

const instances = () => (getState().ec2?.instances ?? []) as Instance[]
const IPV4 = /^\d{1,3}(\.\d{1,3}){3}$/

const lbByName = (name: string) => {
  const lb = elbState().lbs.find((l) => l.name === name)
  if (!lb) throw notFound("load balancer", name)
  return lb
}
const tgByName = (name: string) => {
  const t = elbState().tgs.find((x) => x.name === name)
  if (!t) throw notFound("target group", name)
  return t
}

const usedBy = (tg: string) => elbState().lbs.filter((lb) => lb.listeners.some((l) => l.default_target_group === tg || l.rules.some((r) => r.target_group === tg)))

function tgView(tg: TargetGroup): TargetGroup {
  const used = usedBy(tg.name).some((l) => l.state === "active")
  const targets = tg.targets.map((t): ElbTarget => {
    const inst = instances().find((i) => i.id === t.id)
    const ip = inst?.private_ip ?? t.ip
    if (!used) return { ...t, ip, health: "unused", reason: "not attached to an active load balancer" }
    if (inst) {
      if (inst.state === "running") return { ...t, ip, health: t.health === "unhealthy" ? "unhealthy" : "healthy", reason: t.health === "unhealthy" ? t.reason : undefined }
      if (inst.state === "pending") return { ...t, ip, health: "initial", reason: "Target registration is in progress" }
      return { ...t, ip: "", health: "unavailable", reason: `Target is in the ${inst.state} state` }
    }
    return { ...t, ip }
  })
  return { ...tg, targets }
}

function normalizeHc(h: Partial<TargetGroup["health_check"]>): TargetGroup["health_check"] {
  const path = h.path || "/"
  if (!path.startsWith("/") || /[ \n]/.test(path)) throw badRequest("health check path must start with /")
  const interval = h.interval_seconds || 15
  if (interval < 5 || interval > 300) throw badRequest("interval_seconds must be 5-300")
  return { path, interval_seconds: interval, healthy_threshold: h.healthy_threshold || 2, unhealthy_threshold: h.unhealthy_threshold || 2 }
}

/** registerTarget adds a target to a target group (used by Auto Scaling and ECS). */
export function registerTarget(tgName: string, id: string, port?: number, ip = "") {
  const tg = elbState().tgs.find((x) => x.name === tgName)
  if (!tg) return
  const p = port ?? tg.port
  tg.targets = tg.targets.filter((x) => !(x.id === id && x.port === p))
  tg.targets.push({ id, port: p, ip, health: "initial" })
}
export function deregisterTarget(tgName: string, id: string) {
  const tg = elbState().tgs.find((x) => x.name === tgName)
  if (tg) tg.targets = tg.targets.filter((x) => x.id !== id)
}

function checkListener(l: Partial<ElbListener>, lb: { vpc_id: string }, taken: ElbListener[]): ElbListener {
  const port = Number(l.port)
  if (!Number.isInteger(port) || port < 1 || port > 65535) throw badRequest("listener port must be 1-65535")
  if (taken.some((o) => o.port === port)) throw conflict(`a listener already uses port ${port}`)
  const protocol = String(l.protocol || "HTTP").toUpperCase()
  const out: ElbListener = { id: hex(12), port, protocol, default_target_group: l.default_target_group ?? "", rules: [], public_port: l.public_port || undefined }
  if (protocol === "HTTP") {
    const red = Number(l.redirect_https_port ?? 0)
    if (red < 0 || red > 65535) throw badRequest("redirect_https_port must be a port number")
    if (red > 0 && l.default_target_group) throw badRequest("a listener either redirects to HTTPS or forwards to a target group, not both")
    if (red > 0) return { ...out, redirect_https_port: red, default_target_group: "" }
  } else if (protocol === "HTTPS") {
    if (!l.certificate_arn || !l.certificate_arn.startsWith("arn:aws:acm:")) throw badRequest("HTTPS listeners need a valid certificate_arn (see ACM)")
    out.certificate_arn = l.certificate_arn
  } else throw badRequest("protocol must be HTTP or HTTPS")
  const tg = elbState().tgs.find((x) => x.name === l.default_target_group)
  if (!tg) throw notFound("target group", l.default_target_group)
  if (tg.vpc_id !== lb.vpc_id) throw badRequest(`target group ${tg.name} is in ${tg.vpc_id}, not ${lb.vpc_id}`)
  return out
}

function checkRedirects(ls: ElbListener[]) {
  for (const l of ls) {
    if (l.redirect_https_port && !ls.some((o) => o.protocol === "HTTPS" && o.port === l.redirect_https_port)) {
      throw badRequest(`listener ${l.port} redirects to port ${l.redirect_https_port}, which has no HTTPS listener`)
    }
  }
}

function publishPorts(lb: LoadBalancer) {
  lb.public_ports = lb.scheme === "internet-facing" ? Object.fromEntries(lb.listeners.map((l) => [`${l.port}/tcp`, l.public_port || hostPort(lb.name, l.port)])) : {}
}

/** reprovision recreates the (nginx) load balancer container: provisioning, then active. */
function reprovision(lb: LoadBalancer) {
  lb.state = "provisioning"
  lb.public_ports = {}
  const name = lb.name
  later(2600, () => {
    const cur = elbState().lbs.find((l) => l.name === name)
    if (!cur || cur.state !== "provisioning") return
    cur.state = "active"
    cur.container_id = hex(64)
    publishPorts(cur)
  })
}

function renderConfig(lb: LoadBalancer): string {
  const up = (n: string) => "tg_" + n.replace(/[^a-zA-Z0-9]/g, "_")
  const used = new Set<string>()
  for (const l of lb.listeners) {
    if (l.default_target_group) used.add(l.default_target_group)
    for (const r of l.rules) used.add(r.target_group)
  }
  const out: string[] = [
    "# generated by HomeCloud; do not edit",
    "map $http_upgrade $connection_upgrade { default upgrade; '' close; }",
    `log_format hc '$remote_addr "$request" $status $body_bytes_sent $request_time "$http_user_agent" upstream=$upstream_addr';`,
  ]
  for (const n of [...used].sort()) {
    const tg = elbState().tgs.find((x) => x.name === n)
    out.push(`upstream ${up(n)} {`, `  zone ${up(n)} 64k;`)
    const healthy = tgView(tg ?? ({ name: n, targets: [] } as unknown as TargetGroup)).targets.filter((t) => t.health === "healthy" || t.health === "initial")
    if (healthy.length) for (const t of healthy) out.push(`  server ${t.ip}:${t.port} max_fails=2 fail_timeout=10s;`)
    else out.push("  server 127.0.0.1:9 down; # no healthy targets")
    out.push("  keepalive 16;", "}")
  }
  const proxy = (tg: string) => [
    `    proxy_pass http://${up(tg)};`, "    proxy_http_version 1.1;", "    proxy_set_header Host $host;", "    proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;",
    "    proxy_set_header X-Forwarded-Proto $scheme;", "    proxy_set_header X-Forwarded-Port $server_port;", "    proxy_set_header Upgrade $http_upgrade;",
    "    proxy_set_header Connection $connection_upgrade;", "    proxy_next_upstream error timeout http_502 http_503;",
  ]
  for (const l of lb.listeners) {
    out.push("server {", `  listen ${l.port}${l.protocol === "HTTPS" ? " ssl" : ""};`, "  access_log /var/log/nginx/access.log hc;")
    if (l.protocol === "HTTPS") out.push(`  ssl_certificate /etc/nginx/certs/${stableId("", l.certificate_arn ?? "", 12)}.crt;`, `  ssl_certificate_key /etc/nginx/certs/${stableId("", l.certificate_arn ?? "", 12)}.key;`)
    if (l.redirect_https_port) {
      out.push("  location / {", `    return 301 https://$host:${l.redirect_https_port}$request_uri;`, "  }", "}")
      continue
    }
    for (const r of [...l.rules].sort((a, b) => a.priority - b.priority)) {
      if (r.host_header) out.push(`  # rule ${r.priority}: host ${r.host_header}`)
      out.push(`  location ${r.path_prefix ? r.path_prefix : "/"} {`)
      if (r.host_header) out.push(`    if ($host != "${r.host_header}") { return 404; }`)
      out.push(...proxy(r.target_group), "  }")
    }
    out.push("  location / {", ...proxy(l.default_target_group), "  }", "}")
  }
  return out.join("\n") + "\n"
}

function onLoad(st: { elb?: ElbState }) {
  for (const lb of st.elb?.lbs ?? []) {
    if (lb.state === "provisioning") {
      lb.state = "active"
      lb.container_id ??= hex(64)
      publishPorts(lb)
    }
  }
}

const NAME_RE = /^[a-zA-Z0-9-]{1,32}$/

function routes(r: Router) {
  const L = "/api/v1/elb/load-balancers"
  r.get(L, () => elbState().lbs)
  r.get(`${L}/:name`, ({ params }) => lbByName(params.name))
  r.get(`${L}/:name/config`, ({ params }) => ({ nginx_conf: renderConfig(lbByName(params.name)) }))
  r.post(L, ({ body }) => {
    const s = elbState()
    const name = String(body?.name ?? "")
    if (!NAME_RE.test(name)) throw badRequest("load balancer names are up to 32 letters, digits and hyphens")
    if (s.lbs.some((l) => l.name === name)) throw conflict(`load balancer "${name}" already exists`)
    const scheme = body?.scheme || "internet-facing"
    if (scheme !== "internet-facing" && scheme !== "internal") throw badRequest("scheme must be internet-facing or internal")
    if (!Array.isArray(body?.listeners) || body.listeners.length === 0) throw badRequest("at least one listener is required")
    const sn = body?.subnet_id ? vpcState().subnets.find((x) => x.id === body.subnet_id) : defaultSubnet()
    if (!sn) throw notFound("subnet", body?.subnet_id)
    const ls: ElbListener[] = []
    for (const l of body.listeners) ls.push(checkListener(l, { vpc_id: sn.vpc_id }, ls))
    checkRedirects(ls)
    const lb: LoadBalancer = {
      name, arn: lbArn(name), dns_name: `${name}.elb.internal`, scheme, vpc_id: sn.vpc_id, subnet_id: sn.id, private_ip: allocIp(sn.id), listeners: ls, state: "provisioning",
      public_ports: {}, public_host: "localhost", created_at: new Date().toISOString(), tags: body?.tags && Object.keys(body.tags).length ? body.tags : null,
    }
    s.lbs.push(lb)
    reprovision(lb)
    return lb
  })
  r.del(`${L}/:name`, ({ params }) => {
    const s = elbState()
    const lb = lbByName(params.name)
    s.lbs = s.lbs.filter((l) => l.name !== lb.name)
    return null
  })
  r.post(`${L}/:name/listeners`, ({ params, body }) => {
    const lb = lbByName(params.name)
    const l = checkListener(body ?? {}, lb, lb.listeners)
    lb.listeners.push(l)
    try {
      checkRedirects(lb.listeners)
    } catch (e) {
      lb.listeners.pop()
      throw e
    }
    reprovision(lb)
    return lb
  })
  r.del(`${L}/:name/listeners/:id`, ({ params }) => {
    const lb = lbByName(params.name)
    const rest = lb.listeners.filter((l) => l.id !== params.id)
    if (rest.length === lb.listeners.length) throw notFound("listener", params.id)
    if (rest.length === 0) throw badRequest("a load balancer needs at least one listener")
    checkRedirects(rest)
    lb.listeners = rest
    reprovision(lb)
    return lb
  })
  r.post(`${L}/:name/listeners/:id/rules`, ({ params, body }) => {
    const lb = lbByName(params.name)
    if (!body?.path_prefix && !body?.host_header) throw badRequest("a rule needs a path_prefix or host_header condition")
    if (body.path_prefix && !/^\/[\w.~%/*-]*$/.test(body.path_prefix)) throw badRequest("path_prefix must start with / and use only letters, digits and . _ ~ % / * -")
    if (body.host_header && !/^(\*\.)?[a-zA-Z0-9]([a-zA-Z0-9.-]*[a-zA-Z0-9])?$/.test(body.host_header)) throw badRequest("host_header must be a host name, optionally starting with *.")
    if (body.target_group) {
      const tg = elbState().tgs.find((x) => x.name === body.target_group)
      if (!tg) throw notFound("target group", body.target_group)
      if (tg.vpc_id !== lb.vpc_id) throw badRequest(`target group ${tg.name} is in another VPC`)
    }
    const l = lb.listeners.find((x) => x.id === params.id)
    if (!l) throw notFound("listener", params.id)
    for (const o of l.rules) {
      if (o.path_prefix === (body.path_prefix || undefined) && o.host_header === (body.host_header || undefined)) throw conflict(`listener already has a rule for host "${body.host_header ?? ""}" and path "${body.path_prefix ?? ""}"`)
      if (body.priority && o.priority === body.priority) throw err(409, "PriorityInUse", `listener already has a rule with priority ${body.priority}`)
    }
    l.rules.push({
      id: hex(12), priority: body.priority || l.rules.length + 1, path_prefix: body.path_prefix || undefined, host_header: body.host_header || undefined, target_group: body.target_group ?? "",
    })
    return lb
  })
  r.del(`${L}/:name/listeners/:id/rules/:rule`, ({ params }) => {
    const lb = lbByName(params.name)
    const l = lb.listeners.find((x) => x.id === params.id)
    if (!l) throw notFound("listener", params.id)
    const n = l.rules.length
    l.rules = l.rules.filter((x) => x.id !== params.rule)
    if (l.rules.length === n) throw notFound("rule", params.rule)
    return lb
  })

  // ---- target groups ----
  const T = "/api/v1/elb/target-groups"
  r.get(T, () => elbState().tgs.map(tgView))
  r.get(`${T}/:name`, ({ params }) => tgView(tgByName(params.name)))
  r.post(T, ({ body }) => {
    const s = elbState()
    const name = String(body?.name ?? "")
    if (!NAME_RE.test(name)) throw badRequest("target group names are up to 32 letters, digits and hyphens")
    if (s.tgs.some((x) => x.name === name)) throw conflict(`target group "${name}" already exists`)
    const port = Number(body?.port)
    if (!Number.isInteger(port) || port < 1 || port > 65535) throw badRequest("port must be 1-65535")
    const vpc = body?.vpc_id || vpcState().vpcs.find((v) => v.default)?.id || ""
    if (!vpcState().vpcs.some((v) => v.id === vpc)) throw notFound("vpc", vpc)
    const tg: TargetGroup = { name, arn: tgArn(name), protocol: "HTTP", port, vpc_id: vpc, health_check: normalizeHc(body?.health_check ?? {}), targets: [], created_at: new Date().toISOString() }
    s.tgs.push(tg)
    return tg
  })
  r.patch(`${T}/:name`, ({ params, body }) => {
    const tg = tgByName(params.name)
    tg.health_check = normalizeHc(body ?? {})
    return tgView(tg)
  })
  r.del(`${T}/:name`, ({ params }) => {
    const s = elbState()
    const tg = tgByName(params.name)
    const u = usedBy(tg.name)
    if (u.length) throw err(409, "ResourceInUse", `target group ${tg.name} is used by ${u.map((l) => l.name).join(", ")}`)
    s.tgs = s.tgs.filter((x) => x.name !== tg.name)
    return null
  })
  r.post(`${T}/:name/targets`, ({ params, body }) => {
    const tg = tgByName(params.name)
    for (const t of (body?.targets ?? []) as { id: string; port?: number }[]) {
      const inst = instances().find((i) => i.id === t.id)
      let ip = ""
      if (inst && inst.state === "running") {
        if (inst.vpc_id !== tg.vpc_id) throw badRequest(`target ${t.id} is in ${inst.vpc_id}, not the target group's VPC ${tg.vpc_id}`)
        ip = inst.private_ip
      } else if (IPV4.test(t.id)) ip = t.id
      else if (/^[0-9a-f]{32}$/.test(t.id)) ip = `10.0.11.${100 + (hashStr(t.id) % 100)}`
      else throw badRequest(`target "${t.id}" is not a known instance or task`)
      registerTarget(tg.name, t.id, t.port || tg.port, ip)
    }
    return tgView(tg)
  })
  r.del(`${T}/:name/targets/:target`, ({ params }) => {
    const tg = tgByName(params.name)
    const n = tg.targets.length
    tg.targets = tg.targets.filter((t) => t.id !== params.target)
    if (tg.targets.length === n) throw notFound("target", params.target)
    return tgView(tg)
  })
}

// terminated instances drop out of their target groups
instanceHooks.gone.push((id) => {
  for (const tg of elbState()?.tgs ?? []) tg.targets = tg.targets.filter((t) => t.id !== id)
})

const service: DemoService = { name: "elb", seed, routes, onLoad: (s) => onLoad(s) }
export default service

