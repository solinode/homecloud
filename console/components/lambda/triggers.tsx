"use client"

import { useEffect, useMemo, useState } from "react"
import Link from "next/link"
import { Database, Inbox, Loader2, Network, Plus, Waypoints } from "lucide-react"
import { toast } from "sonner"

import { Button } from "@/components/ui/button"
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select"
import { Switch } from "@/components/ui/switch"
import { ActionsMenu, type ActionItem } from "@/components/console/actions-menu"
import { ConfirmDialog } from "@/components/console/confirm-dialog"
import { CellLink, CellText, DataTable, type Column } from "@/components/console/data-table"
import { EmptyState } from "@/components/console/empty-state"
import { ErrorState } from "@/components/console/error-state"
import { Field } from "@/components/console/form-field"
import { PageHeader } from "@/components/console/page-header"
import { OptionCard, OptionGroup } from "@/components/console/option-card"
import { StatusBadge } from "@/components/console/status-badge"
import { Tag, methodAccent } from "@/components/console/tag"
import { TimeAgo } from "@/components/console/time-ago"
import { apiHref } from "@/components/apigateway/common"
import { tableHref } from "@/components/dynamodb/common"
import { api, errorMessage, seg } from "@/lib/api"
import { revalidate, useApi } from "@/lib/hooks"
import type { DynamoTable, EventSourceMapping, HttpApi, LambdaFunction, Queue } from "@/lib/types"

import { FUNCTIONS_PATH, LAMBDA_PATH, MAPPINGS_PATH, functionHref, queueHref } from "./common"
import { DestinationPicker } from "./pickers"

const mappingPath = (id: string) => `${MAPPINGS_PATH}/${seg(id)}`

type SourceKind = "sqs" | "dynamodb"

/** streamTable extracts the table name from a DynamoDB stream ARN (…:table/NAME/stream/LABEL). */
export function streamTable(arn: string): string | null {
  const m = /:table\/([^/]+)\/stream\//.exec(arn)
  return m ? m[1] : null
}

const sourceKind = (m: Pick<EventSourceMapping, "event_source_arn">): SourceKind => (streamTable(m.event_source_arn) ? "dynamodb" : "sqs")
const sourceName = (m: EventSourceMapping) => streamTable(m.event_source_arn) ?? m.queue_name

/** Batch size limits per source type (HomeCloud: SQS 1-10, DynamoDB streams 1-1000). */
const BATCH_MAX: Record<SourceKind, number> = { sqs: 10, dynamodb: 1000 }

type StreamTable = DynamoTable & { stream_arn?: string; stream_view_type?: string }

function SourceCell({ m }: { m: EventSourceMapping }) {
  const table = streamTable(m.event_source_arn)
  return (
    <span className="flex min-w-0 flex-col">
      {table ? (
        <CellLink href={tableHref(table)} title={m.event_source_arn}>
          {table}
        </CellLink>
      ) : (
        <CellLink href={queueHref(m.queue_name)} title={m.event_source_arn}>
          {m.queue_name}
        </CellLink>
      )}
      <span className="mt-1 flex items-center gap-1.5">
        <Tag accent={table ? "info" : "warning"}>{table ? "DynamoDB stream" : "SQS"}</Tag>
        {table && m.starting_position && <span className="text-muted-foreground font-mono text-[11px]">{m.starting_position}</span>}
      </span>
    </span>
  )
}

