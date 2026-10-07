"use client"

import { useEffect, useState, type ReactNode } from "react"
import Link from "next/link"
import { Clock, Loader2, Plus, Rss } from "lucide-react"
import { toast } from "sonner"

import { Alert, AlertDescription } from "@/components/ui/alert"
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
import { Field } from "@/components/console/form-field"
import { JsonEditor, jsonError } from "@/components/console/json-editor"
import { OptionCard, OptionGroup } from "@/components/console/option-card"
import { StatusBadge } from "@/components/console/status-badge"
import { TimeAgo } from "@/components/console/time-ago"
import { QueueTypeBadge, queueHref } from "@/components/sqs/common"
import { api, errorMessage } from "@/lib/api"
import { formatNumber } from "@/lib/format"
import { revalidate, useApi } from "@/lib/hooks"
import type { FilterPolicyScope, LambdaFunction, Queue, Subscription, SubscriptionProtocol } from "@/lib/types"

import {
  CONFIRM_PROTOCOLS,
  EXAMPLE_POLICY,
  PROTOCOL_LABEL,
  ProtocolBadge,
  RAW_PROTOCOLS,
  SNS_PATH,
  SubscriptionEndpoint,
  SubscriptionStatusBadge,
  compactPolicy,
  filterPolicyError,
  isPending,
  lastSegment,
  subscriptionDlq,
  subscriptionPath,
  topicHref,
  topicPath,
  useTopics,
} from "./common"

const SCOPE_LABEL: Record<FilterPolicyScope, string> = { MessageAttributes: "Message attributes", MessageBody: "Message body" }

/** requestConfirmation re-sends the SubscriptionConfirmation of a pending subscription (Subscribe again with the same endpoint). */
async function requestConfirmation(s: Subscription) {
  try {
    await api.post(`${topicPath(s.topic_name)}/subscriptions`, { protocol: s.protocol, endpoint: s.endpoint })
    toast.success("Confirmation requested", { description: `A new SubscriptionConfirmation message is being sent to ${s.endpoint}.` })
    await revalidate(SNS_PATH)
  } catch (e) {
    toast.error(errorMessage(e))
  }
}

