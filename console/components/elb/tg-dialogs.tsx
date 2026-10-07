"use client"

import { useEffect, useMemo, useState } from "react"
import Link from "next/link"
import { useRouter } from "next/navigation"
import { AlertCircle, Loader2, Server } from "lucide-react"
import { toast } from "sonner"

import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert"
import { Button } from "@/components/ui/button"
import { Checkbox } from "@/components/ui/checkbox"
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog"
import { Input } from "@/components/ui/input"
import { Skeleton } from "@/components/ui/skeleton"
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select"
import { ConfirmDialog } from "@/components/console/confirm-dialog"
import { EmptyState } from "@/components/console/empty-state"
import { Field } from "@/components/console/form-field"
import { StatusBadge } from "@/components/console/status-badge"
import { Tag } from "@/components/console/tag"
import { api, errorMessage, seg } from "@/lib/api"
import { pluralize } from "@/lib/format"
import { revalidate, useApi } from "@/lib/hooks"
import type { CreateTargetGroupInput, ElbHealthCheck, TargetGroup, Vpc } from "@/lib/types"
import { cn } from "@/lib/utils"
import { ELB_PREFIX, LbLink, NAME_RE, TGS_PATH, tgHref, useInstances } from "./shared"

// ---- health check form ----

interface HcForm {
  path: string
  interval: string
  healthy: string
  unhealthy: string
}

const hcToForm = (h?: ElbHealthCheck): HcForm => ({
  path: h?.path ?? "/",
  interval: String(h?.interval_seconds ?? 15),
  healthy: String(h?.healthy_threshold ?? 2),
  unhealthy: String(h?.unhealthy_threshold ?? 2),
})

const intIn = (s: string, lo: number, hi: number) => /^\d+$/.test(s) && Number(s) >= lo && Number(s) <= hi

function hcErrors(f: HcForm): Record<string, string> {
  const e: Record<string, string> = {}
  if (!f.path.startsWith("/") || /\s/.test(f.path)) e.path = "Must start with / and contain no spaces"
  if (!intIn(f.interval, 5, 300)) e.interval = "5-300 seconds"
  if (!intIn(f.healthy, 1, 10)) e.healthy = "1-10"
  if (!intIn(f.unhealthy, 1, 10)) e.unhealthy = "1-10"
  return e
}

const formToHc = (f: HcForm): ElbHealthCheck => ({
  path: f.path,
  interval_seconds: Number(f.interval),
  healthy_threshold: Number(f.healthy),
  unhealthy_threshold: Number(f.unhealthy),
})

function HealthCheckFields({ form, onChange, errors, idPrefix }: { form: HcForm; onChange: (f: HcForm) => void; errors: Record<string, string | undefined>; idPrefix: string }) {
  const set = (patch: Partial<HcForm>) => onChange({ ...form, ...patch })
  return (
    <div className="flex flex-col gap-3">
      <Field label="Health check path" htmlFor={`${idPrefix}-path`} error={errors.path} help="HTTP GET from inside the VPC; any 2xx or 3xx response is healthy.">
        <Input id={`${idPrefix}-path`} value={form.path} onChange={(e) => set({ path: e.target.value })} className="font-mono" spellCheck={false} />
      </Field>
      <div className="grid grid-cols-1 gap-3 sm:grid-cols-3">
        <Field label="Interval (seconds)" htmlFor={`${idPrefix}-interval`} error={errors.interval}>
          <Input id={`${idPrefix}-interval`} inputMode="numeric" value={form.interval} onChange={(e) => set({ interval: e.target.value })} />
        </Field>
        <Field label="Healthy threshold" htmlFor={`${idPrefix}-healthy`} error={errors.healthy}>
          <Input id={`${idPrefix}-healthy`} inputMode="numeric" value={form.healthy} onChange={(e) => set({ healthy: e.target.value })} />
        </Field>
        <Field label="Unhealthy threshold" htmlFor={`${idPrefix}-unhealthy`} error={errors.unhealthy}>
          <Input id={`${idPrefix}-unhealthy`} inputMode="numeric" value={form.unhealthy} onChange={(e) => set({ unhealthy: e.target.value })} />
        </Field>
      </div>
      <p className="text-muted-foreground text-xs">
        Thresholds are consecutive checks needed to change state. Each check times out after 4 seconds. Only unhealthy targets are removed from routing.
      </p>
    </div>
  )
}

// ---- create ----

