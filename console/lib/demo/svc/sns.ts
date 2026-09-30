import { badRequest, err, getState, later, type DemoService, type Router } from "../engine"
import type { Subscription, SubscriptionProtocol, Topic, TopicDetail } from "@/lib/types"
import { DAY, HOUR, ACCOUNT, REGION, ago, arn, nowIso, uid, hex, uuid } from "../util"
import { NAMES } from "../ids"
import { deliverToQueue } from "./sqs"

interface TopicState extends Omit<Topic, "subscriptions"> {
  fifo_seq: number
  dedup: Record<string, string>
}
interface SnsState {
  topics: TopicState[]
  subs: Subscription[]
}

const st = () => getState().sns as SnsState
const topicArn = (n: string) => arn("sns", n)

function mkTopic(name: string, age: number, o: Partial<TopicState> = {}): TopicState {
  return {
    name, arn: topicArn(name), display_name: "", fifo: name.endsWith(".fifo"), attributes: null, created_at: ago(age), tags: { Project: "shop", Environment: "prod" },
    messages_published: 0, fifo_seq: 0, dedup: {}, ...o,
  }
}

function mkSub(topic: string, protocol: SubscriptionProtocol, endpoint: string, age: number, o: Partial<Subscription> = {}): Subscription {
  const pending = ["email", "email-json", "http", "https"].includes(protocol) && o.status === undefined && false
  return {
    arn: `${topicArn(topic)}:${uuid()}`, topic_arn: topicArn(topic), topic_name: topic, protocol, endpoint, raw_message_delivery: false, status: pending ? "PendingConfirmation" : "Confirmed",
    delivered: 0, failed: 0, created_at: ago(age), ...o,
  }
}

function seed(): SnsState {
  const events = mkTopic(NAMES.ordersTopic, 190 * DAY, { display_name: "Shop order events", messages_published: 48211 })
  const alerts = mkTopic(NAMES.alertsTopic, 190 * DAY, { display_name: "Shop alerts", messages_published: 312, tags: { Project: "shop", Environment: "prod", Team: "platform" } })
  const notif = mkTopic("shop-customer-notifications.fifo", 80 * DAY, { attributes: { FifoTopic: "true", ContentBasedDeduplication: "true" }, content_based_deduplication: true, messages_published: 3120, fifo_seq: 3120 })
  const dev = mkTopic("shop-dev-test", 20 * DAY, { tags: { Environment: "dev" } })
  const subs: Subscription[] = [
    mkSub(NAMES.ordersTopic, "sqs", arn("sqs", NAMES.ordersQueue), 190 * DAY, { raw_message_delivery: true, delivered: 48190, redrive_policy: JSON.stringify({ deadLetterTargetArn: arn("sqs", NAMES.ordersDlq) }), last_delivery: ago(40 * 1000) }),
    mkSub(NAMES.ordersTopic, "lambda", arn("lambda", `function:${NAMES.paymentsFn}`), 150 * DAY, { delivered: 48170, failed: 12, last_error: "Lambda function shop-payments returned an error: timeout after 30s", last_delivery: ago(90 * 1000),
      filter_policy: { event: ["order.created", "order.updated"] } }),
    mkSub(NAMES.ordersTopic, "sqs", arn("sqs", "shop-email-outbox"), 120 * DAY, { delivered: 9982, filter_policy: { event: ["order.created"] }, raw_message_delivery: true, last_delivery: ago(4 * 60 * 1000) }),
    mkSub(NAMES.alertsTopic, "email", "ops@example.com", 190 * DAY, { delivered: 310, last_delivery: ago(2 * DAY) }),
    mkSub(NAMES.alertsTopic, "email", "oncall@example.com", 150 * DAY, { delivered: 298, last_delivery: ago(2 * DAY) }),
    mkSub(NAMES.alertsTopic, "https", "https://hooks.example.com/incident/demo-webhook", 60 * DAY, { status: "PendingConfirmation" }),
    mkSub("shop-customer-notifications.fifo", "sqs", arn("sqs", "shop-notifications.fifo"), 80 * DAY, { delivered: 3118, raw_message_delivery: true, last_delivery: ago(3 * 60 * 1000) }),
  ]
  return { topics: [events, alerts, notif, dev], subs }
}

const findTopic = (name: string) => {
  const t = st().topics.find((x) => x.name === name)
  if (!t) throw err(404, "NotFound", `topic ${name} not found`)
  return t
}
const subsOf = (t: TopicState) => st().subs.filter((s) => s.topic_arn === t.arn)
const pub = ({ fifo_seq, dedup, ...t }: TopicState): Topic => {
  void fifo_seq
  void dedup
  return { ...t, subscriptions: subsOf(t as TopicState).length }
}

const NAME_RE = /^[A-Za-z0-9_-]{1,256}$/

