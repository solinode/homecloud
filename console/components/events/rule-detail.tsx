"use client"

import Link from "next/link"
import { useRouter } from "next/navigation"
import { AlertCircle, ArrowLeft, Loader2, Pencil, Play, Power, PowerOff, RefreshCw, Send, Target, Trash2 } from "lucide-react"

import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert"
import { Button } from "@/components/ui/button"
import { ActionsMenu } from "@/components/console/actions-menu"
import { CodeBlock } from "@/components/console/code-block"
import { CopyableText } from "@/components/console/copy-button"
import { CellLink, CellText, DataTable, type Column } from "@/components/console/data-table"
import { EmptyState } from "@/components/console/empty-state"
import { ErrorState } from "@/components/console/error-state"
import { KeyValueGrid } from "@/components/console/key-value"
import { DetailSkeleton } from "@/components/console/loading"
import { PageHeader } from "@/components/console/page-header"
import { Section } from "@/components/console/section"
import { StatTile } from "@/components/console/stat-tile"
import { StatusBadge } from "@/components/console/status-badge"
import { TimeAgo } from "@/components/console/time-ago"
import { ApiError, seg } from "@/lib/api"
import { formatDate, formatNumber } from "@/lib/format"
import { useApi, useQueryParam } from "@/lib/hooks"
import type { EventRule, RuleTarget } from "@/lib/types"
import {
  RULES_PATH,
  RuleTypeTag,
  TargetKindTag,
  busQuery,
  describeSchedule,
  editRuleHref,
  hasNextRun,
  isSchedule,
  targetHref,
  targetKindLabel,
  targetName,
  useRuleActions,
} from "./common"

