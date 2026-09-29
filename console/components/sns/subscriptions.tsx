"use client"

import { useEffect, useState, type ReactNode } from "react"
import Link from "next/link"
import { Loader2, Plus, Rss } from "lucide-react"
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
import { Field } from "@/components/console/form-field"
import { JsonEditor, jsonError } from "@/components/console/json-editor"
import { StatusBadge } from "@/components/console/status-badge"
import { TimeAgo } from "@/components/console/time-ago"
import { QueueTypeBadge } from "@/components/sqs/common"
import { api, errorMessage } from "@/lib/api"
import { formatNumber } from "@/lib/format"
import { revalidate, useApi } from "@/lib/hooks"
import type { LambdaFunction, Queue, Subscription, SubscriptionProtocol } from "@/lib/types"

import {
  PROTOCOL_LABEL,
  ProtocolBadge,
  SNS_PATH,
  SubscriptionEndpoint,
  compactPolicy,
  filterPolicyError,
  subscriptionPath,
  topicHref,
  topicPath,
  useTopics,
} from "./common"

const EXAMPLE_POLICY = '{\n  "severity": ["high", "critical"]\n}'

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
        <span className="font-mono text-[13px]" title={s.arn}>
          {s.arn.slice(s.arn.lastIndexOf(":") + 1, s.arn.lastIndexOf(":") + 9)}…
        </span>
      ),
      value: (s) => s.arn,
      hideBelow: "lg",
    },
    ...(!topic
      ? [
          {
            id: "topic",
            header: "Topic",
            cell: (s: Subscription) => (
              <Link href={topicHref(s.topic_name)} onClick={(e) => e.stopPropagation()} className={cellLinkClass()}>
                {s.topic_name}
              </Link>
            ),
            value: (s: Subscription) => s.topic_name,
          },
        ]
      : []),
    { id: "protocol", header: "Protocol", cell: (s) => <ProtocolBadge protocol={s.protocol} />, value: (s) => s.protocol },
    { id: "endpoint", header: "Endpoint", cell: (s) => <SubscriptionEndpoint sub={s} className="line-clamp-2" />, value: (s) => s.endpoint },
    { id: "status", header: "Status", cell: (s) => <StatusBadge status={s.status} />, value: (s) => s.status, hideBelow: "md" },
    {
      id: "raw",
      header: "Raw delivery",
      cell: (s) => (s.raw_message_delivery ? "Enabled" : <span className="text-muted-foreground">Disabled</span>),
      value: (s) => (s.raw_message_delivery ? "raw" : ""),
      hideBelow: "lg",
    },
    {
      id: "filter",
      header: "Filter policy",
      cell: (s) =>
        compactPolicy(s.filter_policy) ? (
          <code className="bg-muted block max-w-[16rem] truncate rounded px-1 py-0.5 font-mono text-xs" title={JSON.stringify(s.filter_policy, null, 2)}>
            {compactPolicy(s.filter_policy)}
          </code>
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
      cell: (s) => <span className={s.failed ? "text-destructive tabular-nums" : "text-muted-foreground tabular-nums"}>{formatNumber(s.failed)}</span>,
      value: (s) => s.failed,
      hideBelow: "sm",
    },
    { id: "last", header: "Last delivery", cell: (s) => <TimeAgo value={s.last_delivery} />, value: (s) => s.last_delivery ?? "", hideBelow: "md" },
  ]

  const items: ActionItem[] = [
    { label: "Edit", onSelect: () => sel && setEditing(sel), disabled: !sel },
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
        expanded={(s) =>
          s.last_error ? (
            <p className="text-destructive text-xs break-words">
              <span className="font-medium">Last error: </span>
              {s.last_error}
            </p>
          ) : null
        }
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
            description="Subscribe an SQS queue, a Lambda function or an HTTP(S) endpoint to receive every message published to the topic."
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

const URL_RE = (p: string) => new RegExp(`^${p}://[^\\s/$.?#][^\\s]*$`, "i")

export function CreateSubscriptionDialog({ open, onOpenChange, topic }: { open: boolean; onOpenChange: (o: boolean) => void; topic?: string }) {
  const topics = useTopics(0)
  const queues = useApi<Queue[]>(open ? "/api/v1/sqs/queues" : null, { revalidateOnFocus: false })
  const functions = useApi<LambdaFunction[]>(open ? "/api/v1/lambda/functions" : null, { revalidateOnFocus: false })
  const [topicName, setTopicName] = useState("")
  const [protocol, setProtocol] = useState<SubscriptionProtocol>("sqs")
  const [endpoint, setEndpoint] = useState("")
  const [raw, setRaw] = useState(false)
  const [filterOn, setFilterOn] = useState(false)
  const [policy, setPolicy] = useState(EXAMPLE_POLICY)
  const [submitted, setSubmitted] = useState(false)
  const [pending, setPending] = useState(false)

  useEffect(() => {
    if (open) {
      setTopicName(topic ?? "")
      setProtocol("sqs")
      setEndpoint("")
      setRaw(false)
      setFilterOn(false)
      setPolicy(EXAMPLE_POLICY)
      setSubmitted(false)
    }
  }, [open, topic])

  const t = topic ?? topicName
  const errors: Record<string, string | null> = {
    topic: t ? null : "Choose a topic",
    endpoint: !endpoint.trim()
      ? protocol === "sqs"
        ? "Choose a queue"
        : protocol === "lambda"
          ? "Choose a function"
          : "Enter the endpoint URL"
      : (protocol === "http" || protocol === "https") && !URL_RE(protocol).test(endpoint.trim())
        ? `Enter a URL that starts with ${protocol}://`
        : null,
    policy: filterOn ? (jsonError(policy) ?? filterPolicyError(JSON.parse(policy))) : null,
  }
  const err = (k: string) => (submitted ? errors[k] : null)
  const valid = Object.values(errors).every((v) => !v)

  const submit = async (e: React.FormEvent) => {
    e.preventDefault()
    setSubmitted(true)
    if (!valid) return
    setPending(true)
    try {
      await api.post(`${topicPath(t)}/subscriptions`, {
        protocol,
        endpoint: endpoint.trim(),
        raw_message_delivery: raw,
        filter_policy: filterOn ? JSON.parse(policy) : undefined,
      })
      toast.success(`Subscribed ${endpoint.trim()} to ${t}`)
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
        <form onSubmit={submit} className="flex flex-col gap-4">
          <DialogHeader>
            <DialogTitle>Create subscription</DialogTitle>
            <DialogDescription>
              {topic ? (
                <>
                  Deliver messages published to <span className="font-mono">{topic}</span> to an endpoint.
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

          <Field label="Protocol" htmlFor="sub-protocol">
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
                {(Object.keys(PROTOCOL_LABEL) as SubscriptionProtocol[]).map((p) => (
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
                  {(queues.data ?? []).map((q) => (
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
              help="HomeCloud POSTs each message to this URL and retries failed deliveries twice."
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

          <RawDeliverySwitch checked={raw} onChange={setRaw} />
          <FilterPolicyField on={filterOn} onToggle={setFilterOn} value={policy} onChange={setPolicy} error={err("policy")} />

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
  error,
}: {
  on: boolean
  onToggle: (v: boolean) => void
  value: string
  onChange: (v: string) => void
  error?: string | null
}) {
  return (
    <div className="flex flex-col gap-3 rounded-md border p-3">
      <div className="flex items-start justify-between gap-4">
        <div>
          <Label htmlFor="sub-filter" className="font-medium">
            Subscription filter policy
          </Label>
          <p className="text-muted-foreground mt-0.5 text-xs">
            Only deliver messages whose attributes match: every listed attribute must be present with one of the allowed values.
          </p>
        </div>
        <Switch id="sub-filter" checked={on} onCheckedChange={onToggle} />
      </div>
      {on && (
        <>
          <JsonEditor value={value} onChange={onChange} rows={5} validate={filterPolicyError} className={error ? "[&>div:first-child]:border-destructive" : undefined} />
        </>
      )}
    </div>
  )
}

export function EditSubscriptionDialog({ sub, onClose }: { sub: Subscription | null; onClose: () => void }) {
  const [raw, setRaw] = useState(false)
  const [filterOn, setFilterOn] = useState(false)
  const [policy, setPolicy] = useState(EXAMPLE_POLICY)
  const [submitted, setSubmitted] = useState(false)
  const [pending, setPending] = useState(false)

  useEffect(() => {
    if (sub) {
      const has = !!compactPolicy(sub.filter_policy)
      setRaw(sub.raw_message_delivery)
      setFilterOn(has)
      setPolicy(has ? JSON.stringify(sub.filter_policy, null, 2) : EXAMPLE_POLICY)
      setSubmitted(false)
    }
  }, [sub])

  const policyErr = filterOn ? (jsonError(policy) ?? filterPolicyError(JSON.parse(policy))) : null

  const submit = async (e: React.FormEvent) => {
    e.preventDefault()
    setSubmitted(true)
    if (!sub || policyErr) return
    setPending(true)
    try {
      // An empty object clears the filter policy (null would leave it unchanged).
      await api.patch(subscriptionPath(sub.arn), { raw_message_delivery: raw, filter_policy: filterOn ? JSON.parse(policy) : {} })
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
        <form onSubmit={submit} className="flex flex-col gap-4">
          <DialogHeader>
            <DialogTitle>Edit subscription</DialogTitle>
            <DialogDescription asChild>
              <div className="flex flex-wrap items-center gap-2">
                {sub && (
                  <>
                    <ProtocolBadge protocol={sub.protocol} />
                    <SubscriptionEndpoint sub={sub} />
                  </>
                )}
              </div>
            </DialogDescription>
          </DialogHeader>
          <RawDeliverySwitch checked={raw} onChange={setRaw} />
          <FilterPolicyField on={filterOn} onToggle={setFilterOn} value={policy} onChange={setPolicy} error={submitted ? policyErr : null} />
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