function matchValue(rule: unknown, v: string | undefined): boolean {
  if (rule !== null && typeof rule === "object") {
    const o = rule as Record<string, unknown>
    if ("exists" in o) return (v !== undefined) === !!o.exists
    if ("prefix" in o) return v !== undefined && v.startsWith(String(o.prefix))
    if ("anything-but" in o) return v !== undefined && ![o["anything-but"]].flat().map(String).includes(v)
    if ("numeric" in o) return v !== undefined && !Number.isNaN(Number(v))
    return false
  }
  return v !== undefined && String(rule) === v
}

function passesFilter(sub: Subscription, attrs: Record<string, { string_value: string }> | undefined, message: string): boolean {
  const fp = sub.filter_policy
  if (!fp || !Object.keys(fp).length) return true
  if (sub.filter_policy_scope === "MessageBody") {
    let body: Record<string, unknown> = {}
    try {
      body = JSON.parse(message)
    } catch {
      return false
    }
    return Object.entries(fp).every(([k, rules]) => (Array.isArray(rules) ? rules : [rules]).some((r) => matchValue(r, body[k] === undefined ? undefined : String(body[k]))))
  }
  return Object.entries(fp).every(([k, rules]) => (Array.isArray(rules) ? rules : [rules]).some((r) => matchValue(r, attrs?.[k]?.string_value)))
}

function endpointCheck(protocol: string, endpoint: string) {
  if (!endpoint) throw badRequest("endpoint is required")
  switch (protocol) {
    case "sqs":
      if (!endpoint.startsWith("arn:aws:sqs:")) throw badRequest("sqs endpoints must be a queue ARN")
      break
    case "lambda":
      if (!endpoint.startsWith("arn:aws:lambda:")) throw badRequest("lambda endpoints must be a function ARN")
      break
    case "http":
    case "https":
      if (!new RegExp(`^${protocol}://`).test(endpoint)) throw badRequest(`${protocol} endpoints must start with ${protocol}://`)
      break
    case "email":
    case "email-json":
      if (!/^[^@\s]+@[^@\s]+$/.test(endpoint)) throw badRequest("enter a valid email address")
      break
    case "sms":
      if (!/^\+?[0-9]{7,15}$/.test(endpoint)) throw badRequest("enter a phone number in E.164 format")
      break
    default:
      throw badRequest(`unsupported protocol ${protocol}`)
  }
}

