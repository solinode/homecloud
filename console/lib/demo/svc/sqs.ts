import { badRequest, conflict, err, getState, notFound, type DemoService, type Router } from "../engine"
import type { MessageAttribute, PeekedMessage, Queue, ReceivedMessage, RedrivePolicy, SendMessageResult } from "@/lib/types"
import { ACCOUNT, DAY, HOUR, MIN, arn, ago, hex, nowIso, rng, stableId, uuid } from "../util"
import { NAMES } from "../ids"

interface Msg {
  id: string
  body: string
  md5: string
  sent_at: string
  receive_count: number
  /** epoch ms when the message becomes visible */
  visible_at: number
  first_receive?: number
  group_id: string
  attrs: Record<string, MessageAttribute> | null
  receipt: string
  source_queue: string
  sender: string
}

interface QueueState {
  q: Omit<Queue, "approximate_number_of_messages" | "approximate_number_of_messages_not_visible" | "approximate_number_of_messages_delayed" | "messages_sent" | "messages_received" | "messages_deleted" | "dead_letter_source_queues">
  msgs: Msg[]
  sent: number
  received: number
  deleted: number
  dedup: Record<string, number>
  seq: number
}

const st = () => getState().sqs as { queues: QueueState[] }

const queueUrl = (name: string) => `http://homecloud.local:8080/api/v1/sqs/queues/${name}`
const queueArn = (name: string) => arn("sqs", name)
const md5ish = (s: string) => {
  const r = rng(s)
  let o = ""
  for (let i = 0; i < 32; i++) o += "0123456789abcdef"[Math.floor(r() * 16)]
  return o
}

function mkQueue(name: string, age: number, o: Partial<QueueState["q"]> = {}): QueueState["q"] {
  const fifo = name.endsWith(".fifo")
  return {
    name, arn: queueArn(name), url: queueUrl(name), fifo, content_based_deduplication: false,
    ...(fifo ? { deduplication_scope: "queue" as const, fifo_throughput_limit: "perQueue" as const } : {}),
    visibility_timeout: 30, message_retention_seconds: 345600, delay_seconds: 0, receive_wait_time_seconds: 0, max_message_size: 262144,
    redrive_policy: null, sqs_managed_sse_enabled: true, created_at: ago(age), last_modified: ago(age), tags: { Project: "shop", Environment: "prod" }, ...o,
  }
}

function mkMsg(source: string, body: string, age: number, o: Partial<Msg> = {}): Msg {
  return {
    id: uuid(), body, md5: md5ish(body), sent_at: ago(age), receive_count: 0, visible_at: Date.now() - age, group_id: "", attrs: null, receipt: "", source_queue: "", sender: "AIDADEMOADMIN000000", ...o,
  }
  void source
}

function orderBody(n: number, r: () => number) {
  const skus = ["SKU-1001", "SKU-1002", "SKU-1003", "SKU-1004", "SKU-1005"]
  return JSON.stringify({
    event: "order.created", order_id: `ORD-${89000 + n}`, customer_id: `cus_${4100 + Math.floor(r() * 16)}`, currency: "USD",
    total_cents: 1200 + Math.floor(r() * 9000), items: [{ sku: skus[Math.floor(r() * skus.length)], qty: 1 + Math.floor(r() * 3) }],
  })
}

