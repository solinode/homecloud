import { badRequest, conflict, getState, later, notFound, type DemoService, type Router } from "../engine"
import { DAY, HOUR, MIN, ago, arn } from "../util"
import { AMI, FS, INSTANCE, NAMES, SG, SUBNET } from "../ids"
import { vpcState } from "./vpc"
import { allImages, findType, instanceHooks, launchInstances, terminateInstance } from "./ec2"
import { elbState, registerTarget } from "./elb"
import { ASG_GROUP_TAG, type AsgInstance, type AutoScalingGroup, type Instance, type LaunchConfig, type ScalingActivity, type ScalingPolicy } from "@/lib/types"

// EC2 Auto Scaling groups (/api/v1/autoscaling/groups). A group owns the
// instances tagged with its name; changing its capacity launches or terminates
// (demo) instances, and terminating a member gets it replaced.

type GroupRec = Omit<AutoScalingGroup, "instances" | "policies" | "subnet_ids" | "target_groups"> & {
  policies: ScalingPolicy[]
  subnet_ids: string[]
  target_groups: string[]
  activities: ScalingActivity[]
}
interface AsgState {
  groups: GroupRec[]
}
const asgState = () => getState().autoscaling as AsgState

const instances = () => (getState().ec2?.instances ?? []) as Instance[]
const members = (name: string) => instances().filter((i) => i.tags?.[ASG_GROUP_TAG] === name && (i.state === "pending" || i.state === "running"))

function seed(): AsgState {
  const act = (age: number, description: string, cause: string, status = "Successful"): ScalingActivity => ({ time: ago(age), description, cause, status })
  const at = (age: number) => new Date(Date.now() - age).toISOString().replace("T", " ").slice(0, 19) + " UTC"
  const launchCause = (age: number, from: number, to: number) => `At ${at(age)} an instance was launched in response to a difference between desired and actual capacity, increasing the capacity from ${from} to ${to}.`
  const termCause = (age: number, from: number, to: number) => `At ${at(age)} an instance was taken out of service in response to a difference between desired and actual capacity, shrinking the capacity from ${from} to ${to}.`
  return {
    groups: [
      {
        name: NAMES.asg, arn: arn("autoscaling", `autoScalingGroup:${NAMES.asg}`),
        launch: { image_id: AMI.shopWeb, instance_type: "t3.small", security_group_ids: [SG.web], user_data: "#!/bin/sh\nsystemctl enable --now shop-web\n", file_systems: [{ file_system_id: FS.media, mount_path: "/mnt/media", read_only: false }] },
        min_size: 2, max_size: 6, desired_capacity: 2, subnet_ids: [SUBNET.privateA, SUBNET.privateB], target_groups: [NAMES.tgWeb],
        policies: [{ name: "target-CPUUtilization-60", metric: "CPUUtilization", target_value: 60, cooldown_seconds: 180 }],
        health_check_grace_seconds: 120, suspended: false, status: "Active", created_at: ago(90 * DAY), last_scaling: ago(11 * DAY + 2 * HOUR),
        activities: [
          act(11 * DAY + 2 * HOUR, `Launching a new EC2 instance: ${INSTANCE.web2}`, launchCause(11 * DAY + 2 * HOUR, 1, 2)),
          act(11 * DAY + 3 * HOUR, `Launching a new EC2 instance: ${INSTANCE.web1}`, launchCause(11 * DAY + 3 * HOUR, 0, 1)),
          act(11 * DAY + 3 * HOUR + 30 * MIN, "Terminating EC2 instance: i-0aa11bb22cc33dd44", termCause(11 * DAY + 3 * HOUR + 30 * MIN, 3, 2)),
          act(11 * DAY + 4 * HOUR, "Terminating EC2 instance: i-0ee55ff66aa77bb88", termCause(11 * DAY + 4 * HOUR, 4, 3)),
          act(19 * DAY + 5 * HOUR, "Launching a new EC2 instance: i-0aa11bb22cc33dd44", `At ${at(19 * DAY + 5 * HOUR)} a target tracking policy (target-CPUUtilization-60) increased capacity from 3 to 4 because the average CPUUtilization was above the target of 60.`),
          act(19 * DAY + 6 * HOUR, "Launching a new EC2 instance: i-0ee55ff66aa77bb88", `At ${at(19 * DAY + 6 * HOUR)} a target tracking policy (target-CPUUtilization-60) increased capacity from 2 to 3 because the average CPUUtilization was above the target of 60.`),
          act(34 * DAY, "Launching a new EC2 instance", launchCause(34 * DAY, 1, 2), "Failed"),
          act(90 * DAY, "Launching a new EC2 instance: i-0123456789abcdef0", launchCause(90 * DAY, 0, 1)),
        ],
      },
      {
        name: "shop-batch-asg", arn: arn("autoscaling", "autoScalingGroup:shop-batch-asg"),
        launch: { image_id: AMI.amazonLinux, instance_type: "t3.large", security_group_ids: [SG.web] },
        min_size: 0, max_size: 4, desired_capacity: 0, subnet_ids: [SUBNET.privateA, SUBNET.privateB], target_groups: [], policies: [],
        health_check_grace_seconds: 300, suspended: false, status: "Active", created_at: ago(45 * DAY), last_scaling: ago(2 * DAY + 3 * HOUR),
        activities: [
          act(2 * DAY + 3 * HOUR, "Terminating EC2 instance: i-0b7c6d5e4f3a2b1c0", termCause(2 * DAY + 3 * HOUR, 1, 0)),
          act(2 * DAY + 5 * HOUR, "Launching a new EC2 instance: i-0b7c6d5e4f3a2b1c0", launchCause(2 * DAY + 5 * HOUR, 0, 1)),
        ],
      },
    ],
  }
}

