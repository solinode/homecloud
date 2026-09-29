"use client"

import { useEffect, useState, type ReactNode } from "react"
import { Loader2 } from "lucide-react"
import { toast } from "sonner"

import { Button } from "@/components/ui/button"
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog"
import { Input } from "@/components/ui/input"
import { Textarea } from "@/components/ui/textarea"
import { Select, SelectContent, SelectGroup, SelectItem, SelectLabel, SelectTrigger, SelectValue } from "@/components/ui/select"
import { ConfirmDialog } from "@/components/console/confirm-dialog"
import { Field } from "@/components/console/form-field"
import { StatusBadge } from "@/components/console/status-badge"
import { api, errorMessage, seg } from "@/lib/api"
import { revalidate, useApi } from "@/lib/hooks"
import { formatMemoryMB, pluralize } from "@/lib/format"
import type { Image, Instance, InstanceState, InstanceType } from "@/lib/types"

export const INSTANCES_PATH = "/api/v1/ec2/instances"

export const TRANSITIONAL: InstanceState[] = ["pending", "stopping", "shutting-down"]

export const isTransitional = (s: string) => (TRANSITIONAL as string[]).includes(s)

export const instanceLabel = (i: Pick<Instance, "id" | "name">) => i.name || i.id

export const instanceHref = (id: string, tab?: string) => `/ec2/instance/?id=${encodeURIComponent(id)}${tab ? `&tab=${tab}` : ""}`

/** Poll fast while anything is changing state, slowly otherwise. */
export function pollInterval(states: string[]): number {
  return states.some(isTransitional) ? 2000 : 15_000
}

/** vCPU count: t3.nano has half a vCPU. */
export function formatVcpu(n: number) {
  return `${n} vCPU${n === 1 ? "" : "s"}`
}

export function typeSummary(t: Pick<InstanceType, "vcpus" | "memory_mb">) {
  return `${formatVcpu(t.vcpus)}, ${formatMemoryMB(t.memory_mb)}`
}

/** Public port links: "80/tcp" -> http://host:32768. */
export function PublicPorts({ instance, empty = "-", compact }: { instance: Instance; empty?: ReactNode; compact?: boolean }) {
  const entries = Object.entries(instance.public_ports ?? {}).sort((a, b) => parseInt(a[0]) - parseInt(b[0]))
  if (!entries.length) return <span className="text-muted-foreground">{empty}</span>
  const host = instance.public_host || "localhost"
  return (
    <span className="flex flex-wrap gap-x-3 gap-y-1">
      {entries.map(([k, port]) => {
        const tcp = k.endsWith("/tcp")
        const label = compact ? `${k.split("/")[0]} → ${port}` : `${k} → ${host}:${port}`
        return tcp ? (
          <a
            key={k}
            href={`http://${host}:${port}`}
            target="_blank"
            rel="noreferrer"
            onClick={(e) => e.stopPropagation()}
            className="text-primary font-mono text-[13px] whitespace-nowrap hover:underline"
          >
            {label}
          </a>
        ) : (
          <span key={k} className="font-mono text-[13px] whitespace-nowrap">
            {label}
          </span>
        )
      })}
    </span>
  )
}

export function useInstanceTypes() {
  return useApi<InstanceType[]>("/api/v1/ec2/instance-types", { revalidateOnFocus: false })
}

/** Groups instance types by family, preserving catalog order. */
export function groupByFamily(types: InstanceType[]): [string, InstanceType[]][] {
  const m = new Map<string, InstanceType[]>()
  for (const t of types) m.set(t.family, [...(m.get(t.family) ?? []), t])
  return [...m.entries()]
}

/** InstanceTypeSelect is a family-grouped instance type picker. */
export function InstanceTypeSelect({ value, onChange, id, exclude }: { value: string; onChange: (v: string) => void; id?: string; exclude?: string }) {
  const { data, isLoading } = useInstanceTypes()
  return (
    <Select value={value} onValueChange={onChange} disabled={isLoading && !data}>
      <SelectTrigger id={id} className="w-full">
        <SelectValue placeholder={isLoading ? "Loading instance types..." : "Choose an instance type"} />
      </SelectTrigger>
      <SelectContent>
        {groupByFamily(data ?? []).map(([family, types]) => (
          <SelectGroup key={family}>
            <SelectLabel className="text-muted-foreground text-xs">{family}</SelectLabel>
            {types.map((t) => (
              <SelectItem key={t.name} value={t.name} disabled={t.name === exclude}>
                <span className="font-mono text-[13px]">{t.name}</span>
                <span className="text-muted-foreground text-xs">{typeSummary(t)}</span>
              </SelectItem>
            ))}
          </SelectGroup>
        ))}
      </SelectContent>
    </Select>
  )
}