function seed() {
  const r = rng("sqs-seed")
  const orders: QueueState = {
    q: mkQueue(NAMES.ordersQueue, 190 * DAY, {
      visibility_timeout: 60, redrive_policy: { dead_letter_queue: NAMES.ordersDlq, max_receive_count: 5 }, receive_wait_time_seconds: 10,
      tags: { Project: "shop", Environment: "prod", Team: "orders" },
    }),
    msgs: [], sent: 48211, received: 48190, deleted: 48180, dedup: {}, seq: 0,
  }
  for (let i = 0; i < 26; i++) orders.msgs.push(mkMsg(NAMES.ordersQueue, orderBody(i, r), i * 40 * 1000 + 5000, { attrs: i % 3 === 0 ? { source: { data_type: "String", string_value: "web" } } : null }))
  for (let i = 0; i < 4; i++) orders.msgs.push(mkMsg(NAMES.ordersQueue, orderBody(100 + i, r), 20 * 1000 + i * 3000, { receive_count: 1, visible_at: Date.now() + 25_000 + i * 2000, first_receive: Date.now() - 5000, receipt: hex(40) }))
  orders.msgs.push(mkMsg(NAMES.ordersQueue, orderBody(200, r), 8000, { visible_at: Date.now() + 45_000 }))

  const dlq: QueueState = {
    q: mkQueue(NAMES.ordersDlq, 190 * DAY, { message_retention_seconds: 1209600, tags: { Project: "shop", Environment: "prod", Team: "orders" } }),
    msgs: [], sent: 17, received: 4, deleted: 4, dedup: {}, seq: 0,
  }
  for (let i = 0; i < 5; i++) {
    dlq.msgs.push(mkMsg(NAMES.ordersDlq, orderBody(300 + i, r), (i + 1) * 5 * HOUR, { receive_count: 5, source_queue: queueArn(NAMES.ordersQueue), first_receive: Date.now() - (i + 1) * 5 * HOUR }))
  }

  const notif: QueueState = {
    q: mkQueue("shop-notifications.fifo", 95 * DAY, { content_based_deduplication: true, visibility_timeout: 30, tags: { Project: "shop", Environment: "prod" } }),
    msgs: [], sent: 3120, received: 3118, deleted: 3118, dedup: {}, seq: 3120,
  }
  for (let i = 0; i < 3; i++) notif.msgs.push(mkMsg(notif.q.name, JSON.stringify({ type: "shipping.notice", customer_id: `cus_${4100 + i}`, order_id: `ORD-${88900 + i}`, channel: "email" }), i * 12 * 1000 + 3000, { group_id: `cus_${4100 + i}` }))

  const email: QueueState = {
    q: mkQueue("shop-email-outbox", 120 * DAY, { delay_seconds: 5, visibility_timeout: 120, redrive_policy: { dead_letter_queue: "shop-email-outbox-dlq", max_receive_count: 3 } }),
    msgs: [], sent: 9982, received: 9982, deleted: 9980, dedup: {}, seq: 0,
  }
  for (let i = 0; i < 6; i++) email.msgs.push(mkMsg(email.q.name, JSON.stringify({ to: `customer${i}@example.com`, template: "order-confirmation", order_id: `ORD-${89010 + i}` }), i * 20 * 1000 + 10_000))
  const emailDlq: QueueState = { q: mkQueue("shop-email-outbox-dlq", 120 * DAY, { message_retention_seconds: 1209600 }), msgs: [], sent: 2, received: 0, deleted: 0, dedup: {}, seq: 0 }
  emailDlq.msgs.push(mkMsg(emailDlq.q.name, JSON.stringify({ to: "bounce@example.invalid", template: "order-confirmation", order_id: "ORD-88012" }), 2 * DAY, { receive_count: 3, source_queue: queueArn(email.q.name), first_receive: Date.now() - 2 * DAY }))

  const img: QueueState = { q: mkQueue("shop-image-resize", 150 * DAY, { visibility_timeout: 300, tags: { Project: "shop", Team: "web" } }), msgs: [], sent: 5410, received: 5410, deleted: 5410, dedup: {}, seq: 0 }
  const idle: QueueState = { q: mkQueue("shop-dev-scratch", 15 * DAY, { tags: { Environment: "dev" } }), msgs: [], sent: 0, received: 0, deleted: 0, dedup: {}, seq: 0 }
  return { queues: [orders, dlq, notif, email, emailDlq, img, idle] }
}

function find(name: string) {
  const s = st().queues.find((x) => x.q.name === name)
  if (!s) throw err(404, "QueueDoesNotExist", `The specified queue does not exist: ${name}`)
  return s
}

function counts(s: QueueState) {
  const now = Date.now()
  let visible = 0, inflight = 0, delayed = 0
  for (const m of s.msgs) {
    if (m.visible_at <= now) visible++
    else if (m.receive_count > 0) inflight++
    else delayed++
  }
  return { visible, inflight, delayed }
}

function view(s: QueueState): Queue {
  const c = counts(s)
  return {
    ...s.q, approximate_number_of_messages: c.visible, approximate_number_of_messages_not_visible: c.inflight, approximate_number_of_messages_delayed: c.delayed,
    messages_sent: s.sent, messages_received: s.received, messages_deleted: s.deleted,
    dead_letter_source_queues: st().queues.filter((o) => o.q.redrive_policy?.dead_letter_queue === s.q.name).map((o) => o.q.name),
  }
}