export function SubscriptionsTable({
  data,
  loading,
  error,
  onRefresh,
  refreshing,
  topic,
  title = "Subscriptions",
  description,
  filters,
}: {
  data: Subscription[] | undefined
  loading?: boolean
  error?: unknown
  onRefresh: () => void
  refreshing?: boolean
  /** Fixed topic (topic detail page); when unset, a Topic column is shown and the create dialog asks for one. */
  topic?: string
  title?: string
  description?: ReactNode
  filters?: ReactNode
}) {
  const [selected, setSelected] = useState<string[]>([])
  const [creating, setCreating] = useState(false)
  const [editing, setEditing] = useState<Subscription | null>(null)
  const [deleting, setDeleting] = useState<Subscription | null>(null)
  const sel = data?.find((s) => s.arn === selected[0]) ?? null

  const columns: Column<Subscription>[] = [
    {
      id: "id",
      header: "ID",
      cell: (s) => (
        <CellText mono title={s.arn}>
          {`${s.arn.slice(s.arn.lastIndexOf(":") + 1, s.arn.lastIndexOf(":") + 9)}…`}
        </CellText>
      ),
      value: (s) => s.arn,
      hideBelow: "lg",
    },
    ...(!topic
      ? [
          {
            id: "topic",
            header: "Topic",
            cell: (s: Subscription) => <CellLink href={topicHref(s.topic_name)}>{s.topic_name}</CellLink>,
            value: (s: Subscription) => s.topic_name,
          },
        ]
      : []),
    { id: "protocol", header: "Protocol", cell: (s) => <ProtocolBadge protocol={s.protocol} />, value: (s) => s.protocol },
    { id: "endpoint", header: "Endpoint", cell: (s) => <SubscriptionEndpoint sub={s} className="block max-w-[22rem] truncate whitespace-nowrap" />, value: (s) => s.endpoint },
    { id: "status", header: "Status", cell: (s) => <SubscriptionStatusBadge status={s.status} />, value: (s) => s.status, hideBelow: "md" },
    {
      id: "raw",
      header: "Raw delivery",
      cell: (s) => <StatusBadge status={s.raw_message_delivery ? "enabled" : "disabled"} />,
      value: (s) => (s.raw_message_delivery ? "raw" : ""),
      hideBelow: "lg",
    },
    {
      id: "filter",
      header: "Filter policy",
      cell: (s) =>
        compactPolicy(s.filter_policy) ? (
          <span className="flex min-w-0 flex-col gap-0.5">
            <code className="bg-muted block max-w-[16rem] truncate rounded px-1 py-0.5 font-mono text-xs" title={JSON.stringify(s.filter_policy, null, 2)}>
              {compactPolicy(s.filter_policy)}
            </code>
            {s.filter_policy_scope === "MessageBody" && <span className="text-muted-foreground text-[11px]">on message body</span>}
          </span>
        ) : (
          <span className="text-muted-foreground">-</span>
        ),
      value: (s) => compactPolicy(s.filter_policy),
      sortable: false,
      hideBelow: "lg",
    },
    {
      id: "delivered",
      header: "Delivered",
      cell: (s) => <span className="tabular-nums">{formatNumber(s.delivered)}</span>,
      value: (s) => s.delivered,
      hideBelow: "sm",
    },
    {
      id: "failed",
      header: "Failed",
      cell: (s) => <span className={s.failed ? "text-danger tabular-nums" : "text-muted-foreground tabular-nums"}>{formatNumber(s.failed)}</span>,
      value: (s) => s.failed,
      hideBelow: "sm",
    },
    { id: "last", header: "Last delivery", cell: (s) => <TimeAgo value={s.last_delivery} />, value: (s) => s.last_delivery ?? "", hideBelow: "md" },
  ]

  const canConfirm = !!sel && isPending(sel) && CONFIRM_PROTOCOLS.includes(sel.protocol)
  const items: ActionItem[] = [
    { label: "Edit", onSelect: () => sel && setEditing(sel), disabled: !sel },
    {
      label: "Request confirmation",
      onSelect: () => sel && requestConfirmation(sel),
      disabled: !canConfirm,
      hint: "Only for subscriptions pending confirmation",
    },
    { separator: true },
    { label: "Delete", onSelect: () => sel && setDeleting(sel), disabled: !sel, destructive: true },
  ]

  return (
    <>
      <DataTable
        title={title}
        description={description}
        data={data}
        columns={columns}
        rowId={(s) => s.arn}
        loading={loading}
        error={error}
        onRefresh={onRefresh}
        refreshing={refreshing}
        selection="single"
        selected={selected}
        onSelectedChange={setSelected}
        searchPlaceholder="Filter by topic, protocol or endpoint"
        filters={filters}
        expanded={(s) => <SubscriptionNotes sub={s} />}
        actions={
          <>
            <ActionsMenu items={items} disabled={!sel} />
            <Button size="sm" onClick={() => setCreating(true)}>
              <Plus /> Create subscription
            </Button>
          </>
        }
        empty={
          <EmptyState
            icon={Rss}
            title="No subscriptions"
            description="Subscribe an SQS queue, a Lambda function, an HTTP(S) endpoint or an email address to receive every message published to the topic."
            action={
              <Button size="sm" onClick={() => setCreating(true)}>
                <Plus /> Create subscription
              </Button>
            }
          />
        }
      />
      <CreateSubscriptionDialog open={creating} onOpenChange={setCreating} topic={topic} />
      <EditSubscriptionDialog sub={editing} onClose={() => setEditing(null)} />
      <ConfirmDialog
        open={!!deleting}
        onOpenChange={(o) => !o && setDeleting(null)}
        title="Delete subscription?"
        description={
          deleting && (
            <>
              <span className="font-medium">{PROTOCOL_LABEL[deleting.protocol] ?? deleting.protocol}</span> endpoint{" "}
              <span className="font-mono text-[13px] break-all">{deleting.endpoint}</span> will stop receiving messages from {deleting.topic_name}.
            </>
          )
        }
        onConfirm={async () => {
          if (!deleting) return
          await api.del(subscriptionPath(deleting.arn))
          await revalidate(SNS_PATH)
          toast.success("Subscription deleted")
        }}
      />
    </>
  )
}