// ---- validation ----

const NAME_RE = /^[\w.-]{1,255}$/

function checkSizes(min: number, max: number, desired: number) {
  if (![min, max, desired].every(Number.isInteger) || min < 0 || max < min || max > 50) throw badRequest("sizes must satisfy 0 <= min_size <= max_size <= 50")
  if (desired < min || desired > max) throw badRequest("desired_capacity must be between min_size and max_size")
}

function normalizePolicies(input: Partial<ScalingPolicy>[] | undefined | null): ScalingPolicy[] {
  const out: ScalingPolicy[] = []
  for (const p of input ?? []) {
    const metric = p.metric || "CPUUtilization"
    if (metric !== "CPUUtilization" && metric !== "MemoryUtilization") throw badRequest("policy metric must be CPUUtilization or MemoryUtilization")
    const target = Number(p.target_value)
    if (!(target > 0 && target <= 100)) throw badRequest("policy target_value must be 1-100 (percent)")
    const name = !p.name || p.name.startsWith("target-") ? `target-${metric}-${target.toFixed(0)}` : p.name
    if (out.some((q) => q.name === name)) throw badRequest(`policy name "${name}" is used twice`)
    out.push({ name, metric, target_value: target, cooldown_seconds: p.cooldown_seconds || 180 })
  }
  return out
}

function checkPlacement(l: LaunchConfig, subnets: string[], tgs: string[]) {
  const typeName = l.instance_type || "t3.micro"
  if (!findType(typeName)) throw badRequest(`unknown instance type "${typeName}"`)
  if (!allImages().some((i) => i.id === l.image_id)) throw notFound("image", l.image_id)
  const v = vpcState()
  let vpcId = ""
  for (const sid of subnets.length ? subnets : [v.subnets.find((s) => s.default)?.id ?? ""]) {
    const sn = v.subnets.find((s) => s.id === sid)
    if (!sn) throw notFound("subnet", sid)
    if (vpcId && vpcId !== sn.vpc_id) throw badRequest("subnets must all be in the same VPC")
    vpcId = sn.vpc_id
  }
  for (const g of l.security_group_ids ?? []) {
    const sg = v.sgs.find((x) => x.id === g)
    if (!sg) throw notFound("security group", g)
    if (sg.vpc_id !== vpcId) throw badRequest(`security group ${g} belongs to ${sg.vpc_id}, not ${vpcId}`)
  }
  for (const t of tgs) {
    const tg = elbState().tgs.find((x) => x.name === t)
    if (!tg) throw notFound("target group", t)
    if (tg.vpc_id !== vpcId) throw badRequest(`target group ${t} is in ${tg.vpc_id}, but the group's subnets are in ${vpcId}`)
  }
}

// ---- reconcile ----

const cause = (verb: string, from: number, to: number) =>
  `At ${new Date().toISOString().replace("T", " ").slice(0, 19)} UTC an instance was ${verb} in response to a difference between desired and actual capacity, ${verb === "launched" ? "increasing" : "shrinking"} the capacity from ${from} to ${to}.`

function addActivity(g: GroupRec, description: string, causeText: string, status = "Successful") {
  g.activities.unshift({ time: new Date().toISOString(), description, cause: causeText, status })
  if (g.activities.length > 100) g.activities.length = 100
  g.last_scaling = new Date().toISOString()
}

