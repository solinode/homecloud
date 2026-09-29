"use client"

import { useEffect, useMemo, useState } from "react"
import Link from "next/link"
import { useRouter } from "next/navigation"
import { Check, Globe, Info, Loader2, Lock, Network, Plus, X } from "lucide-react"
import { toast } from "sonner"

import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select"
import { ErrorState } from "@/components/console/error-state"
import { Field } from "@/components/console/form-field"
import { PageHeader } from "@/components/console/page-header"
import { Section } from "@/components/console/section"
import { TagsEditor, rowsToTags, type TagRow } from "@/components/console/tags-editor"
import { api, errorMessage } from "@/lib/api"
import { formatNumber, pluralize } from "@/lib/format"
import { revalidate, useApi } from "@/lib/hooks"
import type { CreateLoadBalancerInput, LoadBalancer, LoadBalancerScheme, Subnet } from "@/lib/types"
import { cn } from "@/lib/utils"
import { ELB_PREFIX, LBS_PATH, NAME_RE, TargetGroupSelect, lbHref, useTargetGroups, vpcHref } from "./shared"

interface ListenerRow {
  port: string
  publicPort: string
  tg: string
}

const SCHEMES: { value: LoadBalancerScheme; label: string; icon: typeof Globe; blurb: string }[] = [
  {
    value: "internet-facing",
    label: "Internet-facing",
    icon: Globe,
    blurb: "Listener ports are published on this host, so browsers and clients outside the VPC can reach the load balancer.",
  },
  {
    value: "internal",
    label: "Internal",
    icon: Lock,
    blurb: "Reachable only inside the VPC by its DNS name. Nothing is published on the host.",
  },
]

function sortSubnets(list: Subnet[]) {
  return [...list].sort((a, b) => a.vpc_id.localeCompare(b.vpc_id) || a.availability_zone.localeCompare(b.availability_zone) || a.name.localeCompare(b.name))
}

const validPort = (s: string) => /^\d+$/.test(s) && Number(s) >= 1 && Number(s) <= 65535

