import { badRequest, conflict, err, getState, later, notFound, unavailable, type DemoService, type Req, type Router, type State } from "../engine"
import { AZ_A, AZ_B, DAY, HOUR, MIN, ACCOUNT, REGION, ago, arn, hashStr, hex, stableId, uid } from "../util"
import { AMI, EIP, INSTANCE, KEY_PAIRS, NAMES, SG, SUBNET, VOLUME, VPC_DEFAULT, VPC_SHOP, FS } from "../ids"
import { allocIp, defaultSg, defaultSubnet, vpcState, VPC_DEV, SUBNET_DEV } from "./vpc"
import {
  ASG_GROUP_TAG,
  type CommandResult,
  type ConsoleOutput,
  type ElasticIP,
  type Image,
  type Instance,
  type InstanceType,
  type KeyPair,
  type RunInstancesInput,
  type Snapshot,
  type Tags,
  type Volume,
  type VolumeAttachment,
} from "@/lib/types"

// EC2: instances, AMIs, key pairs, EBS volumes, snapshots, Elastic IPs (/api/v1/ec2).
// Internet gateways and route tables live in vpc.ts.

export interface Ec2State {
  instances: Instance[]
  images: Image[]
  volumes: Volume[]
  snapshots: Snapshot[]
  keys: KeyPair[]
  addresses: ElasticIP[]
}
export const ec2State = (): Ec2State => getState().ec2 as Ec2State

/** Other services (Auto Scaling, ELB) react to instances changing state. */
export const instanceHooks = {
  /** an instance stopped or was terminated (Auto Scaling replaces it) */
  changed: [] as (() => void)[],
  /** an instance was terminated (load balancers drop it from target groups) */
  gone: [] as ((id: string) => void)[],
}

const TYPES: InstanceType[] = [
  { name: "t3.nano", vcpus: 0.5, memory_mb: 512, family: "General purpose (burstable)" },
  { name: "t3.micro", vcpus: 1, memory_mb: 1024, family: "General purpose (burstable)" },
  { name: "t3.small", vcpus: 1, memory_mb: 2048, family: "General purpose (burstable)" },
  { name: "t3.medium", vcpus: 2, memory_mb: 4096, family: "General purpose (burstable)" },
  { name: "t3.large", vcpus: 2, memory_mb: 8192, family: "General purpose (burstable)" },
  { name: "t3.xlarge", vcpus: 4, memory_mb: 16384, family: "General purpose (burstable)" },
  { name: "m5.large", vcpus: 2, memory_mb: 8192, family: "General purpose" },
  { name: "m5.xlarge", vcpus: 4, memory_mb: 16384, family: "General purpose" },
  { name: "c5.large", vcpus: 2, memory_mb: 4096, family: "Compute optimized" },
  { name: "c5.xlarge", vcpus: 4, memory_mb: 8192, family: "Compute optimized" },
  { name: "r5.large", vcpus: 2, memory_mb: 16384, family: "Memory optimized" },
]
export const findType = (n: string) => TYPES.find((t) => t.name === n)

const cat = (id: string, name: string, description: string, ref: string, keep_alive = true): Image => ({
  id, name, description, ref, platform: "linux", keep_alive, owner: "homecloud", state: "available",
})
const CATALOG: Image[] = [
  cat(AMI.ubuntu, "Ubuntu Server 24.04 LTS", "Canonical Ubuntu 24.04 (Noble Numbat)", "ubuntu:24.04"),
  cat("ami-ubuntu-22-04", "Ubuntu Server 22.04 LTS", "Canonical Ubuntu 22.04 (Jammy Jellyfish)", "ubuntu:22.04"),
  cat(AMI.debian, "Debian 12", "Debian GNU/Linux 12 (bookworm)", "debian:12"),
  cat(AMI.amazonLinux, "Amazon Linux 2023", "Amazon Linux 2023 base image", "amazonlinux:2023"),
  cat("ami-rocky-9", "Rocky Linux 9", "Enterprise Linux compatible", "rockylinux:9"),
  cat("ami-fedora-41", "Fedora 41", "Fedora Linux 41", "fedora:41"),
  cat(AMI.alpine, "Alpine Linux 3.20", "Minimal 5 MB Linux", "alpine:3.20"),
  cat("ami-nginx", "NGINX web server", "NGINX serving on port 80", "nginx:alpine", false),
  cat("ami-python-3-12", "Python 3.12", "Debian with Python 3.12", "python:3.12-slim"),
  cat("ami-node-22", "Node.js 22", "Debian with Node.js 22 LTS", "node:22-slim"),
]

export const allImages = (): Image[] => [...CATALOG, ...ec2State().images]

const SUBNET_INFO: Record<string, { vpc: string; az: string }> = {
  [SUBNET.defaultA]: { vpc: VPC_DEFAULT, az: AZ_A },
  [SUBNET.defaultB]: { vpc: VPC_DEFAULT, az: AZ_B },
  [SUBNET.publicA]: { vpc: VPC_SHOP, az: AZ_A },
  [SUBNET.publicB]: { vpc: VPC_SHOP, az: AZ_B },
  [SUBNET.privateA]: { vpc: VPC_SHOP, az: AZ_A },
  [SUBNET.privateB]: { vpc: VPC_SHOP, az: AZ_B },
  [SUBNET_DEV]: { vpc: VPC_DEV, az: AZ_A },
}

const hostPort = (id: string, port: number) => 30000 + ((hashStr(`${id}:${port}`) + port) % 12000)
const dnsOf = (ip: string) => `ip-${ip.replace(/\./g, "-")}.internal`
const PROFILE = arn("iam", "instance-profile/shop-web-role", { region: null })