/** Runs a state change over several instances in parallel and reports the outcome. */
async function bulk(ids: string[], call: (id: string) => Promise<unknown>, done: string) {
  const p = Promise.allSettled(ids.map(call))
  // start/stop block until Docker finishes; refresh early to show "stopping"/"pending".
  setTimeout(() => revalidate(INSTANCES_PATH), 400)
  const res = await p
  const ok = res.filter((r) => r.status === "fulfilled").length
  const failed = res.filter((r): r is PromiseRejectedResult => r.status === "rejected")
  if (ok) toast.success(ids.length === 1 ? `${done} ${ids[0]}` : `${done} ${pluralize(ok, "instance")}`)
  for (const f of failed.slice(0, 3)) toast.error(errorMessage(f.reason))
  await revalidate(INSTANCES_PATH)
}

export interface InstanceActions {
  start: (ids: string[]) => Promise<void>
  stop: (ids: string[]) => Promise<void>
  reboot: (ids: string[]) => Promise<void>
  terminate: (instances: Instance[]) => void
  changeType: (instance: Instance) => void
  createImage: (instance: Instance) => void
  busy: boolean
  dialogs: ReactNode
}

/**
 * useInstanceActions provides the instance state/actions operations shared by
 * the instance list and detail pages, plus the dialogs they open.
 */
export function useInstanceActions(opts: { onTerminated?: () => void } = {}): InstanceActions {
  const [busy, setBusy] = useState(false)
  const [terminating, setTerminating] = useState<Instance[] | null>(null)
  const [resizing, setResizing] = useState<Instance | null>(null)
  const [imaging, setImaging] = useState<Instance | null>(null)

  const wrap = (call: (id: string) => Promise<unknown>, done: string) => async (ids: string[]) => {
    if (!ids.length) return
    setBusy(true)
    try {
      await bulk(ids, call, done)
    } finally {
      setBusy(false)
    }
  }

  const start = wrap((id) => api.post(`${INSTANCES_PATH}/${seg(id)}/start`), "Started")
  const stop = wrap((id) => api.post(`${INSTANCES_PATH}/${seg(id)}/stop`), "Stopped")
  const reboot = wrap((id) => api.post(`${INSTANCES_PATH}/${seg(id)}/reboot`), "Rebooted")

  const one = terminating?.length === 1 ? terminating[0] : null
  const dialogs = (
    <>
      <ConfirmDialog
        open={!!terminating}
        onOpenChange={(o) => !o && setTerminating(null)}
        title={one ? `Terminate ${instanceLabel(one)}?` : `Terminate ${pluralize(terminating?.length ?? 0, "instance")}?`}
        description={
          <>
            Terminating removes the instance container and releases its private IP. Volumes marked <em>delete on termination</em> are deleted;
            other volumes become available. This cannot be undone.
          </>
        }
        confirmText={one ? instanceLabel(one) : "terminate"}
        actionLabel="Terminate"
        onConfirm={async () => {
          const list = terminating ?? []
          const res = await Promise.allSettled(list.map((i) => api.del(`${INSTANCES_PATH}/${seg(i.id)}`)))
          await revalidate("/api/v1/ec2")
          const failed = res.filter((r): r is PromiseRejectedResult => r.status === "rejected")
          const ok = list.length - failed.length
          if (ok) toast.success(list.length === 1 ? `Terminated ${list[0].id}` : `Terminated ${pluralize(ok, "instance")}`)
          if (failed.length) throw failed[0].reason
          opts.onTerminated?.()
        }}
      >
        {terminating && terminating.length > 1 && (
          <ul className="bg-muted/40 max-h-40 overflow-y-auto rounded-md border p-2 text-sm">
            {terminating.map((i) => (
              <li key={i.id} className="flex items-center justify-between gap-2 py-0.5">
                <span className="truncate">
                  {i.name && <span className="mr-2">{i.name}</span>}
                  <span className="text-muted-foreground font-mono text-[13px]">{i.id}</span>
                </span>
                <StatusBadge status={i.state} />
              </li>
            ))}
          </ul>
        )}
      </ConfirmDialog>
      <ChangeTypeDialog instance={resizing} onClose={() => setResizing(null)} />
      <CreateImageDialog instance={imaging} onClose={() => setImaging(null)} />
    </>
  )

  return {
    start,
    stop,
    reboot,
    terminate: (list) => {
      if (list.length) setTerminating(list)
    },
    changeType: setResizing,
    createImage: setImaging,
    busy,
    dialogs,
  }
}

