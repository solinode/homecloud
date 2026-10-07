"use client"

import { useState } from "react"
import { Filter, Plus, Trash2 } from "lucide-react"
import { toast } from "sonner"

import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select"
import { ConfirmDialog } from "@/components/console/confirm-dialog"
import { CellText, DataTable, type Column } from "@/components/console/data-table"
import { EmptyState } from "@/components/console/empty-state"
import { Field } from "@/components/console/form-field"
import { FormDialog } from "@/components/console/form-dialog"
import { Tag } from "@/components/console/tag"
import { TimeAgo } from "@/components/console/time-ago"
import { api, seg } from "@/lib/api"
import { revalidate, useApi } from "@/lib/hooks"
import type { LambdaFunction, MetricFilter, SubscriptionFilter } from "@/lib/types"

const base = (group: string) => `/api/v1/logs/groups/${seg(group)}`

/** MetricFilters lists and edits a log group's metric filters. */
export function MetricFilters({ group }: { group: string }) {
  const path = `${base(group)}/metric-filters`
  const { data, error, isLoading, isValidating, mutate } = useApi<MetricFilter[]>(path)
  const [creating, setCreating] = useState(false)
  const [deleting, setDeleting] = useState<MetricFilter | null>(null)

  const columns: Column<MetricFilter>[] = [
    { id: "name", header: "Filter name", cell: (f) => <CellText className="font-medium">{f.filterName}</CellText>, value: (f) => f.filterName },
    { id: "pattern", header: "Pattern", cell: (f) => (f.filterPattern ? <CellText mono>{f.filterPattern}</CellText> : <span className="text-muted-foreground">(all events)</span>), value: (f) => f.filterPattern },
    {
      id: "metric",
      header: "Metric",
      cell: (f) => <CellText>{`${f.metricTransformations[0]?.metricNamespace} / ${f.metricTransformations[0]?.metricName}`}</CellText>,
      value: (f) => f.metricTransformations[0]?.metricName,
    },
    { id: "value", header: "Value", cell: (f) => <CellText mono>{f.metricTransformations[0]?.metricValue}</CellText>, hideBelow: "md" },
    { id: "created", header: "Created", cell: (f) => <TimeAgo value={new Date(f.creationTime).toISOString()} />, hideBelow: "lg" },
    {
      id: "del",
      header: "",
      cell: (f) => (
        <Button variant="ghost" size="icon" aria-label={`Delete ${f.filterName}`} onClick={() => setDeleting(f)}>
          <Trash2 />
        </Button>
      ),
    },
  ]

  return (
    <>
      <DataTable
        title="Metric filters"
        description="Matching log events are turned into CloudWatch metrics."
        data={data}
        columns={columns}
        rowId={(f) => f.filterName}
        loading={isLoading}
        error={error}
        onRefresh={() => mutate()}
        refreshing={isValidating}
        selection="none"
        actions={
          <Button size="sm" onClick={() => setCreating(true)}>
            <Plus /> Create metric filter
          </Button>
        }
        empty={
          <EmptyState icon={Filter} title="No metric filters" description="Create a filter to count matching events as a metric."
            action={
              <Button size="sm" onClick={() => setCreating(true)}>
                <Plus /> Create metric filter
              </Button>
            }
          />
        }
      />
      {creating && <MetricFilterDialog group={group} path={path} onClose={() => setCreating(false)} />}
      {deleting && (
        <ConfirmDialog
          open
          onOpenChange={(o) => !o && setDeleting(null)}
          title={`Delete metric filter ${deleting.filterName}?`}
          onConfirm={async () => {
            await api.del(`${path}/${seg(deleting.filterName)}`)
            toast.success("Metric filter deleted")
            await revalidate(path)
          }}
        />
      )}
    </>
  )
}

function MetricFilterDialog({ group, path, onClose }: { group: string; path: string; onClose: () => void }) {
  const [name, setName] = useState("")
  const [pattern, setPattern] = useState("")
  const [metric, setMetric] = useState("")
  const [ns, setNs] = useState("")
  const [value, setValue] = useState("1")
  return (
    <FormDialog
      open
      onOpenChange={(o) => !o && onClose()}
      title={`Create metric filter on ${group}`}
      submitLabel="Create metric filter"
      disabled={!name.trim() || !metric.trim() || !ns.trim() || !value.trim()}
      onSubmit={async () => {
        await api.put(`${path}/${seg(name.trim())}`, {
          filterPattern: pattern,
          metricTransformations: [{ metricName: metric.trim(), metricNamespace: ns.trim(), metricValue: value.trim() }],
        })
        toast.success(`Created ${name.trim()}`)
        await revalidate(path)
      }}
    >
      <Field label="Filter name" htmlFor="mf-name">
        <Input id="mf-name" value={name} onChange={(e) => setName(e.target.value)} autoFocus />
      </Field>
      <Field label="Filter pattern" htmlFor="mf-pattern" help='Leave empty to match every event. Examples: ERROR, "timed out", { $.level = "error" }.'>
        <Input id="mf-pattern" value={pattern} onChange={(e) => setPattern(e.target.value)} className="font-mono" spellCheck={false} />
      </Field>
      <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
        <Field label="Metric namespace" htmlFor="mf-ns" help="Not AWS/ or HC/.">
          <Input id="mf-ns" value={ns} onChange={(e) => setNs(e.target.value)} />
        </Field>
        <Field label="Metric name" htmlFor="mf-metric">
          <Input id="mf-metric" value={metric} onChange={(e) => setMetric(e.target.value)} />
        </Field>
      </div>
      <Field label="Metric value" htmlFor="mf-value" help="A number, or a field such as $.latency or $bytes.">
        <Input id="mf-value" value={value} onChange={(e) => setValue(e.target.value)} className="font-mono" />
      </Field>
    </FormDialog>
  )
}

