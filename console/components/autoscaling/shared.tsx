"use client"

import { useEffect, useState } from "react"
import { useRouter } from "next/navigation"
import { Loader2, Plus, X } from "lucide-react"
import { toast } from "sonner"

import { Button } from "@/components/ui/button"
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog"
import { Input } from "@/components/ui/input"
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select"
import { ConfirmDialog } from "@/components/console/confirm-dialog"
import { Field } from "@/components/console/form-field"
import { StatusBadge } from "@/components/console/status-badge"
import { api, errorMessage, seg } from "@/lib/api"
import { pluralize } from "@/lib/format"
import { revalidate, useApi } from "@/lib/hooks"
import type { AutoScalingGroup, ScalingPolicy } from "@/lib/types"

export const ASG_PREFIX = "/api/v1/autoscaling"
export const GROUPS_PATH = `${ASG_PREFIX}/groups`
export const groupPath = (name: string) => `${GROUPS_PATH}/${seg(name)}`
export const groupHref = (name: string) => `/ec2/autoscaling/group/?name=${encodeURIComponent(name)}`

/** Mirrors nameRe in autoscaling.go. */
export const GROUP_NAME_RE = /^[\w.-]{1,255}$/

export const MAX_SIZE = 50

export const METRICS = [
  { value: "CPUUtilization", label: "Average CPU utilization" },
  { value: "MemoryUtilization", label: "Average memory utilization" },
]

export const metricLabel = (m: string) => METRICS.find((x) => x.value === m)?.label ?? m

/** Poll quickly while instances are launching or the group is being deleted. */
export function groupPoll(groups: AutoScalingGroup[] | undefined): number {
  const busy = (groups ?? []).some(
    (g) => g.status !== "Active" || (g.instances ?? []).length !== g.desired_capacity || (g.instances ?? []).some((i) => i.state === "pending"),
  )
  return busy ? 3000 : 15_000
}

export function useGroups() {
  return useApi<AutoScalingGroup[]>(GROUPS_PATH, { refreshInterval: groupPoll })
}

export function GroupStatusBadge({ g }: { g: Pick<AutoScalingGroup, "status" | "suspended" | "instances" | "desired_capacity"> }) {
  if (g.status !== "Active") return <StatusBadge status="deleting" label={g.status} />
  const n = (g.instances ?? []).length
  if (n < g.desired_capacity || (g.instances ?? []).some((i) => i.state === "pending")) return <StatusBadge status="updating" label="Updating capacity" />
  if (n > g.desired_capacity) return <StatusBadge status="updating" label="Scaling in" />
  if (g.suspended) return <StatusBadge status="suspended" label="Active, scaling suspended" />
  return <StatusBadge status="active" label="Active" />
}

export function ActivityStatusBadge({ status }: { status: string }) {
  return <StatusBadge status={status.toLowerCase()} label={status} tone={status === "Successful" ? "success" : status === "Failed" ? "danger" : "neutral"} />
}

const intIn = (s: string, lo: number, hi: number) => /^\d+$/.test(s) && Number(s) >= lo && Number(s) <= hi

/** capacityErrors mirrors validate() in autoscaling.go. */
export function capacityErrors(min: string, desired: string, max: string): Record<string, string> {
  const e: Record<string, string> = {}
  if (!intIn(min, 0, MAX_SIZE)) e.min = `0-${MAX_SIZE}`
  if (!intIn(max, 0, MAX_SIZE)) e.max = `0-${MAX_SIZE}`
  if (!intIn(desired, 0, MAX_SIZE)) e.desired = `0-${MAX_SIZE}`
  if (!e.min && !e.max && Number(max) < Number(min)) e.max = "Must be at least the minimum"
  if (!e.min && !e.max && !e.desired && (Number(desired) < Number(min) || Number(desired) > Number(max))) e.desired = "Must be between the minimum and maximum"
  return e
}

export function CapacityFields({
  min,
  desired,
  max,
  onChange,
  errors,
  idPrefix,
}: {
  min: string
  desired: string
  max: string
  onChange: (v: { min: string; desired: string; max: string }) => void
  errors: Record<string, string | undefined>
  idPrefix: string
}) {
  return (
    <div className="grid grid-cols-3 gap-3">
      <Field label="Minimum" htmlFor={`${idPrefix}-min`} error={errors.min}>
        <Input id={`${idPrefix}-min`} inputMode="numeric" value={min} onChange={(e) => onChange({ min: e.target.value, desired, max })} />
      </Field>
      <Field label="Desired" htmlFor={`${idPrefix}-desired`} error={errors.desired}>
        <Input id={`${idPrefix}-desired`} inputMode="numeric" value={desired} onChange={(e) => onChange({ min, desired: e.target.value, max })} />
      </Field>
      <Field label="Maximum" htmlFor={`${idPrefix}-max`} error={errors.max}>
        <Input id={`${idPrefix}-max`} inputMode="numeric" value={max} onChange={(e) => onChange({ min, desired, max: e.target.value })} />
      </Field>
    </div>
  )
}

