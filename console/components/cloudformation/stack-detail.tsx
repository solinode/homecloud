"use client"

import { useEffect, useMemo, useState } from "react"
import Link from "next/link"
import { useRouter } from "next/navigation"
import { AlertCircle, ArrowLeft, CheckCircle2, ChevronDown, ChevronRight, ExternalLink, Info, Loader2, Pencil, RefreshCw, Trash2 } from "lucide-react"

import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert"
import { Button } from "@/components/ui/button"
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs"
import { ActionsMenu } from "@/components/console/actions-menu"
import { CodeBlock } from "@/components/console/code-block"
import { CopyButton, CopyableText } from "@/components/console/copy-button"
import { CellLink, CellText, DataTable, type Column } from "@/components/console/data-table"
import { EmptyState } from "@/components/console/empty-state"
import { ErrorState } from "@/components/console/error-state"
import { KeyValueGrid } from "@/components/console/key-value"
import { DetailSkeleton } from "@/components/console/loading"
import { PageHeader } from "@/components/console/page-header"
import { Section } from "@/components/console/section"
import { StatTile } from "@/components/console/stat-tile"
import { Tag } from "@/components/console/tag"
import { TimeAgo } from "@/components/console/time-ago"
import { ApiError, api } from "@/lib/api"
import { formatDate, pluralize } from "@/lib/format"
import { useApi, useQueryParam, useSetQueryParam } from "@/lib/hooks"
import type { CfnParamDef, Stack, StackEvent, StackResource, ValidateTemplateResult } from "@/lib/types"
import { cn } from "@/lib/utils"
import {
  StackStatusBadge,
  TYPE_META,
  VALIDATE_PATH,
  canDelete,
  canUpdate,
  isInProgress,
  pollInterval,
  resourceHref,
  stackPath,
  templateFormat,
  templateOutputDescriptions,
  updateStackHref,
  useDeleteStack,
  valueText,
} from "./common"

const TABS = ["events", "resources", "outputs", "parameters", "template"] as const
type Tab = (typeof TABS)[number]

const STACK_TYPE = "HC::CloudFormation::Stack"