export function RuleDetail() {
  const router = useRouter()
  const name = useQueryParam("name")
  const bus = useQueryParam("bus") || "default"
  const { data: rule, error, isLoading, isValidating, mutate } = useApi<EventRule>(name ? `${RULES_PATH}/${seg(name)}` : null, {
    query: busQuery(bus),
    refreshInterval: 15_000,
  })
  const actions = useRuleActions({ onDeleted: () => router.push("/events/") })

  const crumbs = [{ label: "EventBridge", href: "/events/" }, { label: "Rules", href: "/events/" }, { label: name || "Rule" }]

  if (!name) {
    return (
      <>
        <PageHeader title="Rule" breadcrumbs={crumbs} />
        <EmptyState title="No rule selected" description="Open a rule from the rules list." action={<BackButton />} />
      </>
    )
  }
  if (error && !rule) {
    const notFound = error instanceof ApiError && error.status === 404
    return (
      <div className="flex flex-col gap-4">
        <PageHeader title={name} breadcrumbs={crumbs} />
        {notFound ? (
          <Section>
            <EmptyState icon={AlertCircle} title="Rule not found" description={`Rule ${name} does not exist. It may have been deleted.`} action={<BackButton />} />
          </Section>
        ) : (
          <ErrorState error={error} onRetry={() => mutate()} />
        )}
      </div>
    )
  }
  if (isLoading || !rule) return <DetailSkeleton />

  const sched = isSchedule(rule)
  const enabled = rule.state === "ENABLED"
  const targets = rule.targets ?? []

  return (
    <div className="flex flex-col gap-4">
      <PageHeader
        title={rule.name}
        badge={<StatusBadge status={rule.state} />}
        description={rule.description || (sched ? `${describeSchedule(rule.schedule_expression!)} · ${rule.schedule_expression}` : "Runs when a published event matches the pattern.")}
        breadcrumbs={crumbs}
        actions={
          <>
            <Button variant="outline" size="sm" onClick={() => mutate()} disabled={isValidating}>
              {isValidating ? <Loader2 className="animate-spin" /> : <RefreshCw />}
              Refresh
            </Button>
            <Button variant="outline" size="sm" disabled={actions.busy} onClick={() => actions.run([rule.name])} title="Invoke the targets now with a Scheduled Event">
              {actions.busy ? <Loader2 className="animate-spin" /> : <Play />}
              Run now
            </Button>
            <ActionsMenu
              disabled={actions.busy}
              items={[
                enabled
                  ? { label: "Disable rule", icon: <PowerOff />, onSelect: () => actions.disable([rule.name]) }
                  : { label: "Enable rule", icon: <Power />, onSelect: () => actions.enable([rule.name]) },
                { label: "Run now", icon: <Play />, onSelect: () => actions.run([rule.name]), hint: "Invoke the targets now with a Scheduled Event" },
                { separator: true },
                { label: "Delete rule", icon: <Trash2 />, destructive: true, onSelect: () => actions.remove([rule]) },
              ]}
            />
            <Button size="sm" asChild>
              <Link href={editRuleHref(rule.name, bus)}>
                <Pencil /> Edit rule
              </Link>
            </Button>
          </>
        }
      />

      {rule.last_error && (
        <Alert variant="destructive">
          <AlertCircle />
          <AlertTitle>Last delivery error</AlertTitle>
          <AlertDescription>
            <span className="font-mono text-[13px] break-all">{rule.last_error}</span>
          </AlertDescription>
        </Alert>
      )}

      <div className="grid grid-cols-2 gap-3 lg:grid-cols-4">
        <StatTile label="Invocations" value={formatNumber(rule.invocations)} caption="Target deliveries" />
        <StatTile
          label="Failed"
          value={formatNumber(rule.failed_invocations)}
          tone={rule.failed_invocations ? "danger" : undefined}
          caption={rule.failed_invocations ? "See the last error above" : "No failures"}
        />
        <StatTile label="Targets" value={targets.length} caption={targets.length ? "Receive matched events" : "Nothing is delivered"} />
        {sched ? (
          <StatTile
            label="Next run"
            value={<span className="text-xl tracking-[-0.03em]">{enabled && hasNextRun(rule) ? <TimeAgo value={rule.next_run} /> : "-"}</span>}
            caption={!enabled ? "Rule disabled" : hasNextRun(rule) ? formatDate(rule.next_run) : "None within a year"}
          />
        ) : (
          <StatTile
            label="Last triggered"
            value={<span className="text-xl tracking-[-0.03em]">{rule.last_triggered ? <TimeAgo value={rule.last_triggered} /> : "Never"}</span>}
            caption={rule.last_triggered ? formatDate(rule.last_triggered) : "No matching events yet"}
          />
        )}
      </div>

      <Section title="Rule details">
        <KeyValueGrid
          columns={3}
          items={[
            { label: "Rule name", value: rule.name },
            { label: "Status", value: <StatusBadge status={rule.state} /> },
            { label: "Type", value: <RuleTypeTag rule={rule} /> },
            { label: "Event bus", value: rule.event_bus },
            { label: "Description", value: rule.description },
            { label: "Created", value: <span>{formatDate(rule.created_at)} (<TimeAgo value={rule.created_at} />)</span> },
            ...(sched
              ? [
                  {
                    label: "Schedule expression",
                    value: (
                      <span>
                        <span className="font-mono text-[13px]">{rule.schedule_expression}</span>{" "}
                        <span className="text-muted-foreground">({describeSchedule(rule.schedule_expression!)})</span>
                      </span>
                    ),
                  },
                  {
                    label: "Next run",
                    value: hasNextRun(rule) ? (
                      <span>
                        {formatDate(rule.next_run)} (<TimeAgo value={rule.next_run} />){!enabled && <span className="text-muted-foreground"> - rule disabled</span>}
                      </span>
                    ) : (
                      "No run within the next year"
                    ),
                  },
                ]
              : []),
            { label: "Last triggered", value: rule.last_triggered ? <span>{formatDate(rule.last_triggered)} (<TimeAgo value={rule.last_triggered} />)</span> : "Never" },
            { label: "Invocations", value: formatNumber(rule.invocations) },
            {
              label: "Failed invocations",
              value: <span className={rule.failed_invocations ? "text-danger font-medium" : undefined}>{formatNumber(rule.failed_invocations)}</span>,
            },
            { label: "Rule ARN", value: <CopyableText value={rule.arn} />, wide: true },
          ]}
        />
      </Section>

      {!sched && (
        <Section
          title="Event pattern"
          description="Events published to the bus that match this pattern are delivered to the targets."
          actions={
            <Button variant="outline" size="sm" asChild>
              <Link href="/events/send/">
                <Send /> Send a test event
              </Link>
            </Button>
          }
        >
          <CodeBlock code={JSON.stringify(rule.event_pattern ?? {}, null, 2)} title="event-pattern.json" maxHeight="24rem" />
        </Section>
      )}

      <DataTable
        title="Targets"
        data={targets}
        columns={targetColumns}
        rowId={(t) => t.id || t.arn}
        noSearch
        empty={
          <EmptyState
            icon={Target}
            title="No targets"
            description="The rule fires but delivers nothing. Edit the rule to add a Lambda function, SQS queue, SNS topic or state machine."
            action={
              <Button size="sm" asChild>
                <Link href={editRuleHref(rule.name, bus)}>
                  <Pencil /> Edit rule
                </Link>
              </Button>
            }
          />
        }
      />

      {actions.dialogs}
    </div>
  )
}

