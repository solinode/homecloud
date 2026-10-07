"use client"

import { useState } from "react"
import { Dices, Eraser, Loader2, Send, WandSparkles } from "lucide-react"
import { toast } from "sonner"

import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Textarea } from "@/components/ui/textarea"
import { CopyableText } from "@/components/console/copy-button"
import { Field } from "@/components/console/form-field"
import { Section } from "@/components/console/section"
import { AttributesEditor, attrRowsError, attrRowsToMap, prettyBody, type AttrRow } from "@/components/sqs/messages"
import { api, errorMessage } from "@/lib/api"
import { formatTime, pluralize } from "@/lib/format"
import { revalidate } from "@/lib/hooks"
import type { TopicDetail } from "@/lib/types"

import { SNS_PATH, compactPolicy, isPending, policyMatches, topicPath } from "./common"

const MAX_BYTES = 256 * 1024

interface PublishResult {
  message_id: string
  at: number
  subject: string
  matched: number
  /** subscriptions whose filter the preview could not evaluate */
  undetermined: number
  sequence_number?: string
  group_id?: string
  duplicate?: boolean
}

const newId = () => crypto.randomUUID?.() ?? `${Date.now()}-${Math.random().toString(36).slice(2)}`
const ID_RE = /^[A-Za-z0-9!"#$%&'()*+,\-./:;<=>?@[\\\]^_`{|}~]{1,128}$/

/** PublishPanel sends a message to every subscription of a topic whose filter policy matches. */
export function PublishPanel({ topic }: { topic: TopicDetail }) {
  const [subject, setSubject] = useState("")
  const [message, setMessage] = useState("")
  const [attrs, setAttrs] = useState<AttrRow[]>([])
  const [groupId, setGroupId] = useState("")
  const [dedupId, setDedupId] = useState("")
  const [submitted, setSubmitted] = useState(false)
  const [pending, setPending] = useState(false)
  const [results, setResults] = useState<PublishResult[]>([])

  const size = new TextEncoder().encode(message).length
  const trimmed = message.trim()
  const looksJson = trimmed.startsWith("{") || trimmed.startsWith("[")
  const isJson = prettyBody(message).json
  const errors = {
    message: !message ? "Enter a message" : size > MAX_BYTES ? "Messages are limited to 256 KB" : null,
    subject: subject.length > 100 ? "Subjects are at most 100 characters" : null,
    attrs: attrRowsError(attrs),
    group: !topic.fifo ? null : !groupId ? "Enter a message group ID" : !ID_RE.test(groupId) ? "Up to 128 letters, digits and punctuation" : null,
    dedup: !topic.fifo
      ? null
      : !dedupId
        ? topic.content_based_deduplication
          ? null
          : "Enter a deduplication ID (content-based deduplication is off)"
        : !ID_RE.test(dedupId)
          ? "Up to 128 letters, digits and punctuation"
          : null,
  }
  const err = (k: keyof typeof errors) => (submitted ? errors[k] : null)

  const subs = topic.subscription_list ?? []
  // Preview the server's filter evaluation; pending subscriptions receive nothing.
  const matching = (map: ReturnType<typeof attrRowsToMap>) => {
    const r = subs.filter((s) => !isPending(s)).map((s) => policyMatches(s, map, message))
    return { matched: r.filter((x) => x === true).length, undetermined: r.filter((x) => x === null).length }
  }

  const publish = async (e: React.FormEvent) => {
    e.preventDefault()
    setSubmitted(true)
    if (Object.values(errors).some(Boolean)) return
    const map = attrRowsToMap(attrs)
    setPending(true)
    try {
      const res = await api.post<{ message_id: string; sequence_number?: string }>(`${topicPath(topic.name)}/publish`, {
        subject,
        message,
        message_attributes: map,
        ...(topic.fifo ? { message_group_id: groupId, message_deduplication_id: dedupId || undefined } : {}),
      })
      // A FIFO duplicate (same deduplication ID within 5 minutes) returns the original message ID and is not delivered again.
      const duplicate = topic.fifo && results.some((r) => r.message_id === res.message_id)
      const m = duplicate ? { matched: 0, undetermined: 0 } : matching(map)
      setResults((r) =>
        [
          { message_id: res.message_id, at: Date.now(), subject, ...m, sequence_number: res.sequence_number, group_id: topic.fifo ? groupId : undefined, duplicate },
          ...r,
        ].slice(0, 5),
      )
      if (duplicate) toast.info("Duplicate message", { description: `Deduplicated as ${res.message_id}; it was not delivered again.` })
      else toast.success(`Published message ${res.message_id}`)
      if (topic.fifo && dedupId && !topic.content_based_deduplication) setDedupId("")
      setSubmitted(false)
      await revalidate(SNS_PATH)
      // Deliveries run in the background; refresh the stats once they have had time to finish.
      setTimeout(() => revalidate(SNS_PATH), 1500)
    } catch (e) {
      toast.error(errorMessage(e))
    } finally {
      setPending(false)
    }
  }

  const filtered = subs.filter((s) => compactPolicy(s.filter_policy)).length

  return (
    <div className="flex flex-col gap-4">
      <Section title="Message details" description={`Publish a message to ${topic.name}. It is delivered to ${pluralize(subs.length, "subscription")}${filtered ? ` (${filtered} with a filter policy)` : ""}.`}>
        <form onSubmit={publish} className="flex flex-col gap-4">
          {topic.fifo && (
            <div className="grid grid-cols-1 gap-4 md:grid-cols-2">
              <Field
                label="Message group ID"
                htmlFor="pub-group"
                error={err("group")}
                help="Messages in the same group are delivered in order, one after another."
              >
                <Input
                  id="pub-group"
                  value={groupId}
                  onChange={(e) => setGroupId(e.target.value)}
                  placeholder="orders-eu"
                  className="font-mono text-[13px]"
                  autoComplete="off"
                  spellCheck={false}
                  aria-invalid={!!err("group")}
                />
              </Field>
              <Field
                label="Message deduplication ID"
                htmlFor="pub-dedup"
                optional={!!topic.content_based_deduplication}
                error={err("dedup")}
                help={
                  topic.content_based_deduplication
                    ? "Leave empty to deduplicate on a hash of the message body."
                    : "Messages with the same ID within 5 minutes are delivered once."
                }
              >
                <div className="flex gap-2">
                  <Input
                    id="pub-dedup"
                    value={dedupId}
                    onChange={(e) => setDedupId(e.target.value)}
                    className="min-w-0 font-mono text-[13px]"
                    autoComplete="off"
                    spellCheck={false}
                    aria-invalid={!!err("dedup")}
                  />
                  <Button type="button" variant="outline" size="icon" className="shrink-0" onClick={() => setDedupId(newId())} aria-label="Generate deduplication ID" title="Generate">
                    <Dices />
                  </Button>
                </div>
              </Field>
            </div>
          )}
          <Field label="Subject" htmlFor="pub-subject" optional error={err("subject")} help="Included in the notification envelope (Subject) and in email-style endpoints.">
            <Input id="pub-subject" value={subject} onChange={(e) => setSubject(e.target.value)} placeholder="Order shipped" />
          </Field>
          <Field
            label="Message body"
            htmlFor="pub-message"
            error={err("message")}
            help={
              <span className="flex flex-wrap items-center gap-x-3">
                <span>{size.toLocaleString()} bytes of 256 KB</span>
                {looksJson &&
                  (isJson ? (
                    <span className="text-success">Valid JSON</span>
                  ) : (
                    <span className="text-warning">Not valid JSON (sent as text)</span>
                  ))}
              </span>
            }
          >
            <Textarea
              id="pub-message"
              rows={8}
              value={message}
              onChange={(e) => setMessage(e.target.value)}
              placeholder={'{"order_id": 1234, "status": "shipped"}'}
              className="font-mono text-[13px]"
              spellCheck={false}
              aria-invalid={!!err("message")}
            />
          </Field>
          <div className="-mt-2 flex flex-wrap gap-2">
            <Button type="button" variant="ghost" size="sm" disabled={!isJson} onClick={() => setMessage(prettyBody(message).text)}>
              <WandSparkles /> Format JSON
            </Button>
            <Button
              type="button"
              variant="ghost"
              size="sm"
              onClick={() => setMessage(JSON.stringify({ event: "example", id: crypto.randomUUID?.() ?? String(Date.now()), time: new Date().toISOString() }, null, 2))}
            >
              Insert JSON example
            </Button>
          </div>
          <Field
            label="Message attributes"
            optional
            error={err("attrs")}
            help="Subscription filter policies with the MessageAttributes scope match on these attributes."
          >
            <AttributesEditor rows={attrs} onChange={setAttrs} />
          </Field>
          <div className="flex flex-wrap items-center justify-end gap-2 border-t pt-4">
            <Button type="button" variant="outline" onClick={() => (setSubject(""), setMessage(""), setAttrs([]), setGroupId(""), setDedupId(""), setSubmitted(false))} disabled={pending}>
              <Eraser /> Clear
            </Button>
            <Button type="submit" disabled={pending}>
              {pending ? <Loader2 className="animate-spin" /> : <Send />}
              Publish message
            </Button>
          </div>
        </form>
      </Section>

      {results.length > 0 && (
        <Section title="Recently published" flush>
          <ul className="divide-y">
            {results.map((r) => (
              <li key={`${r.message_id}-${r.at}`} className="flex flex-col gap-1 px-5 py-2.5 text-sm sm:flex-row sm:items-center sm:justify-between">
                <span className="flex min-w-0 items-center gap-2">
                  <span className="text-muted-foreground shrink-0 tabular-nums">{formatTime(r.at)}</span>
                  <CopyableText value={r.message_id} />
                </span>
                <span className="text-muted-foreground text-xs">
                  {r.subject && <span className="mr-2">“{r.subject}”</span>}
                  {r.group_id && <span className="mr-2">group {r.group_id}</span>}
                  {r.sequence_number && <span className="mr-2 font-mono">seq {r.sequence_number}</span>}
                  {r.duplicate ? (
                    <span className="text-warning">duplicate, not delivered again</span>
                  ) : (
                    <>
                      matched {pluralize(r.matched, "subscription")}
                      {r.undetermined > 0 && ` (+${r.undetermined} decided by the server)`}
                    </>
                  )}
                </span>
              </li>
            ))}
          </ul>
        </Section>
      )}
    </div>
  )
}