export function StackDetail() {
  const router = useRouter()
  const name = useQueryParam("name")
  const tabParam = useQueryParam("tab")
  const setParam = useSetQueryParam()
  const tab: Tab = (TABS as readonly string[]).includes(tabParam) ? (tabParam as Tab) : "events"

  const { data: st, error, isLoading, isValidating, mutate } = useApi<Stack>(name ? stackPath(name) : null, {
    refreshInterval: (d) => pollInterval([d?.status, ...Object.values(d?.resources ?? {}).map((r) => r.status)]),
  })
  const del = useDeleteStack(() => mutate())
  const defs = useParamDefs(st?.template)

  const crumbs = [{ label: "CloudFormation", href: "/cloudformation/" }, { label: "Stacks", href: "/cloudformation/" }, { label: name || "Stack" }]

  if (!name) {
    return (
      <>
        <PageHeader title="Stack" breadcrumbs={crumbs} />
        <EmptyState title="No stack selected" description="Open a stack from the stacks list." action={<BackButton />} />
      </>
    )
  }
  if (error) {
    const notFound = error instanceof ApiError && error.status === 404
    // A stack disappears once its deletion completes.
    const deleted = notFound && st?.status === "DELETE_IN_PROGRESS"
    if (notFound || !st) {
      return (
        <div className="flex flex-col gap-4">
          <PageHeader title={name} breadcrumbs={crumbs} />
          {notFound ? (
            <Section>
              <EmptyState
                icon={deleted ? CheckCircle2 : AlertCircle}
                title={deleted ? "Stack deleted" : "Stack not found"}
                description={deleted ? `Stack ${name} and its resources were deleted.` : `Stack ${name} does not exist.`}
                action={<BackButton />}
              />
            </Section>
          ) : (
            <ErrorState error={error} onRetry={() => mutate()} />
          )}
        </div>
      )
    }
  }
  if (isLoading || !st) return <DetailSkeleton />

  const status = st.status
  const failed = status.endsWith("_FAILED") || status.includes("ROLLBACK")
  const resources = Object.values(st.resources ?? {})
  const failedResources = resources.filter((r) => r.status.endsWith("_FAILED")).length
  const inProgressResources = resources.filter((r) => isInProgress(r.status)).length

  return (
    <div className="flex flex-col gap-4">
      <PageHeader
        title={st.name}
        badge={<StackStatusBadge status={status} />}
        description={st.description}
        breadcrumbs={crumbs}
        actions={
          <>
            <Button variant="outline" size="sm" onClick={() => mutate()} disabled={isValidating} aria-label="Refresh">
              {isValidating ? <Loader2 className="animate-spin" /> : <RefreshCw />}
              Refresh
            </Button>
            <ActionsMenu
              items={[
                { label: "View events", onSelect: () => setParam("tab", null) },
                { label: "View template", onSelect: () => setParam("tab", "template") },
                { separator: true },
                {
                  label: "Delete stack",
                  destructive: true,
                  icon: <Trash2 />,
                  disabled: !canDelete(status),
                  onSelect: () => del.open(st.name, resources.length),
                },
              ]}
            />
            <Button
              size="sm"
              disabled={!canUpdate(status)}
              title={status === "ROLLBACK_COMPLETE" ? "A stack whose creation was rolled back can only be deleted" : undefined}
              onClick={() => router.push(updateStackHref(st.name))}
            >
              <Pencil /> Update stack
            </Button>
          </>
        }
      />

      {st.status_reason && !isInProgress(status) && (
        <Alert variant={failed ? "destructive" : "default"}>
          {failed ? <AlertCircle /> : <Info />}
          <AlertTitle>Status reason</AlertTitle>
          <AlertDescription>
            <span className="font-mono text-[13px] break-words">{st.status_reason}</span>
            {status === "ROLLBACK_COMPLETE" && (
              <span className="mt-1 block">The creation failed and its resources were deleted. Delete this stack, fix the template and create it again.</span>
            )}
          </AlertDescription>
        </Alert>
      )}
      {isInProgress(status) && (
        <Alert variant="info">
          <Loader2 className="animate-spin" />
          <AlertTitle>{stackStatusSentence(status)}</AlertTitle>
          <AlertDescription>This page refreshes automatically.</AlertDescription>
        </Alert>
      )}

      <div className="grid grid-cols-2 gap-3 lg:grid-cols-4">
        <StatTile
          label="Resources"
          value={resources.length}
          tone={failedResources ? "danger" : inProgressResources ? "warning" : resources.length ? "success" : undefined}
          caption={failedResources ? `${failedResources} failed` : inProgressResources ? `${inProgressResources} in progress` : resources.length ? "No failures" : "None yet"}
        />
        <StatTile label="Events" value={(st.events ?? []).length} caption={<>Last <TimeAgo value={st.updated_at} /></>} />
        <StatTile label="Outputs" value={Object.keys(st.outputs ?? {}).length} caption="Resolved after create/update" />
        <StatTile label="Parameters" value={Object.keys(st.parameters ?? {}).length} caption={templateFormat(st.template) + " template"} />
      </div>

      <Section title="Stack info">
        <KeyValueGrid
          columns={3}
          items={[
            { label: "Stack name", value: st.name },
            { label: "Status", value: <StackStatusBadge status={status} /> },
            { label: "Resources", value: pluralize(resources.length, "resource") },
            { label: "Created", value: <span>{formatDate(st.created_at)} (<TimeAgo value={st.created_at} />)</span> },
            { label: "Last updated", value: <span>{formatDate(st.updated_at)} (<TimeAgo value={st.updated_at} />)</span> },
            { label: "Description", value: st.description },
            { label: "Stack ARN", value: <CopyableText value={st.arn} />, wide: true },
          ]}
        />
      </Section>

      <Tabs value={tab} onValueChange={(v) => setParam("tab", v === "events" ? null : v)}>
        <div className="overflow-x-auto">
          <TabsList>
            <TabsTrigger value="events">Events</TabsTrigger>
            <TabsTrigger value="resources">Resources ({resources.length})</TabsTrigger>
            <TabsTrigger value="outputs">Outputs</TabsTrigger>
            <TabsTrigger value="parameters">Parameters</TabsTrigger>
            <TabsTrigger value="template">Template</TabsTrigger>
          </TabsList>
        </div>
        <TabsContent value="events">
          <EventsTab stack={st} refreshing={isValidating} onRefresh={() => mutate()} />
        </TabsContent>
        <TabsContent value="resources">
          <ResourcesTab stack={st} refreshing={isValidating} onRefresh={() => mutate()} />
        </TabsContent>
        <TabsContent value="outputs">
          <OutputsTab stack={st} />
        </TabsContent>
        <TabsContent value="parameters">
          <ParametersTab stack={st} defs={defs} />
        </TabsContent>
        <TabsContent value="template">
          <TemplateTab stack={st} onUpdate={() => router.push(updateStackHref(st.name))} />
        </TabsContent>
      </Tabs>

      {del.dialog}
    </div>
  )
}