function routes(r: Router) {
  const base = "/api/v1/sns"
  r.get(`${base}/topics`, () => st().topics.map(pub))
  r.post(`${base}/topics`, ({ body }) => {
    const name = String(body?.name ?? "")
    const attrs: Record<string, string> = { ...(body?.attributes ?? {}) }
    const fifo = attrs.FifoTopic === "true"
    const stem = name.replace(/\.fifo$/, "")
    if (!NAME_RE.test(stem)) throw badRequest("topic names are 1-256 letters, digits, hyphens or underscores")
    if (fifo !== name.endsWith(".fifo")) throw badRequest(fifo ? "FIFO topic names must end with .fifo" : "only FIFO topics may end with .fifo")
    const existing = st().topics.find((t) => t.name === name)
    if (existing) return pub(existing)
    if (body?.content_based_deduplication) attrs.ContentBasedDeduplication = "true"
    const t = mkTopic(name, 0, {
      created_at: nowIso(), display_name: body?.display_name ?? "", tags: { ...(body?.tags ?? {}) }, attributes: Object.keys(attrs).length ? attrs : null,
      ...(fifo ? { content_based_deduplication: attrs.ContentBasedDeduplication === "true" } : {}),
    })
    st().topics.push(t)
    return pub(t)
  })
  r.get(`${base}/topics/:name`, ({ params }): TopicDetail => {
    const t = findTopic(params.name)
    return { ...pub(t), subscription_list: subsOf(t) }
  })
  r.patch(`${base}/topics/:name`, ({ params, body }) => {
    const t = findTopic(params.name)
    if (typeof body?.display_name === "string") t.display_name = body.display_name
    if (body?.attributes) {
      t.attributes = { ...(t.attributes ?? {}), ...body.attributes }
      if ("ContentBasedDeduplication" in body.attributes) t.content_based_deduplication = body.attributes.ContentBasedDeduplication === "true"
    }
    if (body?.tags) t.tags = { ...body.tags }
    return pub(t)
  })
  r.del(`${base}/topics/:name`, ({ params }) => {
    const t = findTopic(params.name)
    st().topics = st().topics.filter((x) => x !== t)
    st().subs = st().subs.filter((s) => s.topic_arn !== t.arn)
  })
  r.post(`${base}/topics/:name/publish`, ({ params, body }) => {
    const t = findTopic(params.name)
    const message = String(body?.message ?? "")
    if (!message) throw badRequest("message must not be empty")
    if (new TextEncoder().encode(message).length > 262144) throw badRequest("message exceeds 256 KB")
    const res: { message_id: string; sequence_number?: string } = { message_id: uuid() }
    if (t.fifo) {
      if (!body?.message_group_id) throw badRequest("message_group_id is required for FIFO topics")
      const dedup = body.message_deduplication_id || (t.content_based_deduplication ? `cb:${message}` : "")
      if (!dedup) throw badRequest("message_deduplication_id is required (content-based deduplication is off)")
      const seen = t.dedup[dedup]
      if (seen) return { message_id: seen, sequence_number: String(t.fifo_seq).padStart(20, "0") }
      t.dedup[dedup] = res.message_id
      t.fifo_seq++
      res.sequence_number = String(t.fifo_seq).padStart(20, "0")
      later(5 * 60 * 1000, () => delete t.dedup[dedup])
    }
    t.messages_published++
    const attrs = body?.message_attributes as Record<string, { data_type: string; string_value: string }> | undefined
    for (const s of subsOf(t)) {
      if (s.status !== "Confirmed" || !passesFilter(s, attrs, message)) continue
      const ok = s.protocol !== "http" && s.protocol !== "https"
      later(300, () => {
        if (s.protocol === "sqs") {
          const envelope = s.raw_message_delivery
            ? message
            : JSON.stringify({ Type: "Notification", MessageId: res.message_id, TopicArn: t.arn, Subject: body?.subject || undefined, Message: message, Timestamp: nowIso(), MessageAttributes: attrs })
          if (deliverToQueue(s.endpoint, envelope)) {
            s.delivered++
            s.last_delivery = nowIso()
          } else {
            s.failed++
            s.last_error = `Queue ${s.endpoint} does not exist`
          }
        } else if (ok) {
          s.delivered++
          s.last_delivery = nowIso()
        } else {
          s.failed++
          s.last_error = "HTTP endpoint is not reachable from the demo"
        }
      })
    }
    return res
  })

  r.get(`${base}/subscriptions`, ({ query }) => st().subs.filter((s) => !query.topic || s.topic_name === query.topic))
  r.post(`${base}/topics/:name/subscriptions`, ({ params, body }) => {
    const t = findTopic(params.name)
    const protocol = String(body?.protocol ?? "")
    const endpoint = String(body?.endpoint ?? "").trim()
    endpointCheck(protocol, endpoint)
    if (st().subs.some((s) => s.topic_arn === t.arn && s.protocol === protocol && s.endpoint === endpoint)) {
      return st().subs.find((s) => s.topic_arn === t.arn && s.protocol === protocol && s.endpoint === endpoint)
    }
    if (t.fifo && !["sqs", "lambda"].includes(protocol)) throw badRequest(`FIFO topics only support sqs and lambda subscriptions, not ${protocol}`)
    const pending = ["http", "https", "email", "email-json"].includes(protocol)
    const sub: Subscription = {
      arn: `${t.arn}:${uuid()}`, topic_arn: t.arn, topic_name: t.name, protocol: protocol as SubscriptionProtocol, endpoint, raw_message_delivery: !!body?.raw_message_delivery,
      filter_policy: body?.filter_policy && Object.keys(body.filter_policy).length ? body.filter_policy : null, status: pending ? "PendingConfirmation" : "Confirmed",
      delivered: 0, failed: 0, created_at: nowIso(),
    }
    if (body?.filter_policy_scope) sub.filter_policy_scope = body.filter_policy_scope
    if (body?.redrive_policy) sub.redrive_policy = body.redrive_policy
    st().subs.push(sub)
    return sub
  })
  r.patch(`${base}/subscriptions/:arn`, ({ params, body }) => {
    const sub = st().subs.find((s) => s.arn === params.arn)
    if (!sub) throw err(404, "NotFound", `subscription ${params.arn} not found`)
    if (typeof body?.raw_message_delivery === "boolean") sub.raw_message_delivery = body.raw_message_delivery
    if (body?.filter_policy !== undefined && body.filter_policy !== null) sub.filter_policy = Object.keys(body.filter_policy).length ? body.filter_policy : null
    if (body?.filter_policy_scope) sub.filter_policy_scope = body.filter_policy_scope
    return sub
  })
  r.del(`${base}/subscriptions/:arn`, ({ params }) => {
    const i = st().subs.findIndex((s) => s.arn === params.arn)
    if (i < 0) throw err(404, "NotFound", `subscription ${params.arn} not found`)
    st().subs.splice(i, 1)
  })
}

const service: DemoService = { name: "sns", seed, routes }

void HOUR
void ACCOUNT
void REGION
void uid
void hex
void mkSub
export default service