function ResultBadge({ result }: { result: string }) {
  if (!result) return <span className="text-muted-foreground">-</span>
  if (result === "OK") return <StatusBadge status="ok" label="OK" tone="success" />
  if (result.startsWith("PROBLEM")) return <StatusBadge status="error" label={result.replace(/^PROBLEM:\s*/, "Problem: ")} tone="danger" />
  return <CellText muted>{result}</CellText>
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
            toast.success(`${v ? "Enabled" : "Disabled"} trigger ${sourceName(m)} → ${m.function_name}`)
            await revalidate(MAPPINGS_PATH)
          } catch (e) {
            toast.error(errorMessage(e))
          } finally {
            setPending(false)
          }
        }}
      />
      <span className={m.enabled ? "text-success text-xs font-medium" : "text-muted-foreground text-xs"}>{m.enabled ? "Enabled" : "Disabled"}</span>
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
            cell: (m: EventSourceMapping) => <CellLink href={functionHref(m.function_name, "triggers")}>{m.function_name}</CellLink>,
          },
        ]),
    {
      id: "queue",
      header: "Event source",
      value: (m) => sourceName(m),
      cell: (m) => <SourceCell m={m} />,
    },
    { id: "batch", header: "Batch size", value: (m) => m.batch_size, cell: (m) => <span className="tabular-nums">{m.batch_size}</span>, hideBelow: "sm" },
    { id: "enabled", header: "State", value: (m) => (m.enabled ? "enabled" : "disabled"), cell: (m) => <EnabledSwitch m={m} /> },
    { id: "result", header: "Last result", value: (m) => m.last_processing_result, cell: (m) => <ResultBadge result={m.last_processing_result} />, hideBelow: "md" },
    { id: "invoked", header: "Last invoked", value: (m) => m.last_invoked_at ?? "", cell: (m) => <TimeAgo value={m.last_invoked_at} />, hideBelow: "md" },
    {
      id: "uuid",
      header: "UUID",
      value: (m) => m.id,
      cell: (m) => <CellText mono muted>{m.id}</CellText>,
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
        title={functionName ? "Event source triggers" : "Event source mappings"}
        description={
          functionName
            ? "SQS messages and DynamoDB stream records are delivered to the function in batches."
            : "Each mapping polls an SQS queue or a DynamoDB stream and invokes a function with batches of records."
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
        searchPlaceholder="Filter by function or source"
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
                ? "Add an SQS queue or a DynamoDB stream as a trigger to process its records with this function."
                : "Connect an SQS queue or DynamoDB stream to a function to process records automatically."
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
              <span className="font-mono">{deleting.function_name}</span> stops receiving records from{" "}
              <span className="font-mono">{sourceName(deleting)}</span>.{" "}
              {sourceKind(deleting) === "sqs" ? "Messages stay in the queue." : "The stream keeps its records until they expire."}
            </>
          )
        }
        onConfirm={async () => {
          if (!deleting) return
          await api.del(mappingPath(deleting.id))
          toast.success(`Deleted trigger ${sourceName(deleting)} → ${deleting.function_name}`)
          await revalidate(MAPPINGS_PATH)
        }}
      />
    </>
  )
}

function batchError(v: string, max: number) {
  const n = Number(v)
  return Number.isInteger(n) && n >= 1 && n <= max ? null : `Enter a whole number from 1 to ${max}`
}