const targetColumns: Column<RuleTarget>[] = [
  { id: "type", header: "Type", cell: (t) => <TargetKindTag arn={t.arn} />, value: (t) => targetKindLabel(t.arn) },
  {
    id: "name",
    header: "Name",
    cell: (t) => {
      const href = targetHref(t.arn)
      return href ? <CellLink href={href}>{targetName(t.arn)}</CellLink> : <CellText>{targetName(t.arn)}</CellText>
    },
    value: (t) => targetName(t.arn),
  },
  { id: "arn", header: "ARN", cell: (t) => <CopyableText value={t.arn} className="max-w-md" />, hideBelow: "md" },
  { id: "input", header: "Input", cell: (t) => <TargetInput t={t} /> },
  {
    id: "retry",
    header: "Retry / DLQ",
    cell: (t) => (
      <div className="text-xs whitespace-nowrap">
        <div>
          {t.retry_policy?.maximum_retry_attempts ?? 185} retries, {t.retry_policy?.maximum_event_age_in_seconds ?? 86400}s max age
        </div>
        {t.dead_letter_arn ? (
          <Link href={targetHref(t.dead_letter_arn) ?? "#"} className="text-primary hover:underline">
            DLQ: {targetName(t.dead_letter_arn)}
          </Link>
        ) : (
          <span className="text-muted-foreground">No DLQ</span>
        )}
      </div>
    ),
  },
]

function BackButton() {
  return (
    <Button variant="outline" size="sm" asChild>
      <Link href="/events/">
        <ArrowLeft /> Back to rules
      </Link>
    </Button>
  )
}

function TargetInput({ t }: { t: RuleTarget }) {
  const code = (label: string, v: string) => (
    <div className="flex flex-col gap-0.5">
      <span className="text-muted-foreground text-xs">{label}</span>
      <code className="bg-muted block max-w-xs truncate rounded border px-1.5 py-0.5 font-mono text-[12.5px]" title={v}>
        {v}
      </code>
    </div>
  )
  if (t.input_transformer)
    return (
      <div className="flex flex-col gap-1">
        {t.input_transformer.input_paths_map && code("Input transformer paths", JSON.stringify(t.input_transformer.input_paths_map))}
        {code("Template", t.input_transformer.input_template)}
      </div>
    )
  if (t.input_path) return code("Part of the event", t.input_path)
  if (t.input) return code("Constant", t.input)
  return <span className="text-muted-foreground">Matched event</span>
}
