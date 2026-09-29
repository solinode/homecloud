"use client"

import { useState } from "react"
import { Eraser, Loader2, Send, WandSparkles } from "lucide-react"
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

import { SNS_PATH, compactPolicy, topicPath } from "./common"

const MAX_BYTES = 256 * 1024

interface PublishResult {
  message_id: string
  at: number
  subject: string
  matched: number
}

/** PublishPanel sends a message to every subscription of a topic whose filter policy matches. */
export function PublishPanel({ topic }: { topic: TopicDetail }) {
  const [subject, setSubject] = useState("")
  const [message, setMessage] = useState("")
  const [attrs, setAttrs] = useState<AttrRow[]>([])
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
  }
  const err = (k: keyof typeof errors) => (submitted ? errors[k] : null)

  const subs = topic.subscription_list ?? []
  // Mirror the server's filter: every policy key present with an allowed value.
  const matching = (map: ReturnType<typeof attrRowsToMap>) =>
    subs.filter((s) => Object.entries(s.filter_policy ?? {}).every(([k, allowed]) => map?.[k] && allowed.includes(map[k].string_value))).length

  const publish = async (e: React.FormEvent) => {
    e.preventDefault()
    setSubmitted(true)
    if (errors.message || errors.subject || errors.attrs) return
    const map = attrRowsToMap(attrs)
    setPending(true)
    try {
      const res = await api.post<{ message_id: string }>(`${topicPath(topic.name)}/publish`, { subject, message, message_attributes: map })
      setResults((r) => [{ message_id: res.message_id, at: Date.now(), subject, matched: matching(map) }, ...r].slice(0, 5))
      toast.success(`Published message ${res.message_id}`)
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
                    <span className="text-emerald-600 dark:text-emerald-400">Valid JSON</span>
                  ) : (
                    <span className="text-amber-700 dark:text-amber-300">Not valid JSON (sent as text)</span>
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
            help="Subscription filter policies match on String values of these attributes."
          >
            <AttributesEditor rows={attrs} onChange={setAttrs} />
          </Field>
          <div className="flex flex-wrap items-center gap-2 border-t pt-4">
            <Button type="submit" disabled={pending}>
              {pending ? <Loader2 className="animate-spin" /> : <Send />}
              Publish message
            </Button>
            <Button type="button" variant="outline" onClick={() => (setSubject(""), setMessage(""), setAttrs([]), setSubmitted(false))} disabled={pending}>
              <Eraser /> Clear
            </Button>
          </div>
        </form>
      </Section>

      {results.length > 0 && (
        <Section title="Recently published" flush>
          <ul className="divide-y">
            {results.map((r) => (
              <li key={r.message_id} className="flex flex-col gap-1 px-4 py-2.5 text-sm sm:flex-row sm:items-center sm:justify-between">
                <span className="flex min-w-0 items-center gap-2">
                  <span className="text-muted-foreground shrink-0 tabular-nums">{formatTime(r.at)}</span>
                  <CopyableText value={r.message_id} />
                </span>
                <span className="text-muted-foreground text-xs">
                  {r.subject && <span className="mr-2">“{r.subject}”</span>}
                  matched {pluralize(r.matched, "subscription")}
                </span>
              </li>
            ))}
          </ul>
        </Section>
      )}
    </div>
  )
}