function ChangeTypeDialog({ instance, onClose }: { instance: Instance | null; onClose: () => void }) {
  const [type, setType] = useState("")
  const [pending, setPending] = useState(false)
  useEffect(() => {
    if (instance) setType(instance.instance_type)
  }, [instance])

  const submit = async (e: React.FormEvent) => {
    e.preventDefault()
    if (!instance || !type || type === instance.instance_type) return
    setPending(true)
    try {
      await api.patch(`${INSTANCES_PATH}/${seg(instance.id)}`, { instance_type: type })
      toast.success(`Changed ${instanceLabel(instance)} to ${type}`)
      await revalidate(INSTANCES_PATH)
      onClose()
    } catch (err) {
      toast.error(errorMessage(err))
    } finally {
      setPending(false)
    }
  }

  return (
    <Dialog open={!!instance} onOpenChange={(o) => !o && !pending && onClose()}>
      <DialogContent>
        <form onSubmit={submit} className="flex flex-col gap-4">
          <DialogHeader>
            <DialogTitle>Change instance type</DialogTitle>
            <DialogDescription>
              The instance must be stopped. The new CPU and memory limits apply the next time it starts.
            </DialogDescription>
          </DialogHeader>
          {instance && (
            <p className="text-sm">
              Current type: <span className="font-mono text-[13px]">{instance.instance_type}</span>{" "}
              <span className="text-muted-foreground">({typeSummary({ vcpus: instance.vcpus, memory_mb: instance.memory_mb })})</span>
            </p>
          )}
          <Field label="New instance type" htmlFor="new-type">
            <InstanceTypeSelect id="new-type" value={type} onChange={setType} />
          </Field>
          <DialogFooter>
            <Button type="button" variant="outline" onClick={onClose} disabled={pending}>
              Cancel
            </Button>
            <Button type="submit" disabled={pending || !instance || type === instance.instance_type}>
              {pending && <Loader2 className="animate-spin" />}
              Apply
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}

function CreateImageDialog({ instance, onClose }: { instance: Instance | null; onClose: () => void }) {
  const [name, setName] = useState("")
  const [description, setDescription] = useState("")
  const [error, setError] = useState<string | null>(null)
  const [pending, setPending] = useState(false)
  useEffect(() => {
    if (instance) {
      setName(`${instanceLabel(instance)}-image`)
      setDescription("")
      setError(null)
    }
  }, [instance])

  const submit = async (e: React.FormEvent) => {
    e.preventDefault()
    if (!instance) return
    if (!name.trim()) {
      setError("Enter an image name")
      return
    }
    if (name.length > 128) {
      setError("Image names are at most 128 characters")
      return
    }
    setPending(true)
    try {
      const im = await api.post<Image>(`${INSTANCES_PATH}/${seg(instance.id)}/image`, { name: name.trim(), description: description.trim() })
      toast.success(`Created image ${im.id}`)
      await revalidate("/api/v1/ec2/images")
      onClose()
    } catch (err) {
      toast.error(errorMessage(err))
    } finally {
      setPending(false)
    }
  }

  return (
    <Dialog open={!!instance} onOpenChange={(o) => !o && !pending && onClose()}>
      <DialogContent>
        <form onSubmit={submit} className="flex flex-col gap-4">
          <DialogHeader>
            <DialogTitle>Create image</DialogTitle>
            <DialogDescription>
              Captures the instance&apos;s filesystem as a new AMI you can launch more instances from. Attached volumes are not included.
            </DialogDescription>
          </DialogHeader>
          <Field label="Image name" htmlFor="img-name" error={error}>
            <Input id="img-name" value={name} onChange={(e) => (setName(e.target.value), setError(null))} autoFocus />
          </Field>
          <Field label="Description" htmlFor="img-desc" optional>
            <Textarea id="img-desc" rows={3} value={description} onChange={(e) => setDescription(e.target.value)} />
          </Field>
          <DialogFooter>
            <Button type="button" variant="outline" onClick={onClose} disabled={pending}>
              Cancel
            </Button>
            <Button type="submit" disabled={pending}>
              {pending && <Loader2 className="animate-spin" />}
              Create image
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}