function inRange(v: unknown, lo: number, hi: number, name: string) {
  if (v === undefined || v === null) return
  if (typeof v !== "number" || v < lo || v > hi) throw err(400, "InvalidAttributeValue", `Invalid value for the parameter ${name}. Reason: must be between ${lo} and ${hi}.`)
}

function applyAttrs(q: QueueState["q"], b: any) {
  inRange(b.visibility_timeout, 0, 43200, "VisibilityTimeout")
  inRange(b.message_retention_seconds, 60, 1209600, "MessageRetentionPeriod")
  inRange(b.delay_seconds, 0, 900, "DelaySeconds")
  inRange(b.receive_wait_time_seconds, 0, 20, "ReceiveMessageWaitTimeSeconds")
  inRange(b.max_message_size, 1024, 262144, "MaximumMessageSize")
  inRange(b.kms_data_key_reuse_period_seconds, 60, 86400, "KmsDataKeyReusePeriodSeconds")
  for (const k of ["visibility_timeout", "message_retention_seconds", "delay_seconds", "receive_wait_time_seconds", "max_message_size", "kms_data_key_reuse_period_seconds"] as const) {
    if (typeof b[k] === "number") (q as any)[k] = b[k]
  }
  if (!q.fifo && (b.content_based_deduplication || b.deduplication_scope || b.fifo_throughput_limit)) throw err(400, "InvalidAttributeName", "Unknown Attribute ContentBasedDeduplication.")
  if (typeof b.content_based_deduplication === "boolean") q.content_based_deduplication = b.content_based_deduplication
  if (b.deduplication_scope) q.deduplication_scope = b.deduplication_scope
  if (b.fifo_throughput_limit) q.fifo_throughput_limit = b.fifo_throughput_limit
  for (const k of ["policy", "redrive_allow_policy"] as const) {
    if (typeof b[k] === "string") {
      if (b[k]) {
        try {
          JSON.parse(b[k])
        } catch {
          throw err(400, "InvalidAttributeValue", `Invalid value for the parameter ${k === "policy" ? "Policy" : "RedriveAllowPolicy"}.`)
        }
      }
      q[k] = b[k]
    }
  }
  if (typeof b.kms_master_key_id === "string") {
    q.kms_master_key_id = b.kms_master_key_id
    if (b.kms_master_key_id) {
      q.sqs_managed_sse_enabled = false
      q.kms_data_key_reuse_period_seconds = q.kms_data_key_reuse_period_seconds || 300
    }
  }
  if (typeof b.sqs_managed_sse_enabled === "boolean") {
    q.sqs_managed_sse_enabled = b.sqs_managed_sse_enabled
    if (b.sqs_managed_sse_enabled) {
      q.kms_master_key_id = ""
      q.kms_data_key_reuse_period_seconds = undefined
    }
  }
  if (b.redrive_policy) {
    const rp = b.redrive_policy as RedrivePolicy
    if (!rp.dead_letter_queue) q.redrive_policy = null
    else {
      if (!st().queues.some((x) => x.q.name === rp.dead_letter_queue)) throw err(400, "InvalidParameterValue", `dead-letter queue ${rp.dead_letter_queue} does not exist`)
      if (rp.max_receive_count < 1 || rp.max_receive_count > 1000) throw err(400, "InvalidAttributeValue", "maxReceiveCount must be between 1 and 1000")
      q.redrive_policy = { dead_letter_queue: rp.dead_letter_queue, max_receive_count: rp.max_receive_count }
    }
  }
  if (b.tags) q.tags = { ...b.tags }
  q.last_modified = nowIso()
}

