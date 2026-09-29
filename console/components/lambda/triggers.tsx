"use client"

import { useEffect, useMemo, useState } from "react"
import Link from "next/link"
import { Loader2, Network, Plus, Waypoints } from "lucide-react"
import { toast } from "sonner"

import { Button } from "@/components/ui/button"
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select"
import { Switch } from "@/components/ui/switch"
import { ActionsMenu, type ActionItem } from "@/components/console/actions-menu"
import { ConfirmDialog } from "@/components/console/confirm-dialog"
import { DataTable, cellLinkClass, type Column } from "@/components/console/data-table"
import { EmptyState } from "@/components/console/empty-state"
import { ErrorState } from "@/components/console/error-state"
import { Field } from "@/components/console/form-field"
import { PageHeader } from "@/components/console/page-header"
import { Section } from "@/components/console/section"
import { StatusBadge } from "@/components/console/status-badge"
import { TimeAgo } from "@/components/console/time-ago"
import { MethodBadge, apiHref } from "@/components/apigateway/common"
import { api, errorMessage, seg } from "@/lib/api"
import { revalidate, useApi } from "@/lib/hooks"
import type { EventSourceMapping, HttpApi, LambdaFunction, Queue } from "@/lib/types"

import { FUNCTIONS_PATH, LAMBDA_PATH, MAPPINGS_PATH, functionHref, queueHref } from "./common"

const mappingPath = (id: string) => `${MAPPINGS_PATH}/${seg(id)}`

function ResultBadge({ result }: { result: string }) {
  if (!result) return <span className="text-muted-foreground">-</span>
  if (result === "OK") return <StatusBadge status="ok" label="OK" tone="success" />
  if (result.startsWith("PROBLEM")) return <StatusBadge status="error" label={result.replace(/^PROBLEM:\s*/, "Problem: ")} tone="danger" />
  return <span className="text-muted-foreground text-sm">{result}</span>
}

function EnabledSwitch({ m }: { m: EventSourceMapping }) {
  const [pending, setPending] = useState(false)
  return (
    <span className="inline-flex items-center gap-2" onClick={(e) => e.stopPropagation()}>
      <Switch
        checked={m.enabled}
        disabled={pending}
        aria-label={m.enabled ? "Disable trigger" : "Enable trigger"}
        onCheckedChange={async (v) => {
          setPending(true)
          try {
            await api.patch(mappingPath(m.id), { enabled: v })
            toast.success(`${v ? "Enabled" : "Disabled"} trigger ${m.queue_name} → ${m.function_name}`)
            await revalidate(MAPPINGS_PATH)
          } catch (e) {
            toast.error(errorMessage(e))
          } finally {
            setPending(false)
          }
        }}
      />
      <span className="text-xs">{m.enabled ? "Enabled" : "Disabled"}</span>
    </span>
  )
}

/**
 * MappingsTable lists SQS event source mappings, with enable/disable,
 * batch size editing, delete and an Add trigger dialog. With `functionName`
 * set it shows one function's triggers.
 */