interface InstSpec {
  id: string
  name: string
  image: string
  ref: string
  type: string
  subnet: string
  ip: string
  sgs: string[]
  state?: Instance["state"]
  launched: number
  volumes?: VolumeAttachment[]
  fs?: Instance["file_systems"]
  key?: string
  tags?: Tags
  keep?: boolean
  ports?: Record<string, number>
  profile?: boolean
  terminated?: number
  reason?: string
}

function mkInstance(o: InstSpec): Instance {
  const t = findType(o.type)!
  const sn = SUBNET_INFO[o.subnet]
  const state = o.state ?? "running"
  return {
    id: o.id,
    name: o.name,
    arn: arn("ec2", `instance/${o.id}`),
    image_id: o.image,
    image_ref: o.ref,
    instance_type: o.type,
    vcpus: t.vcpus,
    memory_mb: t.memory_mb,
    state,
    state_reason: o.reason ?? (state === "stopped" ? "Client.UserInitiatedShutdown" : undefined),
    container_id: o.terminated ? undefined : hex(64),
    vpc_id: sn.vpc,
    subnet_id: o.subnet,
    availability_zone: sn.az,
    private_ip: o.ip,
    private_dns: dnsOf(o.ip),
    security_groups: o.sgs,
    volumes: o.volumes ?? [],
    file_systems: o.fs ?? [],
    keep_alive: o.keep ?? true,
    public_ports: state === "running" ? (o.ports ?? {}) : {},
    public_host: "localhost",
    launch_time: ago(o.launched),
    terminated_at: o.terminated ? ago(o.terminated) : undefined,
    tags: { Name: o.name, ...o.tags },
    key_name: o.key,
    iam_profile_arn: o.profile ? PROFILE : undefined,
    iam_profile_id: o.profile ? "AIPADEMOWEBROLE0000" : undefined,
    metadata_options: { http_tokens: o.profile ? "required" : "optional", http_endpoint: "enabled", hop_limit: o.profile ? 2 : 1, instance_metadata_tags: "disabled" },
  }
}

const PUB_TAGS: Tags = { Environment: "production", Project: "shop" }

