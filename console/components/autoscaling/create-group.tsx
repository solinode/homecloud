"use client"

import { useEffect, useMemo, useState } from "react"
import Link from "next/link"
import { useRouter } from "next/navigation"
import { ExternalLink, Info, Loader2, Plus, Scaling, X } from "lucide-react"
import { toast } from "sonner"

import { Button } from "@/components/ui/button"
import { Checkbox } from "@/components/ui/checkbox"
import { Input } from "@/components/ui/input"
import { Select, SelectContent, SelectGroup, SelectItem, SelectLabel, SelectTrigger, SelectValue } from "@/components/ui/select"
import { Skeleton } from "@/components/ui/skeleton"
import { Switch } from "@/components/ui/switch"
import { Textarea } from "@/components/ui/textarea"
import { ErrorState } from "@/components/console/error-state"
import { Field } from "@/components/console/form-field"
import { PageHeader } from "@/components/console/page-header"
import { Section } from "@/components/console/section"
import { InstanceTypeSelect } from "@/components/ec2/instance-actions"
import { ruleSummary } from "@/components/ec2/launch-wizard"
import { FILE_SYSTEMS_PATH, fsLabel } from "@/components/efs/common"
import { useTargetGroups } from "@/components/elb/shared"
import { api, errorMessage } from "@/lib/api"
import { formatNumber, pluralize } from "@/lib/format"
import { revalidate, useApi } from "@/lib/hooks"
import type { AutoScalingGroup, CreateAutoScalingGroupInput, FileSystem, Image, SecurityGroup, Subnet, Vpc } from "@/lib/types"
import { cn } from "@/lib/utils"

import { ASG_PREFIX, CapacityFields, GROUP_NAME_RE, PolicyFields, capacityErrors, draftToPolicy, groupHref, metricLabel, newPolicyDraft, policyDraftErrors, type PolicyDraft } from "./shared"

const DEFAULT_IMAGE = "ami-alpine-3-20"
const DEFAULT_TYPE = "t3.micro"

interface FsRow {
  id: string
  mount: string
  ro: boolean
}