export function CreateTargetGroupDialog({ open, onOpenChange }: { open: boolean; onOpenChange: (o: boolean) => void }) {
  const router = useRouter()
  const vpcs = useApi<Vpc[]>(open ? "/api/v1/vpc/vpcs" : null, { revalidateOnFocus: false })
  const [name, setName] = useState("")
  const [port, setPort] = useState("80")
  const [vpcId, setVpcId] = useState("")
  const [hc, setHc] = useState<HcForm>(hcToForm())
  const [submitted, setSubmitted] = useState(false)
  const [pending, setPending] = useState(false)

  useEffect(() => {
    if (!open) return
    setName("")
    setPort("80")
    setVpcId("")
    setHc(hcToForm())
    setSubmitted(false)
  }, [open])
  useEffect(() => {
    if (open && !vpcId && vpcs.data?.length) setVpcId((vpcs.data.find((v) => v.default) ?? vpcs.data[0]).id)
  }, [open, vpcId, vpcs.data])

  const errors: Record<string, string> = { ...hcErrors(hc) }
  if (!NAME_RE.test(name)) errors.name = "1-32 letters, digits and hyphens; must not start or end with a hyphen"
  if (!intIn(port, 1, 65535)) errors.port = "1-65535"
  if (!vpcId) errors.vpc = "Choose a VPC"
  const err = (k: string) => (submitted ? errors[k] : undefined)

  const submit = async (e: React.FormEvent) => {
    e.preventDefault()
    setSubmitted(true)
    if (Object.keys(errors).length) return
    const body: CreateTargetGroupInput = { name, port: Number(port), vpc_id: vpcId, health_check: formToHc(hc) }
    setPending(true)
    try {
      await api.post<TargetGroup>(TGS_PATH, body)
      toast.success(`Created target group ${name}`)
      await revalidate(ELB_PREFIX)
      onOpenChange(false)
      router.push(tgHref(name))
    } catch (e) {
      toast.error(errorMessage(e))
    } finally {
      setPending(false)
    }
  }

  return (
    <Dialog open={open} onOpenChange={(o) => !pending && onOpenChange(o)}>
      <DialogContent className="max-h-[90vh] overflow-y-auto sm:max-w-xl">
        <form onSubmit={submit} className="flex flex-col gap-4">
          <DialogHeader>
            <DialogTitle>Create target group</DialogTitle>
            <DialogDescription>A set of EC2 instances or ECS tasks that load balancers forward HTTP requests to.</DialogDescription>
          </DialogHeader>
          <Field label="Target group name" htmlFor="tg-name" error={err("name")}>
            <Input id="tg-name" value={name} onChange={(e) => setName(e.target.value)} placeholder="e.g. web-tg" autoFocus autoComplete="off" spellCheck={false} />
          </Field>
          <div className="grid grid-cols-1 gap-3 sm:grid-cols-[10rem_minmax(0,1fr)]">
            <Field label="Protocol : Port" htmlFor="tg-port" error={err("port")} help="Default port targets receive traffic on.">
              <div className="flex items-center gap-1.5">
                <span className="text-muted-foreground font-mono text-xs">HTTP:</span>
                <Input id="tg-port" inputMode="numeric" value={port} onChange={(e) => setPort(e.target.value)} />
              </div>
            </Field>
            <Field label="VPC" htmlFor="tg-vpc" error={err("vpc")} help="Targets and load balancers must be in this VPC.">
              <Select value={vpcId} onValueChange={(v) => v && setVpcId(v)} disabled={!vpcs.data}>
                <SelectTrigger id="tg-vpc" className="w-full">
                  <SelectValue placeholder={vpcs.data ? "Choose a VPC" : "Loading VPCs..."} />
                </SelectTrigger>
                <SelectContent>
                  {(vpcs.data ?? []).map((v) => (
                    <SelectItem key={v.id} value={v.id}>
                      <span className="font-medium">{v.name || v.id}</span>
                      <span className="text-muted-foreground font-mono text-xs">
                        {v.id} · {v.cidr}
                        {v.default ? " · default" : ""}
                      </span>
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </Field>
          </div>
          <div className="bg-muted/30 flex flex-col gap-3 rounded-lg border p-3">
            <span className="hc-eyebrow">Health checks</span>
            <HealthCheckFields form={hc} onChange={setHc} errors={{ path: err("path"), interval: err("interval"), healthy: err("healthy"), unhealthy: err("unhealthy") }} idPrefix="tg-hc" />
          </div>
          <DialogFooter>
            <Button type="button" variant="outline" onClick={() => onOpenChange(false)} disabled={pending}>
              Cancel
            </Button>
            <Button type="submit" disabled={pending}>
              {pending && <Loader2 className="animate-spin" />}
              Create target group
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}

// ---- edit health check ----

export function EditHealthCheckDialog({ tg, onClose }: { tg: TargetGroup | null; onClose: () => void }) {
  const [hc, setHc] = useState<HcForm>(hcToForm())
  const [submitted, setSubmitted] = useState(false)
  const [pending, setPending] = useState(false)

  useEffect(() => {
    if (tg) {
      setHc(hcToForm(tg.health_check))
      setSubmitted(false)
    }
  }, [tg])

  const errors = hcErrors(hc)
  const err = (k: string) => (submitted ? errors[k] : undefined)

  const submit = async (e: React.FormEvent) => {
    e.preventDefault()
    setSubmitted(true)
    if (!tg || Object.keys(errors).length) return
    setPending(true)
    try {
      await api.patch(`${TGS_PATH}/${seg(tg.name)}`, formToHc(hc))
      toast.success(`Updated health checks for ${tg.name}`)
      await revalidate(ELB_PREFIX)
      onClose()
    } catch (e) {
      toast.error(errorMessage(e))
    } finally {
      setPending(false)
    }
  }

  return (
    <Dialog open={!!tg} onOpenChange={(o) => !o && !pending && onClose()}>
      <DialogContent className="sm:max-w-xl">
        <form onSubmit={submit} className="flex flex-col gap-4">
          <DialogHeader>
            <DialogTitle>Edit health check settings</DialogTitle>
            <DialogDescription>Applies to every load balancer that forwards to {tg?.name}. Takes effect at the next check.</DialogDescription>
          </DialogHeader>
          <HealthCheckFields form={hc} onChange={setHc} errors={{ path: err("path"), interval: err("interval"), healthy: err("healthy"), unhealthy: err("unhealthy") }} idPrefix="ehc" />
          <DialogFooter>
            <Button type="button" variant="outline" onClick={onClose} disabled={pending}>
              Cancel
            </Button>
            <Button type="submit" disabled={pending}>
              {pending && <Loader2 className="animate-spin" />}
              Save changes
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}

// ---- register targets ----

export function RegisterTargetsDialog({ tg, onClose }: { tg: TargetGroup | null; onClose: () => void }) {
  const instances = useInstances()
  const [selected, setSelected] = useState<string[]>([])
  const [port, setPort] = useState("")
  const [pending, setPending] = useState(false)

  useEffect(() => {
    if (tg) {
      setSelected([])
      setPort(String(tg.port))
    }
  }, [tg])

  const running = useMemo(
    () => (instances.data ?? []).filter((i) => i.state === "running" && (!tg || i.vpc_id === tg.vpc_id)).sort((a, b) => (a.name || a.id).localeCompare(b.name || b.id)),
    [instances.data, tg],
  )
  const otherVpc = (instances.data ?? []).filter((i) => i.state === "running" && tg && i.vpc_id !== tg.vpc_id).length
  const portOk = intIn(port, 1, 65535)
  const registered = (id: string) => tg?.targets.some((t) => t.id === id && String(t.port) === port)

  const submit = async (e: React.FormEvent) => {
    e.preventDefault()
    if (!tg || !selected.length || !portOk) return
    setPending(true)
    try {
      await api.post(`${TGS_PATH}/${seg(tg.name)}/targets`, { targets: selected.map((id) => ({ id, port: Number(port) })) })
      toast.success(`Registered ${pluralize(selected.length, "target")} in ${tg.name}`)
      await revalidate(ELB_PREFIX)
      onClose()
    } catch (e) {
      toast.error(errorMessage(e))
    } finally {
      setPending(false)
    }
  }

  return (
    <Dialog open={!!tg} onOpenChange={(o) => !o && !pending && onClose()}>
      <DialogContent className="max-h-[90vh] overflow-y-auto sm:max-w-2xl">
        <form onSubmit={submit} className="flex flex-col gap-4">
          <DialogHeader>
            <DialogTitle>Register targets</DialogTitle>
            <DialogDescription>Choose running instances in {tg?.vpc_id} to receive traffic from {tg?.name}.</DialogDescription>
          </DialogHeader>
          {instances.error ? (
            <Alert variant="destructive">
              <AlertCircle />
              <AlertTitle>Could not load instances</AlertTitle>
              <AlertDescription>{errorMessage(instances.error)}</AlertDescription>
            </Alert>
          ) : !instances.data ? (
            <Skeleton className="h-32 w-full rounded-lg" />
          ) : running.length === 0 ? (
            <div className="rounded-lg border border-dashed">
              <EmptyState
                icon={Server}
                title="No running instances in this VPC"
                description="Targets must be running instances in the target group's VPC."
                action={
                  <Button type="button" size="sm" variant="outline" asChild>
                    <Link href="/ec2/launch/">Launch an instance</Link>
                  </Button>
                }
                className="py-8"
              />
            </div>
          ) : (
            <div className="max-h-72 divide-y overflow-y-auto rounded-lg border">
              <label className="bg-muted/40 flex items-center gap-3 px-3 py-2 text-xs font-medium">
                <Checkbox
                  checked={selected.length === running.length ? true : selected.length ? "indeterminate" : false}
                  onCheckedChange={(v) => setSelected(v ? running.map((i) => i.id) : [])}
                  aria-label="Select all instances"
                />
                <span className="text-muted-foreground">Instance</span>
              </label>
              {running.map((i) => {
                const checked = selected.includes(i.id)
                return (
                  <label key={i.id} className={cn("flex cursor-pointer items-center gap-3 px-3 py-2", checked ? "bg-brand-soft" : "hover:bg-muted/50")}>
                    <Checkbox checked={checked} onCheckedChange={(v) => setSelected(v ? [...selected, i.id] : selected.filter((x) => x !== i.id))} />
                    <span className="flex min-w-0 flex-1 flex-wrap items-center gap-x-3 gap-y-0.5">
                      <span className="max-w-[14rem] truncate text-sm font-medium whitespace-nowrap" title={i.name || i.id}>{i.name || i.id}</span>
                      <span className="text-muted-foreground font-mono text-xs">{i.id}</span>
                      <span className="text-muted-foreground font-mono text-xs">{i.private_ip}</span>
                      <Tag>{i.instance_type}</Tag>
                    </span>
                    {registered(i.id) && <StatusBadge status="registered" tone="info" label="Registered" />}
                  </label>
                )
              })}
            </div>
          )}
          {otherVpc > 0 && <p className="text-muted-foreground text-xs">{pluralize(otherVpc, "running instance")} in other VPCs are hidden.</p>}
          <Field label="Port" htmlFor="rt-port" error={!portOk ? "1-65535" : undefined} help={`Port the instances serve on. Defaults to the target group port (${tg?.port}).`}>
            <Input id="rt-port" inputMode="numeric" value={port} onChange={(e) => setPort(e.target.value)} className="w-32" />
          </Field>
          <p className="text-muted-foreground text-xs">
            ECS services with a load balancer register and deregister their tasks automatically; they don&apos;t need to be added here.
          </p>
          <DialogFooter>
            <Button type="button" variant="outline" onClick={onClose} disabled={pending}>
              Cancel
            </Button>
            <Button type="submit" disabled={pending || !selected.length || !portOk}>
              {pending && <Loader2 className="animate-spin" />}
              Register {selected.length ? pluralize(selected.length, "target") : "targets"}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}

// ---- delete ----

export function DeleteTargetGroupDialog({
  tg,
  usedBy,
  onClose,
  onDeleted,
}: {
  tg: TargetGroup | null
  usedBy: string[]
  onClose: () => void
  onDeleted?: () => void
}) {
  return (
    <ConfirmDialog
      open={!!tg}
      onOpenChange={(o) => !o && onClose()}
      title={`Delete target group ${tg?.name ?? ""}?`}
      description={
        usedBy.length ? (
          <Alert variant="destructive">
            <AlertCircle />
            <AlertTitle>Target group in use</AlertTitle>
            <AlertDescription>
              <span>
                It is used by{" "}
                {usedBy.map((n, i) => (
                  <span key={n}>
                    {i > 0 && ", "}
                    <LbLink name={n} />
                  </span>
                ))}
                . Delete the listeners or rules that forward to it first, or the deletion will be rejected.
              </span>
            </AlertDescription>
          </Alert>
        ) : (
          <>The target group and its {pluralize(tg?.targets.length ?? 0, "target registration")} are deleted. The instances themselves keep running.</>
        )
      }
      confirmText={tg?.name}
      onConfirm={async () => {
        if (!tg) return
        await api.del(`${TGS_PATH}/${seg(tg.name)}`)
        toast.success(`Deleted target group ${tg.name}`)
        await revalidate(ELB_PREFIX)
        onDeleted?.()
      }}
    />
  )
}