function seed(): Ec2State {
  const shopWebRef = `homecloud/ami:${AMI.shopWeb}`
  const mediaFs = { file_system_id: FS.media, mount_path: "/mnt/media", read_only: false }
  const asgTags: Tags = { ...PUB_TAGS, Role: "web", [ASG_GROUP_TAG]: NAMES.asg }
  const vol = (id: string, name: string, size: number, az: string, age: number, extra: Partial<Volume> = {}): Volume => ({
    id, name, size_gb: size, state: "available", availability_zone: az, created_at: ago(age), volume_type: "gp3", tags: { Name: name, ...PUB_TAGS }, ...extra,
  })
  const attached = (v: Volume, inst: string, mount: string, device: string): Volume => ({ ...v, state: "in-use", attached_to: inst, mount_path: mount, device })
  const snap = (id: string, volume_id: string, size: number, age: number, description: string, tags?: Tags): Snapshot => ({
    id, volume_id, volume_size: size, state: "completed", description, start_time: ago(age), completed_at: ago(age - 4 * MIN), encrypted: false, tags,
  })
  const keyMat = (n: string, kind: "ed25519" | "rsa") =>
    kind === "ed25519" ? `ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIDemoNotARealPublicKey${stableId("", n, 10)} ${n}` : `ssh-rsa AAAAB3NzaC1yc2EAAAADAQABAAABgQDemoNotARealPublicKey${stableId("", n, 24)}== ${n}`
  const fp = (n: string, kind: string) => (kind === "ed25519" ? `SHA256:${btoa(stableId("", n, 24)).replace(/=/g, "")}` : stableId("", n, 32).replace(/(..)(?=.)/g, "$1:"))
  return {
    instances: [
      mkInstance({
        id: INSTANCE.web1, name: "shop-web-1", image: AMI.shopWeb, ref: shopWebRef, type: "t3.small", subnet: SUBNET.privateA, ip: "10.0.11.21", sgs: [SG.web], launched: 11 * DAY + 3 * HOUR,
        volumes: [{ volume_id: VOLUME.web1, mount_path: "/data", delete_on_termination: true }], fs: [mediaFs], key: KEY_PAIRS[0], tags: asgTags, ports: { "80/tcp": hostPort(INSTANCE.web1, 80) }, profile: true,
      }),
      mkInstance({
        id: INSTANCE.web2, name: "shop-web-2", image: AMI.shopWeb, ref: shopWebRef, type: "t3.small", subnet: SUBNET.privateB, ip: "10.0.12.34", sgs: [SG.web], launched: 11 * DAY + 2 * HOUR,
        volumes: [{ volume_id: VOLUME.web2, mount_path: "/data", delete_on_termination: true }], fs: [mediaFs], key: KEY_PAIRS[0], tags: asgTags, ports: { "80/tcp": hostPort(INSTANCE.web2, 80) }, profile: true,
      }),
      mkInstance({
        id: INSTANCE.bastion, name: "shop-bastion", image: AMI.ubuntu, ref: "ubuntu:24.04", type: "t3.nano", subnet: SUBNET.publicA, ip: "10.0.1.12", sgs: [SG.bastion], launched: 70 * DAY,
        volumes: [{ volume_id: VOLUME.bastion, mount_path: "/data", delete_on_termination: true }], key: KEY_PAIRS[2], tags: { ...PUB_TAGS, Role: "bastion" },
      }),
      mkInstance({
        id: INSTANCE.batch, name: "shop-batch-worker", image: AMI.amazonLinux, ref: "amazonlinux:2023", type: "t3.large", subnet: SUBNET.privateA, ip: "10.0.11.58", sgs: [SG.web], launched: 34 * DAY,
        volumes: [{ volume_id: VOLUME.data, mount_path: "/data", delete_on_termination: false }], key: KEY_PAIRS[0], tags: { ...PUB_TAGS, Role: "batch", Schedule: "nightly" }, profile: true,
      }),
      mkInstance({
        id: INSTANCE.dev, name: "dev-sandbox", image: AMI.debian, ref: "debian:12", type: "t3.micro", subnet: SUBNET.defaultA, ip: "172.31.3.44", sgs: [SG.defaultVpc], launched: 40 * DAY, state: "stopped",
        key: KEY_PAIRS[1], tags: { Environment: "dev", Owner: "alice" },
      }),
      mkInstance({
        id: stableId("i-", "shop-web-old"), name: "shop-web-old", image: AMI.amazonLinux, ref: "amazonlinux:2023", type: "t3.small", subnet: SUBNET.privateA, ip: "10.0.11.19", sgs: [SG.web], launched: 60 * DAY, state: "terminated",
        terminated: 3 * DAY, reason: "Client.UserInitiatedShutdown", tags: { ...PUB_TAGS, Role: "web", "note": "replaced by the ASG" },
      }),
    ],
    images: [
      {
        id: AMI.shopWeb, name: "shop-web-v14", description: "Shop storefront, release 14 (nginx, Node.js 22, app bundle)", ref: `homecloud/ami:${AMI.shopWeb}`, platform: "linux", keep_alive: true, owner: ACCOUNT,
        state: "available", created_at: ago(12 * DAY), source_instance: INSTANCE.web1,
      },
      {
        id: stableId("ami-", "shop-web-v13"), name: "shop-web-v13", description: "Shop storefront, release 13", ref: `homecloud/ami:${stableId("ami-", "shop-web-v13")}`, platform: "linux", keep_alive: true, owner: ACCOUNT,
        state: "available", created_at: ago(41 * DAY), source_instance: stableId("i-", "shop-web-old"),
      },
      {
        id: stableId("ami-", "shop-batch-base"), name: "shop-batch-base", description: "Batch worker with the reporting toolchain preinstalled", ref: "registry.example.com/shop/batch-base:2.4", platform: "linux", keep_alive: true,
        owner: ACCOUNT, state: "available", created_at: ago(58 * DAY),
      },
    ],
    volumes: [
      attached(vol(VOLUME.web1, "shop-web-1-data", 20, AZ_A, 11 * DAY + 3 * HOUR), INSTANCE.web1, "/data", "/dev/sdf"),
      attached(vol(VOLUME.web2, "shop-web-2-data", 20, AZ_B, 11 * DAY + 2 * HOUR), INSTANCE.web2, "/data", "/dev/sdf"),
      attached(vol(VOLUME.bastion, "shop-bastion-data", 8, AZ_A, 70 * DAY), INSTANCE.bastion, "/data", "/dev/sdf"),
      attached(vol(VOLUME.data, "shop-batch-data", 100, AZ_A, 34 * DAY, { tags: { Name: "shop-batch-data", ...PUB_TAGS, Backup: "daily" } }), INSTANCE.batch, "/data", "/dev/sdf"),
      vol(VOLUME.scratch, "scratch-restore", 50, AZ_A, 9 * DAY, { tags: { Name: "scratch-restore", Owner: "alice" }, snapshot_id: stableId("snap-", "batch-data-2") }),
    ],
    snapshots: [
      snap(stableId("snap-", "batch-data-1"), VOLUME.data, 100, 2 * DAY + 5 * HOUR, "shop-batch-data nightly", { Backup: "daily" }),
      snap(stableId("snap-", "batch-data-2"), VOLUME.data, 100, 3 * DAY + 5 * HOUR, "shop-batch-data nightly", { Backup: "daily" }),
      snap(stableId("snap-", "batch-data-3"), VOLUME.data, 100, 4 * DAY + 5 * HOUR, "shop-batch-data nightly", { Backup: "daily" }),
      snap(stableId("snap-", "web-1-data"), VOLUME.web1, 20, 12 * DAY, "Before release 14 rollout"),
      snap(stableId("snap-", "old-data"), stableId("vol-", "deleted-volume"), 30, 75 * DAY, "Volume of a terminated instance"),
    ],
    keys: [
      { id: stableId("key-", KEY_PAIRS[0]), name: KEY_PAIRS[0], type: "ed25519", fingerprint: fp(KEY_PAIRS[0], "ed25519"), public_key: keyMat(KEY_PAIRS[0], "ed25519"), created_at: ago(96 * DAY), tags: PUB_TAGS },
      { id: stableId("key-", KEY_PAIRS[1]), name: KEY_PAIRS[1], type: "rsa", fingerprint: fp(KEY_PAIRS[1], "rsa"), public_key: keyMat(KEY_PAIRS[1], "rsa"), created_at: ago(52 * DAY) },
      { id: stableId("key-", KEY_PAIRS[2]), name: KEY_PAIRS[2], type: "ed25519", fingerprint: fp(KEY_PAIRS[2], "ed25519"), public_key: keyMat(KEY_PAIRS[2], "ed25519"), created_at: ago(30 * DAY) },
    ],
    addresses: [
      {
        allocation_id: EIP.bastion, public_ip: "203.0.113.12", domain: "vpc", association_id: stableId("eipassoc-", "bastion"), instance_id: INSTANCE.bastion, private_ip: "10.0.1.12", created_at: ago(70 * DAY),
        tags: { Name: "shop-bastion-eip" },
      },
      { allocation_id: EIP.nat, public_ip: "203.0.113.20", domain: "vpc", created_at: ago(96 * DAY), tags: { Name: "shop-nat-eip", Project: "shop" } },
    ],
  }
}

// ---- helpers ----

const instById = (id: string): Instance => {
  const i = ec2State().instances.find((x) => x.id === id)
  if (!i) throw err(400, "InvalidInstanceID.NotFound", `The instance ID '${id}' does not exist`)
  return i
}
const wrongState = (i: Instance, extra = "") => err(409, "IncorrectInstanceState", `instance ${i.id} is ${i.state}${extra}`)
const live = (i: Instance) => i.state !== "terminated"