/** reconcile launches or terminates instances until the group has its desired capacity. */
function reconcile(name: string) {
  const g = asgState().groups.find((x) => x.name === name)
  if (!g || g.status !== "Active" || g.suspended) return
  let live = members(name)
  const before = live.length
  if (live.length < g.desired_capacity) {
    const subnets = g.subnet_ids.length ? g.subnet_ids : [""]
    for (let n = live.length; n < g.desired_capacity; n++) {
      const sub = subnets[n % subnets.length]
      try {
        const [inst] = launchInstances(
          { name: g.name, image_id: g.launch.image_id, instance_type: g.launch.instance_type, subnet_id: sub || undefined, security_group_ids: g.launch.security_group_ids ?? undefined, user_data: g.launch.user_data, file_systems: g.launch.file_systems ?? undefined },
          { [ASG_GROUP_TAG]: g.name },
        )
        for (const tg of g.target_groups) registerTarget(tg, inst.id, undefined, inst.private_ip)
        addActivity(g, `Launching a new EC2 instance: ${inst.id}`, cause("launched", before, g.desired_capacity))
      } catch (e) {
        addActivity(g, "Launching a new EC2 instance", (e as Error).message, "Failed")
        break
      }
    }
  } else if (live.length > g.desired_capacity) {
    live = [...live].sort((a, b) => a.launch_time.localeCompare(b.launch_time))
    for (const i of live.slice(0, live.length - g.desired_capacity)) {
      terminateInstance(i.id)
      addActivity(g, `Terminating EC2 instance: ${i.id}`, cause("taken out of service", before, g.desired_capacity))
    }
  }
}

function view(g: GroupRec, detail: boolean): AutoScalingGroup {
  const ins: AsgInstance[] = members(g.name).map((i) => ({ id: i.id, state: i.state, private_ip: i.private_ip, subnet_id: i.subnet_id, launch_time: i.launch_time }))
  const { activities, ...rest } = g
  return { ...rest, instances: ins, ...(detail ? { activities } : {}) }
}

function onLoad() {
  // nothing transitional is stored here; the instances settle themselves
}

function routes(r: Router) {
  const P = "/api/v1/autoscaling/groups"
  const find = (name: string) => {
    const g = asgState().groups.find((x) => x.name === name)
    if (!g) throw notFound("auto scaling group", name)
    return g
  }
  r.get(P, () => asgState().groups.map((g) => view(g, false)))
  r.get(`${P}/:name`, ({ params }) => view(find(params.name), true))
  r.post(P, ({ body }) => {
    const s = asgState()
    const name = String(body?.name ?? "")
    if (!NAME_RE.test(name)) throw badRequest("group names are 1-255 letters, digits, dots, hyphens or underscores")
    if (s.groups.some((g) => g.name === name)) throw conflict(`auto scaling group "${name}" already exists`)
    const launch = (body?.launch ?? {}) as LaunchConfig
    if (!launch.image_id) throw badRequest("launch.image_id is required")
    const min = Number(body.min_size ?? 0)
    const desired = Number(body.desired_capacity ?? min)
    const max = Number(body.max_size) || Math.max(desired, 1)
    checkSizes(min, max, desired)
    const policies = normalizePolicies(body.policies)
    const subnets = (body.subnet_ids ?? []) as string[]
    const tgs = (body.target_groups ?? []) as string[]
    checkPlacement(launch, subnets, tgs)
    const g: GroupRec = {
      name, arn: arn("autoscaling", `autoScalingGroup:${name}`), launch: { ...launch, security_group_ids: launch.security_group_ids ?? [] }, min_size: min, max_size: max, desired_capacity: desired,
      subnet_ids: subnets, target_groups: tgs, policies, health_check_grace_seconds: body.health_check_grace_seconds || 120, suspended: false, status: "Active", created_at: new Date().toISOString(),
      last_scaling: null, activities: [],
    }
    s.groups.push(g)
    reconcile(name)
    return view(g, true)
  })
  r.patch(`${P}/:name`, ({ params, body }) => {
    const g = find(params.name)
    const min = body?.min_size ?? g.min_size
    const max = body?.max_size ?? g.max_size
    let desired = body?.desired_capacity ?? g.desired_capacity
    if (body?.desired_capacity === undefined) desired = Math.min(Math.max(desired, min), max)
    checkSizes(min, max, desired)
    const policies = body?.policies !== undefined ? normalizePolicies(body.policies) : g.policies
    if (body?.launch) checkPlacement(body.launch, g.subnet_ids, g.target_groups)
    g.min_size = min
    g.max_size = max
    g.desired_capacity = desired
    g.policies = policies
    if (body?.launch) g.launch = { ...body.launch, security_group_ids: body.launch.security_group_ids ?? [] }
    if (typeof body?.suspended === "boolean") g.suspended = body.suspended
    reconcile(g.name)
    return view(g, true)
  })
  r.del(`${P}/:name`, ({ params }) => {
    const g = find(params.name)
    g.status = "Delete in progress"
    for (const i of members(g.name)) terminateInstance(i.id)
    addActivity(g, "Deleting the group", "A user request deleted the Auto Scaling group.")
    const name = g.name
    later(3200, () => {
      const s = asgState()
      s.groups = s.groups.filter((x) => x.name !== name)
    })
    return view(g, true)
  })
}

// a terminated or stopped member gets replaced a few seconds later
instanceHooks.changed.push(() => {
  later(3500, () => {
    for (const g of asgState().groups) reconcile(g.name)
  })
})

const service: DemoService = { name: "autoscaling", seed, routes, onLoad }
export default service