function stackStatusSentence(status: string) {
  switch (status) {
    case "CREATE_IN_PROGRESS":
      return "Creating the stack's resources."
    case "UPDATE_IN_PROGRESS":
      return "Updating the stack's resources."
    case "DELETE_IN_PROGRESS":
      return "Deleting the stack's resources."
    default:
      return "An operation is in progress."
  }
}

/** useParamDefs asks the server to parse the stack's template to learn its parameter declarations (for NoEcho). */
function useParamDefs(template?: string) {
  const [defs, setDefs] = useState<Record<string, CfnParamDef> | null>(null)
  useEffect(() => {
    if (!template) return
    let live = true
    api
      .post<ValidateTemplateResult>(VALIDATE_PATH, { template })
      .then((r) => live && setDefs(r.valid ? (r.parameters ?? {}) : {}))
      .catch(() => live && setDefs({}))
    return () => {
      live = false
    }
  }, [template])
  return defs
}

function BackButton() {
  return (
    <Button variant="outline" size="sm" asChild>
      <Link href="/cloudformation/">
        <ArrowLeft /> Back to stacks
      </Link>
    </Button>
  )
}

function PhysicalId({ type, id }: { type: string; id: string }) {
  if (!id) return <span className="text-muted-foreground">-</span>
  const href = resourceHref(type, id)
  const meta = TYPE_META[type]
  return (
    <span className="inline-flex max-w-full items-center gap-1">
      {href ? (
        <CellLink href={href} mono max="18rem" title={meta?.listOnly ? `${id} (opens the ${meta.service} list)` : id}>
          {id}
        </CellLink>
      ) : (
        <CellText mono max="18rem">
          {id}
        </CellText>
      )}
      <CopyButton value={id} label="Copy physical ID" />
    </span>
  )
}

// ---- events ----

type EventRow = StackEvent & { idx: number }

const eventColumns: Column<EventRow>[] = [
  {
    id: "time",
    header: "Timestamp",
    cell: (e) => (
      <span className="whitespace-nowrap" title={e.time}>
        {formatDate(e.time)}
      </span>
    ),
    value: (e) => e.idx,
    sortable: false,
  },
  {
    id: "logical",
    header: "Logical ID",
    cell: (e) => <CellText className={cn("font-medium", e.type === STACK_TYPE && "text-primary")}>{e.logical_id}</CellText>,
    value: (e) => e.logical_id,
  },
  { id: "type", header: "Type", cell: (e) => <Tag>{e.type}</Tag>, value: (e) => e.type, hideBelow: "md" },
  { id: "status", header: "Status", cell: (e) => <StackStatusBadge status={e.status} />, value: (e) => e.status },
  {
    id: "reason",
    header: "Status reason",
    cell: (e) => (e.reason ? <span className="block max-w-xl text-xs break-words">{e.reason}</span> : <span className="text-muted-foreground">-</span>),
    value: (e) => e.reason,
    hideBelow: "sm",
  },
]

function EventsTab({ stack, refreshing, onRefresh }: { stack: Stack; refreshing: boolean; onRefresh: () => void }) {
  // The API returns events newest first.
  const rows = useMemo(() => (stack.events ?? []).map((e, idx) => ({ ...e, idx })), [stack.events])
  return (
    <DataTable
      title="Events"
      description={isInProgress(stack.status) ? "Newest first. Refreshing every 2 seconds while the operation runs." : "Newest first."}
      data={rows}
      columns={eventColumns}
      rowId={(e) => String(e.idx)}
      onRefresh={onRefresh}
      refreshing={refreshing}
      searchPlaceholder="Filter by logical ID, type, status or reason"
      pageSize={50}
      rowClassName={(e) => (e.type === STACK_TYPE ? "bg-muted/30" : undefined)}
      empty={<EmptyState title="No events" />}
    />
  )
}

// ---- resources ----