export function MappingsTable({ functionName }: { functionName?: string }) {
  const { data, error, isLoading, isValidating, mutate } = useApi<EventSourceMapping[]>(MAPPINGS_PATH, {
    query: functionName ? { function: functionName } : undefined,
    refreshInterval: 10_000,
  })
  const [selected, setSelected] = useState<string[]>([])
  const [adding, setAdding] = useState(false)
  const [editing, setEditing] = useState<EventSourceMapping | null>(null)
  const [deleting, setDeleting] = useState<EventSourceMapping | null>(null)
  const sel = data?.find((m) => m.id === selected[0]) ?? null

  const columns: Column<EventSourceMapping>[] = [
    ...(functionName
      ? []
      : [
          {
            id: "function",
            header: "Function",
            value: (m: EventSourceMapping) => m.function_name,
            cell: (m: EventSourceMapping) => (
              <Link href={functionHref(m.function_name, "triggers")} onClick={(e) => e.stopPropagation()} className={cellLinkClass()}>
                {m.function_name}
              </Link>
            ),
          },
        ]),
    {
      id: "queue",
      header: "SQS queue",
      value: (m) => m.queue_name,
      cell: (m) => (
        <Link href={queueHref(m.queue_name)} onClick={(e) => e.stopPropagation()} className={cellLinkClass()} title={m.event_source_arn}>
          {m.queue_name}
        </Link>
      ),
    },
    { id: "batch", header: "Batch size", value: (m) => m.batch_size, cell: (m) => m.batch_size, hideBelow: "sm" },
    { id: "enabled", header: "State", value: (m) => (m.enabled ? "enabled" : "disabled"), cell: (m) => <EnabledSwitch m={m} /> },
    { id: "result", header: "Last result", value: (m) => m.last_processing_result, cell: (m) => <ResultBadge result={m.last_processing_result} />, hideBelow: "md" },
    { id: "invoked", header: "Last invoked", value: (m) => m.last_invoked_at ?? "", cell: (m) => <TimeAgo value={m.last_invoked_at} />, hideBelow: "md" },
    {
      id: "uuid",
      header: "UUID",
      value: (m) => m.id,
      cell: (m) => <span className="text-muted-foreground font-mono text-xs">{m.id}</span>,
      hideBelow: "lg",
    },
  ]

  const items: ActionItem[] = [
    { label: "Edit batch size", onSelect: () => setEditing(sel), disabled: !sel },
    { separator: true },
    { label: "Delete", destructive: true, onSelect: () => setDeleting(sel), disabled: !sel },
  ]

  return (
    <>
      <DataTable
        title={functionName ? "SQS triggers" : "Event source mappings"}
        description={
          functionName
            ? "Messages from these queues are delivered to the function in batches and deleted when it succeeds."
            : "Each mapping polls an SQS queue and invokes a function with batches of messages."
        }
        data={data}
        columns={columns}
        rowId={(m) => m.id}
        loading={isLoading}
        error={error}
        onRefresh={() => mutate()}
        refreshing={isValidating}
        selection="single"
        selected={selected}
        onSelectedChange={setSelected}
        noSearch={!!functionName}
        searchPlaceholder="Filter by function or queue"
        defaultSort={{ id: functionName ? "queue" : "function" }}
        actions={
          <>
            <ActionsMenu items={items} disabled={!sel} />
            <Button size="sm" onClick={() => setAdding(true)}>
              <Plus /> Add trigger
            </Button>
          </>
        }
        empty={
          <EmptyState
            icon={Waypoints}
            title="No triggers"
            description={
              functionName
                ? "Add an SQS queue as a trigger to process its messages with this function."
                : "Connect an SQS queue to a function to process messages automatically."
            }
            action={
              <Button size="sm" onClick={() => setAdding(true)}>
                <Plus /> Add trigger
              </Button>
            }
          />
        }
      />
      <AddTriggerDialog open={adding} onOpenChange={setAdding} functionName={functionName} />
      <BatchSizeDialog mapping={editing} onClose={() => setEditing(null)} />
      <ConfirmDialog
        open={!!deleting}
        onOpenChange={(o) => !o && setDeleting(null)}
        title="Delete trigger?"
        description={
          deleting && (
            <>
              <span className="font-mono">{deleting.function_name}</span> stops receiving messages from{" "}
              <span className="font-mono">{deleting.queue_name}</span>. Messages stay in the queue.
            </>
          )
        }
        onConfirm={async () => {
          if (!deleting) return
          await api.del(mappingPath(deleting.id))
          toast.success(`Deleted trigger ${deleting.queue_name} → ${deleting.function_name}`)
          await revalidate(MAPPINGS_PATH)
        }}
      />
    </>
  )
}

function batchError(v: string) {
  const n = Number(v)
  return Number.isInteger(n) && n >= 1 && n <= 100 ? null : "Enter a whole number from 1 to 100"
}

