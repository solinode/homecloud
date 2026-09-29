"use client"

import { useEffect, useState } from "react"
import { Loader2 } from "lucide-react"
import { toast } from "sonner"

import { Button } from "@/components/ui/button"
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog"
import { Input } from "@/components/ui/input"
import { RadioGroup, RadioGroupItem } from "@/components/ui/radio-group"
import { ConfirmDialog } from "@/components/console/confirm-dialog"
import { Field } from "@/components/console/form-field"
import { KeyValueGrid } from "@/components/console/key-value"
import { Section } from "@/components/console/section"
import { ApiError, api, errorMessage } from "@/lib/api"
import { revalidate, useApi } from "@/lib/hooks"
import type { EventInvokeConfig, FunctionDetail, LambdaFunction } from "@/lib/types"
import { cn } from "@/lib/utils"

import { LAMBDA_PATH, fnPath, useAccountSettings } from "./common"
import { DestinationPicker, destinationKind } from "./pickers"

/** Unreserved concurrency the account must keep (the backend's MinUnreserved). */
const MIN_UNRESERVED = 10

export function ConcurrencyConfig({ fn, detail }: { fn: LambdaFunction; detail: FunctionDetail }) {
  const account = useAccountSettings()
  const [editing, setEditing] = useState(false)
  const reserved = fn.reserved_concurrency
  const lim = account.data?.AccountLimit
  const unreserved = lim?.UnreservedConcurrentExecutions

  return (
    <Section
      title="Concurrency"
      description="The number of execution environments that can run this function at once. Invocations beyond it are throttled."
      actions={
        <Button variant="outline" size="sm" onClick={() => setEditing(true)}>
          Edit
        </Button>
      }
    >
      <KeyValueGrid
        columns={3}
        items={[
          {
            label: "Reserved concurrency",
            value:
              reserved == null ? (
                <span className="text-muted-foreground">Not reserved: uses unreserved account concurrency</span>
              ) : reserved === 0 ? (
                <span className="text-destructive">0 (all invocations are throttled)</span>
              ) : (
                String(reserved)
              ),
          },
          { label: "Concurrency limit", value: detail.concurrency_limit != null ? String(detail.concurrency_limit) : "" },
          {
            label: "Running now",
            value: `${detail.concurrent_executions ?? 0} executing, ${detail.environments ?? 0} ${detail.environments === 1 ? "environment" : "environments"}`,
          },
          { label: "Account concurrency", value: lim ? String(lim.ConcurrentExecutions) : "" },
          { label: "Unreserved account concurrency", value: unreserved != null ? String(unreserved) : "" },
        ]}
      />
      <ConcurrencyDialog open={editing} onOpenChange={setEditing} fn={fn} unreserved={unreserved} />
    </Section>
  )
}