function ResourcesTab({ stack, refreshing, onRefresh }: { stack: Stack; refreshing: boolean; onRefresh: () => void }) {
  const [open, setOpen] = useState<string[]>([])
  const rows = useMemo(() => {
    const res = stack.resources ?? {}
    const order = (stack.order ?? []).filter((id) => res[id])
    const rest = Object.keys(res)
      .filter((id) => !order.includes(id))
      .sort()
    return [...order, ...rest].map((id) => res[id])
  }, [stack.resources, stack.order])
  const toggle = (id: string) => setOpen((o) => (o.includes(id) ? o.filter((x) => x !== id) : [...o, id]))

  const columns: Column<StackResource>[] = [
    {
      id: "expand",
      header: <span className="sr-only">Details</span>,
      cell: (r) => (
        <button
          type="button"
          onClick={(e) => {
            e.stopPropagation()
            toggle(r.logical_id)
          }}
          aria-label={open.includes(r.logical_id) ? "Hide details" : "Show details"}
          className="text-muted-foreground hover:text-foreground hover:bg-accent inline-flex size-6 items-center justify-center rounded"
        >
          {open.includes(r.logical_id) ? <ChevronDown className="size-4" /> : <ChevronRight className="size-4" />}
        </button>
      ),
      className: "w-8 pr-0",
    },
    { id: "logical", header: "Logical ID", cell: (r) => <CellText className="font-medium">{r.logical_id}</CellText>, value: (r) => r.logical_id },
    { id: "physical", header: "Physical ID", cell: (r) => <PhysicalId type={r.type} id={r.physical_id} />, value: (r) => r.physical_id },
    { id: "type", header: "Type", cell: (r) => <Tag>{r.type}</Tag>, value: (r) => r.type, hideBelow: "md" },
    { id: "status", header: "Status", cell: (r) => <StackStatusBadge status={r.status} />, value: (r) => r.status },
    {
      id: "reason",
      header: "Status reason",
      cell: (r) => (r.reason ? <span className="block max-w-md text-xs break-words">{r.reason}</span> : <span className="text-muted-foreground">-</span>),
      value: (r) => r.reason,
      hideBelow: "lg",
    },
    { id: "updated", header: "Updated", cell: (r) => <TimeAgo value={r.updated_at} />, value: (r) => r.updated_at, hideBelow: "lg" },
  ]

  return (
    <DataTable
      title="Resources"
      description="In creation order. Expand a resource to see the properties sent to the service and the attributes available to Fn::GetAtt."
      data={rows}
      columns={columns}
      rowId={(r) => r.logical_id}
      onRefresh={onRefresh}
      refreshing={refreshing}
      onRowClick={(r) => toggle(r.logical_id)}
      searchPlaceholder="Filter by logical ID, physical ID, type or status"
      expanded={(r) => (open.includes(r.logical_id) ? <ResourceDetails r={r} /> : null)}
      empty={
        <EmptyState
          title="No resources"
          description={isInProgress(stack.status) ? "Resources appear here as they are created." : "This stack currently has no resources."}
        />
      }
    />
  )
}

function ResourceDetails({ r }: { r: StackResource }) {
  const meta = TYPE_META[r.type]
  const href = resourceHref(r.type, r.physical_id)
  return (
    <div className="flex flex-col gap-3">
      <div className="flex flex-wrap items-center gap-x-4 gap-y-1 text-sm">
        {meta && (
          <span>
            <span className="text-muted-foreground">Ref returns:</span> {meta.ref}
          </span>
        )}
        {href && (
          <Link href={href} className="text-primary inline-flex items-center gap-1 hover:underline">
            Open in {meta?.service ?? "console"} <ExternalLink className="size-3.5" />
          </Link>
        )}
      </div>
      <div className="grid grid-cols-1 gap-3 lg:grid-cols-2">
        <JsonBlock title="Properties (resolved)" value={r.properties} />
        <JsonBlock title="Attributes" value={r.attributes} />
      </div>
    </div>
  )
}

function JsonBlock({ title, value }: { title: string; value: unknown }) {
  const text = value && Object.keys(value as object).length ? JSON.stringify(value, null, 2) : ""
  if (text) return <CodeBlock title={title} code={text} copyLabel={`Copy ${title.toLowerCase()}`} maxHeight="18rem" />
  return (
    <div className="flex min-w-0 flex-col gap-1">
      <span className="hc-eyebrow">{title}</span>
      <p className="text-muted-foreground text-sm">None</p>
    </div>
  )
}

// ---- outputs ----

interface OutputRow {
  key: string
  value: string
  description: string
}