/** send appends a message; shared with nothing else, kept simple. */
function send(s: QueueState, b: any, sender = "AIDADEMOADMIN000000"): SendMessageResult {
  const body = String(b?.body ?? "")
  if (!body) throw badRequest("Message body must not be empty")
  const size = new TextEncoder().encode(body).length
  if (size > s.q.max_message_size) throw err(400, "InvalidParameterValue", `One or more parameters are invalid. Reason: Message must be shorter than ${s.q.max_message_size} bytes.`)
  const fifo = s.q.fifo
  if (fifo && !b?.group_id) throw err(400, "MissingParameter", "The request must contain the parameter MessageGroupId.")
  const res: SendMessageResult = { message_id: uuid(), md5_of_body: md5ish(body) }
  if (fifo) {
    const dedup = b.dedup_id || (s.q.content_based_deduplication ? md5ish(body) : "")
    if (!dedup) throw err(400, "InvalidParameterValue", "The queue should either have ContentBasedDeduplication enabled or MessageDeduplicationId provided explicitly")
    const seen = s.dedup[dedup]
    if (seen && Date.now() - seen < 5 * MIN) return { ...res, duplicate: true, sequence_number: String(s.seq) }
    s.dedup[dedup] = Date.now()
    s.seq++
    res.sequence_number = String(s.seq).padStart(20, "0")
  }
  const delay = typeof b.delay_seconds === "number" && !fifo ? b.delay_seconds : s.q.delay_seconds
  s.msgs.push({
    id: res.message_id, body, md5: res.md5_of_body, sent_at: nowIso(), receive_count: 0, visible_at: Date.now() + delay * 1000, group_id: b.group_id ?? "",
    attrs: b.message_attributes && Object.keys(b.message_attributes).length ? b.message_attributes : null, receipt: "", source_queue: "", sender,
  })
  s.sent++
  return res
}

const pause = (ms: number) => new Promise((r) => setTimeout(r, ms))

function moveMessages(src: QueueState, dest: string): number {
  let moved = 0
  const kept: Msg[] = []
  for (const m of src.msgs) {
    const d = dest || (m.source_queue ? m.source_queue.split(":").pop()! : "")
    const to = d && d !== src.q.name ? st().queues.find((x) => x.q.name === d) : undefined
    if (!to) {
      kept.push(m)
      continue
    }
    to.msgs.push({ ...m, source_queue: "", receive_count: 0, receipt: "", visible_at: Date.now(), first_receive: undefined })
    moved++
  }
  src.msgs = kept
  return moved
}