function checkTags(t?: Tags | null) {
  for (const k of Object.keys(t ?? {})) if (k.toLowerCase().startsWith("hc:")) throw badRequest("tag keys starting with hc: are reserved")
}

/** publishedPorts maps ingress rules open to the world (or this host) to host ports. */
function publishedPorts(i: Instance): Record<string, number> {
  const out: Record<string, number> = {}
  const groups = vpcState().sgs.filter((g) => i.security_groups.includes(g.id))
  for (const g of groups) {
    for (const r of g.ingress) {
      if (r.protocol !== "tcp" || (r.cidr !== "0.0.0.0/0" && r.cidr !== "127.0.0.1/32")) continue
      for (let p = r.from_port; p <= r.to_port && Object.keys(out).length < 8; p++) out[`${p}/tcp`] = hostPort(i.id, p)
    }
  }
  if (!i.keep_alive && !out["80/tcp"]) out["80/tcp"] = hostPort(i.id, 80)
  return out
}

function settleRunning(i: Instance) {
  i.state = "running"
  i.state_reason = undefined
  i.public_ports = publishedPorts(i)
}

function releaseVolumes(i: Instance) {
  const s = ec2State()
  for (const a of i.volumes) {
    if (a.delete_on_termination) {
      s.volumes = s.volumes.filter((v) => v.id !== a.volume_id)
    } else {
      const v = s.volumes.find((x) => x.id === a.volume_id)
      if (v) {
        v.state = "available"
        delete v.attached_to
        delete v.mount_path
        delete v.device
      }
    }
  }
}

function finishTerminate(id: string) {
  const s = ec2State()
  const i = s.instances.find((x) => x.id === id)
  if (!i || i.state !== "shutting-down") return
  i.state = "terminated"
  i.state_reason = "Client.UserInitiatedShutdown"
  i.terminated_at = new Date().toISOString()
  i.public_ports = {}
  delete i.container_id
  releaseVolumes(i)
  i.volumes = []
  for (const a of s.addresses) {
    if (a.instance_id === id) {
      delete a.association_id
      delete a.instance_id
      delete a.private_ip
    }
  }
  instanceHooks.gone.forEach((f) => f(id))
  instanceHooks.changed.forEach((f) => f())
}

/** terminateInstance starts shutting an instance down (used by the API and Auto Scaling). */
export function terminateInstance(id: string, delay = 2500) {
  const i = instById(id)
  if (i.state === "terminated" || i.state === "shutting-down") return i
  i.state = "shutting-down"
  later(delay, () => finishTerminate(id))
  return i
}

/** launchInstances validates and starts instances (RunInstances). */
export function launchInstances(inp: RunInstancesInput, extraTags: Tags = {}): Instance[] {
  const s = ec2State()
  const count = inp.count ?? 1
  if (!Number.isInteger(count) || count < 1 || count > 20) throw badRequest("count must be between 1 and 20")
  const typeName = inp.instance_type || "t3.micro"
  const type = findType(typeName)
  if (!type) throw err(400, "InvalidParameterValue", `unknown instance type "${typeName}"`)
  if (!inp.image_id) throw err(400, "MissingParameter", "image_id is required")
  const img = [...CATALOG, ...s.images].find((x) => x.id === inp.image_id)
  if (!img) throw notFound("image", inp.image_id)
  if (inp.key_name && !s.keys.some((k) => k.name === inp.key_name || k.id === inp.key_name)) throw err(400, "InvalidKeyPair.NotFound", `The key pair '${inp.key_name}' does not exist`)
  const md = inp.metadata_options ?? {}
  const fsList = inp.file_systems ?? []
  const fsState = (getState().efs?.fileSystems ?? []) as { id: string; read_only: boolean }[]
  for (const m of fsList) {
    if (!m.mount_path.startsWith("/")) throw badRequest("file system mount_path must be absolute")
    const fs = fsState.find((f) => f.id === m.file_system_id)
    if (!fs) throw notFound("file system", m.file_system_id)
    if (fs.read_only && !m.read_only) throw badRequest(`file system ${fs.id} is read-only`)
  }
  for (const v of inp.volumes ?? []) {
    if (!v.mount_path.startsWith("/")) throw badRequest("volume mount_path must be absolute")
    if (v.volume_id) {
      const vol = s.volumes.find((x) => x.id === v.volume_id)
      if (!vol) throw notFound("volume", v.volume_id)
      if (vol.state !== "available") throw conflict(`volume ${vol.id} is ${vol.state}`)
      if (count > 1) throw badRequest("an existing volume can only be attached when count is 1")
    }
  }
  const sn = inp.subnet_id ? vpcState().subnets.find((x) => x.id === inp.subnet_id) : defaultSubnet()
  if (!sn) throw notFound("subnet", inp.subnet_id)
  const sgs = inp.security_group_ids?.length ? inp.security_group_ids : [defaultSg(sn.vpc_id)]
  for (const g of sgs) {
    const rec = vpcState().sgs.find((x) => x.id === g)
    if (!rec) throw notFound("security group", g)
    if (rec.vpc_id !== sn.vpc_id) throw badRequest(`security group ${g} belongs to ${rec.vpc_id}, not ${sn.vpc_id}`)
  }
  const profileArn = inp.iam_instance_profile
    ? inp.iam_instance_profile.startsWith("arn:")
      ? inp.iam_instance_profile
      : arn("iam", `instance-profile/${inp.iam_instance_profile}`, { region: null })
    : undefined
  const out: Instance[] = []
  for (let n = 0; n < count; n++) {
    const id = uid("i-")
    const name = inp.name && count > 1 ? `${inp.name}-${n + 1}` : (inp.name ?? "")
    const ip = allocIp(sn.id)
    const attachments: VolumeAttachment[] = []
    for (const v of inp.volumes ?? []) {
      const del = v.delete_on_termination ?? !v.volume_id
      let volId = v.volume_id
      if (!volId) {
        volId = uid("vol-")
        s.volumes.push({
          id: volId, name: "", size_gb: v.size_gb || 8, state: "in-use", availability_zone: sn.availability_zone, created_at: new Date().toISOString(), volume_type: "gp3", tags: {},
          attached_to: id, mount_path: v.mount_path, device: "/dev/sdf",
        })
      } else {
        const vol = s.volumes.find((x) => x.id === volId)!
        Object.assign(vol, { state: "in-use", attached_to: id, mount_path: v.mount_path, device: "/dev/sdf" })
      }
      attachments.push({ volume_id: volId, mount_path: v.mount_path, delete_on_termination: del })
    }
    const inst: Instance = {
      id, name, arn: arn("ec2", `instance/${id}`), image_id: img.id, image_ref: img.ref, instance_type: type.name, vcpus: type.vcpus, memory_mb: type.memory_mb, state: "pending",
      vpc_id: sn.vpc_id, subnet_id: sn.id, availability_zone: sn.availability_zone, private_ip: ip, private_dns: dnsOf(ip), security_groups: sgs, volumes: attachments, file_systems: fsList,
      user_data: inp.user_data || undefined, keep_alive: img.keep_alive, public_ports: {}, public_host: "localhost", launch_time: new Date().toISOString(),
      tags: { ...(name ? { Name: name } : {}), ...(inp.tags ?? {}), ...extraTags }, key_name: inp.key_name || undefined, iam_profile_arn: profileArn,
      iam_profile_id: profileArn ? "AIPA" + hex(16).toUpperCase() : undefined,
      metadata_options: { http_tokens: md.http_tokens ?? "required", http_endpoint: md.http_endpoint ?? "enabled", hop_limit: md.hop_limit ?? 2, instance_metadata_tags: md.instance_metadata_tags ?? "disabled" },
    }
    s.instances.push(inst)
    out.push(inst)
    later(2200 + n * 400, () => {
      const cur = ec2State().instances.find((x) => x.id === id)
      if (cur && cur.state === "pending") {
        cur.container_id = hex(64)
        settleRunning(cur)
      }
    })
  }
  return out
}