/** SubscriptionNotes is the expanded row: pending confirmation, DLQ and the last delivery error. */
function SubscriptionNotes({ sub: s }: { sub: Subscription }) {
  const dlq = subscriptionDlq(s)
  const pending = isPending(s) && CONFIRM_PROTOCOLS.includes(s.protocol)
  if (!pending && !dlq && !s.last_error) return null
  return (
    <div className="flex flex-col gap-1.5 text-xs">
      {pending && (
        <Alert variant="warning" className="py-2 text-xs">
          <Clock />
          <AlertDescription className="flex flex-wrap items-center gap-x-2 gap-y-1 text-xs">
            <span>
              Waiting for the endpoint to confirm.{" "}
              {s.protocol.startsWith("http")
                ? "HomeCloud POSTed a SubscriptionConfirmation message; the endpoint must visit its SubscribeURL."
                : "The confirmation link is written to the HomeCloud server log."}{" "}
              Messages are not delivered until then.
            </span>
            <Button
              variant="outline"
              size="sm"
              className="h-6 px-2 text-xs"
              onClick={(e) => {
                e.stopPropagation()
                requestConfirmation(s)
              }}
            >
              Request confirmation
            </Button>
          </AlertDescription>
        </Alert>
      )}
      {dlq && (
        <p className="text-muted-foreground">
          <span className="font-medium">Dead-letter queue: </span>
          <Link href={queueHref(lastSegment(dlq))} onClick={(e) => e.stopPropagation()} className="text-primary hover:underline" title={dlq}>
            {lastSegment(dlq)}
          </Link>
        </p>
      )}
      {s.last_error && (
        <p className="text-danger break-words">
          <span className="font-medium">Last error: </span>
          {s.last_error}
        </p>
      )}
    </div>
  )
}

const URL_RE = (p: string) => new RegExp(`^${p}://[^\\s/$.?#][^\\s]*$`, "i")
const EMAIL_RE = /^[^\s@]+@[^\s@]+$/
const PHONE_RE = /^\+?[0-9]{5,15}$/

function endpointError(protocol: SubscriptionProtocol, endpoint: string): string | null {
  const e = endpoint.trim()
  if (!e)
    return {
      sqs: "Choose a queue",
      lambda: "Choose a function",
      http: "Enter the endpoint URL",
      https: "Enter the endpoint URL",
      email: "Enter an email address",
      "email-json": "Enter an email address",
      sms: "Enter a phone number",
    }[protocol]
  if ((protocol === "http" || protocol === "https") && !URL_RE(protocol).test(e)) return `Enter a URL that starts with ${protocol}://`
  if ((protocol === "email" || protocol === "email-json") && !EMAIL_RE.test(e)) return "Enter a valid email address"
  if (protocol === "sms" && !PHONE_RE.test(e)) return "Enter a phone number in E.164 format, e.g. +14155550100"
  return null
}

function policyErrorFor(on: boolean, text: string, scope: FilterPolicyScope) {
  if (!on) return null
  return jsonError(text) ?? filterPolicyError(JSON.parse(text), scope)
}