export function AddTriggerDialog({ open, onOpenChange, functionName }: { open: boolean; onOpenChange: (o: boolean) => void; functionName?: string }) {
  const queues = useApi<Queue[]>(open ? "/api/v1/sqs/queues" : null)
  const functions = useApi<LambdaFunction[]>(open && !functionName ? FUNCTIONS_PATH : null)
  const [fn, setFn] = useState("")
  const [queue, setQueue] = useState("")
  const [batch, setBatch] = useState("10")
  const [enabled, setEnabled] = useState(true)
  const [pending, setPending] = useState(false)
  const [touched, setTouched] = useState(false)

  useEffect(() => {
    if (open) {
      setFn(functionName ?? "")
      setQueue("")
      setBatch("10")
      setEnabled(true)
      setTouched(false)
    }
  }, [open, functionName])

  const bErr = batchError(batch)
  const q = queues.data?.find((x) => x.name === queue)

  const submit = async (e: React.FormEvent) => {
    e.preventDefault()
    setTouched(true)
    if (!fn || !queue || bErr) return
    setPending(true)
    try {
      await api.post(MAPPINGS_PATH, { function_name: fn, queue_name: queue, batch_size: Number(batch), enabled })
      toast.success(`Added trigger ${queue} → ${fn}`)
      await revalidate(LAMBDA_PATH)
      onOpenChange(false)
    } catch (err) {
      toast.error(errorMessage(err))
    } finally {
      setPending(false)
    }
  }

  return (
    <Dialog open={open} onOpenChange={(o) => !pending && onOpenChange(o)}>
      <DialogContent className="sm:max-w-lg">
        <form onSubmit={submit} className="flex flex-col gap-5">
          <DialogHeader>
            <DialogTitle>Add SQS trigger</DialogTitle>
            <DialogDescription>
              HomeCloud polls the queue every second and invokes {functionName ? <span className="font-mono">{functionName}</span> : "the function"} with up
              to batch-size messages. Messages are deleted when the function succeeds; return {"{ batchItemFailures }"} to retry some of them.
            </DialogDescription>
          </DialogHeader>
          {!functionName && (
            <Field label="Function" htmlFor="trg-fn" error={touched && !fn ? "Choose a function" : undefined}>
              {functions.error ? (
                <ErrorState error={functions.error} onRetry={() => functions.mutate()} />
              ) : (
                <Select value={fn} onValueChange={setFn} disabled={!functions.data}>
                  <SelectTrigger id="trg-fn" className="w-full">
                    <SelectValue placeholder={functions.data ? "Choose a function" : "Loading functions..."} />
                  </SelectTrigger>
                  <SelectContent>
                    {(functions.data ?? []).map((f) => (
                      <SelectItem key={f.name} value={f.name}>
                        {f.name}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              )}
            </Field>
          )}
          <Field
            label="SQS queue"
            htmlFor="trg-queue"
            error={touched && !queue ? "Choose a queue" : undefined}
            help={
              queues.data && queues.data.length === 0 ? (
                <>
                  No queues yet.{" "}
                  <Link href="/sqs/" className="text-primary hover:underline">
                    Create a queue
                  </Link>{" "}
                  first.
                </>
              ) : q ? (
                <span className="font-mono">{q.arn}</span>
              ) : undefined
            }
          >
            {queues.error ? (
              <ErrorState error={queues.error} onRetry={() => queues.mutate()} />
            ) : (
              <Select value={queue} onValueChange={setQueue} disabled={!queues.data}>
                <SelectTrigger id="trg-queue" className="w-full">
                  <SelectValue placeholder={queues.data ? "Choose a queue" : "Loading queues..."} />
                </SelectTrigger>
                <SelectContent>
                  {(queues.data ?? []).map((x) => (
                    <SelectItem key={x.name} value={x.name}>
                      {x.name}
                      {x.fifo && <span className="text-muted-foreground text-xs">FIFO</span>}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            )}
          </Field>
          <Field label="Batch size" htmlFor="trg-batch" error={touched || batch ? (bErr ?? undefined) : undefined} help="1-100 messages per invocation.">
            <Input id="trg-batch" type="number" min={1} max={100} value={batch} onChange={(e) => setBatch(e.target.value)} className="h-8 w-28" />
          </Field>
          <div className="flex items-start justify-between gap-4 rounded-md border p-3">
            <div>
              <Label htmlFor="trg-enabled" className="font-medium">
                Activate trigger
              </Label>
              <p className="text-muted-foreground mt-0.5 text-xs">Start polling the queue right away. You can pause it later.</p>
            </div>
            <Switch id="trg-enabled" checked={enabled} onCheckedChange={setEnabled} />
          </div>
          <DialogFooter>
            <Button type="button" variant="outline" onClick={() => onOpenChange(false)} disabled={pending}>
              Cancel
            </Button>
            <Button type="submit" disabled={pending}>
              {pending && <Loader2 className="animate-spin" />}
              Add trigger
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}

function BatchSizeDialog({ mapping, onClose }: { mapping: EventSourceMapping | null; onClose: () => void }) {
  const [batch, setBatch] = useState("")
  const [pending, setPending] = useState(false)
  useEffect(() => {
    if (mapping) setBatch(String(mapping.batch_size))
  }, [mapping])
  const err = batchError(batch)

  const submit = async (e: React.FormEvent) => {
    e.preventDefault()
    if (!mapping || err) return
    setPending(true)
    try {
      await api.patch(mappingPath(mapping.id), { batch_size: Number(batch) })
      toast.success(`Batch size set to ${batch}`)
      await revalidate(MAPPINGS_PATH)
      onClose()
    } catch (e) {
      toast.error(errorMessage(e))
    } finally {
      setPending(false)
    }
  }

  return (
    <Dialog open={!!mapping} onOpenChange={(o) => !o && !pending && onClose()}>
      <DialogContent className="sm:max-w-sm">
        <form onSubmit={submit} className="flex flex-col gap-4">
          <DialogHeader>
            <DialogTitle>Edit batch size</DialogTitle>
            <DialogDescription>
              {mapping?.queue_name} → {mapping?.function_name}
            </DialogDescription>
          </DialogHeader>
          <Field label="Batch size" htmlFor="edit-batch" error={err ?? undefined}>
            <Input id="edit-batch" type="number" min={1} max={100} value={batch} onChange={(e) => setBatch(e.target.value)} autoFocus className="w-28" />
          </Field>
          <DialogFooter>
            <Button type="button" variant="outline" onClick={onClose} disabled={pending}>
              Cancel
            </Button>
            <Button type="submit" disabled={pending || !!err || Number(batch) === mapping?.batch_size}>
              {pending && <Loader2 className="animate-spin" />}
              Save
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}

/** TriggersTab: SQS mappings plus the API Gateway routes that target the function. */
export function TriggersTab({ fn }: { fn: LambdaFunction }) {
  const apis = useApi<HttpApi[]>("/api/v1/apigateway/apis", { refreshInterval: 30_000 })
  const routes = useMemo(
    () => (apis.data ?? []).flatMap((a) => (a.routes ?? []).filter((r) => r.function_name === fn.name).map((r) => ({ api: a, route: r }))),
    [apis.data, fn.name],
  )

  return (
    <div className="flex flex-col gap-4">
      <MappingsTable functionName={fn.name} />
      <Section
        title={`API Gateway routes (${apis.data ? routes.length : "…"})`}
        description="HTTP API routes that invoke this function."
        actions={
          <Button variant="outline" size="sm" asChild>
            <Link href="/apigateway/">
              <Network /> API Gateway
            </Link>
          </Button>
        }
        flush={routes.length > 0}
      >
        {apis.error ? (
          <ErrorState error={apis.error} onRetry={() => apis.mutate()} />
        ) : !apis.data ? (
          <p className="text-muted-foreground text-sm">Loading…</p>
        ) : routes.length === 0 ? (
          <p className="text-muted-foreground text-sm">
            No API routes target this function. Add a route in{" "}
            <Link href="/apigateway/" className="text-primary hover:underline">
              API Gateway
            </Link>{" "}
            to expose it over HTTP.
          </p>
        ) : (
          <ul className="divide-y">
            {routes.map(({ api: a, route: r }) => (
              <li key={`${a.id}-${r.id}`} className="flex flex-wrap items-center gap-x-3 gap-y-1 px-4 py-2.5 text-sm">
                <MethodBadge method={r.method} />
                <span className="font-mono text-[13px] break-all">{r.path}</span>
                <span className="text-muted-foreground">in</span>
                <Link href={apiHref(a.id)} className={cellLinkClass()}>
                  {a.name}
                </Link>
                <span className="text-muted-foreground font-mono text-xs break-all sm:ml-auto">
                  {a.endpoint}
                  {r.path}
                </span>
              </li>
            ))}
          </ul>
        )}
      </Section>
    </div>
  )
}

export function TriggersList() {
  return (
    <div className="flex flex-col gap-4">
      <PageHeader
        title="Event source mappings"
        description="SQS queues that trigger functions. HomeCloud polls each enabled queue and invokes the function with batches of messages."
        breadcrumbs={[{ label: "Lambda", href: "/lambda/" }, { label: "Event source mappings" }]}
      />
      <MappingsTable />
    </div>
  )
}