const outputColumns: Column<OutputRow>[] = [
  { id: "key", header: "Key", cell: (o) => <CellText className="font-medium">{o.key}</CellText>, value: (o) => o.key },
  {
    id: "value",
    header: "Value",
    cell: (o) => (o.value ? <CopyableText value={o.value} className="max-w-[28rem]" /> : <span className="text-muted-foreground">-</span>),
    value: (o) => o.value,
  },
  {
    id: "description",
    header: "Description",
    cell: (o) => (o.description ? o.description : <span className="text-muted-foreground">-</span>),
    value: (o) => o.description,
    hideBelow: "md",
  },
]

function OutputsTab({ stack }: { stack: Stack }) {
  const descs = useMemo(() => templateOutputDescriptions(stack.template), [stack.template])
  const rows = useMemo(
    () =>
      Object.entries(stack.outputs ?? {})
        .map(([key, v]) => ({ key, value: valueText(v), description: descs[key] ?? "" }))
        .sort((a, b) => a.key.localeCompare(b.key)),
    [stack.outputs, descs],
  )
  return (
    <DataTable
      title="Outputs"
      description="Values from the template's Outputs section, resolved after the last successful create or update."
      data={rows}
      columns={outputColumns}
      rowId={(o) => o.key}
      noSearch={rows.length < 8}
      empty={
        <EmptyState
          title="No outputs"
          description={
            isInProgress(stack.status)
              ? "Outputs are computed when the operation completes."
              : "The template declares no outputs, or the last operation did not complete."
          }
        />
      }
    />
  )
}

// ---- parameters ----

interface ParamRow {
  key: string
  value: string
  type: string
  description: string
  noEcho: boolean
}

const paramColumns: Column<ParamRow>[] = [
  { id: "key", header: "Key", cell: (p) => <CellText mono className="font-medium">{p.key}</CellText>, value: (p) => p.key },
  {
    id: "value",
    header: "Value",
    cell: (p) =>
      p.noEcho ? (
        <span className="text-muted-foreground font-mono text-[13px]" title="NoEcho parameter">
          ****
        </span>
      ) : p.value ? (
        <CopyableText value={p.value} className="max-w-[28rem]" />
      ) : (
        <span className="text-muted-foreground">-</span>
      ),
    value: (p) => (p.noEcho ? "" : p.value),
  },
  { id: "type", header: "Type", cell: (p) => (p.type ? <Tag>{p.type}</Tag> : <span className="text-muted-foreground">-</span>), value: (p) => p.type, hideBelow: "sm" },
  {
    id: "description",
    header: "Description",
    cell: (p) => (p.description ? p.description : <span className="text-muted-foreground">-</span>),
    value: (p) => p.description,
    hideBelow: "md",
  },
]

function ParametersTab({ stack, defs }: { stack: Stack; defs: Record<string, CfnParamDef> | null }) {
  const rows = useMemo(
    () =>
      Object.entries(stack.parameters ?? {})
        .map(([key, v]) => {
          const d = defs?.[key]
          // Until the declarations load, hide every value rather than flash a NoEcho one.
          return { key, value: valueText(v), type: d?.Type ?? "", description: d?.Description ?? "", noEcho: defs ? !!d?.NoEcho : true }
        })
        .sort((a, b) => a.key.localeCompare(b.key)),
    [stack.parameters, defs],
  )
  return (
    <DataTable
      title="Parameters"
      description="Values used by the last create or update. NoEcho parameters are masked."
      data={rows}
      columns={paramColumns}
      rowId={(p) => p.key}
      noSearch={rows.length < 8}
      empty={<EmptyState title="No parameters" description="The template declares no parameters." />}
    />
  )
}

// ---- template ----

function TemplateTab({ stack, onUpdate }: { stack: Stack; onUpdate: () => void }) {
  return (
    <Section
      title="Template"
      description={`${templateFormat(stack.template)} · ${pluralize(stack.template.split("\n").length, "line")}. The template of the last create or update.`}
      actions={
        <Button size="sm" onClick={onUpdate} disabled={!canUpdate(stack.status)}>
          <Pencil /> Update stack
        </Button>
      }
    >
      <CodeBlock
        title={`template.${templateFormat(stack.template) === "JSON" ? "json" : "yaml"}`}
        code={stack.template}
        copyLabel="Copy template"
        maxHeight="70vh"
      />
    </Section>
  )
}