function onLoad(st: State) {
  const s = st.ec2 as Ec2State
  for (const i of s.instances) {
    if (i.state === "pending") {
      i.container_id ??= hex(64)
      settleRunning(i)
    } else if (i.state === "stopping") {
      i.state = "stopped"
      i.public_ports = {}
    } else if (i.state === "shutting-down") finishTerminateNow(s, i)
  }
  for (const v of s.volumes) if ((v.state as string) === "creating") v.state = "available"
  for (const sn of s.snapshots) {
    if (sn.state === "pending") {
      sn.state = "completed"
      sn.completed_at ??= new Date().toISOString()
    }
  }
}

function finishTerminateNow(s: Ec2State, i: Instance) {
  i.state = "terminated"
  i.state_reason = "Client.UserInitiatedShutdown"
  i.terminated_at = new Date().toISOString()
  i.public_ports = {}
  for (const a of i.volumes) {
    if (a.delete_on_termination) s.volumes = s.volumes.filter((v) => v.id !== a.volume_id)
    else {
      const v = s.volumes.find((x) => x.id === a.volume_id)
      if (v) {
        v.state = "available"
        delete v.attached_to
      }
    }
  }
  i.volumes = []
}

const FAKE_PRIVATE_KEY = (name: string) =>
  `-----BEGIN OPENSSH PRIVATE KEY-----\ndemo-not-a-real-private-key-${name}\nThis is placeholder text generated by the HomeCloud demo.\nInstall HomeCloud to create real key pairs.\n-----END OPENSSH PRIVATE KEY-----\n`

function consoleLog(i: Instance): string {
  const t = (n: number) => new Date(new Date(i.launch_time).getTime() + n * 1000).toISOString().replace("T", " ").slice(0, 19)
  const lines = [
    `[homecloud] ${t(0)} creating instance ${i.id} (${i.instance_type}) from ${i.image_ref}`,
    `[homecloud] ${t(1)} network ${i.vpc_id} ${i.subnet_id} address ${i.private_ip}`,
    `[homecloud] ${t(1)} waiting for the instance metadata service route`,
    `[homecloud] ${t(2)} running user data`,
    ...(i.user_data ? i.user_data.split("\n").filter((l) => l.trim() && !l.startsWith("#")).slice(0, 6).map((l) => `+ ${l}`) : []),
    `[homecloud] ${t(6)} user data finished`,
    `[homecloud] ${t(6)} instance ready`,
  ]
  if (i.tags?.Role === "web") {
    lines.push(
      `${t(9)} shop-web[212]: loading configuration from /etc/shop/web.env`,
      `${t(10)} shop-web[212]: connected to postgres (shop-db) and redis (shop-cache)`,
      `${t(10)} shop-web[212]: listening on :8080`,
      `${t(14)} nginx: 10.0.1.7 "GET /healthz HTTP/1.1" 200 2`,
    )
  }
  return lines.join("\n") + "\n"
}