export function CreateLoadBalancer() {
  const router = useRouter()
  const subnets = useApi<Subnet[]>("/api/v1/vpc/subnets", { revalidateOnFocus: false })
  const tgs = useTargetGroups()

  const [name, setName] = useState("")
  const [scheme, setScheme] = useState<LoadBalancerScheme>("internet-facing")
  const [subnetId, setSubnetId] = useState("")
  const [listeners, setListeners] = useState<ListenerRow[]>([{ port: "80", publicPort: "", tg: "" }])
  const [tagRows, setTagRows] = useState<TagRow[]>([])
  const [submitted, setSubmitted] = useState(false)
  const [pending, setPending] = useState(false)

  const sortedSubnets = useMemo(() => sortSubnets(subnets.data ?? []), [subnets.data])
  useEffect(() => {
    if (subnetId || !sortedSubnets.length) return
    const pick = sortedSubnets.find((s) => s.default && s.availability_zone.endsWith("a")) ?? sortedSubnets.find((s) => s.default) ?? sortedSubnets[0]
    setSubnetId(pick.id)
  }, [sortedSubnets, subnetId])
  const subnet = sortedSubnets.find((s) => s.id === subnetId)
  const vpcId = subnet?.vpc_id ?? ""
  const vpcGroups = useMemo(() => (tgs.data ?? []).filter((g) => g.vpc_id === vpcId), [tgs.data, vpcId])

  // Default the first listener to the VPC's only target group; clear groups from another VPC.
  useEffect(() => {
    if (!tgs.data || !vpcId) return
    setListeners((rows) => {
      const next = rows.map((r) => (r.tg && !vpcGroups.some((g) => g.name === r.tg) ? { ...r, tg: "" } : r))
      if (next.length === 1 && !next[0].tg && vpcGroups.length === 1) next[0] = { ...next[0], tg: vpcGroups[0].name }
      return next.some((r, i) => r !== rows[i]) ? next : rows
    })
  }, [tgs.data, vpcId, vpcGroups])

  const errors = useMemo(() => {
    const e: Record<string, string> = {}
    if (!NAME_RE.test(name)) e.name = "1-32 letters, digits and hyphens; must not start or end with a hyphen"
    if (!subnetId) e.subnet = "Choose a subnet"
    if (!listeners.length) e.listeners = "Add at least one listener"
    const ports = new Set<string>()
    const pubs = new Set<string>()
    listeners.forEach((l, i) => {
      if (!validPort(l.port)) e[`l${i}port`] = "1-65535"
      else if (ports.has(l.port)) e[`l${i}port`] = "Ports must be unique"
      ports.add(l.port)
      if (scheme === "internet-facing" && l.publicPort) {
        if (!validPort(l.publicPort)) e[`l${i}pub`] = "1-65535, or empty for any free port"
        else if (pubs.has(l.publicPort)) e[`l${i}pub`] = "Host ports must be unique"
        pubs.add(l.publicPort)
      }
      if (!l.tg) e[`l${i}tg`] = "Choose a target group"
    })
    const keys = tagRows.map((r) => r.key.trim()).filter(Boolean)
    if (new Set(keys).size !== keys.length) e.tags = "Tag keys must be unique"
    else if (tagRows.some((r) => !r.key.trim() && r.value.trim())) e.tags = "Every tag with a value needs a key"
    return e
  }, [name, subnetId, listeners, scheme, tagRows])
  const err = (k: string) => (submitted ? errors[k] : undefined)
  const valid = Object.keys(errors).length === 0

  const setRow = (i: number, patch: Partial<ListenerRow>) => setListeners(listeners.map((l, j) => (j === i ? { ...l, ...patch } : l)))

  const create = async () => {
    setSubmitted(true)
    if (!valid) {
      toast.error("Fix the highlighted fields before creating the load balancer")
      return
    }
    const body: CreateLoadBalancerInput = {
      name,
      scheme,
      subnet_id: subnetId,
      listeners: listeners.map((l) => ({
        port: Number(l.port),
        protocol: "HTTP",
        public_port: scheme === "internet-facing" && l.publicPort ? Number(l.publicPort) : undefined,
        default_target_group: l.tg,
      })),
      tags: rowsToTags(tagRows),
    }
    setPending(true)
    try {
      const lb = await api.post<LoadBalancer>(LBS_PATH, body)
      toast.success(`Creating load balancer ${lb.name}`)
      await revalidate(ELB_PREFIX)
      router.push(lbHref(lb.name))
    } catch (e) {
      toast.error(errorMessage(e))
      setPending(false)
    }
  }

  return (
    <div className="flex flex-col gap-4">
      <PageHeader
        title="Create application load balancer"
        description="An HTTP load balancer with host- and path-based routing to target groups. HomeCloud runs it as an nginx container in the chosen subnet."
        breadcrumbs={[{ label: "ELB", href: "/elb/" }, { label: "Load balancers", href: "/elb/" }, { label: "Create" }]}
      />

      <form
        onSubmit={(e) => {
          e.preventDefault()
          create()
        }}
        className="grid grid-cols-1 items-start gap-4 xl:grid-cols-[minmax(0,1fr)_320px]"
      >
        <div className="flex min-w-0 flex-col gap-4">
          <Section title="Basic configuration">
            <div className="flex flex-col gap-5">
              <Field
                label="Load balancer name"
                htmlFor="lb-name"
                error={err("name")}
                help={`Up to 32 letters, digits and hyphens. Inside the VPC it answers at ${name || "<name>"}.elb.internal.`}
              >
                <Input
                  id="lb-name"
                  value={name}
                  onChange={(e) => setName(e.target.value)}
                  placeholder="e.g. web-lb"
                  className="max-w-md"
                  autoComplete="off"
                  spellCheck={false}
                  aria-invalid={!!err("name")}
                />
              </Field>
              <div className="flex flex-col gap-2">
                <span className="text-sm font-medium">Scheme</span>
                <div className="grid grid-cols-1 gap-3 sm:grid-cols-2" role="radiogroup" aria-label="Scheme">
                  {SCHEMES.map((s) => {
                    const active = s.value === scheme
                    const Icon = s.icon
                    return (
                      <button
                        key={s.value}
                        type="button"
                        role="radio"
                        aria-checked={active}
                        onClick={() => setScheme(s.value)}
                        className={cn(
                          "relative flex flex-col gap-1.5 rounded-lg border p-3 text-left transition-colors",
                          active ? "border-primary bg-primary/5 ring-primary ring-1 dark:bg-primary/10" : "hover:bg-muted/50",
                        )}
                      >
                        {active && (
                          <span className="bg-primary text-primary-foreground absolute top-2 right-2 flex size-5 items-center justify-center rounded-full">
                            <Check className="size-3.5" />
                          </span>
                        )}
                        <span className="flex items-center gap-2 pr-6 text-sm font-medium">
                          <Icon className="text-muted-foreground size-4" /> {s.label}
                        </span>
                        <span className="text-muted-foreground text-xs">{s.blurb}</span>
                      </button>
                    )
                  })}
                </div>
              </div>
            </div>
          </Section>

          <Section title="Network mapping" description="The load balancer gets a private IP in this subnet and health-checks targets from inside the VPC.">
            <Field
              label="Subnet"
              htmlFor="lb-subnet"
              error={err("subnet")}
              help={
                subnet ? (
                  <>
                    VPC{" "}
                    <Link href={vpcHref(subnet.vpc_id)} className="text-primary font-mono hover:underline">
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
                <Select value={subnetId} onValueChange={(v) => v && setSubnetId(v)} disabled={!subnets.data}>
                  <SelectTrigger id="lb-subnet" className="w-full max-w-xl">
                    <SelectValue placeholder={subnets.data ? "Choose a subnet" : "Loading subnets..."} />
                  </SelectTrigger>
                  <SelectContent>
                    {sortedSubnets.map((s) => (
                      <SelectItem key={s.id} value={s.id} disabled={s.available_ips <= 0}>
                        <span className="font-medium">{s.name || s.id}</span>
                        <span className="text-muted-foreground font-mono text-xs">
                          {s.id} · {s.vpc_id} · {s.cidr} · {s.availability_zone}
                        </span>
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              )}
            </Field>
          </Section>

          <Section
            title="Listeners and routing"
            description="Each listener accepts HTTP on a port and forwards to its default target group. Add path or host rules after creation."
          >
            <div className="flex flex-col gap-3">
              {tgs.data && vpcId && vpcGroups.length === 0 && (
                <p className="flex gap-2 rounded-md border border-amber-600/30 bg-amber-50 p-3 text-sm text-amber-800 dark:border-amber-400/30 dark:bg-amber-500/10 dark:text-amber-300">
                  <Info className="mt-0.5 size-4 shrink-0" />
                  <span>
                    This VPC has no target groups yet.{" "}
                    <Link href="/elb/target-groups/?create=1" className="font-medium underline">
                      Create a target group
                    </Link>{" "}
                    with your instances first, then come back.
                  </span>
                </p>
              )}
              {listeners.map((l, i) => (
                <div key={i} className="grid grid-cols-2 items-start gap-3 rounded-md border p-3 sm:grid-cols-[7rem_9rem_minmax(0,1fr)_auto]">
                  <Field label="Protocol : Port" htmlFor={`l-port-${i}`} error={err(`l${i}port`)}>
                    <div className="flex items-center gap-1.5">
                      <span className="text-muted-foreground font-mono text-xs">HTTP:</span>
                      <Input id={`l-port-${i}`} inputMode="numeric" value={l.port} onChange={(e) => setRow(i, { port: e.target.value })} className="h-8" />
                    </div>
                  </Field>
                  <Field label="Host port" htmlFor={`l-pub-${i}`} optional error={err(`l${i}pub`)}>
                    <Input
                      id={`l-pub-${i}`}
                      inputMode="numeric"
                      value={scheme === "internal" ? "" : l.publicPort}
                      disabled={scheme === "internal"}
                      onChange={(e) => setRow(i, { publicPort: e.target.value })}
                      placeholder={scheme === "internal" ? "Not published" : "Any free port"}
                      className="h-8"
                    />
                  </Field>
                  <Field label="Default action: forward to" htmlFor={`l-tg-${i}`} error={err(`l${i}tg`)} className="col-span-2 sm:col-span-1">
                    <TargetGroupSelect id={`l-tg-${i}`} value={l.tg} onChange={(v) => setRow(i, { tg: v })} vpcId={vpcId} invalid={!!err(`l${i}tg`)} />
                  </Field>
                  <Button
                    type="button"
                    variant="ghost"
                    size="icon"
                    className="col-start-2 row-start-1 size-8 justify-self-end sm:col-start-4 sm:mt-6"
                    onClick={() => setListeners(listeners.filter((_, j) => j !== i))}
                    disabled={listeners.length === 1}
                    aria-label="Remove listener"
                  >
                    <X />
                  </Button>
                </div>
              ))}
              {err("listeners") && <p className="text-destructive text-xs">{err("listeners")}</p>}
              <div className="flex flex-wrap items-center gap-3">
                <Button
                  type="button"
                  variant="outline"
                  size="sm"
                  disabled={listeners.length >= 10}
                  onClick={() => {
                    const used = new Set(listeners.map((l) => l.port))
                    const port = ["80", "8080", "8000", "8081", "8888", "3000"].find((p) => !used.has(p)) ?? ""
                    setListeners([...listeners, { port, publicPort: "", tg: "" }])
                  }}
                >
                  <Plus /> Add listener
                </Button>
                <Link href="/elb/target-groups/" className="text-primary text-sm hover:underline">
                  Manage target groups
                </Link>
              </div>
              <p className="text-muted-foreground flex gap-1.5 text-xs">
                <Info className="mt-px size-3.5 shrink-0" />
                <span>
                  {scheme === "internet-facing"
                    ? "Each listener port is published on this host: leave Host port empty to pick a free port, or set one to get a stable URL. Only HTTP is supported."
                    : "Internal load balancers are reached inside the VPC at http://<name>.elb.internal:<port>. Only HTTP is supported."}
                </span>
              </p>
            </div>
          </Section>

          <Section title="Tags" description="Optional key/value labels.">
            <Field label="Tags" optional error={err("tags")}>
              <TagsEditor rows={tagRows} onChange={setTagRows} />
            </Field>
          </Section>
        </div>

        <aside className="xl:sticky xl:top-4">
          <Section title="Summary">
            <div className="flex flex-col gap-4">
              <dl className="flex flex-col gap-3 text-sm">
                <SummaryItem label="Name">{name ? <span className="font-medium">{name}</span> : "-"}</SummaryItem>
                <SummaryItem label="DNS name">{name ? <span className="font-mono text-xs">{name}.elb.internal</span> : "-"}</SummaryItem>
                <SummaryItem label="Scheme">{scheme === "internal" ? "Internal" : "Internet-facing"}</SummaryItem>
                <SummaryItem label="Subnet">
                  {subnet ? (
                    <>
                      <div>{subnet.name || subnet.id}</div>
                      <div className="text-muted-foreground font-mono text-xs">{subnet.vpc_id}</div>
                    </>
                  ) : (
                    "-"
                  )}
                </SummaryItem>
                <SummaryItem label={`Listeners (${listeners.length})`}>
                  <ul className="flex flex-col gap-0.5">
                    {listeners.map((l, i) => (
                      <li key={i} className="font-mono text-xs">
                        HTTP:{l.port || "?"}
                        {scheme === "internet-facing" && l.publicPort ? ` (host ${l.publicPort})` : ""} → {l.tg || "?"}
                      </li>
                    ))}
                  </ul>
                </SummaryItem>
                {rowsToTags(tagRows) && <SummaryItem label="Tags">{pluralize(Object.keys(rowsToTags(tagRows) ?? {}).length, "tag")}</SummaryItem>}
              </dl>
              {submitted && !valid && <p className="text-destructive text-xs">Some settings need attention. Check the highlighted fields.</p>}
              <div className="flex flex-col gap-2 border-t pt-4">
                <Button type="submit" disabled={pending}>
                  {pending ? <Loader2 className="animate-spin" /> : <Network />}
                  Create load balancer
                </Button>
                <Button type="button" variant="outline" asChild>
                  <Link href="/elb/">Cancel</Link>
                </Button>
              </div>
              <p className="text-muted-foreground text-xs">Provisioning pulls nginx on first use and usually takes a few seconds.</p>
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
