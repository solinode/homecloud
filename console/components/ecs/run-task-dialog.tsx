"use client"

import { useEffect, useState } from "react"
import { useRouter } from "next/navigation"
import { Loader2 } from "lucide-react"
import { toast } from "sonner"

import { Button } from "@/components/ui/button"
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog"
import { Input } from "@/components/ui/input"
import { Field } from "@/components/console/form-field"
import { TagsEditor, rowsToTags, type TagRow } from "@/components/console/tags-editor"
import { api, errorMessage } from "@/lib/api"
import { pluralize } from "@/lib/format"
import { revalidate } from "@/lib/hooks"
import type { EcsRunTaskInput, EcsTask } from "@/lib/types"
import { ECS_PATH, TASKS_PATH, TaskDefinitionPicker, joinCommand, shortId, splitCommand, taskHref, useTaskDefinitions } from "./common"
import { NetworkFields, useNetworkSelection } from "./network-fields"

/**
 * RunTaskDialog starts standalone tasks. The API runs one task per call, so
 * a count above 1 issues several calls.
 */
export function RunTaskDialog({ open, onClose, initial }: { open: boolean; onClose: () => void; initial?: string }) {
  const router = useRouter()
  const tds = useTaskDefinitions()
  const net = useNetworkSelection()
  const [td, setTd] = useState("")
  const [count, setCount] = useState("1")
  const [command, setCommand] = useState("")
  const [envRows, setEnvRows] = useState<TagRow[]>([])
  const [pending, setPending] = useState(false)
  const [submitted, setSubmitted] = useState(false)

  useEffect(() => {
    if (open) {
      setTd(initial ?? "")
      setCount("1")
      setCommand("")
      setEnvRows([])
      setSubmitted(false)
    }
  }, [open, initial])

  const taskDef = tds.data?.find((x) => `${x.family}:${x.revision}` === td)
  const n = Number(count)
  const cmd = splitCommand(command)
  const errors: Record<string, string> = {}
  if (!td) errors.td = "Choose a task definition"
  if (!Number.isInteger(n) || n < 1 || n > 10) errors.count = "1-10"
  if (cmd.error) errors.command = cmd.error
  const envKeys = envRows.map((r) => r.key.trim()).filter(Boolean)
  if (new Set(envKeys).size !== envKeys.length) errors.env = "Variable names must be unique"
  const err = (k: string) => (submitted ? errors[k] : undefined)

  const submit = async (e: React.FormEvent) => {
    e.preventDefault()
    setSubmitted(true)
    if (Object.keys(errors).length) return
    const body: EcsRunTaskInput = {
      task_definition: td,
      subnet_id: net.subnetId || undefined,
      security_groups: net.sgIds,
      command: cmd.argv.length ? cmd.argv : undefined,
      environment: rowsToTags(envRows),
    }
    setPending(true)
    try {
      const res = await Promise.allSettled(Array.from({ length: n }, () => api.post<EcsTask>(TASKS_PATH, body)))
      await revalidate(ECS_PATH)
      const ok = res.filter((r): r is PromiseFulfilledResult<EcsTask> => r.status === "fulfilled").map((r) => r.value)
      const failed = res.filter((r): r is PromiseRejectedResult => r.status === "rejected")
      const started = ok.filter((t) => t.last_status !== "STOPPED")
      const stopped = ok.filter((t) => t.last_status === "STOPPED")
      if (started.length) toast.success(started.length === 1 ? `Started task ${shortId(started[0].id)}` : `Started ${pluralize(started.length, "task")}`)
      for (const t of stopped.slice(0, 2)) toast.error(`Task ${shortId(t.id)} failed to start: ${t.stop_reason || "unknown reason"}`)
      for (const f of failed.slice(0, 2)) toast.error(errorMessage(f.reason))
      if (!ok.length) return
      onClose()
      if (ok.length === 1) router.push(taskHref(ok[0].id))
    } finally {
      setPending(false)
    }
  }

  return (
    <Dialog open={open} onOpenChange={(o) => !o && !pending && onClose()}>
      <DialogContent className="max-h-[90vh] overflow-y-auto sm:max-w-2xl">
        <form onSubmit={submit} className="flex flex-col gap-4">
          <DialogHeader>
            <DialogTitle>Run task</DialogTitle>
            <DialogDescription>
              Standalone tasks run once and are not replaced when they stop. Ports allowed by the security groups are published on this host.
            </DialogDescription>
          </DialogHeader>
          <Field label="Task definition" error={err("td")}>
            {tds.data && tds.data.length === 0 ? (
              <p className="text-muted-foreground text-sm">No active task definitions. Register one first.</p>
            ) : (
              <TaskDefinitionPicker value={td} onChange={setTd} idPrefix="run-td" />
            )}
          </Field>
          <NetworkFields net={net} idPrefix="run" compact />
          <Field label="Number of tasks" htmlFor="run-count" error={err("count")}>
            <Input id="run-count" type="number" min={1} max={10} value={count} onChange={(e) => setCount(e.target.value)} className="w-24" />
          </Field>
          <Field
            label="Command override"
            htmlFor="run-cmd"
            optional
            error={err("command")}
            help={
              cmd.argv.length ? (
                <span className="font-mono break-all">{JSON.stringify(cmd.argv)}</span>
              ) : (
                <>Shell-style (quotes allowed) or a JSON array. Default: {joinCommand(taskDef?.command) || "the image's command"}</>
              )
            }
          >
            <Input
              id="run-cmd"
              value={command}
              onChange={(e) => setCommand(e.target.value)}
              placeholder={joinCommand(taskDef?.command) || 'sh -c "echo hello"'}
              className="font-mono text-[13px]"
              spellCheck={false}
            />
          </Field>
          <Field label="Environment overrides" optional error={err("env")} help="Merged over the task definition's environment.">
            <TagsEditor rows={envRows} onChange={setEnvRows} keyPlaceholder="Name" valuePlaceholder="Value" addLabel="Add variable" />
          </Field>
          <DialogFooter>
            <Button type="button" variant="outline" onClick={onClose} disabled={pending}>
              Cancel
            </Button>
            <Button type="submit" disabled={pending || !td}>
              {pending && <Loader2 className="animate-spin" />}
              Run {Number.isInteger(n) && n > 1 ? `${n} tasks` : "task"}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}