function runCommand(i: Instance, cmd: string): Omit<CommandResult, "command_id" | "instance_id" | "duration_ms"> {
  const c = cmd.trim()
  const ok = (stdout: string) => ({ status: "Success" as const, exit_code: 0, stdout, stderr: "" })
  const [bin, ...args] = c.split(/\s+/)
  switch (bin) {
    case "echo":
      return ok(args.join(" ").replace(/^["']|["']$/g, "") + "\n")
    case "whoami":
      return ok("root\n")
    case "id":
      return ok("uid=0(root) gid=0(root) groups=0(root)\n")
    case "hostname":
      return ok(i.private_dns.replace(/\.internal$/, "") + "\n")
    case "pwd":
      return ok("/root\n")
    case "date":
      return ok(new Date().toUTCString() + "\n")
    case "uname":
      return ok(args.includes("-a") ? `Linux ${i.private_dns.replace(/\.internal$/, "")} 6.1.102-homecloud #1 SMP x86_64 GNU/Linux\n` : "Linux\n")
    case "uptime":
      return ok(` ${new Date().toISOString().slice(11, 19)} up ${Math.max(1, Math.floor((Date.now() - new Date(i.launch_time).getTime()) / DAY))} days,  0 users,  load average: 0.08, 0.11, 0.09\n`)
    case "ls":
      return ok(args.length && !args[0].startsWith("-") ? "" : "app\nbin\ndata\nlogs\n")
    case "df":
      return ok("Filesystem      Size  Used Avail Use% Mounted on\noverlay          59G   14G   42G  25% /\n/dev/vdb         20G  3.1G   16G  17% /data\ntmpfs           64M     0   64M   0% /dev\n")
    case "free":
      return ok(`               total        used        free      shared  buff/cache   available\nMem:          ${i.memory_mb}         ${Math.round(i.memory_mb * 0.31)}         ${Math.round(i.memory_mb * 0.52)}           3         ${Math.round(i.memory_mb * 0.17)}         ${Math.round(i.memory_mb * 0.62)}\nSwap:              0           0           0\n`)
    case "cat":
      if (args[0] === "/etc/os-release") return ok(`PRETTY_NAME="${i.image_ref}"\nNAME="${i.image_ref.split(":")[0]}"\nID=${i.image_ref.split(":")[0]}\n`)
      break
    case "ps":
      return ok("  PID TTY          TIME CMD\n    1 ?        00:00:00 sh\n  212 ?        00:04:12 node\n  318 ?        00:00:41 nginx\n  455 ?        00:00:00 ps\n")
    case "ip":
    case "ifconfig":
      return ok(`2: eth0: <BROADCAST,MULTICAST,UP,LOWER_UP> mtu 1500\n    inet ${i.private_ip}/24 brd ${i.private_ip.replace(/\.\d+$/, ".255")} scope global eth0\n`)
  }
  return { status: "Failed", exit_code: 127, stdout: "", stderr: `/bin/sh: 1: ${bin}: not found\n` }
}

function fakeFingerprint(kind: string): string {
  return kind === "ed25519" ? `SHA256:${btoa(hex(24)).replace(/=/g, "")}` : hex(32).replace(/(..)(?=.)/g, "$1:")
}

const nextEip = (s: Ec2State) => {
  const used = new Set(s.addresses.map((a) => a.public_ip))
  for (let n = 1; n < 255; n++) if (!used.has(`203.0.113.${n}`)) return `203.0.113.${n}`
  throw err(400, "AddressLimitExceeded", "The elastic IP pool 203.0.113.0/24 is exhausted")
}

function routes(r: Router) {
  const I = "/api/v1/ec2/instances"
  r.get(I, () => ec2State().instances)
  r.get(`${I}/:id`, ({ params }) => instById(params.id))
  r.post(I, ({ body }) => launchInstances(body as RunInstancesInput))
  r.patch(`${I}/:id`, ({ params, body }) => {
    const i = instById(params.id)
    checkTags(body?.tags)
    if (i.state === "terminated") throw wrongState(i)
    if (body?.instance_type && body.instance_type !== i.instance_type) {
      const t = findType(body.instance_type)
      if (!t) throw err(400, "InvalidParameterValue", `unknown instance type "${body.instance_type}"`)
      if (i.state !== "stopped") throw err(409, "IncorrectInstanceState", `the instance ${i.id} must be stopped to change its type`)
      Object.assign(i, { instance_type: t.name, vcpus: t.vcpus, memory_mb: t.memory_mb })
    }
    if (typeof body?.name === "string") {
      i.name = body.name
      i.tags = { ...(i.tags ?? {}) }
      if (body.name) i.tags.Name = body.name
    }
    if (body?.tags) {
      const keep = Object.fromEntries(Object.entries(i.tags ?? {}).filter(([k]) => k.toLowerCase().startsWith("hc:")))
      i.tags = { ...body.tags, ...keep }
    }
    return i
  })
  r.post(`${I}/:id/start`, ({ params }) => {
    const i = instById(params.id)
    if (i.state !== "stopped") throw wrongState(i)
    i.state = "pending"
    later(2200, () => settleRunning(instById(params.id)))
    return i
  })
  r.post(`${I}/:id/stop`, ({ params }) => {
    const i = instById(params.id)
    if (i.state !== "running") throw wrongState(i)
    i.state = "stopping"
    later(2500, () => {
      const cur = instById(params.id)
      if (cur.state !== "stopping") return
      cur.state = "stopped"
      cur.state_reason = "Client.UserInitiatedShutdown"
      cur.public_ports = {}
      instanceHooks.changed.forEach((f) => f())
    })
    return i
  })
  r.post(`${I}/:id/reboot`, ({ params }) => {
    const i = instById(params.id)
    if (i.state !== "running") throw wrongState(i)
    return i
  })
  r.del(`${I}/:id`, ({ params }) => terminateInstance(params.id))
  r.get(`${I}/:id/console-output`, ({ params }): ConsoleOutput => {
    const i = instById(params.id)
    if (!i.container_id || i.state === "terminated") return { instance_id: i.id, output: "" }
    return { instance_id: i.id, output: consoleLog(i), timestamp: new Date().toISOString() }
  })
  r.post(`${I}/:id/commands`, ({ params, body }): CommandResult => {
    if (!String(body?.command ?? "").trim()) throw badRequest("command is required")
    const i = instById(params.id)
    if (i.state !== "running") throw err(409, "InvalidInstanceState", `instance ${i.id} is ${i.state}`)
    const res = runCommand(i, body.command)
    return { command_id: uid("cmd-", 16), instance_id: i.id, duration_ms: 18 + Math.floor(Math.random() * 60), ...res }
  })
  r.get(`${I}/:id/terminal`, () => {
    throw unavailable("The browser terminal")
  })
  r.post(`${I}/:id/image`, ({ params, body }) => createImage(instById(params.id), body?.name, body?.description))

  r.get("/api/v1/ec2/instance-types", () => TYPES)

  // ---- images ----
  const IM = "/api/v1/ec2/images"
  r.get(IM, () => [...CATALOG, ...ec2State().images])
  r.get(`${IM}/:id`, ({ params }) => {
    const im = [...CATALOG, ...ec2State().images].find((x) => x.id === params.id)
    if (!im) throw notFound("image", params.id)
    return im
  })
  r.post(IM, ({ body }) => {
    if (!body?.name || !body?.ref) throw badRequest("name and ref (a Docker image reference) are required")
    const im: Image = {
      id: uid("ami-"), name: body.name, description: body.description ?? "", ref: body.ref, platform: "linux", keep_alive: !!body.keep_alive, owner: ACCOUNT, state: "available",
      created_at: new Date().toISOString(),
    }
    ec2State().images.push(im)
    return im
  })
  r.del(`${IM}/:id`, ({ params }) => {
    const s = ec2State()
    if (!s.images.some((x) => x.id === params.id)) throw notFound("image", params.id)
    s.images = s.images.filter((x) => x.id !== params.id)
    return null
  })

  // ---- volumes ----
  const V = "/api/v1/ec2/volumes"
  r.get(V, () => ec2State().volumes)
  r.get(`${V}/:id`, ({ params }) => {
    const v = ec2State().volumes.find((x) => x.id === params.id)
    if (!v) throw notFound("volume", params.id)
    return v
  })
  r.post(V, ({ body }) => {
    const s = ec2State()
    let size = Number(body?.size_gb) || 0
    let snapId: string | undefined
    if (body?.snapshot_id) {
      const sn = s.snapshots.find((x) => x.id === body.snapshot_id)
      if (!sn) throw err(400, "InvalidSnapshot.NotFound", `The snapshot '${body.snapshot_id}' does not exist.`)
      if (sn.state !== "completed") throw err(400, "IncorrectState", `snapshot ${sn.id} is ${sn.state}`)
      if (!size) size = sn.volume_size
      if (size < sn.volume_size) throw err(400, "InvalidParameterValue", `volume size ${size} GiB is smaller than snapshot ${sn.id} (${sn.volume_size} GiB)`)
      snapId = sn.id
    }
    if (!size) size = 8
    if (size < 1 || size > 16384) throw err(400, "InvalidParameterValue", "volume size must be between 1 and 16384 GiB")
    const tags: Tags = body?.tags ?? {}
    const v: Volume = {
      id: uid("vol-"), name: body?.name || tags.Name || "", size_gb: size, state: snapId ? "creating" : "available", availability_zone: body?.availability_zone || `${REGION}a`,
      created_at: new Date().toISOString(), tags, volume_type: "gp3", snapshot_id: snapId,
    }
    s.volumes.push(v)
    if (snapId) {
      later(3000, () => {
        const cur = ec2State().volumes.find((x) => x.id === v.id)
        if (cur && (cur.state as string) === "creating") cur.state = "available"
      })
    }
    return v
  })
  r.del(`${V}/:id`, ({ params }) => {
    const s = ec2State()
    const v = s.volumes.find((x) => x.id === params.id)
    if (!v) throw notFound("volume", params.id)
    if (v.state === "in-use") throw err(409, "VolumeInUse", `volume ${v.id} is attached to ${v.attached_to}`)
    s.volumes = s.volumes.filter((x) => x.id !== v.id)
    return null
  })

  // ---- snapshots ----
  const SN = "/api/v1/ec2/snapshots"
  r.get(SN, () => [...ec2State().snapshots].sort((a, b) => b.start_time.localeCompare(a.start_time)))
  r.post(SN, ({ body }) => {
    const s = ec2State()
    const v = s.volumes.find((x) => x.id === body?.volume_id)
    if (!v) throw notFound("volume", String(body?.volume_id ?? ""))
    if ((v.state as string) === "creating" || (v.state as string) === "error") throw err(400, "IncorrectState", `volume ${v.id} is ${v.state}`)
    const sn: Snapshot = {
      id: uid("snap-"), volume_id: v.id, volume_size: v.size_gb, state: "pending", description: body?.description || undefined, start_time: new Date().toISOString(), encrypted: false,
      tags: body?.tags,
    }
    s.snapshots.push(sn)
    later(3500, () => {
      const cur = ec2State().snapshots.find((x) => x.id === sn.id)
      if (cur && cur.state === "pending") {
        cur.state = "completed"
        cur.completed_at = new Date().toISOString()
      }
    })
    return sn
  })
  r.del(`${SN}/:id`, ({ params }) => {
    const s = ec2State()
    const sn = s.snapshots.find((x) => x.id === params.id)
    if (!sn) throw err(400, "InvalidSnapshot.NotFound", `The snapshot '${params.id}' does not exist.`)
    const busy = s.volumes.find((v) => v.snapshot_id === sn.id && (v.state as string) === "creating")
    if (busy) throw err(400, "InvalidSnapshot.InUse", `snapshot ${sn.id} is being restored to ${busy.id}`)
    s.snapshots = s.snapshots.filter((x) => x.id !== sn.id)
    return null
  })

  // ---- key pairs ----
  const K = "/api/v1/ec2/key-pairs"
  const checkKeyName = (name: string) => {
    if (!/^[\x20-\x7e]{1,255}$/.test(name ?? "")) throw err(400, "InvalidParameterValue", "key pair names are 1 to 255 ASCII characters")
    if (ec2State().keys.some((k) => k.name === name)) throw err(400, "InvalidKeyPair.Duplicate", `The keypair '${name}' already exists.`)
  }
  r.get(K, () => [...ec2State().keys].sort((a, b) => a.name.localeCompare(b.name)))
  r.post(K, ({ body }) => {
    checkKeyName(body?.name)
    const type = body?.type || "rsa"
    if (type !== "rsa" && type !== "ed25519") throw err(400, "InvalidParameterValue", "key type must be rsa or ed25519")
    const k: KeyPair = {
      id: uid("key-"), name: body.name, type, fingerprint: fakeFingerprint(type),
      public_key: (type === "rsa" ? "ssh-rsa AAAAB3NzaC1yc2EAAAADAQABAAABgQDemoNotARealPublicKey" : "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIDemoNotARealPublicKey") + hex(24) + " " + body.name,
      created_at: new Date().toISOString(), tags: body.tags,
    }
    ec2State().keys.push(k)
    return { key_pair: k, private_key: FAKE_PRIVATE_KEY(body.name) }
  })
  r.post(`${K}/import`, ({ body }) => {
    checkKeyName(body?.name)
    const pub = String(body?.public_key ?? "").trim()
    const parts = pub.split(/\s+/)
    if (!/^ssh-(rsa|ed25519)$/.test(parts[0] ?? "") || !parts[1]) {
      throw err(400, "InvalidKey.Format", /^ssh-|^ecdsa-/.test(pub) ? `only RSA and ED25519 keys are supported, not ${parts[0]}` : "Key is not in valid OpenSSH public key format")
    }
    const type = parts[0] === "ssh-rsa" ? "rsa" : "ed25519"
    const k: KeyPair = { id: uid("key-"), name: body.name, type, fingerprint: fakeFingerprint(type), public_key: pub, created_at: new Date().toISOString(), tags: body.tags }
    ec2State().keys.push(k)
    return k
  })
  r.del(`${K}/:name`, ({ params }) => {
    const s = ec2State()
    if (!s.keys.some((k) => k.name === params.name)) throw err(400, "InvalidKeyPair.NotFound", `The key pair '${params.name}' does not exist`)
    s.keys = s.keys.filter((k) => k.name !== params.name)
    return null
  })

  // ---- Elastic IPs ----
  const A = "/api/v1/ec2/addresses"
  const addr = (id: string) => {
    const a = ec2State().addresses.find((x) => x.allocation_id === id)
    if (!a) throw err(400, "InvalidAllocationID.NotFound", `The allocation ID '${id}' does not exist`)
    return a
  }
  r.get(A, () => ec2State().addresses)
  r.post(A, ({ body }) => {
    checkTags(body?.tags)
    const s = ec2State()
    const a: ElasticIP = { allocation_id: uid("eipalloc-"), public_ip: nextEip(s), domain: "vpc", created_at: new Date().toISOString(), tags: body?.tags }
    s.addresses.push(a)
    return a
  })
  r.del(`${A}/:id`, ({ params }) => {
    const s = ec2State()
    const a = addr(params.id)
    if (a.association_id) throw err(400, "InvalidIPAddress.InUse", `The address ${a.public_ip} is associated with ${a.instance_id} and cannot be released`)
    s.addresses = s.addresses.filter((x) => x.allocation_id !== a.allocation_id)
    return null
  })
  r.post(`${A}/:id/associate`, ({ params, body }: Req) => {
    const s = ec2State()
    if (!body?.instance_id) throw badRequest("instance_id is required")
    const i = s.instances.find((x) => x.id === body.instance_id)
    if (!i || i.state === "terminated" || i.state === "shutting-down") throw err(400, "InvalidInstanceID.NotFound", `The instance ID '${body.instance_id}' does not exist`)
    const a = addr(params.id)
    if (a.instance_id === i.id) return a
    if (a.association_id && !body.reassociate) throw err(400, "Resource.AlreadyAssociated", `resource ${a.allocation_id} is already associated with associate-id ${a.association_id}`)
    for (const o of s.addresses) {
      if (o.instance_id === i.id && o.allocation_id !== a.allocation_id) {
        delete o.association_id
        delete o.instance_id
        delete o.private_ip
      }
    }
    Object.assign(a, { association_id: uid("eipassoc-"), instance_id: i.id, private_ip: i.private_ip })
    return a
  })
  r.post(`${A}/:id/disassociate`, ({ params }) => {
    const a = addr(params.id)
    delete a.association_id
    delete a.instance_id
    delete a.private_ip
    return a
  })
}

function createImage(i: Instance, nameIn: string | undefined, description: string | undefined): Image {
  const s = ec2State()
  if (!i.container_id || i.state === "terminated") throw err(409, "IncorrectInstanceState", `instance ${i.id} has no disk to capture`)
  const name = nameIn || `${i.id}-image`
  const dup = [...CATALOG, ...s.images].find((x) => x.name === name)
  if (dup) throw err(409, "InvalidAMIName.Duplicate", `AMI name ${name} is already in use by AMI ${dup.id}`)
  const id = uid("ami-")
  const im: Image = {
    id, name, description: description ?? "", ref: `homecloud/ami:${id}`, platform: "linux", keep_alive: i.keep_alive, owner: ACCOUNT, state: "available", created_at: new Date().toISOString(),
    source_instance: i.id,
  }
  s.images.push(im)
  return im
}

const service: DemoService = { name: "ec2", seed, routes, onLoad }
export default service