export function AddTriggerDialog({ open, onOpenChange, functionName }: { open: boolean; onOpenChange: (o: boolean) => void; functionName?: string }) {
  const [kind, setKind] = useState<SourceKind>("sqs")
  const queues = useApi<Queue[]>(open && kind === "sqs" ? "/api/v1/sqs/queues" : null)
  const tables = useApi<StreamTable[]>(open && kind === "dynamodb" ? "/api/v1/dynamodb/tables" : null)
  const functions = useApi<LambdaFunction[]>(open ? FUNCTIONS_PATH : null)
  const [fn, setFn] = useState("")
  const [queue, setQueue] = useState("")
  const [table, setTable] = useState("")
  const [position, setPosition] = useState("LATEST")
  const [batch, setBatch] = useState("10")
  const [windowSec, setWindowSec] = useState("0")
  const [retries, setRetries] = useState("-1")
  const [bisect, setBisect] = useState(false)
  const [onFailure, setOnFailure] = useState("")
  const [partial, setPartial] = useState(false)
  const [enabled, setEnabled] = useState(true)
  const [pending, setPending] = useState(false)
  const [touched, setTouched] = useState(false)

  useEffect(() => {
    if (open) {
      setKind("sqs")
      setFn(functionName ?? "")
      setQueue("")
      setTable("")
      setPosition("LATEST")
      setBatch("10")
      setWindowSec("0")
      setRetries("-1")
      setBisect(false)
      setOnFailure("")
      setPartial(false)
      setEnabled(true)
      setTouched(false)
    }
  }, [open, functionName])

  useEffect(() => setBatch(kind === "sqs" ? "10" : "100"), [kind])

  const bErr = batchError(batch, BATCH_MAX[kind])
  const w = Number(windowSec)
  const wErr = Number.isInteger(w) && w >= 0 && w <= 300 ? null : "0-300 seconds"
  const r = Number(retries)
  const rErr = kind === "dynamodb" && !(Number.isInteger(r) && r >= -1 && r <= 10000) ? "-1 (infinite) to 10000" : null
  const q = queues.data?.find((x) => x.name === queue)
  const t = tables.data?.find((x) => x.name === table)
  const source = kind === "sqs" ? queue : table
  const fnArn = functions.data?.find((f) => f.name === fn)?.arn ?? functions.data?.[0]?.arn ?? ""

  const submit = async (e: React.FormEvent) => {
    e.preventDefault()
    setTouched(true)
    if (!fn || !source || bErr || wErr || rErr) return
    if (kind === "dynamodb" && !t?.stream_arn) return
    setPending(true)
    try {
      const body =
        kind === "sqs"
          ? {
              function_name: fn,
              queue_name: queue,
              batch_size: Number(batch),
              batching_window_seconds: w,
              function_response_types: partial ? ["ReportBatchItemFailures"] : [],
              enabled,
            }
          : {
              function_name: fn,
              event_source_arn: t!.stream_arn,
              starting_position: position,
              batch_size: Number(batch),
              batching_window_seconds: w,
              maximum_retry_attempts: r,
              bisect_batch_on_function_error: bisect,
              on_failure: onFailure,
              function_response_types: partial ? ["ReportBatchItemFailures"] : [],
              enabled,
            }
      await api.post(MAPPINGS_PATH, body)
      toast.success(`Added trigger ${source} → ${fn}`)
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
            <DialogTitle>Add trigger</DialogTitle>
            <DialogDescription>
              {kind === "sqs" ? (
                <>
                  HomeCloud polls the queue every second and invokes {functionName ? <span className="font-mono">{functionName}</span> : "the function"} with up
                  to batch-size messages. Messages are deleted when the function succeeds.
                </>
              ) : (
                <>
                  HomeCloud reads the table&apos;s stream in order and invokes {functionName ? <span className="font-mono">{functionName}</span> : "the function"}{" "}
                  with batches of change records. A failing batch is retried until it succeeds, expires or runs out of retries.
                </>
              )}
            </DialogDescription>
          </DialogHeader>
          <Field label="Source">
            <OptionGroup label="Source">
              <OptionCard selected={kind === "sqs"} onSelect={() => setKind("sqs")} icon={Inbox} title="SQS queue" description="Batches of queue messages." />
              <OptionCard
                selected={kind === "dynamodb"}
                onSelect={() => setKind("dynamodb")}
                icon={Database}
                title="DynamoDB stream"
                description="Ordered item-level change records."
              />
            </OptionGroup>
          </Field>
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
          {kind === "sqs" ? (
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
                        {x.fifo && <Tag accent="violet">FIFO</Tag>}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              )}
            </Field>
          ) : (
            <>
              <Field
                label="DynamoDB table"
                htmlFor="trg-table"
                error={touched && !table ? "Choose a table" : touched && t && !t.stream_arn ? "Enable the table's stream first" : undefined}
                help={
                  t?.stream_arn ? (
                    <span className="font-mono break-all">{t.stream_arn}</span>
                  ) : t ? (
                    <>
                      This table has no stream.{" "}
                      <Link href={tableHref(t.name)} target="_blank" className="text-primary hover:underline">
                        Enable it
                      </Link>{" "}
                      in the table&apos;s settings.
                    </>
                  ) : tables.data && !tables.data.length ? (
                    <>
                      No tables yet.{" "}
                      <Link href="/dynamodb/" className="text-primary hover:underline">
                        Create a table
                      </Link>{" "}
                      first.
                    </>
                  ) : undefined
                }
              >
                {tables.error ? (
                  <ErrorState error={tables.error} onRetry={() => tables.mutate()} />
                ) : (
                  <Select value={table} onValueChange={setTable} disabled={!tables.data}>
                    <SelectTrigger id="trg-table" className="w-full">
                      <SelectValue placeholder={tables.data ? "Choose a table" : "Loading tables..."} />
                    </SelectTrigger>
                    <SelectContent>
                      {(tables.data ?? []).map((x) => (
                        <SelectItem key={x.name} value={x.name}>
                          {x.name}
                          <span className="text-muted-foreground text-xs">{x.stream_arn ? (x.stream_view_type ?? "stream") : "no stream"}</span>
                        </SelectItem>
                      ))}
                    </SelectContent>
                  </Select>
                )}
              </Field>
              <Field label="Starting position" htmlFor="trg-pos" help="LATEST reads only new records; TRIM_HORIZON starts at the oldest record still in the stream.">
                <Select value={position} onValueChange={setPosition}>
                  <SelectTrigger id="trg-pos" className="w-full sm:w-56">
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value="LATEST">LATEST</SelectItem>
                    <SelectItem value="TRIM_HORIZON">TRIM_HORIZON</SelectItem>
                  </SelectContent>
                </Select>
              </Field>
            </>
          )}
          <div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
            <Field
              label="Batch size"
              htmlFor="trg-batch"
              error={touched || batch ? (bErr ?? undefined) : undefined}
              help={`1-${BATCH_MAX[kind]} ${kind === "sqs" ? "messages" : "records"} per invocation.`}
            >
              <Input id="trg-batch" type="number" min={1} max={BATCH_MAX[kind]} value={batch} onChange={(e) => setBatch(e.target.value)} className="h-8 w-28" />
            </Field>
            <Field label="Batching window (seconds)" htmlFor="trg-window" error={wErr ?? undefined} help="Wait up to this long to fill a batch.">
              <Input id="trg-window" type="number" min={0} max={300} value={windowSec} onChange={(e) => setWindowSec(e.target.value)} className="h-8 w-28" />
            </Field>
          </div>
          {kind === "dynamodb" && (
            <>
              <div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
                <Field label="Retry attempts" htmlFor="trg-retries" error={rErr ?? undefined} help="-1 retries until the records expire.">
                  <Input id="trg-retries" type="number" min={-1} max={10000} value={retries} onChange={(e) => setRetries(e.target.value)} className="h-8 w-28" />
                </Field>
                <label className="flex items-start gap-2 pt-6 text-sm">
                  <Switch checked={bisect} onCheckedChange={setBisect} aria-label="Split batch on error" />
                  <span>
                    Split batch on error
                    <span className="text-muted-foreground block text-xs">Retry each half of a failing batch separately.</span>
                  </span>
                </label>
              </div>
              <Field label="On-failure destination" optional help="Receives details of batches that are discarded after the retries.">
                <DestinationPicker value={onFailure} onChange={setOnFailure} selfArn={fnArn} kinds={["sqs", "sns"]} />
              </Field>
            </>
          )}
          <label className="flex items-start gap-2 text-sm">
            <Switch checked={partial} onCheckedChange={setPartial} aria-label="Report batch item failures" />
            <span>
              Report batch item failures
              <span className="text-muted-foreground block text-xs">
                The function returns {"{ batchItemFailures: [{ itemIdentifier }] }"} to retry only the failed {kind === "sqs" ? "messages" : "records"}.
              </span>
            </span>
          </label>
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
  const err = batchError(batch, BATCH_MAX[mapping ? sourceKind(mapping) : "sqs"])

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
              {mapping ? sourceName(mapping) : ""} → {mapping?.function_name}
            </DialogDescription>
          </DialogHeader>
          <Field
            label="Batch size"
            htmlFor="edit-batch"
            error={err ?? undefined}
            help={mapping ? `1-${BATCH_MAX[sourceKind(mapping)]} ${sourceKind(mapping) === "sqs" ? "messages" : "records"} per invocation.` : undefined}
          >
            <Input id="edit-batch" type="number" min={1} value={batch} onChange={(e) => setBatch(e.target.value)} autoFocus className="w-28" />
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

type RouteRow = { api: HttpApi; route: NonNullable<HttpApi["routes"]>[number] }

const routeColumns: Column<RouteRow>[] = [
  {
    id: "method",
    header: "Method",
    value: (x) => x.route.method,
    cell: (x) => <Tag accent={methodAccent(x.route.method)}>{x.route.method}</Tag>,
  },
  { id: "path", header: "Path", value: (x) => x.route.path, cell: (x) => <CellText mono>{x.route.path}</CellText> },
  { id: "api", header: "API", value: (x) => x.api.name, cell: (x) => <CellLink href={apiHref(x.api.id)}>{x.api.name}</CellLink> },
  {
    id: "endpoint",
    header: "Invoke URL",
    value: (x) => x.api.endpoint + x.route.path,
    cell: (x) => (
      <CellText mono muted max="28rem">
        {x.api.endpoint + x.route.path}
      </CellText>
    ),
    hideBelow: "md",
  },
]

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
      <DataTable
        title="API Gateway routes"
        description="HTTP API routes that invoke this function."
        data={apis.data ? routes : undefined}
        columns={routeColumns}
        rowId={(x) => `${x.api.id}-${x.route.id}`}
        loading={apis.isLoading}
        error={apis.error}
        onRetry={() => apis.mutate()}
        onRefresh={() => apis.mutate()}
        refreshing={apis.isValidating}
        noSearch
        actions={
          <Button variant="outline" size="sm" asChild>
            <Link href="/apigateway/">
              <Network /> API Gateway
            </Link>
          </Button>
        }
        empty={
          <EmptyState
            icon={Network}
            title="No API routes"
            description="No API routes target this function. Add a route in API Gateway to expose it over HTTP."
            action={
              <Button size="sm" asChild>
                <Link href="/apigateway/">
                  <Plus /> Add a route in API Gateway
                </Link>
              </Button>
            }
          />
        }
      />
    </div>
  )
}

export function TriggersList() {
  return (
    <div className="flex flex-col gap-4">
      <PageHeader
        title="Event source mappings"
        description="SQS queues and DynamoDB streams that trigger functions. HomeCloud polls each enabled source and invokes the function with batches of records."
        breadcrumbs={[{ label: "Lambda", href: "/lambda/" }, { label: "Event source mappings" }]}
      />
      <MappingsTable />
    </div>
  )
}