// ---- target tracking policies ----

export interface PolicyDraft {
  name: string
  metric: string
  target: string
  cooldown: string
}

export const policyToDraft = (p: ScalingPolicy): PolicyDraft => ({ name: p.name, metric: p.metric, target: String(p.target_value), cooldown: String(p.cooldown_seconds || 180) })

export function policyDraftErrors(p: PolicyDraft): Record<string, string> {
  const e: Record<string, string> = {}
  const t = Number(p.target)
  if (!p.target || Number.isNaN(t) || t < 1 || t > 100) e.target = "1-100 %"
  if (p.cooldown && !intIn(p.cooldown, 60, 86400)) e.cooldown = "60-86400 s"
  return e
}

/** The name is left out so HomeCloud names the policy after its metric and target. */
export const draftToPolicy = (p: PolicyDraft): Partial<ScalingPolicy> => ({
  metric: p.metric,
  target_value: Number(p.target),
  cooldown_seconds: p.cooldown ? Number(p.cooldown) : undefined,
})

export function PolicyFields({ draft, onChange, errors, idPrefix, onRemove }: { draft: PolicyDraft; onChange: (p: PolicyDraft) => void; errors: Record<string, string | undefined>; idPrefix: string; onRemove?: () => void }) {
  return (
    <div className="grid grid-cols-2 items-start gap-3 rounded-md border p-3 sm:grid-cols-[minmax(0,1fr)_6rem_7rem_auto]">
      <Field label="Metric" htmlFor={`${idPrefix}-metric`} className="col-span-2 sm:col-span-1">
        <Select value={draft.metric} onValueChange={(v) => onChange({ ...draft, metric: v })}>
          <SelectTrigger id={`${idPrefix}-metric`} className="h-8 w-full">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            {METRICS.map((m) => (
              <SelectItem key={m.value} value={m.value}>
                {m.label}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
      </Field>
      <Field label="Target %" htmlFor={`${idPrefix}-target`} error={errors.target}>
        <Input id={`${idPrefix}-target`} inputMode="decimal" value={draft.target} onChange={(e) => onChange({ ...draft, target: e.target.value })} className="h-8" />
      </Field>
      <Field label="Cooldown (s)" htmlFor={`${idPrefix}-cool`} error={errors.cooldown}>
        <Input id={`${idPrefix}-cool`} inputMode="numeric" value={draft.cooldown} onChange={(e) => onChange({ ...draft, cooldown: e.target.value })} className="h-8" placeholder="180" />
      </Field>
      {onRemove && (
        <Button type="button" variant="ghost" size="icon" className="col-start-2 row-start-1 size-8 justify-self-end sm:col-start-4 sm:mt-6" onClick={onRemove} aria-label="Remove policy">
          <X />
        </Button>
      )}
    </div>
  )
}

export const newPolicyDraft = (): PolicyDraft => ({ name: "", metric: "CPUUtilization", target: "50", cooldown: "180" })

// ---- dialogs shared by the list and detail pages ----

export function EditCapacityDialog({ group, onClose }: { group: AutoScalingGroup | null; onClose: () => void }) {
  const [v, setV] = useState({ min: "", desired: "", max: "" })
  const [pending, setPending] = useState(false)
  useEffect(() => {
    if (group) setV({ min: String(group.min_size), desired: String(group.desired_capacity), max: String(group.max_size) })
  }, [group])
  const errors = capacityErrors(v.min, v.desired, v.max)

  const submit = async (e: React.FormEvent) => {
    e.preventDefault()
    if (!group || Object.keys(errors).length) return
    setPending(true)
    try {
      await api.patch(groupPath(group.name), { min_size: Number(v.min), max_size: Number(v.max), desired_capacity: Number(v.desired) })
      toast.success(`Updated capacity of ${group.name}`)
      await revalidate(ASG_PREFIX)
      await revalidate("/api/v1/ec2")
      onClose()
    } catch (err) {
      toast.error(errorMessage(err))
    } finally {
      setPending(false)
    }
  }

  const cur = group ? (group.instances ?? []).length : 0
  const d = Number(v.desired)
  return (
    <Dialog open={!!group} onOpenChange={(o) => !o && !pending && onClose()}>
      <DialogContent className="sm:max-w-md">
        <form onSubmit={submit} className="flex flex-col gap-4">
          <DialogHeader>
            <DialogTitle>Edit capacity of {group?.name}</DialogTitle>
            <DialogDescription>Target tracking policies adjust the desired capacity between the minimum and maximum.</DialogDescription>
          </DialogHeader>
          <CapacityFields min={v.min} desired={v.desired} max={v.max} onChange={setV} errors={errors} idPrefix="cap" />
          {!errors.desired && group && d !== cur && (
            <p className="text-muted-foreground text-xs">
              {d > cur ? `Launches ${pluralize(d - cur, "instance")}.` : `Terminates the ${pluralize(cur - d, "newest instance")}.`} Takes effect within about 10 seconds.
            </p>
          )}
          <DialogFooter>
            <Button type="button" variant="outline" onClick={onClose} disabled={pending}>
              Cancel
            </Button>
            <Button type="submit" disabled={pending || Object.keys(errors).length > 0}>
              {pending && <Loader2 className="animate-spin" />}
              Update
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}

export function EditPoliciesDialog({ group, onClose }: { group: AutoScalingGroup | null; onClose: () => void }) {
  const [drafts, setDrafts] = useState<PolicyDraft[]>([])
  const [pending, setPending] = useState(false)
  useEffect(() => {
    if (group) setDrafts((group.policies ?? []).map(policyToDraft))
  }, [group])
  const errs = drafts.map(policyDraftErrors)
  const invalid = errs.some((e) => Object.keys(e).length)

  const submit = async (e: React.FormEvent) => {
    e.preventDefault()
    if (!group || invalid) return
    setPending(true)
    try {
      await api.patch(groupPath(group.name), { policies: drafts.map(draftToPolicy) })
      toast.success(drafts.length ? `Saved ${pluralize(drafts.length, "scaling policy", "scaling policies")}` : "Removed scaling policies")
      await revalidate(ASG_PREFIX)
      onClose()
    } catch (err) {
      toast.error(errorMessage(err))
    } finally {
      setPending(false)
    }
  }

  return (
    <Dialog open={!!group} onOpenChange={(o) => !o && !pending && onClose()}>
      <DialogContent className="max-h-[90vh] overflow-y-auto sm:max-w-xl">
        <form onSubmit={submit} className="flex flex-col gap-4">
          <DialogHeader>
            <DialogTitle>Dynamic scaling policies</DialogTitle>
            <DialogDescription>
              Target tracking keeps the average metric of running instances near the target by changing the desired capacity (within min and max), at most once per
              cooldown.
            </DialogDescription>
          </DialogHeader>
          {drafts.length === 0 && <p className="text-muted-foreground text-sm">No policies: the group keeps its desired capacity.</p>}
          {drafts.map((d, i) => (
            <PolicyFields
              key={i}
              draft={d}
              onChange={(p) => setDrafts(drafts.map((x, j) => (j === i ? p : x)))}
              errors={errs[i]}
              idPrefix={`pol-${i}`}
              onRemove={() => setDrafts(drafts.filter((_, j) => j !== i))}
            />
          ))}
          <div>
            <Button type="button" variant="outline" size="sm" onClick={() => setDrafts([...drafts, newPolicyDraft()])} disabled={drafts.length >= 4}>
              <Plus /> Add target tracking policy
            </Button>
          </div>
          <DialogFooter>
            <Button type="button" variant="outline" onClick={onClose} disabled={pending}>
              Cancel
            </Button>
            <Button type="submit" disabled={pending || invalid}>
              {pending && <Loader2 className="animate-spin" />}
              Save policies
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}

export function DeleteGroupDialog({ group, onClose, redirect }: { group: AutoScalingGroup | null; onClose: () => void; redirect?: boolean }) {
  const router = useRouter()
  const n = (group?.instances ?? []).length
  return (
    <ConfirmDialog
      open={!!group}
      onOpenChange={(o) => !o && onClose()}
      title={`Delete Auto Scaling group ${group?.name ?? ""}`}
      confirmText={group?.name}
      description={
        <div className="flex flex-col gap-2">
          <p className="text-destructive">
            {n ? `The group's ${pluralize(n, "instance")} will be terminated` : "Any instances the group launches will be terminated"} and deregistered from its target
            groups.
          </p>
          <p>Volumes and EFS file systems are not deleted.</p>
        </div>
      }
      onConfirm={async () => {
        if (!group) return
        await api.del(groupPath(group.name))
        toast.success(`Deleting Auto Scaling group ${group.name}`)
        if (redirect) router.push("/ec2/autoscaling/")
        await revalidate(ASG_PREFIX)
      }}
    />
  )
}