/** SubscriptionFilters lists and edits a log group's Lambda subscription filters. */
export function SubscriptionFilters({ group }: { group: string }) {
  const path = `${base(group)}/subscription-filters`
  const { data, error, isLoading, isValidating, mutate } = useApi<SubscriptionFilter[]>(path)
  const [creating, setCreating] = useState(false)
  const [deleting, setDeleting] = useState<SubscriptionFilter | null>(null)

  const columns: Column<SubscriptionFilter>[] = [
    { id: "name", header: "Filter name", cell: (f) => <CellText className="font-medium">{f.filterName}</CellText>, value: (f) => f.filterName },
    { id: "pattern", header: "Pattern", cell: (f) => (f.filterPattern ? <CellText mono>{f.filterPattern}</CellText> : <span className="text-muted-foreground">(all events)</span>), value: (f) => f.filterPattern },
    { id: "dest", header: "Destination", cell: (f) => <CellText mono title={f.destinationArn}>{f.destinationArn.split(":function:")[1] ?? f.destinationArn}</CellText>, value: (f) => f.destinationArn },
    { id: "dist", header: "Distribution", cell: (f) => <Tag mono={false}>{f.distribution}</Tag>, hideBelow: "md" },
    {
      id: "del",
      header: "",
      cell: (f) => (
        <Button variant="ghost" size="icon" aria-label={`Delete ${f.filterName}`} onClick={() => setDeleting(f)}>
          <Trash2 />
        </Button>
      ),
    },
  ]

  return (
    <>
      <DataTable
        title="Subscription filters"
        description="Matching events are delivered to a Lambda function (at most 2 per log group)."
        data={data}
        columns={columns}
        rowId={(f) => f.filterName}
        loading={isLoading}
        error={error}
        onRefresh={() => mutate()}
        refreshing={isValidating}
        selection="none"
        actions={
          <Button size="sm" onClick={() => setCreating(true)}>
            <Plus /> Create subscription filter
          </Button>
        }
        empty={
          <EmptyState icon={Filter} title="No subscription filters" description="Create a filter to stream matching events to a Lambda function."
            action={
              <Button size="sm" onClick={() => setCreating(true)}>
                <Plus /> Create subscription filter
              </Button>
            }
          />
        }
      />
      {creating && <SubscriptionDialog group={group} path={path} onClose={() => setCreating(false)} />}
      {deleting && (
        <ConfirmDialog
          open
          onOpenChange={(o) => !o && setDeleting(null)}
          title={`Delete subscription filter ${deleting.filterName}?`}
          onConfirm={async () => {
            await api.del(`${path}/${seg(deleting.filterName)}`)
            toast.success("Subscription filter deleted")
            await revalidate(path)
          }}
        />
      )}
    </>
  )
}

function SubscriptionDialog({ group, path, onClose }: { group: string; path: string; onClose: () => void }) {
  const fns = useApi<LambdaFunction[]>("/api/v1/lambda/functions", { revalidateOnFocus: false })
  const [name, setName] = useState("")
  const [pattern, setPattern] = useState("")
  const [fn, setFn] = useState("")
  return (
    <FormDialog
      open
      onOpenChange={(o) => !o && onClose()}
      title={`Create subscription filter on ${group}`}
      submitLabel="Create subscription filter"
      disabled={!name.trim() || !fn}
      onSubmit={async () => {
        await api.put(`${path}/${seg(name.trim())}`, { filterPattern: pattern, destinationArn: fn })
        toast.success(`Created ${name.trim()}`)
        await revalidate(path)
      }}
    >
      <Field label="Filter name" htmlFor="sf-name">
        <Input id="sf-name" value={name} onChange={(e) => setName(e.target.value)} autoFocus />
      </Field>
      <Field label="Filter pattern" htmlFor="sf-pattern" help="Leave empty to deliver every event.">
        <Input id="sf-pattern" value={pattern} onChange={(e) => setPattern(e.target.value)} className="font-mono" spellCheck={false} />
      </Field>
      <Field label="Destination Lambda function" htmlFor="sf-fn">
        <Select value={fn} onValueChange={setFn}>
          <SelectTrigger id="sf-fn" className="w-full">
            <SelectValue placeholder={fns.data ? "Select a function" : "Loading functions..."} />
          </SelectTrigger>
          <SelectContent>
            {(fns.data ?? []).map((f) => (
              <SelectItem key={f.arn} value={f.arn}>
                {f.name}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
      </Field>
    </FormDialog>
  )
}