function ConcurrencyDialog({ open, onOpenChange, fn, unreserved }: { open: boolean; onOpenChange: (o: boolean) => void; fn: LambdaFunction; unreserved?: number }) {
  const [mode, setMode] = useState<"unreserved" | "reserved">("unreserved")
  const [value, setValue] = useState("")
  const [pending, setPending] = useState(false)
  useEffect(() => {
    if (open) {
      setMode(fn.reserved_concurrency == null ? "unreserved" : "reserved")
      setValue(fn.reserved_concurrency == null ? "" : String(fn.reserved_concurrency))
    }
  }, [open, fn.reserved_concurrency])

  // The function's own reservation is returned to the pool when it changes.
  const max = unreserved != null ? unreserved + (fn.reserved_concurrency ?? 0) - MIN_UNRESERVED : undefined
  const n = Number(value)
  const err =
    mode === "reserved"
      ? !value.trim() || !Number.isInteger(n) || n < 0
        ? "Enter a whole number, 0 or more"
        : max != null && n > max
          ? `At most ${max}: ${MIN_UNRESERVED} must stay unreserved for other functions`
          : null
      : null

  const submit = async (e: React.FormEvent) => {
    e.preventDefault()
    if (err) return
    setPending(true)
    try {
      if (mode === "unreserved") {
        await api.del(`${fnPath(fn.name)}/concurrency`)
        toast.success("Removed the reserved concurrency")
      } else {
        await api.put(`${fnPath(fn.name)}/concurrency`, { reserved_concurrent_executions: n })
        toast.success(`Reserved concurrency set to ${n}`)
      }
      await revalidate(LAMBDA_PATH)
      onOpenChange(false)
    } catch (e) {
      toast.error(errorMessage(e))
    } finally {
      setPending(false)
    }
  }

  return (
    <Dialog open={open} onOpenChange={(o) => !pending && onOpenChange(o)}>
      <DialogContent className="sm:max-w-md">
        <form onSubmit={submit} className="flex flex-col gap-5">
          <DialogHeader>
            <DialogTitle>Edit concurrency</DialogTitle>
            <DialogDescription>Reserving concurrency guarantees capacity for this function and caps it at the same number.</DialogDescription>
          </DialogHeader>
          <RadioGroup value={mode} onValueChange={(v) => setMode(v as typeof mode)} className="gap-2">
            {(
              [
                { v: "unreserved", title: "Use unreserved account concurrency", text: `Shares the account pool${unreserved != null ? ` (${unreserved} available)` : ""}.` },
                { v: "reserved", title: "Reserve concurrency", text: "Set 0 to throttle every invocation, e.g. to pause a function." },
              ] as const
            ).map((o) => (
              <label
                key={o.v}
                htmlFor={`conc-${o.v}`}
                className={cn("flex cursor-pointer items-start gap-3 rounded-md border p-3", mode === o.v && "border-primary bg-primary/5 dark:bg-primary/10")}
              >
                <RadioGroupItem id={`conc-${o.v}`} value={o.v} className="mt-0.5" />
                <span className="flex flex-col gap-0.5">
                  <span className="text-sm font-medium">{o.title}</span>
                  <span className="text-muted-foreground text-xs">{o.text}</span>
                </span>
              </label>
            ))}
          </RadioGroup>
          {mode === "reserved" && (
            <Field label="Reserved concurrency" htmlFor="conc-value" error={err ?? undefined} help={max != null ? `0-${max}` : undefined}>
              <Input id="conc-value" type="number" min={0} max={max} value={value} onChange={(e) => setValue(e.target.value)} className="w-32" autoFocus />
            </Field>
          )}
          <DialogFooter>
            <Button type="button" variant="outline" onClick={() => onOpenChange(false)} disabled={pending}>
              Cancel
            </Button>
            <Button type="submit" disabled={pending || !!err}>
              {pending && <Loader2 className="animate-spin" />}
              Save
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}

// ---- asynchronous invocation ----

const DEFAULT_AGE = 21600
const DEFAULT_RETRIES = 2

function formatAge(s: number) {
  const h = Math.floor(s / 3600)
  const m = Math.floor((s % 3600) / 60)
  const r = s % 60
  return [h && `${h} h`, m && `${m} min`, r && `${r} s`].filter(Boolean).join(" ") || "0 s"
}

function DestinationValue({ arn }: { arn?: string }) {
  if (!arn) return <span className="text-muted-foreground">None</span>
  const kind = destinationKind(arn)
  const label = { sqs: "SQS", sns: "SNS", lambda: "Lambda", events: "EventBridge", "": "" }[kind]
  return (
    <span className="flex min-w-0 flex-col">
      <span>
        {label && <span className="text-muted-foreground text-xs">{label}: </span>}
        {arn.split(/[:/]/).pop()}
      </span>
      <span className="text-muted-foreground font-mono text-xs break-all">{arn}</span>
    </span>
  )
}

/** AsyncConfig: retries, maximum event age, destinations and the dead-letter queue for asynchronous invocations. */
export function AsyncConfig({ fn, qualifier }: { fn: LambdaFunction; qualifier?: string }) {
  const path = `${fnPath(fn.name)}/event-invoke-config`
  const cfg = useApi<EventInvokeConfig>(path, { query: { qualifier } })
  const [editing, setEditing] = useState(false)
  const [removing, setRemoving] = useState(false)
  const notFound = cfg.error instanceof ApiError && cfg.error.status === 404
  const c = notFound ? null : cfg.data

  return (
    <Section
      title="Asynchronous invocation"
      description="How events queued with invocation type Event are retried, and where results are sent."
      actions={
        <>
          {c && (
            <Button variant="outline" size="sm" className="text-destructive hover:text-destructive" onClick={() => setRemoving(true)}>
              Reset
            </Button>
          )}
          <Button variant="outline" size="sm" onClick={() => setEditing(true)}>
            Edit
          </Button>
        </>
      }
    >
      {cfg.error && !notFound ? (
        <p className="text-destructive text-sm">{errorMessage(cfg.error)}</p>
      ) : (
        <KeyValueGrid
          columns={3}
          items={[
            { label: "Maximum age of event", value: formatAge(c?.maximum_event_age_seconds ?? DEFAULT_AGE) },
            { label: "Retry attempts", value: String(c?.maximum_retry_attempts ?? DEFAULT_RETRIES) },
            { label: "Dead-letter queue", value: <DestinationValue arn={fn.dead_letter_target} /> },
            { label: "On success destination", value: <DestinationValue arn={c?.on_success} /> },
            { label: "On failure destination", value: <DestinationValue arn={c?.on_failure} /> },
          ]}
        />
      )}
      <AsyncDialog open={editing} onOpenChange={setEditing} fn={fn} cfg={c ?? null} path={path} qualifier={qualifier} />
      <ConfirmDialog
        open={removing}
        onOpenChange={setRemoving}
        title="Reset asynchronous invocation settings?"
        description="The retry and age limits return to their defaults (2 retries, 6 hours) and the destinations are removed."
        actionLabel="Reset"
        onConfirm={async () => {
          await api.del(path + (qualifier ? `?qualifier=${encodeURIComponent(qualifier)}` : ""))
          toast.success("Reset the asynchronous invocation settings")
          await revalidate(LAMBDA_PATH)
        }}
      />
    </Section>
  )
}

function AsyncDialog({
  open,
  onOpenChange,
  fn,
  cfg,
  path,
  qualifier,
}: {
  open: boolean
  onOpenChange: (o: boolean) => void
  fn: LambdaFunction
  cfg: EventInvokeConfig | null
  path: string
  qualifier?: string
}) {
  const [age, setAge] = useState("")
  const [retries, setRetries] = useState("")
  const [onSuccess, setOnSuccess] = useState("")
  const [onFailure, setOnFailure] = useState("")
  const [dlq, setDlq] = useState("")
  const [pending, setPending] = useState(false)
  useEffect(() => {
    if (!open) return
    setAge(String(cfg?.maximum_event_age_seconds ?? DEFAULT_AGE))
    setRetries(String(cfg?.maximum_retry_attempts ?? DEFAULT_RETRIES))
    setOnSuccess(cfg?.on_success ?? "")
    setOnFailure(cfg?.on_failure ?? "")
    setDlq(fn.dead_letter_target ?? "")
  }, [open, cfg, fn.dead_letter_target])

  const a = Number(age)
  const r = Number(retries)
  const errors: Record<string, string> = {}
  if (!Number.isInteger(a) || a < 60 || a > 21600) errors.age = "60-21600 seconds (1 minute to 6 hours)"
  if (!Number.isInteger(r) || r < 0 || r > 2) errors.retries = "0, 1 or 2"
  const valid = Object.keys(errors).length === 0

  const submit = async (e: React.FormEvent) => {
    e.preventDefault()
    if (!valid) return
    setPending(true)
    try {
      const q = qualifier ? `?qualifier=${encodeURIComponent(qualifier)}` : ""
      await api.put(path + q, { maximum_event_age_seconds: a, maximum_retry_attempts: r, on_success: onSuccess, on_failure: onFailure })
      if (!qualifier && dlq !== (fn.dead_letter_target ?? "")) await api.patch(fnPath(fn.name), { dead_letter_target: dlq })
      toast.success("Saved the asynchronous invocation settings")
      await revalidate(LAMBDA_PATH)
      onOpenChange(false)
    } catch (e) {
      toast.error(errorMessage(e))
    } finally {
      setPending(false)
    }
  }

  const Clearable = ({ value, onClear }: { value: string; onClear: () => void }) =>
    value ? (
      <Button type="button" variant="link" size="sm" className="h-auto self-start p-0 text-xs" onClick={onClear}>
        Remove destination
      </Button>
    ) : null

  return (
    <Dialog open={open} onOpenChange={(o) => !pending && onOpenChange(o)}>
      <DialogContent className="sm:max-w-2xl">
        <form onSubmit={submit} className="flex flex-col gap-5">
          <DialogHeader>
            <DialogTitle>Edit asynchronous invocation</DialogTitle>
            <DialogDescription>
              Failed asynchronous invocations are retried with backoff. Events older than the maximum age, or that exhaust their retries, go to the on-failure
              destination (or the dead-letter queue).
            </DialogDescription>
          </DialogHeader>
          <div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
            <Field label="Maximum age of event (seconds)" htmlFor="async-age" error={errors.age} help="60-21600 seconds">
              <Input id="async-age" type="number" min={60} max={21600} value={age} onChange={(e) => setAge(e.target.value)} className="w-36" />
            </Field>
            <Field label="Retry attempts" htmlFor="async-retries" error={errors.retries} help="0-2 retries after the first attempt">
              <Input id="async-retries" type="number" min={0} max={2} value={retries} onChange={(e) => setRetries(e.target.value)} className="w-24" />
            </Field>
          </div>
          <Field label="On success destination" optional help="Receives an invocation record with the function's response.">
            <DestinationPicker value={onSuccess} onChange={setOnSuccess} selfArn={fn.arn} />
            <Clearable value={onSuccess} onClear={() => setOnSuccess("")} />
          </Field>
          <Field label="On failure destination" optional help="Receives an invocation record with the error after the retries are exhausted.">
            <DestinationPicker value={onFailure} onChange={setOnFailure} selfArn={fn.arn} />
            <Clearable value={onFailure} onClear={() => setOnFailure("")} />
          </Field>
          {!qualifier && (
            <Field label="Dead-letter queue" optional help="Receives the original event of a discarded invocation. SQS queues and SNS topics only.">
              <DestinationPicker value={dlq} onChange={setDlq} selfArn={fn.arn} kinds={["sqs", "sns"]} />
              <Clearable value={dlq} onClear={() => setDlq("")} />
            </Field>
          )}
          <DialogFooter>
            <Button type="button" variant="outline" onClick={() => onOpenChange(false)} disabled={pending}>
              Cancel
            </Button>
            <Button type="submit" disabled={pending || !valid}>
              {pending && <Loader2 className="animate-spin" />}
              Save
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}