export function CreateGroup() {
  const router = useRouter()
  const images = useApi<Image[]>("/api/v1/ec2/images", { revalidateOnFocus: false })
  const vpcs = useApi<Vpc[]>("/api/v1/vpc/vpcs", { revalidateOnFocus: false })
  const subnets = useApi<Subnet[]>("/api/v1/vpc/subnets", { revalidateOnFocus: false })
  const fileSystems = useApi<FileSystem[]>(FILE_SYSTEMS_PATH, { revalidateOnFocus: false })
  const tgs = useTargetGroups()

  const [name, setName] = useState("")
  const [imageId, setImageId] = useState("")
  const [type, setType] = useState(DEFAULT_TYPE)
  const [vpcId, setVpcId] = useState("")
  const [subnetIds, setSubnetIds] = useState<string[]>([])
  const [sgIds, setSgIds] = useState<string[]>([])
  const [userData, setUserData] = useState("")
  const [fsRows, setFsRows] = useState<FsRow[]>([])
  const [tgNames, setTgNames] = useState<string[]>([])
  const [cap, setCap] = useState({ min: "1", desired: "1", max: "2" })
  const [grace, setGrace] = useState("120")
  const [scaling, setScaling] = useState(false)
  const [policy, setPolicy] = useState<PolicyDraft>(newPolicyDraft())
  const [submitted, setSubmitted] = useState(false)
  const [pending, setPending] = useState(false)

  // Defaults: Alpine (small and fast to boot), the default VPC and all of its subnets.
  useEffect(() => {
    if (imageId || !images.data?.length) return
    setImageId(images.data.find((i) => i.id === DEFAULT_IMAGE)?.id ?? images.data[0].id)
  }, [images.data, imageId])
  useEffect(() => {
    if (vpcId || !vpcs.data?.length) return
    setVpcId((vpcs.data.find((v) => v.default) ?? vpcs.data[0]).id)
  }, [vpcs.data, vpcId])
  const vpcSubnets = useMemo(
    () => (subnets.data ?? []).filter((s) => s.vpc_id === vpcId).sort((a, b) => a.availability_zone.localeCompare(b.availability_zone) || a.name.localeCompare(b.name)),
    [subnets.data, vpcId],
  )
  const sgs = useApi<SecurityGroup[]>(vpcId ? "/api/v1/vpc/security-groups" : null, { query: { vpc_id: vpcId } })
  const groups = (sgs.data ?? []).filter((g) => g.vpc_id === vpcId)
  const vpcTgs = (tgs.data ?? []).filter((t) => t.vpc_id === vpcId)

  // Changing the VPC resets the choices that belong to it.
  const [initFor, setInitFor] = useState("")
  useEffect(() => {
    if (!vpcId || !subnets.data || !sgs.data || initFor === vpcId) return
    setSubnetIds(vpcSubnets.map((s) => s.id))
    const def = groups.find((g) => g.name === "default")
    setSgIds(def ? [def.id] : [])
    setTgNames([])
    setInitFor(vpcId)
  }, [vpcId, subnets.data, sgs.data, initFor, vpcSubnets, groups])

  const image = images.data?.find((i) => i.id === imageId)
  const catalog = (images.data ?? []).filter((i) => i.owner === "homecloud")
  const mine = (images.data ?? []).filter((i) => i.owner !== "homecloud")

  const errors = useMemo(() => {
    const e: Record<string, string> = {}
    if (!name) e.name = "Enter a name"
    else if (!GROUP_NAME_RE.test(name)) e.name = "1-255 letters, digits, dots, hyphens or underscores"
    if (!imageId) e.image = "Choose an image"
    if (!type) e.type = "Choose an instance type"
    if (!subnetIds.length) e.subnets = "Choose at least one subnet"
    Object.assign(e, capacityErrors(cap.min, cap.desired, cap.max))
    if (!/^\d+$/.test(grace) || Number(grace) > 3600) e.grace = "0-3600 seconds"
    const mounts = new Set<string>()
    fsRows.forEach((f, i) => {
      if (!f.id) e[`fs${i}id`] = "Choose a file system"
      if (!f.mount.startsWith("/") || f.mount === "/" || /\s/.test(f.mount)) e[`fs${i}mount`] = "An absolute path other than /, without spaces"
      else if (mounts.has(f.mount)) e[`fs${i}mount`] = "Mount paths must be unique"
      mounts.add(f.mount)
    })
    if (userData.length > 16 * 1024) e.userData = "User data is limited to 16 KB"
    if (scaling) for (const [k, v] of Object.entries(policyDraftErrors(policy))) e[`pol${k}`] = v
    return e
  }, [name, imageId, type, subnetIds, cap, grace, fsRows, userData, scaling, policy])
  const err = (k: string) => (submitted ? errors[k] : undefined)
  const valid = Object.keys(errors).length === 0

  const create = async () => {
    setSubmitted(true)
    if (!valid) {
      toast.error("Fix the highlighted fields before creating the group")
      return
    }
    const body: CreateAutoScalingGroupInput = {
      name,
      launch: {
        image_id: imageId,
        instance_type: type,
        security_group_ids: sgIds,
        user_data: userData || undefined,
        file_systems: fsRows.length ? fsRows.map((f) => ({ file_system_id: f.id, mount_path: f.mount.replace(/(.)\/+$/, "$1"), read_only: f.ro })) : undefined,
      },
      min_size: Number(cap.min),
      max_size: Number(cap.max),
      desired_capacity: Number(cap.desired),
      subnet_ids: subnetIds,
      target_groups: tgNames.length ? tgNames : undefined,
      health_check_grace_seconds: Number(grace) || undefined,
      policies: scaling ? [draftToPolicy(policy)] : undefined,
    }
    setPending(true)
    try {
      const g = await api.post<AutoScalingGroup>(`${ASG_PREFIX}/groups`, body)
      toast.success(`Created Auto Scaling group ${g.name}; launching ${pluralize(g.desired_capacity, "instance")}`)
      await revalidate(ASG_PREFIX)
      router.push(groupHref(g.name))
    } catch (e) {
      toast.error(errorMessage(e))
      setPending(false)
    }
  }

  const setFs = (i: number, patch: Partial<FsRow>) => setFsRows(fsRows.map((f, j) => (j === i ? { ...f, ...patch } : f)))
  const fsById = (id: string) => fileSystems.data?.find((f) => f.id === id)

  return (
    <div className="flex flex-col gap-4">
      <PageHeader
        title="Create Auto Scaling group"
        description="A group launches identical instances from its launch configuration and keeps the running count at the desired capacity."
        breadcrumbs={[{ label: "EC2", href: "/ec2/" }, { label: "Auto Scaling groups", href: "/ec2/autoscaling/" }, { label: "Create" }]}
      />
      <form
        onSubmit={(e) => {
          e.preventDefault()
          create()
        }}
        className="grid grid-cols-1 items-start gap-4 xl:grid-cols-[minmax(0,1fr)_320px]"
      >
        <div className="flex min-w-0 flex-col gap-4">
          <Section title="Name">
            <Field label="Auto Scaling group name" htmlFor="asg-name" error={err("name")} help="Also the name of the instances it launches.">
              <Input id="asg-name" autoFocus value={name} onChange={(e) => setName(e.target.value)} placeholder="web-asg" className="max-w-md" autoComplete="off" />
            </Field>
          </Section>

          <Section title="Launch configuration" description="Applies to every instance the group launches. You can change it later; new instances use the new configuration.">
            <div className="flex flex-col gap-4">
              <Field label="Image (AMI)" htmlFor="asg-image" error={err("image")} help={image ? `${image.ref}${image.keep_alive ? " · boots like a VM and runs user data" : " · runs the image's own process"}` : undefined}>
                {images.error ? (
                  <ErrorState error={images.error} onRetry={() => images.mutate()} />
                ) : (
                  <Select value={imageId} onValueChange={setImageId} disabled={!images.data}>
                    <SelectTrigger id="asg-image" className="w-full max-w-xl">
                      <SelectValue placeholder={images.data ? "Choose an image" : "Loading images..."} />
                    </SelectTrigger>
                    <SelectContent>
                      {catalog.length > 0 && (
                        <SelectGroup>
                          <SelectLabel>HomeCloud catalog</SelectLabel>
                          {catalog.map((i) => (
                            <SelectItem key={i.id} value={i.id}>
                              {i.name}
                              <span className="text-muted-foreground font-mono text-xs">{i.id}</span>
                            </SelectItem>
                          ))}
                        </SelectGroup>
                      )}
                      {mine.length > 0 && (
                        <SelectGroup>
                          <SelectLabel>My AMIs</SelectLabel>
                          {mine.map((i) => (
                            <SelectItem key={i.id} value={i.id}>
                              {i.name}
                              <span className="text-muted-foreground font-mono text-xs">{i.id}</span>
                            </SelectItem>
                          ))}
                        </SelectGroup>
                      )}
                    </SelectContent>
                  </Select>
                )}
              </Field>
              <Field label="Instance type" htmlFor="asg-type" error={err("type")}>
                <div className="max-w-md">
                  <InstanceTypeSelect id="asg-type" value={type} onChange={setType} />
                </div>
              </Field>

              <div className="flex flex-col gap-2">
                <div className="flex flex-wrap items-baseline justify-between gap-2">
                  <span className="text-sm font-medium">Security groups</span>
                  <Link href="/vpc/security-groups/" className="text-primary inline-flex items-center gap-1 text-sm hover:underline">
                    Manage security groups <ExternalLink className="size-3.5" />
                  </Link>
                </div>
                {sgs.error ? (
                  <ErrorState error={sgs.error} onRetry={() => sgs.mutate()} />
                ) : !sgs.data ? (
                  <Skeleton className="h-16 rounded-md" />
                ) : groups.length === 0 ? (
                  <p className="text-muted-foreground text-sm">This VPC has no security groups; the default group is used.</p>
                ) : (
                  <div className="divide-y rounded-md border">
                    {groups.map((g) => {
                      const checked = sgIds.includes(g.id)
                      return (
                        <label key={g.id} className={cn("flex cursor-pointer items-start gap-3 px-3 py-2", checked && "bg-primary/5 dark:bg-primary/10")}>
                          <Checkbox className="mt-0.5" checked={checked} onCheckedChange={(v) => setSgIds(v ? [...sgIds, g.id] : sgIds.filter((x) => x !== g.id))} />
                          <span className="flex min-w-0 flex-col gap-0.5 text-sm">
                            <span>
                              <span className="font-medium">{g.name}</span> <span className="text-muted-foreground font-mono text-xs">{g.id}</span>
                            </span>
                            <span className="flex flex-wrap gap-1">
                              {g.ingress.length === 0 ? (
                                <span className="text-muted-foreground text-xs">No inbound rules</span>
                              ) : (
                                g.ingress.map((r) => (
                                  <span key={r.id} className="bg-muted rounded border px-1.5 py-0.5 font-mono text-[11px]">
                                    {ruleSummary(r)}
                                  </span>
                                ))
                              )}
                            </span>
                          </span>
                        </label>
                      )
                    })}
                  </div>
                )}
              </div>

              <Field label="User data" htmlFor="asg-ud" optional error={err("userData")} help="A shell script run once at first boot by keep-alive (VM-like) images.">
                <Textarea
                  id="asg-ud"
                  rows={5}
                  value={userData}
                  onChange={(e) => setUserData(e.target.value)}
                  placeholder={"#!/bin/sh\napk add --no-cache nginx && nginx"}
                  className="font-mono text-[13px]"
                  spellCheck={false}
                />
              </Field>

              <div className="flex flex-col gap-2">
                <span className="text-sm font-medium">
                  EFS file systems <span className="text-muted-foreground font-normal">- optional</span>
                </span>
                {fileSystems.data?.length === 0 && <p className="text-muted-foreground text-sm">You have no file systems.</p>}
                {fsRows.map((f, i) => (
                  <div key={i} className="grid grid-cols-[1fr_auto] items-start gap-3 rounded-md border p-3 sm:grid-cols-[minmax(0,1fr)_minmax(0,1fr)_auto_auto]">
                    <Field label="File system" htmlFor={`asg-fs-${i}`} error={err(`fs${i}id`)}>
                      <Select value={f.id} onValueChange={(v) => v && setFs(i, { id: v, ro: f.ro || !!fsById(v)?.read_only })}>
                        <SelectTrigger id={`asg-fs-${i}`} className="h-8 w-full">
                          <SelectValue placeholder="Choose a file system" />
                        </SelectTrigger>
                        <SelectContent>
                          {(fileSystems.data ?? []).map((x) => (
                            <SelectItem key={x.id} value={x.id}>
                              {fsLabel(x)}
                              <span className="text-muted-foreground font-mono text-xs">{x.id}</span>
                            </SelectItem>
                          ))}
                        </SelectContent>
                      </Select>
                    </Field>
                    <Field label="Mount path" htmlFor={`asg-fsm-${i}`} error={err(`fs${i}mount`)} className="col-span-2 row-start-2 sm:col-span-1 sm:row-start-1">
                      <Input id={`asg-fsm-${i}`} value={f.mount} onChange={(e) => setFs(i, { mount: e.target.value })} className="h-8 font-mono" />
                    </Field>
                    <label className="col-span-2 row-start-3 flex items-center gap-2 text-sm sm:col-span-1 sm:row-start-1 sm:mt-7">
                      <Checkbox checked={f.ro} disabled={!!fsById(f.id)?.read_only} onCheckedChange={(c) => setFs(i, { ro: !!c })} />
                      Read-only
                    </label>
                    <Button
                      type="button"
                      variant="ghost"
                      size="icon"
                      className="col-start-2 row-start-1 size-8 sm:col-start-4 sm:mt-6"
                      onClick={() => setFsRows(fsRows.filter((_, j) => j !== i))}
                      aria-label="Remove file system"
                    >
                      <X />
                    </Button>
                  </div>
                ))}
                {(fileSystems.data?.length ?? 0) > 0 && (
                  <div>
                    <Button
                      type="button"
                      variant="outline"
                      size="sm"
                      disabled={fsRows.length >= 8}
                      onClick={() => setFsRows([...fsRows, { id: "", mount: fsRows.length ? `/mnt/efs${fsRows.length + 1}` : "/mnt/efs", ro: false }])}
                    >
                      <Plus /> Add file system
                    </Button>
                  </div>
                )}
              </div>
            </div>
          </Section>

          <Section title="Network" description="Instances are spread across the chosen subnets in turn.">
            <div className="flex flex-col gap-4">
              <Field label="VPC" htmlFor="asg-vpc">
                <Select value={vpcId} onValueChange={setVpcId} disabled={!vpcs.data}>
                  <SelectTrigger id="asg-vpc" className="w-full max-w-md">
                    <SelectValue placeholder={vpcs.data ? "Choose a VPC" : "Loading VPCs..."} />
                  </SelectTrigger>
                  <SelectContent>
                    {(vpcs.data ?? []).map((v) => (
                      <SelectItem key={v.id} value={v.id}>
                        {v.name || v.id}
                        <span className="text-muted-foreground font-mono text-xs">
                          {v.id} · {v.cidr}
                        </span>
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              </Field>
              <Field label="Subnets" error={err("subnets")}>
                {!subnets.data ? (
                  <Skeleton className="h-16 rounded-md" />
                ) : vpcSubnets.length === 0 ? (
                  <p className="text-muted-foreground text-sm">This VPC has no subnets.</p>
                ) : (
                  <div className="divide-y rounded-md border">
                    {vpcSubnets.map((s) => (
                      <label key={s.id} className="flex cursor-pointer items-center gap-3 px-3 py-2 text-sm">
                        <Checkbox checked={subnetIds.includes(s.id)} onCheckedChange={(v) => setSubnetIds(v ? [...subnetIds, s.id] : subnetIds.filter((x) => x !== s.id))} />
                        <span className="font-medium">{s.name || s.id}</span>
                        <span className="text-muted-foreground font-mono text-xs">
                          {s.id} · {s.cidr} · {s.availability_zone} · {formatNumber(s.available_ips)} IPs
                        </span>
                      </label>
                    ))}
                  </div>
                )}
              </Field>
            </div>
          </Section>

          <Section
            title="Load balancing"
            description="Running members are registered with these target groups and deregistered when they are terminated."
            actions={
              <Link href="/elb/target-groups/" className="text-primary text-sm hover:underline">
                Manage target groups
              </Link>
            }
          >
            {!tgs.data ? (
              <Skeleton className="h-12 rounded-md" />
            ) : vpcTgs.length === 0 ? (
              <p className="text-muted-foreground text-sm">No target groups in this VPC. The group can run without a load balancer.</p>
            ) : (
              <div className="divide-y rounded-md border">
                {vpcTgs.map((t) => (
                  <label key={t.name} className="flex cursor-pointer items-center gap-3 px-3 py-2 text-sm">
                    <Checkbox checked={tgNames.includes(t.name)} onCheckedChange={(v) => setTgNames(v ? [...tgNames, t.name] : tgNames.filter((x) => x !== t.name))} />
                    <span className="font-medium">{t.name}</span>
                    <span className="text-muted-foreground font-mono text-xs">
                      {t.protocol}:{t.port} · {pluralize(t.targets.length, "target")}
                    </span>
                  </label>
                ))}
              </div>
            )}
          </Section>

          <Section title="Group size and health">
            <div className="flex flex-col gap-4">
              <div className="max-w-md">
                <CapacityFields min={cap.min} desired={cap.desired} max={cap.max} onChange={setCap} errors={{ min: err("min"), desired: err("desired"), max: err("max") }} idPrefix="asg" />
              </div>
              <Field
                label="Health check grace period (seconds)"
                htmlFor="asg-grace"
                error={err("grace")}
                help="New instances are left out of scaling metrics for this long while they boot. Stopped instances are always replaced."
              >
                <Input id="asg-grace" inputMode="numeric" value={grace} onChange={(e) => setGrace(e.target.value)} className="w-28" />
              </Field>
            </div>
          </Section>

          <Section title="Scaling policy" description="Optional target tracking: adjust the desired capacity to keep a metric near a target.">
            <div className="flex flex-col gap-3">
              <label className="flex items-center gap-3 text-sm">
                <Switch checked={scaling} onCheckedChange={setScaling} />
                Target tracking scaling policy
              </label>
              {scaling && (
                <PolicyFields draft={policy} onChange={setPolicy} errors={{ target: err("poltarget"), cooldown: err("polcooldown") }} idPrefix="asg-pol" />
              )}
              <p className="text-muted-foreground flex gap-1.5 text-xs">
                <Info className="mt-px size-3.5 shrink-0" />
                Metrics come from CloudWatch (HC/EC2, per instance, 1-minute averages). Scale-in happens one instance at a time when the average is below 80% of the
                target.
              </p>
            </div>
          </Section>
        </div>

        <aside className="xl:sticky xl:top-4">
          <Section title="Summary">
            <div className="flex flex-col gap-4">
              <dl className="flex flex-col gap-3 text-sm">
                <SummaryItem label="Name">{name || "-"}</SummaryItem>
                <SummaryItem label="Image">{image ? `${image.name} (${image.id})` : "-"}</SummaryItem>
                <SummaryItem label="Instance type">
                  <span className="font-mono text-[13px]">{type || "-"}</span>
                </SummaryItem>
                <SummaryItem label="Capacity">
                  desired {cap.desired || "?"} (min {cap.min || "?"}, max {cap.max || "?"})
                </SummaryItem>
                <SummaryItem label="Subnets">{subnetIds.length ? pluralize(subnetIds.length, "subnet") : "-"}</SummaryItem>
                <SummaryItem label="Target groups">{tgNames.length ? tgNames.join(", ") : "None"}</SummaryItem>
                <SummaryItem label="Scaling">{scaling ? `${metricLabel(policy.metric)} at ${policy.target || "?"}%` : "Manual (fixed desired capacity)"}</SummaryItem>
              </dl>
              {submitted && !valid && <p className="text-destructive text-xs">Some settings need attention. Check the highlighted fields.</p>}
              <div className="flex flex-col gap-2 border-t pt-4">
                <Button type="submit" disabled={pending}>
                  {pending ? <Loader2 className="animate-spin" /> : <Scaling />}
                  Create Auto Scaling group
                </Button>
                <Button type="button" variant="outline" asChild>
                  <Link href="/ec2/autoscaling/">Cancel</Link>
                </Button>
              </div>
            </div>
          </Section>
        </aside>
      </form>
    </div>
  )
}

function SummaryItem({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div>
      <dt className="text-muted-foreground text-xs font-medium">{label}</dt>
      <dd className="min-w-0 break-words">{children}</dd>
    </div>
  )
}
