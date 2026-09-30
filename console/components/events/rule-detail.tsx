"use client"

import Link from "next/link"
import { useRouter } from "next/navigation"
import { AlertCircle, ArrowLeft, Loader2, Pencil, Play, Power, PowerOff, Trash2 } from "lucide-react"

import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert"
import { Button } from "@/components/ui/button"
import { CopyableText } from "@/components/console/copy-button"
import { EmptyState } from "@/components/console/empty-state"
import { ErrorState } from "@/components/console/error-state"
import { KeyValueGrid } from "@/components/console/key-value"
import { DetailSkeleton } from "@/components/console/loading"
import { PageHeader } from "@/components/console/page-header"
import { Section } from "@/components/console/section"
import { StatusBadge } from "@/components/console/status-badge"
import { TimeAgo } from "@/components/console/time-ago"
import { ApiError, seg } from "@/lib/api"
import { formatDate, formatNumber } from "@/lib/format"
import { useApi, useQueryParam } from "@/lib/hooks"
import type { EventRule, RuleTarget } from "@/lib/types"
import { RULES_PATH, busQuery, describeSchedule, editRuleHref, hasNextRun, isSchedule, targetHref, targetKindLabel, targetName, useRuleActions } from "./common"

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
              {isValidating && <Loader2 className="animate-spin" />}
              Refresh
            </Button>
            {enabled ? (
              <Button variant="outline" size="sm" disabled={actions.busy} onClick={() => actions.disable([rule.name])}>
                <PowerOff /> Disable
              </Button>
            ) : (
              <Button variant="outline" size="sm" disabled={actions.busy} onClick={() => actions.enable([rule.name])}>
                <Power /> Enable
              </Button>
            )}
            <Button variant="outline" size="sm" disabled={actions.busy} onClick={() => actions.run([rule.name])} title="Invoke the targets now with a Scheduled Event">
              {actions.busy ? <Loader2 className="animate-spin" /> : <Play />}
              Run now
            </Button>
            <Button variant="outline" size="sm" asChild>
              <Link href={editRuleHref(rule.name, bus)}>
                <Pencil /> Edit
              </Link>
            </Button>
            <Button variant="outline" size="sm" onClick={() => actions.remove([rule])}>
              <Trash2 /> Delete
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

      <Section title="Rule details">
        <KeyValueGrid
          columns={3}
          items={[
            { label: "Rule name", value: rule.name },
            { label: "Status", value: <StatusBadge status={rule.state} /> },
            { label: "Type", value: sched ? "Schedule" : "Event pattern" },
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
              value: <span className={rule.failed_invocations ? "text-destructive font-medium" : undefined}>{formatNumber(rule.failed_invocations)}</span>,
            },
            { label: "Rule ARN", value: <CopyableText value={rule.arn} />, wide: true },
          ]}
        />
      </Section>

      {!sched && (
        <Section title="Event pattern" actions={<Link href="/events/send/" className="text-primary text-sm hover:underline">Send a test event</Link>}>
          <pre className="bg-muted/50 max-h-96 overflow-auto rounded-md border p-3 font-mono text-[12.5px]">{JSON.stringify(rule.event_pattern ?? {}, null, 2)}</pre>
        </Section>
      )}

      <Section title={`Targets (${targets.length})`} flush>
        {targets.length ? (
          <div className="overflow-x-auto">
            <table className="w-full text-sm">
              <thead>
                <tr className="bg-muted/40 border-b">
                  <th className="text-muted-foreground px-4 py-2 text-left text-xs font-semibold">Type</th>
                  <th className="text-muted-foreground px-3 py-2 text-left text-xs font-semibold">Name</th>
                  <th className="text-muted-foreground hidden px-3 py-2 text-left text-xs font-semibold md:table-cell">ARN</th>
                  <th className="text-muted-foreground px-3 py-2 text-left text-xs font-semibold">Input</th>
                  <th className="text-muted-foreground px-4 py-2 text-left text-xs font-semibold">Retry / DLQ</th>
                </tr>
              </thead>
              <tbody>
                {targets.map((t) => {
                  const href = targetHref(t.arn)
                  return (
                    <tr key={t.id || t.arn} className="border-b align-top last:border-0">
                      <td className="px-4 py-2 whitespace-nowrap">{targetKindLabel(t.arn)}</td>
                      <td className="px-3 py-2">
                        {href ? (
                          <Link href={href} className="text-primary font-medium hover:underline">
                            {targetName(t.arn)}
                          </Link>
                        ) : (
                          targetName(t.arn)
                        )}
                      </td>
                      <td className="hidden max-w-md px-3 py-2 md:table-cell">
                        <CopyableText value={t.arn} />
                      </td>
                      <td className="px-3 py-2">
                        <TargetInput t={t} />
                      </td>
                      <td className="px-4 py-2 text-xs whitespace-nowrap">
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
                      </td>
                    </tr>
                  )
                })}
              </tbody>
            </table>
          </div>
        ) : (
          <EmptyState
            title="No targets"
            description="The rule fires but delivers nothing. Edit the rule to add a Lambda function, SQS queue, SNS topic or state machine."
            action={
              <Button size="sm" variant="outline" asChild>
                <Link href={editRuleHref(rule.name, bus)}>
                  <Pencil /> Edit rule
                </Link>
              </Button>
            }
          />
        )}
      </Section>

      {actions.dialogs}
    </div>
  )
}

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
      <code className="bg-muted block max-w-xs truncate rounded px-1.5 py-0.5 font-mono text-[12.5px]" title={v}>
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
