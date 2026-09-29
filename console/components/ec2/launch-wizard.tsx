"use client"

import { useEffect, useMemo, useState } from "react"
import Link from "next/link"
import { useRouter } from "next/navigation"
import { AlertTriangle, Check, ExternalLink, Info, Loader2, Plus, Rocket, Search, X } from "lucide-react"
import { toast } from "sonner"

import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Checkbox } from "@/components/ui/checkbox"
import { Input } from "@/components/ui/input"
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select"
import { Skeleton } from "@/components/ui/skeleton"
import { Textarea } from "@/components/ui/textarea"
import { ErrorState } from "@/components/console/error-state"
import { Field } from "@/components/console/form-field"
import { PageHeader } from "@/components/console/page-header"
import { Section } from "@/components/console/section"
import { TagsEditor, rowsToTags, type TagRow } from "@/components/console/tags-editor"
import { InstanceProfilePicker } from "@/components/iam/instance-profile-picker"
import { FILE_SYSTEMS_PATH, fileSystemHref, fsLabel } from "@/components/efs/common"
import { api, errorMessage } from "@/lib/api"
import { revalidate, useApi, useQueryParam } from "@/lib/hooks"
import { formatMemoryMB, formatNumber, pluralize } from "@/lib/format"
import type { FileSystem, Image, Instance, RunInstancesInput, SecurityGroup, SecurityGroupRule, Subnet } from "@/lib/types"
import { cn } from "@/lib/utils"
import { INSTANCES_PATH, InstanceTypeSelect, formatVcpu, instanceHref, useInstanceTypes } from "./instance-actions"

const DEFAULT_IMAGE = "ami-ubuntu-24-04"
const DEFAULT_TYPE = "t3.micro"

interface VolumeRow {
  size: string
  mount: string
  del: boolean
}

interface FsRow {
  id: string
  mount: string
  ro: boolean
}

type ImageFilter = "all" | "catalog" | "mine"

export function ruleSummary(r: SecurityGroupRule) {
  const ports = r.from_port === r.to_port ? `${r.from_port}` : `${r.from_port}-${r.to_port}`
  return `${r.protocol.toUpperCase()} ${ports} from ${r.cidr}`
}

function sortSubnets(list: Subnet[]) {
  return [...list].sort((a, b) => a.vpc_id.localeCompare(b.vpc_id) || a.availability_zone.localeCompare(b.availability_zone) || a.name.localeCompare(b.name))
}