export function CreateSubscriptionDialog({ open, onOpenChange, topic }: { open: boolean; onOpenChange: (o: boolean) => void; topic?: string }) {
  const topics = useTopics(0)
  const queues = useApi<Queue[]>(open ? "/api/v1/sqs/queues" : null, { revalidateOnFocus: false })
  const functions = useApi<LambdaFunction[]>(open ? "/api/v1/lambda/functions" : null, { revalidateOnFocus: false })
  const [topicName, setTopicName] = useState("")
  const [protocol, setProtocol] = useState<SubscriptionProtocol>("sqs")
  const [endpoint, setEndpoint] = useState("")
  const [raw, setRaw] = useState(false)
  const [filterOn, setFilterOn] = useState(false)
  const [scope, setScope] = useState<FilterPolicyScope>("MessageAttributes")
  const [policy, setPolicy] = useState(EXAMPLE_POLICY.MessageAttributes)
  const [dlqOn, setDlqOn] = useState(false)
  const [dlq, setDlq] = useState("")
  const [submitted, setSubmitted] = useState(false)
  const [pending, setPending] = useState(false)

  useEffect(() => {
    if (open) {
      setTopicName(topic ?? "")
      setProtocol("sqs")
      setEndpoint("")
      setRaw(false)
      setFilterOn(false)
      setScope("MessageAttributes")
      setPolicy(EXAMPLE_POLICY.MessageAttributes)
      setDlqOn(false)
      setDlq("")
      setSubmitted(false)
    }
  }, [open, topic])

  const t = topic ?? topicName
  const fifoTopic = !!topics.data?.find((x) => x.name === t)?.fifo || t.endsWith(".fifo")
  const protocols = (Object.keys(PROTOCOL_LABEL) as SubscriptionProtocol[]).filter((p) => !fifoTopic || p === "sqs")
  const rawSupported = RAW_PROTOCOLS.includes(protocol)
  const queueList = [...(queues.data ?? [])].sort((a, b) => (fifoTopic ? Number(b.fifo) - Number(a.fifo) : 0) || a.name.localeCompare(b.name))

  useEffect(() => {
    if (fifoTopic && protocol !== "sqs") {
      setProtocol("sqs")
      setEndpoint("")
    }
  }, [fifoTopic, protocol])

  const errors: Record<string, string | null> = {
    topic: t ? null : "Choose a topic",
    endpoint: endpointError(protocol, endpoint),
    policy: policyErrorFor(filterOn, policy, scope),
    dlq: dlqOn && !dlq ? "Choose a dead-letter queue" : null,
  }
  const err = (k: string) => (submitted ? errors[k] : null)
  const valid = Object.values(errors).every((v) => !v)

  const submit = async (e: React.FormEvent) => {
    e.preventDefault()
    setSubmitted(true)
    if (!valid) return
    const dlqArn = dlqOn ? queues.data?.find((q) => q.name === dlq)?.arn : undefined
    setPending(true)
    try {
      const sub = await api.post<Subscription>(`${topicPath(t)}/subscriptions`, {
        protocol,
        endpoint: endpoint.trim(),
        raw_message_delivery: rawSupported && raw,
        filter_policy: filterOn ? JSON.parse(policy) : undefined,
        filter_policy_scope: filterOn ? scope : undefined,
        redrive_policy: dlqArn ? JSON.stringify({ deadLetterTargetArn: dlqArn }) : undefined,
      })
      if (isPending(sub))
        toast.success(`Subscribed ${endpoint.trim()} to ${t}`, {
          description: "The subscription is pending confirmation: the endpoint must confirm it before messages are delivered.",
        })
      else toast.success(`Subscribed ${endpoint.trim()} to ${t}`)
      await revalidate(SNS_PATH)
      onOpenChange(false)
    } catch (err) {
      toast.error(errorMessage(err))
    } finally {
      setPending(false)
    }
  }

  return (
    <Dialog open={open} onOpenChange={(o) => !pending && onOpenChange(o)}>
      <DialogContent className="max-h-[90vh] overflow-y-auto sm:max-w-xl">
        <form onSubmit={submit} className="flex min-w-0 flex-col gap-4">
          <DialogHeader>
            <DialogTitle>Create subscription</DialogTitle>
            <DialogDescription>
              {topic ? (
                <>
                  Deliver messages published to <span className="font-mono break-all">{topic}</span> to an endpoint.
                </>
              ) : (
                "Deliver messages published to a topic to an endpoint."
              )}
            </DialogDescription>
          </DialogHeader>

          {!topic && (
            <Field label="Topic" htmlFor="sub-topic" error={err("topic")}>
              <Select value={topicName} onValueChange={setTopicName}>
                <SelectTrigger id="sub-topic" className="w-full">
                  <SelectValue placeholder={topics.data ? "Choose a topic" : "Loading topics..."} />
                </SelectTrigger>
                <SelectContent>
                  {(topics.data ?? []).map((x) => (
                    <SelectItem key={x.name} value={x.name}>
                      {x.name}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </Field>
          )}

          <Field label="Protocol" htmlFor="sub-protocol" help={fifoTopic ? "FIFO topics deliver to Amazon SQS queues only." : undefined}>
            <Select
              value={protocol}
              onValueChange={(v) => {
                setProtocol(v as SubscriptionProtocol)
                setEndpoint((cur) => (v === "http" || v === "https" ? (/^https?:\/\//.test(cur) ? cur.replace(/^https?/, v) : `${v}://`) : ""))
              }}
            >
              <SelectTrigger id="sub-protocol" className="w-full">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {protocols.map((p) => (
                  <SelectItem key={p} value={p}>
                    {PROTOCOL_LABEL[p]}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </Field>

          {protocol === "sqs" && (
            <Field
              label="Queue"
              htmlFor="sub-queue"
              error={err("endpoint")}
              help={
                queues.data && !queues.data.length ? (
                  <>
                    No queues yet.{" "}
                    <Link href="/sqs/create/" className="text-primary hover:underline">
                      Create a queue
                    </Link>
                    .
                  </>
                ) : fifoTopic ? (
                  "Use a FIFO queue to keep the topic's ordering and deduplication."
                ) : (
                  "Messages are sent to the queue as the SNS JSON envelope, or the raw message with raw delivery."
                )
              }
            >
              <Select value={endpoint} onValueChange={setEndpoint}>
                <SelectTrigger id="sub-queue" className="w-full">
                  <SelectValue placeholder={queues.data ? "Choose a queue" : "Loading queues..."} />
                </SelectTrigger>
                <SelectContent>
                  {queueList.map((q) => (
                    <SelectItem key={q.name} value={q.name}>
                      {q.name}
                      <QueueTypeBadge fifo={q.fifo} className="ml-auto" />
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </Field>
          )}
          {protocol === "lambda" && (
            <Field label="Function" htmlFor="sub-fn" error={err("endpoint")} help="The function is invoked with an SNS event (Records[].Sns).">
              <Select value={endpoint} onValueChange={setEndpoint}>
                <SelectTrigger id="sub-fn" className="w-full">
                  <SelectValue placeholder={functions.data ? "Choose a function" : "Loading functions..."} />
                </SelectTrigger>
                <SelectContent>
                  {(functions.data ?? []).map((f) => (
                    <SelectItem key={f.name} value={f.name}>
                      {f.name}
                      <span className="text-muted-foreground text-xs">{f.runtime}</span>
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </Field>
          )}
          {(protocol === "http" || protocol === "https") && (
            <Field
              label="Endpoint URL"
              htmlFor="sub-url"
              error={err("endpoint")}
              help="HomeCloud first POSTs a SubscriptionConfirmation message; the endpoint must visit its SubscribeURL before it receives notifications."
            >
              <Input
                id="sub-url"
                value={endpoint}
                onChange={(e) => setEndpoint(e.target.value)}
                placeholder={`${protocol}://example.internal/hooks/sns`}
                className="font-mono text-[13px]"
                autoComplete="off"
                spellCheck={false}
              />
            </Field>
          )}
          {(protocol === "email" || protocol === "email-json") && (
            <Field
              label="Email address"
              htmlFor="sub-email"
              error={err("endpoint")}
              help="The subscription stays pending until confirmed; HomeCloud writes the confirmation link to its server log."
            >
              <Input
                id="sub-email"
                type="email"
                value={endpoint}
                onChange={(e) => setEndpoint(e.target.value)}
                placeholder="ops@example.com"
                autoComplete="off"
                spellCheck={false}
              />
            </Field>
          )}
          {protocol === "sms" && (
            <Field label="Phone number" htmlFor="sub-sms" error={err("endpoint")} help="E.164 format, e.g. +14155550100.">
              <Input
                id="sub-sms"
                inputMode="tel"
                value={endpoint}
                onChange={(e) => setEndpoint(e.target.value)}
                placeholder="+14155550100"
                autoComplete="off"
                className="font-mono text-[13px]"
              />
            </Field>
          )}

          {rawSupported && <RawDeliverySwitch checked={raw} onChange={setRaw} />}
          <FilterPolicyField
            on={filterOn}
            onToggle={setFilterOn}
            value={policy}
            onChange={setPolicy}
            scope={scope}
            onScopeChange={(s) => {
              // Swap in the other example if the policy is still an untouched example.
              if (policy === EXAMPLE_POLICY[scope]) setPolicy(EXAMPLE_POLICY[s])
              setScope(s)
            }}
            error={err("policy")}
          />
          <DeadLetterField on={dlqOn} onToggle={setDlqOn} value={dlq} onChange={setDlq} queues={queues.data} error={err("dlq")} />

          <DialogFooter>
            <Button type="button" variant="outline" onClick={() => onOpenChange(false)} disabled={pending}>
              Cancel
            </Button>
            <Button type="submit" disabled={pending}>
              {pending && <Loader2 className="animate-spin" />}
              Create subscription
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}

function RawDeliverySwitch({ checked, onChange }: { checked: boolean; onChange: (v: boolean) => void }) {
  return (
    <div className="flex items-start justify-between gap-4 rounded-md border p-3">
      <div>
        <Label htmlFor="sub-raw" className="font-medium">
          Raw message delivery
        </Label>
        <p className="text-muted-foreground mt-0.5 text-xs">
          Deliver only the message body instead of the JSON envelope. For SQS, message attributes become queue message attributes.
        </p>
      </div>
      <Switch id="sub-raw" checked={checked} onCheckedChange={onChange} />
    </div>
  )
}

function FilterPolicyField({
  on,
  onToggle,
  value,
  onChange,
  scope,
  onScopeChange,
  error,
}: {
  on: boolean
  onToggle: (v: boolean) => void
  value: string
  onChange: (v: string) => void
  scope: FilterPolicyScope
  onScopeChange: (s: FilterPolicyScope) => void
  error?: string | null
}) {
  return (
    <div className="flex min-w-0 flex-col gap-3 rounded-md border p-3">
      <div className="flex items-start justify-between gap-4">
        <div>
          <Label htmlFor="sub-filter" className="font-medium">
            Subscription filter policy
          </Label>
          <p className="text-muted-foreground mt-0.5 text-xs">
            Only deliver messages that match. Keys are ANDed and the values listed for a key are ORed; supports exact values, prefix, suffix,
            equals-ignore-case, anything-but, numeric, exists, cidr and $or.
          </p>
        </div>
        <Switch id="sub-filter" checked={on} onCheckedChange={onToggle} />
      </div>
      {on && (
        <>
          <Field label="Filter policy scope">
            <OptionGroup label="Filter policy scope">
              {(Object.keys(SCOPE_LABEL) as FilterPolicyScope[]).map((sc) => (
                <OptionCard
                  key={sc}
                  selected={scope === sc}
                  onSelect={() => onScopeChange(sc)}
                  title={SCOPE_LABEL[sc]}
                  description={sc === "MessageAttributes" ? "Match the message attributes." : "Match fields of a JSON message body (may nest)."}
                />
              ))}
            </OptionGroup>
          </Field>
          <Field label="Filter policy" error={error}>
            <JsonEditor
              value={value}
              onChange={onChange}
              rows={6}
              validate={(p) => filterPolicyError(p, scope)}
              className={error ? "[&>div:first-child]:border-destructive" : undefined}
            />
          </Field>
          <div className="-mt-1 flex flex-wrap items-center gap-2">
            <Button type="button" variant="ghost" size="sm" onClick={() => onChange(EXAMPLE_POLICY[scope])}>
              Insert example
            </Button>
          </div>
        </>
      )}
    </div>
  )
}

function DeadLetterField({
  on,
  onToggle,
  value,
  onChange,
  queues,
  error,
}: {
  on: boolean
  onToggle: (v: boolean) => void
  value: string
  onChange: (v: string) => void
  queues: Queue[] | undefined
  error?: string | null
}) {
  return (
    <div className="flex min-w-0 flex-col gap-3 rounded-md border p-3">
      <div className="flex items-start justify-between gap-4">
        <div>
          <Label htmlFor="sub-dlq" className="font-medium">
            Redrive policy (dead-letter queue)
          </Label>
          <p className="text-muted-foreground mt-0.5 text-xs">Messages that can&apos;t be delivered after retries are sent to an SQS queue instead of being dropped.</p>
        </div>
        <Switch id="sub-dlq" checked={on} onCheckedChange={onToggle} />
      </div>
      {on && (
        <Field
          label="Dead-letter queue"
          htmlFor="sub-dlq-queue"
          error={error}
          help={
            queues && !queues.length ? (
              <>
                No queues yet.{" "}
                <Link href="/sqs/create/" className="text-primary hover:underline">
                  Create a queue
                </Link>
                .
              </>
            ) : undefined
          }
        >
          <Select value={value} onValueChange={onChange}>
            <SelectTrigger id="sub-dlq-queue" className="w-full">
              <SelectValue placeholder={queues ? "Choose a queue" : "Loading queues..."} />
            </SelectTrigger>
            <SelectContent>
              {(queues ?? []).map((q) => (
                <SelectItem key={q.name} value={q.name}>
                  {q.name}
                  <QueueTypeBadge fifo={q.fifo} className="ml-auto" />
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </Field>
      )}
    </div>
  )
}

export function EditSubscriptionDialog({ sub, onClose }: { sub: Subscription | null; onClose: () => void }) {
  const [raw, setRaw] = useState(false)
  const [filterOn, setFilterOn] = useState(false)
  const [scope, setScope] = useState<FilterPolicyScope>("MessageAttributes")
  const [policy, setPolicy] = useState(EXAMPLE_POLICY.MessageAttributes)
  const [submitted, setSubmitted] = useState(false)
  const [pending, setPending] = useState(false)

  useEffect(() => {
    if (sub) {
      const has = !!compactPolicy(sub.filter_policy)
      const sc = sub.filter_policy_scope ?? "MessageAttributes"
      setRaw(sub.raw_message_delivery)
      setFilterOn(has)
      setScope(sc)
      setPolicy(has ? JSON.stringify(sub.filter_policy, null, 2) : EXAMPLE_POLICY[sc])
      setSubmitted(false)
    }
  }, [sub])

  const policyErr = policyErrorFor(filterOn, policy, scope)
  const dlq = sub ? subscriptionDlq(sub) : null
  const rawSupported = !!sub && RAW_PROTOCOLS.includes(sub.protocol)

  const submit = async (e: React.FormEvent) => {
    e.preventDefault()
    setSubmitted(true)
    if (!sub || policyErr) return
    setPending(true)
    try {
      // An empty object clears the filter policy (null would leave it unchanged).
      await api.patch(subscriptionPath(sub.arn), {
        ...(rawSupported ? { raw_message_delivery: raw } : {}),
        filter_policy: filterOn ? JSON.parse(policy) : {},
        filter_policy_scope: filterOn ? scope : "MessageAttributes",
      })
      toast.success("Subscription updated")
      await revalidate(SNS_PATH)
      onClose()
    } catch (err) {
      toast.error(errorMessage(err))
    } finally {
      setPending(false)
    }
  }

  return (
    <Dialog open={!!sub} onOpenChange={(o) => !o && !pending && onClose()}>
      <DialogContent className="max-h-[90vh] overflow-y-auto sm:max-w-xl">
        <form onSubmit={submit} className="flex min-w-0 flex-col gap-4">
          <DialogHeader>
            <DialogTitle>Edit subscription</DialogTitle>
            <DialogDescription asChild>
              <div className="flex flex-wrap items-center gap-2">
                {sub && (
                  <>
                    <ProtocolBadge protocol={sub.protocol} />
                    <SubscriptionEndpoint sub={sub} />
                    <SubscriptionStatusBadge status={sub.status} />
                  </>
                )}
              </div>
            </DialogDescription>
          </DialogHeader>
          {sub && isPending(sub) && CONFIRM_PROTOCOLS.includes(sub.protocol) && (
            <Alert variant="warning">
              <Clock />
              <AlertDescription className="flex flex-wrap items-center justify-between gap-2">
                <span>This subscription is waiting for its endpoint to confirm it.</span>
                <Button type="button" variant="outline" size="sm" className="h-7" onClick={() => requestConfirmation(sub)}>
                  Request confirmation
                </Button>
              </AlertDescription>
            </Alert>
          )}
          {rawSupported && <RawDeliverySwitch checked={raw} onChange={setRaw} />}
          <FilterPolicyField
            on={filterOn}
            onToggle={setFilterOn}
            value={policy}
            onChange={setPolicy}
            scope={scope}
            onScopeChange={(s) => {
              if (policy === EXAMPLE_POLICY[scope]) setPolicy(EXAMPLE_POLICY[s])
              setScope(s)
            }}
            error={submitted ? policyErr : null}
          />
          <div className="flex flex-col gap-1 rounded-md border p-3 text-sm">
            <span className="font-medium">Redrive policy (dead-letter queue)</span>
            <span className="text-muted-foreground text-xs">
              {dlq ? (
                <>
                  Undeliverable messages go to{" "}
                  <Link href={queueHref(lastSegment(dlq))} className="text-primary hover:underline" title={dlq}>
                    {lastSegment(dlq)}
                  </Link>
                  .{" "}
                </>
              ) : (
                "None. "
              )}
              The redrive policy is set when subscribing; to change it, delete the subscription and create it again.
            </span>
          </div>
          <DialogFooter>
            <Button type="button" variant="outline" onClick={onClose} disabled={pending}>
              Cancel
            </Button>
            <Button type="submit" disabled={pending}>
              {pending && <Loader2 className="animate-spin" />}
              Save changes
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}