function routes(r: Router) {
  const base = "/api/v1/sqs/queues"
  r.get(base, ({ query }) => st().queues.filter((s) => !query.prefix || s.q.name.startsWith(query.prefix)).map(view))
  r.post(base, ({ body }) => {
    const name = String(body?.name ?? "")
    const fifo = !!body?.fifo
    const stem = name.replace(/\.fifo$/, "")
    if (!/^[A-Za-z0-9_-]{1,80}$/.test(stem)) throw err(400, "InvalidParameterValue", "Can only include alphanumeric characters, hyphens, or underscores. 1 to 80 in length")
    if (fifo !== name.endsWith(".fifo")) throw err(400, "InvalidParameterValue", fifo ? "The name of a FIFO queue must end with the .fifo suffix." : "Only FIFO queue names may end with .fifo")
    if (st().queues.some((s) => s.q.name === name)) throw err(409, "QueueAlreadyExists", `queue "${name}" already exists`)
    const q = mkQueue(name, 0, { tags: {}, created_at: nowIso(), last_modified: nowIso() })
    applyAttrs(q, body)
    if (q.kms_master_key_id) q.sqs_managed_sse_enabled = false
    st().queues.push({ q, msgs: [], sent: 0, received: 0, deleted: 0, dedup: {}, seq: 0 })
    return view(st().queues[st().queues.length - 1])
  })
  r.get(`${base}/:name`, ({ params }) => view(find(params.name)))
  r.patch(`${base}/:name`, ({ params, body }) => {
    const s = find(params.name)
    applyAttrs(s.q, body ?? {})
    return view(s)
  })
  r.del(`${base}/:name`, ({ params }) => {
    const s = find(params.name)
    const src = st().queues.find((o) => o.q.redrive_policy?.dead_letter_queue === s.q.name)
    if (src) throw conflict(`queue "${s.q.name}" is the dead-letter queue of "${src.q.name}"; remove that redrive policy first`)
    st().queues = st().queues.filter((x) => x !== s)
  })
  r.post(`${base}/:name/purge`, ({ params }) => {
    find(params.name).msgs = []
  })
  r.post(`${base}/:name/messages`, ({ params, body }) => {
    const s = find(params.name)
    if (Array.isArray(body?.entries) && body.entries.length) {
      if (body.entries.length > 10) throw badRequest("a batch holds at most 10 messages")
      return body.entries.map((e: any) => {
        try {
          return send(s, e)
        } catch (x) {
          return { message_id: "", md5_of_body: "", error: (x as Error).message }
        }
      })
    }
    return send(s, body)
  })
  r.post(`${base}/:name/messages/receive`, async ({ params, body }): Promise<ReceivedMessage[]> => {
    const s = find(params.name)
    const max = Math.min(Math.max(Number(body?.max_messages) || 1, 1), 10)
    const vis = typeof body?.visibility_timeout === "number" ? body.visibility_timeout : s.q.visibility_timeout
    const wait = typeof body?.wait_seconds === "number" ? body.wait_seconds : s.q.receive_wait_time_seconds
    const pick = () => s.msgs.filter((m) => m.visible_at <= Date.now()).slice(0, max)
    let got = pick()
    if (!got.length && wait > 0) {
      await pause(Math.min(wait, 2) * 1000)
      got = pick()
    }
    const out: ReceivedMessage[] = []
    for (const m of got) {
      m.receive_count++
      const rp = s.q.redrive_policy
      if (rp && m.receive_count > rp.max_receive_count) {
        const dlq = st().queues.find((x) => x.q.name === rp.dead_letter_queue)
        if (dlq) {
          s.msgs = s.msgs.filter((x) => x !== m)
          dlq.msgs.push({ ...m, source_queue: s.q.arn, receive_count: m.receive_count - 1, visible_at: Date.now() })
          continue
        }
      }
      m.receipt = hex(40)
      m.visible_at = Date.now() + vis * 1000
      m.first_receive ??= Date.now()
      s.received++
      const attributes: Record<string, string> = {
        SenderId: m.sender, SentTimestamp: String(Date.parse(m.sent_at)), ApproximateReceiveCount: String(m.receive_count), ApproximateFirstReceiveTimestamp: String(m.first_receive),
      }
      if (m.group_id) attributes.MessageGroupId = m.group_id
      out.push({ message_id: m.id, receipt_handle: m.receipt, body: m.body, md5_of_body: m.md5, attributes, message_attributes: m.attrs })
    }
    return out
  })
  r.post(`${base}/:name/messages/delete`, ({ params, body }) => {
    const s = find(params.name)
    const handles: string[] = [...(body?.receipt_handles ?? []), ...(body?.receipt_handle ? [body.receipt_handle] : [])]
    const failed: string[] = []
    for (const h of handles) {
      const i = s.msgs.findIndex((m) => m.receipt && m.receipt === h)
      if (i >= 0 && s.msgs[i].visible_at > Date.now()) {
        s.msgs.splice(i, 1)
        s.deleted++
      } else failed.push(h)
    }
    return { deleted: handles.length - failed.length, failed }
  })
  r.post(`${base}/:name/messages/visibility`, ({ params, body }) => {
    const s = find(params.name)
    const m = s.msgs.find((x) => x.receipt && x.receipt === body?.receipt_handle)
    if (!m) throw err(400, "ReceiptHandleIsInvalid", "The receipt handle is not valid or the message is no longer in flight")
    inRange(body?.visibility_timeout, 0, 43200, "VisibilityTimeout")
    m.visible_at = Date.now() + body.visibility_timeout * 1000
  })
  r.get(`${base}/:name/messages/peek`, ({ params, query }): PeekedMessage[] => {
    const s = find(params.name)
    const limit = Number(query.limit) || 50
    const now = Date.now()
    return s.msgs.slice(0, limit).map((m) => ({
      message_id: m.id, body: m.body, sent_at: m.sent_at, receive_count: m.receive_count,
      state: m.visible_at > now ? (m.receive_count > 0 ? "in-flight" : "delayed") : "available", group_id: m.group_id, message_attributes: m.attrs,
      size: m.body.length, source_queue: m.source_queue,
    }))
  })
  r.post(`${base}/:name/redrive`, ({ params, body }) => {
    const s = find(params.name)
    if (body?.destination) find(body.destination)
    return { moved: moveMessages(s, body?.destination ?? "") }
  })
}

/** deliverToQueue is used by SNS to push a message into a queue by ARN; returns false if the queue is unknown. */
export function deliverToQueue(queueArnStr: string, body: string, sender = "sns.amazonaws.com"): boolean {
  const s = st().queues.find((x) => x.q.arn === queueArnStr)
  if (!s) return false
  s.msgs.push({ id: uuid(), body, md5: md5ish(body), sent_at: nowIso(), receive_count: 0, visible_at: Date.now(), group_id: "", attrs: null, receipt: "", source_queue: "", sender })
  s.sent++
  return true
}

const service: DemoService = { name: "sqs", seed, routes }

void notFound
void ACCOUNT
void stableId
export default service
