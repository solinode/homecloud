"use client"

import { useEffect, useState } from "react"
import { Loader2 } from "lucide-react"
import { toast } from "sonner"

import { Button } from "@/components/ui/button"
import { Checkbox } from "@/components/ui/checkbox"
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog"
import { Input } from "@/components/ui/input"
import { ConfirmDialog } from "@/components/console/confirm-dialog"
import { Field } from "@/components/console/form-field"
import { api, errorMessage, seg } from "@/lib/api"
import { pluralize } from "@/lib/format"
import { revalidate } from "@/lib/hooks"
import type { EcsService, EcsTask, EcsTaskDefinition, EcsUpdateServiceInput } from "@/lib/types"
import { ECS_PATH, SERVICES_PATH, TASKS_PATH, TASK_DEFS_PATH, TaskDefinitionPicker, shortId, tdKey } from "./common"

export function DeleteServiceDialog({
  service,
  onClose,
  onDeleted,
}: {
  service: Pick<EcsService, "name" | "running_count" | "load_balancer"> | null
  onClose: () => void
  onDeleted?: () => void
}) {
  return (
    <ConfirmDialog
      open={!!service}
      onOpenChange={(o) => !o && onClose()}
      title={`Delete service ${service?.name ?? ""}?`}
      description={
        <>
          The service is set to draining and all of its tasks are stopped
          {service?.running_count ? ` (${pluralize(service.running_count, "running task")})` : ""}
          {service?.load_balancer ? ` and deregistered from target group ${service.load_balancer.target_group}` : ""}. You don&apos;t need to scale it
          to 0 first. The service disappears once its tasks have stopped.
        </>
      }
      confirmText={service?.name}
      onConfirm={async () => {
        if (!service) return
        await api.del(`${SERVICES_PATH}/${seg(service.name)}`)
        await revalidate(ECS_PATH)
        toast.success(`Deleting service ${service.name}`)
        onDeleted?.()
      }}
    />
  )
}

export function StopTaskDialog({ task, onClose }: { task: Pick<EcsTask, "id" | "service"> | null; onClose: () => void }) {
  return (
    <ConfirmDialog
      open={!!task}
      onOpenChange={(o) => !o && onClose()}
      title={`Stop task ${task ? shortId(task.id) : ""}?`}
      description={
        task?.service
          ? `The container is stopped and deregistered from the load balancer. Service ${task.service} will start a replacement task to keep its desired count.`
          : "The container is stopped. Stopped tasks stay visible for an hour."
      }
      actionLabel="Stop"
      onConfirm={async () => {
        if (!task) return
        await api.post(`${TASKS_PATH}/${seg(task.id)}/stop`)
        await revalidate(ECS_PATH)
        toast.success(`Stopped task ${shortId(task.id)}`)
      }}
    />
  )
}

export function DeregisterDialog({ td, onClose }: { td: Pick<EcsTaskDefinition, "family" | "revision"> | null; onClose: () => void }) {
  return (
    <ConfirmDialog
      open={!!td}
      onOpenChange={(o) => !o && onClose()}
      title={`Deregister ${td ? tdKey(td) : ""}?`}
      description="The revision becomes INACTIVE: it can no longer be used to run tasks or create services. Services already using it keep running. This cannot be undone."
      actionLabel="Deregister"
      onConfirm={async () => {
        if (!td) return
        await api.del(`${TASK_DEFS_PATH}/${seg(tdKey(td))}`)
        await revalidate(ECS_PATH)
        toast.success(`Deregistered ${tdKey(td)}`)
      }}
    />
  )
}

/** UpdateServiceDialog scales a service, deploys another revision and/or forces a new deployment. */
export function UpdateServiceDialog({ service, onClose }: { service: EcsService | null; onClose: () => void }) {
  const [count, setCount] = useState("")
  const [td, setTd] = useState("")
  const [force, setForce] = useState(false)
  const [pending, setPending] = useState(false)
  useEffect(() => {
    if (service) {
      setCount(String(service.desired_count))
      setTd(service.task_definition)
      setForce(false)
    }
  }, [service])

  const n = Number(count)
  const countErr = count !== "" && (!Number.isInteger(n) || n < 0 || n > 50) ? "Enter a whole number from 0 to 50" : undefined
  const changedCount = service && count !== "" && n !== service.desired_count
  const changedTd = service && td && td !== service.task_definition
  const dirty = !!(changedCount || changedTd || force)

  const submit = async (e: React.FormEvent) => {
    e.preventDefault()
    if (!service || countErr || !dirty) return
    const body: EcsUpdateServiceInput = {}
    if (changedCount) body.desired_count = n
    if (changedTd) body.task_definition = td
    if (force) body.force_new_deployment = true
    setPending(true)
    try {
      await api.patch(`${SERVICES_PATH}/${seg(service.name)}`, body)
      toast.success(changedTd || force ? `Deployment started for ${service.name}` : `Updated ${service.name}`)
      await revalidate(ECS_PATH)
      onClose()
    } catch (err) {
      toast.error(errorMessage(err))
    } finally {
      setPending(false)
    }
  }

  return (
    <Dialog open={!!service} onOpenChange={(o) => !o && !pending && onClose()}>
      <DialogContent className="sm:max-w-lg">
        <form onSubmit={submit} className="flex flex-col gap-4">
          <DialogHeader>
            <DialogTitle>Update service</DialogTitle>
            <DialogDescription>
              Deployments are rolling: new tasks start one at a time, and tasks of the previous revision stop once the new ones are running.
            </DialogDescription>
          </DialogHeader>
          <Field label="Desired tasks" htmlFor="upd-count" error={countErr} help="0-50. Scaling to 0 stops all tasks but keeps the service.">
            <Input id="upd-count" type="number" min={0} max={50} value={count} onChange={(e) => setCount(e.target.value)} className="w-28" />
          </Field>
          <Field
            label="Task definition revision"
            help={
              service ? (
                <>
                  Currently <span className="font-mono">{service.task_definition}</span>. Choosing another revision starts a deployment.
                </>
              ) : undefined
            }
          >
            <TaskDefinitionPicker value={td} onChange={setTd} idPrefix="upd-td" autoSelect={false} />
          </Field>
          <label className="flex items-start gap-2 text-sm">
            <Checkbox className="mt-0.5" checked={force} onCheckedChange={(v) => setForce(!!v)} />
            <span>
              Force new deployment
              <span className="text-muted-foreground block text-xs">
                Replace every running task, even with the same revision (e.g. to pick up a new image pushed under the same tag or rotated secrets).
              </span>
            </span>
          </label>
          <DialogFooter>
            <Button type="button" variant="outline" onClick={onClose} disabled={pending}>
              Cancel
            </Button>
            <Button type="submit" disabled={pending || !dirty || !!countErr}>
              {pending && <Loader2 className="animate-spin" />}
              Update
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}