export function LaunchWizard() {
  const router = useRouter()
  const imageParam = useQueryParam("image")

  const images = useApi<Image[]>("/api/v1/ec2/images", { revalidateOnFocus: false })
  const types = useInstanceTypes()
  const subnets = useApi<Subnet[]>("/api/v1/vpc/subnets", { revalidateOnFocus: false })
  const fileSystems = useApi<FileSystem[]>(FILE_SYSTEMS_PATH, { revalidateOnFocus: false })

  const [name, setName] = useState("")
  const [tagRows, setTagRows] = useState<TagRow[]>([])
  const [imageId, setImageId] = useState("")
  const [imageFilter, setImageFilter] = useState<ImageFilter>("all")
  const [imageQuery, setImageQuery] = useState("")
  const [type, setType] = useState(DEFAULT_TYPE)
  const [subnetId, setSubnetId] = useState("")
  const [sgIds, setSgIds] = useState<string[]>([])
  const [sgVpc, setSgVpc] = useState("")
  const [volumes, setVolumes] = useState<VolumeRow[]>([])
  const [fsRows, setFsRows] = useState<FsRow[]>([])
  const [userData, setUserData] = useState("")
  const [keyName, setKeyName] = useState("")
  const [profile, setProfile] = useState("")
  const [mdEndpoint, setMdEndpoint] = useState<"enabled" | "disabled">("enabled")
  const [mdTokens, setMdTokens] = useState<"optional" | "required">("required")
  const [mdHops, setMdHops] = useState("2")
  const [mdTags, setMdTags] = useState<"enabled" | "disabled">("disabled")
  const [count, setCount] = useState("1")
  const [submitted, setSubmitted] = useState(false)
  const [pending, setPending] = useState(false)

  // Default image: ?image=, else Ubuntu 24.04, else the first.
  useEffect(() => {
    if (imageId || !images.data?.length) return
    const want = [imageParam, DEFAULT_IMAGE].find((id) => id && images.data!.some((im) => im.id === id))
    setImageId(want ?? images.data[0].id)
    if (imageParam && images.data.some((im) => im.id === imageParam && im.owner !== "homecloud")) setImageFilter("mine")
  }, [images.data, imageParam, imageId])

  const sortedSubnets = useMemo(() => sortSubnets(subnets.data ?? []), [subnets.data])
  // Default subnet: the default VPC's subnet in zone "a".
  useEffect(() => {
    if (subnetId || !sortedSubnets.length) return
    const pick = sortedSubnets.find((s) => s.default && s.availability_zone.endsWith("a")) ?? sortedSubnets.find((s) => s.default) ?? sortedSubnets[0]
    setSubnetId(pick.id)
  }, [sortedSubnets, subnetId])

  const subnet = sortedSubnets.find((s) => s.id === subnetId)
  const vpcId = subnet?.vpc_id ?? ""
  const sgs = useApi<SecurityGroup[]>(vpcId ? "/api/v1/vpc/security-groups" : null, { query: { vpc_id: vpcId } })

  // Reset the security group selection to the VPC's default group whenever the VPC changes.
  useEffect(() => {
    if (!vpcId || !sgs.data || sgVpc === vpcId) return
    const groups = sgs.data.filter((g) => g.vpc_id === vpcId)
    const def = groups.find((g) => g.name === "default")
    setSgIds(def ? [def.id] : [])
    setSgVpc(vpcId)
  }, [vpcId, sgs.data, sgVpc])

  const image = images.data?.find((im) => im.id === imageId)
  const instanceType = types.data?.find((t) => t.name === type)
  const groups = (sgs.data ?? []).filter((g) => g.vpc_id === vpcId)
  const selectedGroups = groups.filter((g) => sgIds.includes(g.id))
  const publishedPorts = Array.from(
    new Set(selectedGroups.flatMap((g) => g.ingress.map((r) => (r.from_port === r.to_port ? `${r.from_port}/${r.protocol}` : `${r.from_port}-${r.to_port}/${r.protocol}`)))),
  )

  const visibleImages = useMemo(() => {
    const q = imageQuery.trim().toLowerCase()
    return (images.data ?? []).filter((im) => {
      if (imageFilter === "catalog" && im.owner !== "homecloud") return false
      if (imageFilter === "mine" && im.owner === "homecloud") return false
      if (!q) return true
      return [im.name, im.description, im.ref, im.id].some((s) => s?.toLowerCase().includes(q))
    })
  }, [images.data, imageFilter, imageQuery])

  // ---- validation ----
  const n = Number(count)
  const errors = useMemo(() => {
    const e: Record<string, string> = {}
    if (name.length > 128) e.name = "Names are at most 128 characters"
    if (!imageId) e.image = "Choose an image"
    if (!type) e.type = "Choose an instance type"
    if (!subnetId) e.subnet = "Choose a subnet"
    if (!Number.isInteger(n) || n < 1 || n > 20) e.count = "Enter a whole number from 1 to 20"
    const keys = tagRows.map((r) => r.key.trim()).filter(Boolean)
    if (new Set(keys).size !== keys.length) e.tags = "Tag keys must be unique"
    else if (tagRows.some((r) => !r.key.trim() && r.value.trim())) e.tags = "Every tag with a value needs a key"
    const mounts = new Set<string>()
    volumes.forEach((v, i) => {
      const size = Number(v.size)
      if (!Number.isInteger(size) || size < 1 || size > 16384) e[`vol${i}size`] = "1-16384 GiB"
      if (!v.mount.startsWith("/")) e[`vol${i}mount`] = "Must be an absolute path starting with /"
      else if (v.mount === "/" || /\s/.test(v.mount)) e[`vol${i}mount`] = "Choose a directory other than /, without spaces"
      else if (mounts.has(v.mount.replace(/\/+$/, ""))) e[`vol${i}mount`] = "Mount paths must be unique"
      mounts.add(v.mount.replace(/\/+$/, ""))
    })
    fsRows.forEach((f, i) => {
      if (!f.id) e[`fs${i}id`] = "Choose a file system"
      if (!f.mount.startsWith("/")) e[`fs${i}mount`] = "Must be an absolute path starting with /"
      else if (f.mount === "/" || /\s/.test(f.mount)) e[`fs${i}mount`] = "Choose a directory other than /, without spaces"
      else if (mounts.has(f.mount.replace(/\/+$/, ""))) e[`fs${i}mount`] = "Mount paths must be unique"
      mounts.add(f.mount.replace(/\/+$/, ""))
    })
    if (userData.length > 16 * 1024) e.userData = "User data is limited to 16 KB"
    if (keyName.trim() && !/^[\x20-\x7e]{1,255}$/.test(keyName.trim())) e.keyName = "Key pair names are 1-255 ASCII characters"
    const hops = Number(mdHops)
    if (!Number.isInteger(hops) || hops < 1 || hops > 64) e.mdHops = "Enter a hop limit from 1 to 64"
    return e
  }, [name, imageId, type, subnetId, n, tagRows, volumes, fsRows, userData, keyName, mdHops])
  const err = (k: string) => (submitted ? errors[k] : undefined)
  const valid = Object.keys(errors).length === 0

  const launch = async () => {
    setSubmitted(true)
    if (!valid) {
      toast.error("Fix the highlighted fields before launching")
      return
    }
    const body: RunInstancesInput = {
      name: name.trim() || undefined,
      image_id: imageId,
      instance_type: type,
      subnet_id: subnetId,
      security_group_ids: sgIds.length ? sgIds : undefined,
      user_data: userData || undefined,
      key_name: keyName.trim() || undefined,
      iam_instance_profile: profile || undefined,
      metadata_options: { http_endpoint: mdEndpoint, http_tokens: mdTokens, hop_limit: Number(mdHops), instance_metadata_tags: mdTags },
      count: n,
      tags: rowsToTags(tagRows),
      volumes: volumes.length
        ? volumes.map((v) => ({ size_gb: Number(v.size), mount_path: v.mount.replace(/(.)\/+$/, "$1"), delete_on_termination: v.del }))
        : undefined,
      file_systems: fsRows.length
        ? fsRows.map((f) => ({ file_system_id: f.id, mount_path: f.mount.replace(/(.)\/+$/, "$1"), read_only: f.ro }))
        : undefined,
    }
    setPending(true)
    try {
      const out = await api.post<Instance[]>(INSTANCES_PATH, body)
      toast.success(out.length === 1 ? `Launched ${out[0].id}` : `Launched ${pluralize(out.length, "instance")}: ${out.map((i) => i.id).join(", ")}`)
      await revalidate("/api/v1/ec2")
      router.push(out.length === 1 ? instanceHref(out[0].id) : "/ec2/")
    } catch (e) {
      toast.error(errorMessage(e))
      setPending(false)
    }
  }

  const setVol = (i: number, patch: Partial<VolumeRow>) => setVolumes(volumes.map((v, j) => (j === i ? { ...v, ...patch } : v)))
  const setFs = (i: number, patch: Partial<FsRow>) => setFsRows(fsRows.map((f, j) => (j === i ? { ...f, ...patch } : f)))
  const fsById = (id: string) => fileSystems.data?.find((f) => f.id === id)
  const addFs = () => {
    const free = (fileSystems.data ?? []).find((f) => !fsRows.some((r) => r.id === f.id))
    const used = new Set(fsRows.map((r) => r.mount))
    let mount = "/mnt/efs"
    for (let k = 2; used.has(mount); k++) mount = `/mnt/efs${k}`
    setFsRows([...fsRows, { id: free?.id ?? "", mount, ro: !!free?.read_only }])
  }

  return (
    <div className="flex flex-col gap-4">
      <PageHeader
        title="Launch an instance"
        description="Instances are containers with CPU and memory limits, a private IP in your VPC and ports published on this host by their security groups."
        breadcrumbs={[{ label: "EC2", href: "/ec2/" }, { label: "Instances", href: "/ec2/" }, { label: "Launch an instance" }]}
      />

      <form
        onSubmit={(e) => {
          e.preventDefault()
          launch()
        }}
        className="grid grid-cols-1 items-start gap-4 xl:grid-cols-[minmax(0,1fr)_320px]"
      >
        <div className="flex min-w-0 flex-col gap-4">
          {/* ---- Name and tags ---- */}
          <Section title="Name and tags">
            <div className="flex flex-col gap-4">
              <Field
                label="Name"
                htmlFor="inst-name"
                optional
                error={err("name")}
                help={n > 1 && name ? `Instances are named ${name}-1 ... ${name}-${n}.` : "Also a DNS alias for the instance inside its VPC network."}
              >
                <Input id="inst-name" value={name} onChange={(e) => setName(e.target.value)} placeholder="e.g. my-web-server" className="max-w-md" />
              </Field>
              <Field label="Additional tags" optional error={err("tags")}>
                <TagsEditor rows={tagRows} onChange={setTagRows} />
              </Field>
            </div>
          </Section>

          {/* ---- Image ---- */}
          <Section
            title="Application and OS Images (Amazon Machine Image)"
            description="An AMI is a Docker image plus how to boot it. Catalog OS images boot like a VM and stay up; application images run their own process."
          >
            <div className="flex flex-col gap-3">
              <div className="flex flex-col gap-2 sm:flex-row sm:items-center">
                <div className="relative w-full sm:max-w-xs">
                  <Search className="text-muted-foreground pointer-events-none absolute top-1/2 left-2.5 size-4 -translate-y-1/2" />
                  <Input value={imageQuery} onChange={(e) => setImageQuery(e.target.value)} placeholder="Search images" className="h-8 pl-8" />
                </div>
                <div className="bg-muted inline-flex rounded-md p-0.5 text-sm" role="tablist">
                  {(
                    [
                      ["all", "All"],
                      ["catalog", "Quick Start"],
                      ["mine", "My AMIs"],
                    ] as [ImageFilter, string][]
                  ).map(([k, label]) => (
                    <button
                      key={k}
                      type="button"
                      role="tab"
                      aria-selected={imageFilter === k}
                      onClick={() => setImageFilter(k)}
                      className={cn(
                        "rounded px-3 py-1 font-medium transition-colors",
                        imageFilter === k ? "bg-background text-foreground shadow-xs" : "text-muted-foreground hover:text-foreground",
                      )}
                    >
                      {label}
                    </button>
                  ))}
                </div>
              </div>
              {images.error ? (
                <ErrorState error={images.error} onRetry={() => images.mutate()} />
              ) : !images.data ? (
                <div className="grid grid-cols-1 gap-3 sm:grid-cols-2 2xl:grid-cols-3">
                  {Array.from({ length: 6 }, (_, i) => (
                    <Skeleton key={i} className="h-24 rounded-lg" />
                  ))}
                </div>
              ) : visibleImages.length === 0 ? (
                <p className="text-muted-foreground rounded-lg border border-dashed p-6 text-center text-sm">
                  {imageFilter === "mine" && !imageQuery ? (
                    <>
                      You have no custom images yet. Create one from an instance or{" "}
                      <Link href="/ec2/images/" className="text-primary hover:underline">
                        register a Docker image
                      </Link>
                      .
                    </>
                  ) : (
                    "No images match."
                  )}
                </p>
              ) : (
                <div className="grid grid-cols-1 gap-3 sm:grid-cols-2 2xl:grid-cols-3" role="radiogroup" aria-label="Image">
                  {visibleImages.map((im) => {
                    const active = im.id === imageId
                    return (
                      <button
                        key={im.id}
                        type="button"
                        role="radio"
                        aria-checked={active}
                        onClick={() => setImageId(im.id)}
                        className={cn(
                          "relative flex flex-col gap-1 rounded-lg border p-3 text-left transition-colors",
                          active ? "border-primary bg-primary/5 ring-primary ring-1 dark:bg-primary/10" : "hover:bg-muted/50",
                        )}
                      >
                        {active && (
                          <span className="bg-primary text-primary-foreground absolute top-2 right-2 flex size-5 items-center justify-center rounded-full">
                            <Check className="size-3.5" />
                          </span>
                        )}
                        <span className="pr-6 text-sm font-medium">{im.name}</span>
                        {im.description && <span className="text-muted-foreground line-clamp-2 text-xs">{im.description}</span>}
                        <span className="text-muted-foreground truncate font-mono text-xs" title={im.ref}>
                          {im.ref}
                        </span>
                        <span className="mt-1 flex flex-wrap gap-1">
                          <Badge variant={im.owner === "homecloud" ? "secondary" : "outline"}>{im.owner === "homecloud" ? "HomeCloud catalog" : "Custom"}</Badge>
                          {!im.keep_alive && <Badge variant="outline">Application image</Badge>}
                        </span>
                      </button>
                    )
                  })}
                </div>
              )}
              {err("image") && <p className="text-destructive text-xs">{err("image")}</p>}
              {image && (
                <p className="text-muted-foreground text-xs">
                  Selected <span className="text-foreground font-mono">{image.id}</span> ({image.ref}).{" "}
                  {image.keep_alive
                    ? "Boots like a VM: runs user data once, then stays up until stopped."
                    : "Runs the image's own entrypoint; the instance stops when that process exits."}
                </p>
              )}
            </div>
          </Section>

          {/* ---- Instance type ---- */}
          <Section title="Instance type" actions={<Link href="/ec2/instance-types/" className="text-primary text-sm hover:underline">Compare instance types</Link>}>
            <div className="flex flex-col gap-3">
              <Field label="Instance type" htmlFor="inst-type" error={err("type")}>
                <div className="max-w-md">
                  <InstanceTypeSelect id="inst-type" value={type} onChange={setType} />
                </div>
              </Field>
              {instanceType && (
                <div className="bg-muted/40 grid max-w-md grid-cols-3 gap-2 rounded-md border p-3 text-sm">
                  <div>
                    <div className="text-muted-foreground text-xs">Family</div>
                    <div>{instanceType.family}</div>
                  </div>
                  <div>
                    <div className="text-muted-foreground text-xs">vCPUs</div>
                    <div>{instanceType.vcpus}</div>
                  </div>
                  <div>
                    <div className="text-muted-foreground text-xs">Memory</div>
                    <div>{formatMemoryMB(instanceType.memory_mb)}</div>
                  </div>
                </div>
              )}
            </div>
          </Section>

          {/* ---- Key pair ---- */}
          <Section title="Key pair (login)" description="The key pair's public key is added to root's authorized_keys, for SSH to keep-alive images.">
            <Field
              label="Key pair name"
              htmlFor="key-name"
              optional
              error={err("keyName")}
              help="An existing key pair created or imported with the EC2 API (aws ec2 create-key-pair / import-key-pair). Leave empty to launch without one."
            >
              <Input id="key-name" value={keyName} onChange={(e) => setKeyName(e.target.value)} placeholder="my-key" className="max-w-md" autoComplete="off" spellCheck={false} />
            </Field>
          </Section>

          {/* ---- Network ---- */}
          <Section title="Network settings">
            <div className="flex flex-col gap-5">
              <Field
                label="Subnet"
                htmlFor="inst-subnet"
                error={err("subnet")}
                help={
                  subnet ? (
                    <>
                      VPC{" "}
                      <Link href="/vpc/" className="text-primary font-mono hover:underline">
                        {subnet.vpc_id}
                      </Link>{" "}
                      · {subnet.cidr} · {subnet.availability_zone} · {formatNumber(subnet.available_ips)} available IPs
                    </>
                  ) : undefined
                }
              >
                {subnets.error ? (
                  <ErrorState error={subnets.error} onRetry={() => subnets.mutate()} />
                ) : (
                  <Select value={subnetId} onValueChange={setSubnetId} disabled={!subnets.data}>
                    <SelectTrigger id="inst-subnet" className="w-full max-w-xl">
                      <SelectValue placeholder={subnets.data ? "Choose a subnet" : "Loading subnets..."} />
                    </SelectTrigger>
                    <SelectContent>
                      {sortedSubnets.map((s) => (
                        <SelectItem key={s.id} value={s.id} disabled={s.available_ips <= 0}>
                          <span className="font-medium">{s.name || s.id}</span>
                          <span className="text-muted-foreground font-mono text-xs">
                            {s.id} · {s.vpc_id} · {s.cidr} · {s.availability_zone} · {formatNumber(s.available_ips)} IPs
                          </span>
                        </SelectItem>
                      ))}
                    </SelectContent>
                  </Select>
                )}
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
                ) : !sgs.data || !vpcId ? (
                  <Skeleton className="h-20 rounded-md" />
                ) : groups.length === 0 ? (
                  <p className="text-muted-foreground text-sm">This VPC has no security groups.</p>
                ) : (
                  <div className="divide-y rounded-md border">
                    {groups.map((g) => {
                      const checked = sgIds.includes(g.id)
                      return (
                        <label key={g.id} className={cn("flex cursor-pointer items-start gap-3 px-3 py-2.5", checked && "bg-primary/5 dark:bg-primary/10")}>
                          <Checkbox
                            className="mt-0.5"
                            checked={checked}
                            onCheckedChange={(v) => setSgIds(v ? [...sgIds, g.id] : sgIds.filter((x) => x !== g.id))}
                          />
                          <span className="flex min-w-0 flex-col gap-0.5">
                            <span className="text-sm">
                              <span className="font-medium">{g.name}</span>{" "}
                              <span className="text-muted-foreground font-mono text-xs">{g.id}</span>
                            </span>
                            {g.description && <span className="text-muted-foreground text-xs">{g.description}</span>}
                            <span className="flex flex-wrap gap-1 pt-0.5">
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
                {sgs.data && sgIds.length === 0 && (
                  <p className="text-xs text-amber-700 dark:text-amber-400">No group selected: the VPC&apos;s default security group will be used.</p>
                )}
                <p className="text-muted-foreground flex gap-1.5 text-xs">
                  <Info className="mt-px size-3.5 shrink-0" />
                  <span>
                    Inbound (ingress) rules decide which ports are published on this host: each allowed port is mapped to a random host port, shown on the
                    instance as a clickable link. Rules are applied at launch.
                    {selectedGroups.length > 0 && (
                      <> Ports to publish: {publishedPorts.length ? <span className="text-foreground font-mono">{publishedPorts.join(", ")}</span> : "none"}.</>
                    )}
                  </span>
                </p>
              </div>
            </div>
          </Section>

          {/* ---- Storage ---- */}
          <Section title="Configure storage" description="The root filesystem is the image's own layer. Add EBS volumes for data that must outlive the container.">
            <div className="flex flex-col gap-3">
              {volumes.length === 0 && <p className="text-muted-foreground text-sm">No additional volumes.</p>}
              {volumes.map((v, i) => (
                <div key={i} className="grid grid-cols-[1fr_auto] items-start gap-3 rounded-md border p-3 sm:grid-cols-[8rem_1fr_auto_auto]">
                  <Field label="Size (GiB)" htmlFor={`vol-size-${i}`} error={err(`vol${i}size`)}>
                    <Input id={`vol-size-${i}`} type="number" min={1} value={v.size} onChange={(e) => setVol(i, { size: e.target.value })} className="h-8" />
                  </Field>
                  <Field label="Mount path" htmlFor={`vol-mount-${i}`} error={err(`vol${i}mount`)} className="col-span-2 row-start-2 sm:col-span-1 sm:row-start-1">
                    <Input id={`vol-mount-${i}`} value={v.mount} onChange={(e) => setVol(i, { mount: e.target.value })} placeholder="/data" className="h-8 font-mono" />
                  </Field>
                  <label className="col-span-2 row-start-3 flex items-center gap-2 text-sm sm:col-span-1 sm:row-start-1 sm:mt-7">
                    <Checkbox checked={v.del} onCheckedChange={(c) => setVol(i, { del: !!c })} />
                    Delete on termination
                  </label>
                  <Button
                    type="button"
                    variant="ghost"
                    size="icon"
                    className="col-start-2 row-start-1 size-8 sm:col-start-4 sm:mt-6"
                    onClick={() => setVolumes(volumes.filter((_, j) => j !== i))}
                    aria-label="Remove volume"
                  >
                    <X />
                  </Button>
                </div>
              ))}
              <div>
                <Button
                  type="button"
                  variant="outline"
                  size="sm"
                  disabled={volumes.length >= 8}
                  onClick={() => setVolumes([...volumes, { size: "8", mount: volumes.length ? `/data${volumes.length + 1}` : "/data", del: true }])}
                >
                  <Plus /> Add new volume
                </Button>
              </div>
              <p className="text-muted-foreground text-xs">Sizes are advisory: Docker volumes are not capped.</p>
            </div>
          </Section>

          {/* ---- File systems (EFS) ---- */}
          <Section
            title="File systems"
            description="Mount shared EFS file systems. Several instances can mount the same file system and see the same files."
            actions={
              <Link href="/efs/" className="text-primary inline-flex items-center gap-1 text-sm hover:underline">
                Manage file systems <ExternalLink className="size-3.5" />
              </Link>
            }
          >
            <div className="flex flex-col gap-3">
              {fileSystems.error ? (
                <ErrorState error={fileSystems.error} onRetry={() => fileSystems.mutate()} />
              ) : fileSystems.data?.length === 0 ? (
                <p className="text-muted-foreground text-sm">
                  You have no file systems.{" "}
                  <Link href="/efs/?create=1" className="text-primary hover:underline">
                    Create a file system
                  </Link>{" "}
                  to share files between instances.
                </p>
              ) : (
                fsRows.length === 0 && <p className="text-muted-foreground text-sm">No file systems mounted.</p>
              )}
              {fsRows.map((f, i) => {
                const fs = fsById(f.id)
                return (
                  <div key={i} className="grid grid-cols-[1fr_auto] items-start gap-3 rounded-md border p-3 sm:grid-cols-[minmax(0,1fr)_minmax(0,1fr)_auto_auto]">
                    <Field label="File system" htmlFor={`fs-id-${i}`} error={err(`fs${i}id`)}>
                      <Select value={f.id} onValueChange={(v) => v && setFs(i, { id: v, ro: f.ro || !!fsById(v)?.read_only })}>
                        <SelectTrigger id={`fs-id-${i}`} className="h-8 w-full">
                          <SelectValue placeholder={fileSystems.data ? "Choose a file system" : "Loading..."} />
                        </SelectTrigger>
                        <SelectContent>
                          {(fileSystems.data ?? []).map((x) => (
                            <SelectItem key={x.id} value={x.id}>
                              <span className="font-medium">{fsLabel(x)}</span>
                              <span className="text-muted-foreground font-mono text-xs">
                                {x.id}
                                {x.read_only ? " · read-only" : ""}
                              </span>
                            </SelectItem>
                          ))}
                        </SelectContent>
                      </Select>
                    </Field>
                    <Field label="Mount path" htmlFor={`fs-mount-${i}`} error={err(`fs${i}mount`)} className="col-span-2 row-start-2 sm:col-span-1 sm:row-start-1">
                      <Input id={`fs-mount-${i}`} value={f.mount} onChange={(e) => setFs(i, { mount: e.target.value })} placeholder="/mnt/efs" className="h-8 font-mono" />
                    </Field>
                    <label className="col-span-2 row-start-3 flex items-center gap-2 text-sm sm:col-span-1 sm:row-start-1 sm:mt-7">
                      <Checkbox checked={f.ro} disabled={!!fs?.read_only} onCheckedChange={(c) => setFs(i, { ro: !!c })} />
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
                    {fs?.read_only && <p className="text-muted-foreground col-span-full text-xs">This file system is read-only, so it is mounted read-only.</p>}
                  </div>
                )
              })}
              {(fileSystems.data?.length ?? 0) > 0 && (
                <div>
                  <Button type="button" variant="outline" size="sm" disabled={fsRows.length >= 8} onClick={addFs}>
                    <Plus /> Add file system
                  </Button>
                </div>
              )}
            </div>
          </Section>

          {/* ---- Advanced ---- */}
          <Section title="Advanced details">
            <div className="flex flex-col gap-4">
              <Field
                label="IAM instance profile"
                htmlFor="inst-profile"
                optional
                help="The profile's role credentials are served to the instance by the metadata service (169.254.169.254)."
              >
                <div className="max-w-md">
                  <InstanceProfilePicker id="inst-profile" value={profile} onChange={setProfile} />
                </div>
              </Field>
              <div className="grid max-w-2xl gap-4 sm:grid-cols-2">
                <Field label="Metadata accessible" htmlFor="md-endpoint">
                  <Select value={mdEndpoint} onValueChange={(v) => setMdEndpoint(v as "enabled" | "disabled")}>
                    <SelectTrigger id="md-endpoint" className="w-full">
                      <SelectValue />
                    </SelectTrigger>
                    <SelectContent>
                      <SelectItem value="enabled">Enabled</SelectItem>
                      <SelectItem value="disabled">Disabled</SelectItem>
                    </SelectContent>
                  </Select>
                </Field>
                <Field label="Metadata version" htmlFor="md-tokens" help={mdTokens === "required" ? "Requests need a session token (PUT /latest/api/token)." : "Token-less IMDSv1 requests are also accepted."}>
                  <Select value={mdTokens} onValueChange={(v) => setMdTokens(v as "optional" | "required")} disabled={mdEndpoint === "disabled"}>
                    <SelectTrigger id="md-tokens" className="w-full">
                      <SelectValue />
                    </SelectTrigger>
                    <SelectContent>
                      <SelectItem value="required">V2 only (token required)</SelectItem>
                      <SelectItem value="optional">V1 and V2 (token optional)</SelectItem>
                    </SelectContent>
                  </Select>
                </Field>
                <Field label="Metadata response hop limit" htmlFor="md-hops" error={err("mdHops")}>
                  <Input id="md-hops" type="number" min={1} max={64} value={mdHops} onChange={(e) => setMdHops(e.target.value)} className="w-28" disabled={mdEndpoint === "disabled"} />
                </Field>
                <Field label="Allow tags in metadata" htmlFor="md-tags">
                  <Select value={mdTags} onValueChange={(v) => setMdTags(v as "enabled" | "disabled")} disabled={mdEndpoint === "disabled"}>
                    <SelectTrigger id="md-tags" className="w-full">
                      <SelectValue />
                    </SelectTrigger>
                    <SelectContent>
                      <SelectItem value="disabled">Disable</SelectItem>
                      <SelectItem value="enabled">Enable</SelectItem>
                    </SelectContent>
                  </Select>
                </Field>
              </div>
              <Field
                label="User data"
                htmlFor="user-data"
                optional
                error={err("userData")}
                help="A shell script written to /var/lib/homecloud/user-data. Keep-alive (VM-like) images run it once at first boot; output appears in the console output."
              >
                <Textarea
                  id="user-data"
                  rows={7}
                  value={userData}
                  onChange={(e) => setUserData(e.target.value)}
                  placeholder={"#!/bin/sh\napt-get update && apt-get install -y curl"}
                  className="font-mono text-[13px]"
                  spellCheck={false}
                />
              </Field>
              {image && !image.keep_alive && userData.trim() && (
                <p className="flex gap-2 rounded-md border border-amber-600/30 bg-amber-50 p-3 text-sm text-amber-800 dark:border-amber-400/30 dark:bg-amber-500/10 dark:text-amber-300">
                  <AlertTriangle className="mt-0.5 size-4 shrink-0" />
                  <span>
                    <strong>{image.name}</strong> is an application image: it runs its own entrypoint, so user data is written to
                    /var/lib/homecloud/user-data but not executed. Only keep-alive images run it.
                  </span>
                </p>
              )}
            </div>
          </Section>
        </div>

        {/* ---- Summary ---- */}
        <aside className="xl:sticky xl:top-4">
          <Section title="Summary">
            <div className="flex flex-col gap-4">
              <Field label="Number of instances" htmlFor="count" error={err("count")}>
                <Input id="count" type="number" min={1} max={20} value={count} onChange={(e) => setCount(e.target.value)} className="h-8 w-28" />
              </Field>
              <dl className="flex flex-col gap-3 border-t pt-3 text-sm">
                <SummaryItem label="Software image (AMI)">
                  {image ? (
                    <>
                      <div>{image.name}</div>
                      <div className="text-muted-foreground font-mono text-xs">{image.id}</div>
                    </>
                  ) : (
                    "-"
                  )}
                </SummaryItem>
                <SummaryItem label="Instance type">
                  <span className="font-mono text-[13px]">{type || "-"}</span>
                  {instanceType && (
                    <span className="text-muted-foreground text-xs">
                      {" "}
                      ({formatVcpu(instanceType.vcpus)}, {formatMemoryMB(instanceType.memory_mb)})
                    </span>
                  )}
                </SummaryItem>
                <SummaryItem label="Subnet">
                  {subnet ? (
                    <>
                      <div>{subnet.name || subnet.id}</div>
                      <div className="text-muted-foreground text-xs">{subnet.availability_zone}</div>
                    </>
                  ) : (
                    "-"
                  )}
                </SummaryItem>
                <SummaryItem label="Security groups">
                  {selectedGroups.length ? selectedGroups.map((g) => g.name).join(", ") : "default"}
                </SummaryItem>
                <SummaryItem label="Key pair">{keyName.trim() || "None"}</SummaryItem>
                <SummaryItem label="Instance profile">{profile || "None"}</SummaryItem>
                <SummaryItem label="Storage">
                  {volumes.length
                    ? `Root + ${pluralize(volumes.length, "volume")} (${volumes.reduce((a, v) => a + (Number(v.size) || 0), 0)} GiB)`
                    : "Root filesystem only"}
                </SummaryItem>
                {fsRows.length > 0 && (
                  <SummaryItem label="File systems">
                    {fsRows.map((f, i) => {
                      const fs = fsById(f.id)
                      return (
                        <div key={i} className="min-w-0">
                          {fs ? (
                            <Link href={fileSystemHref(fs.id)} className="text-primary hover:underline">
                              {fsLabel(fs)}
                            </Link>
                          ) : (
                            <span className="text-muted-foreground">Not chosen</span>
                          )}{" "}
                          <span className="text-muted-foreground font-mono text-xs">
                            {f.mount || "-"}
                            {f.ro ? " (ro)" : ""}
                          </span>
                        </div>
                      )
                    })}
                  </SummaryItem>
                )}
                {Number.isInteger(n) && n > 1 && instanceType && (
                  <SummaryItem label="Total reserved">
                    {formatNumber(instanceType.vcpus * n)} vCPUs, {formatMemoryMB(instanceType.memory_mb * n)}
                  </SummaryItem>
                )}
              </dl>
              {submitted && !valid && <p className="text-destructive text-xs">Some settings need attention. Check the highlighted fields.</p>}
              <div className="flex flex-col gap-2 border-t pt-4">
                <Button type="submit" disabled={pending || !image}>
                  {pending ? <Loader2 className="animate-spin" /> : <Rocket />}
                  Launch {Number.isInteger(n) && n > 1 ? `${n} instances` : "instance"}
                </Button>
                <Button type="button" variant="outline" asChild>
                  <Link href="/ec2/">Cancel</Link>
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
