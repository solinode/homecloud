import type { TrailEvent } from "@/lib/types"
import { getState, type DemoService } from "../engine"
import { ACCOUNT, DAY, HOUR, MIN, arn, hex, rng } from "../util"
import { INSTANCE, NAMES } from "../ids"

interface Actor {
  name: string
  ip: string
  ua: string
  key?: string
}

const CHROME_MAC = "Mozilla/5.0 (Macintosh; Intel Mac OS X 14_5) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 Safari/537.36"
const FIREFOX = "Mozilla/5.0 (X11; Linux x86_64; rv:128.0) Gecko/20100101 Firefox/128.0"
const CHROME_WIN = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 Safari/537.36"

const ACTORS: Record<string, Actor> = {
  "demo-admin": { name: "demo-admin", ip: "203.0.113.9", ua: CHROME_WIN },
  alice: { name: "alice", ip: "203.0.113.24", ua: CHROME_MAC },
  bob: { name: "bob", ip: "198.51.100.77", ua: FIREFOX },
  "ci-deploy": { name: "ci-deploy", ip: "192.0.2.44", ua: "aws-cli/2.17.5 md/Botocore#2.0.0 ua/2.0 os/linux#6.5.0 md/arch#x86_64 lang/python#3.11.9", key: "AKIADEMOEXAMPLE0CI01" },
}

const userArn = (n: string) => `arn:aws:iam::${ACCOUNT}:user/${n}`

type Spec = [action: string, method: string, path: string, resource: string, status?: number]

const fn = (n: string) => arn("lambda", `function:${n}`)
const inst = (id: string) => arn("ec2", `instance/${id}`)
const q = (n: string) => arn("sqs", n)
const bucket = (n: string) => `arn:aws:s3:::${n}`
const alarm = (n: string) => arn("cloudwatch", `alarm:${n}`)
const stack = (n: string) => arn("cloudformation", `stack/${n}`)

const FUNCS = [NAMES.paymentsFn, "shop-order-notifier", "shop-image-resizer", "shop-nightly-cleanup"]

// what each person does on an ordinary day, as [weight, spec builder]
type Gen = (r: () => number) => Spec
const pick = <T,>(r: () => number, xs: T[]): T => xs[Math.floor(r() * xs.length)]

const ALICE: [number, Gen][] = [
  [4, (r) => { const f = pick(r, FUNCS); return ["lambda:UpdateFunctionCode", "PUT", `/api/v1/lambda/functions/${f}/code`, fn(f)] }],
  [3, (r) => { const f = pick(r, FUNCS); return ["lambda:UpdateFunctionConfiguration", "PUT", `/api/v1/lambda/functions/${f}/configuration`, fn(f)] }],
  [5, (r) => { const f = pick(r, FUNCS); return ["lambda:InvokeFunction", "POST", `/api/v1/lambda/functions/${f}/invoke`, fn(f)] }],
  [3, () => ["s3:PutObject", "PUT", `/api/v1/s3/buckets/${NAMES.bucketAssets}/object`, bucket(`${NAMES.bucketAssets}/img/banner-autumn.png`)]],
  [2, () => ["dynamodb:PutItem", "POST", "/api/v1/dynamodb/tables/shop-orders/items", arn("dynamodb", "table/shop-orders")]],
  [2, () => ["sqs:SendMessage", "POST", `/api/v1/sqs/queues/${NAMES.ordersQueue}/messages`, q(NAMES.ordersQueue)]],
  [1, () => ["logs:PutRetentionPolicy", "PUT", `/api/v1/logs/groups/${encodeURIComponent(`/aws/lambda/${NAMES.paymentsFn}`)}/retention`, arn("logs", `log-group:/aws/lambda/${NAMES.paymentsFn}`)]],
  [1, () => ["cloudwatch:PutMetricAlarm", "PUT", "/api/v1/cloudwatch/alarms/shop-payments-errors", alarm("shop-payments-errors")]],
  [1, () => ["events:PutRule", "PUT", "/api/v1/events/rules/shop-nightly-cleanup", arn("events", "rule/default/shop-nightly-cleanup")]],
  [1, () => ["ssm:PutParameter", "PUT", "/api/v1/ssm/parameter", arn("ssm", "parameter/shop/feature-flags")]],
]

const BOB: [number, Gen][] = [
  [3, () => ["ec2:StopInstances", "POST", `/api/v1/ec2/instances/${INSTANCE.dev}/stop`, inst(INSTANCE.dev)]],
  [3, () => ["ec2:StartInstances", "POST", `/api/v1/ec2/instances/${INSTANCE.dev}/start`, inst(INSTANCE.dev)]],
  [2, () => ["ec2:RebootInstances", "POST", `/api/v1/ec2/instances/${INSTANCE.web2}/reboot`, inst(INSTANCE.web2)]],
  [1, () => ["ec2:RunInstances", "POST", "/api/v1/ec2/instances", inst("*")]],
  [2, () => ["ec2:CreateSnapshot", "POST", "/api/v1/ec2/snapshots", arn("ec2", "snapshot/*")]],
  [2, () => ["ec2:AuthorizeSecurityGroupIngress", "POST", "/api/v1/vpc/security-groups/sg-web/rules", arn("ec2", "security-group/*")]],
  [2, () => ["rds:CreateDBSnapshot", "POST", `/api/v1/rds/instances/${NAMES.db}/snapshots`, arn("rds", `db:${NAMES.db}`)]],
  [1, () => ["rds:RebootDBInstance", "POST", `/api/v1/rds/instances/${NAMES.db}/reboot`, arn("rds", `db:${NAMES.db}`)]],
  [3, () => ["autoscaling:SetDesiredCapacity", "PUT", `/api/v1/autoscaling/groups/${NAMES.asg}`, arn("autoscaling", `autoScalingGroup:*:autoScalingGroupName/${NAMES.asg}`)]],
  [2, () => ["route53:ChangeResourceRecordSets", "POST", "/api/v1/route53/zones/Z0DEMOEXAMPLE1/records", `arn:aws:route53:::hostedzone/Z0DEMOEXAMPLE1`]],
  [1, () => ["elasticloadbalancing:ModifyListener", "PUT", `/api/v1/elb/load-balancers/${NAMES.alb}/listeners/80`, arn("elasticloadbalancing", `loadbalancer/app/${NAMES.alb}/*`)]],
  [1, () => ["cloudwatch:PutMetricAlarm", "PUT", "/api/v1/cloudwatch/alarms/shop-web-2-cpu-high", alarm("shop-web-2-cpu-high")]],
]

const ADMIN: [number, Gen][] = [
  [2, () => ["iam:CreateUser", "POST", "/api/v1/iam/users", arn("iam", "user/contractor-dana", { region: null })]],
  [2, () => ["iam:AttachUserPolicy", "POST", "/api/v1/iam/users/alice/policies", arn("iam", "user/alice", { region: null })]],
  [1, () => ["iam:CreateAccessKey", "POST", "/api/v1/iam/users/ci-deploy/access-keys", arn("iam", "user/ci-deploy", { region: null })]],
  [1, () => ["iam:CreateRole", "POST", "/api/v1/iam/roles", arn("iam", "role/shop-lambda-exec", { region: null })]],
  [1, () => ["kms:CreateKey", "POST", "/api/v1/kms/keys", arn("kms", "key/*")]],
  [1, () => ["secretsmanager:CreateSecret", "POST", "/api/v1/secrets", arn("secretsmanager", "secret:shop/payments/api-key")]],
  [2, () => ["s3:CreateBucket", "POST", "/api/v1/s3/buckets", bucket("shop-exports-prod")]],
  [1, () => ["s3:PutBucketPolicy", "PUT", `/api/v1/s3/buckets/${NAMES.bucketAssets}/policy`, bucket(NAMES.bucketAssets)]],
  [1, () => ["sns:CreateTopic", "POST", "/api/v1/sns/topics", arn("sns", "shop-oncall-pager")]],
  [1, () => ["sns:Subscribe", "POST", `/api/v1/sns/topics/${NAMES.alertsTopic}/subscriptions`, arn("sns", NAMES.alertsTopic)]],
  [1, () => ["acm:RequestCertificate", "POST", "/api/v1/acm/certificates", arn("acm", "certificate/*")]],
  [1, () => ["cognito-idp:UpdateUserPool", "PUT", "/api/v1/cognito/user-pools/us-east-1_DemoPool1", arn("cognito-idp", "userpool/us-east-1_DemoPool1")]],
  [1, () => ["ec2:CreateKeyPair", "POST", "/api/v1/ec2/key-pairs", arn("ec2", "key-pair/*")]],
]

const CI: [number, Gen][] = [
  [3, () => ["ecr:PutImage", "PUT", "/api/v1/ecr/repositories/shop-web/images", arn("ecr", "repository/shop-web")]],
  [3, () => ["ecs:UpdateService", "PUT", `/api/v1/ecs/services/${NAMES.ecsWorker}`, arn("ecs", `service/${NAMES.ecsCluster}/${NAMES.ecsWorker}`)]],
  [3, () => ["lambda:UpdateFunctionCode", "PUT", `/api/v1/lambda/functions/${NAMES.paymentsFn}/code`, fn(NAMES.paymentsFn)]],
  [2, () => ["lambda:PublishVersion", "POST", `/api/v1/lambda/functions/${NAMES.paymentsFn}/versions`, fn(NAMES.paymentsFn)]],
  [2, () => ["s3:PutObject", "PUT", `/api/v1/s3/buckets/${NAMES.bucketAssets}/object`, bucket(`${NAMES.bucketAssets}/static/app.js`)]],
  [1, () => ["cloudformation:UpdateStack", "PUT", "/api/v1/cloudformation/stacks/shop-app", stack("shop-app")]],
  [1, () => ["ssm:PutParameter", "PUT", "/api/v1/ssm/parameter", arn("ssm", "parameter/shop/release/current")]],
]

/** failed attempts: [user, spec] with the 4xx status of the response */
const FAILURES: [string, Spec][] = [
  ["alice", ["iam:ListUsers", "GET", "/api/v1/iam/users", arn("iam", "user/*", { region: null }), 403]],
  ["alice", ["secretsmanager:GetSecretValue", "GET", "/api/v1/secrets/shop%2Fpayments%2Fapi-key/value", arn("secretsmanager", "secret:shop/payments/api-key"), 403]],
  ["alice", ["kms:ScheduleKeyDeletion", "POST", "/api/v1/kms/keys/3c9e1a7b-0d5f-4e1a-9a41-5f4f0a8ab1d2/schedule-deletion", arn("kms", "key/3c9e1a7b-0d5f-4e1a-9a41-5f4f0a8ab1d2"), 403]],
  ["bob", ["iam:CreateUser", "POST", "/api/v1/iam/users", arn("iam", "user/intern", { region: null }), 403]],
  ["bob", ["s3:DeleteBucket", "DELETE", `/api/v1/s3/buckets/${NAMES.bucketBackups}`, bucket(NAMES.bucketBackups), 403]],
  ["bob", ["rds:DeleteDBInstance", "DELETE", `/api/v1/rds/instances/${NAMES.db}`, arn("rds", `db:${NAMES.db}`), 403]],
  ["ci-deploy", ["iam:PassRole", "POST", "/api/v1/lambda/functions", arn("iam", "role/shop-admin-role", { region: null }), 403]],
  ["ci-deploy", ["lambda:UpdateFunctionCode", "PUT", "/api/v1/lambda/functions/shop-legacy-importer/code", fn("shop-legacy-importer"), 404]],
  ["alice", ["lambda:InvokeFunction", "POST", "/api/v1/lambda/functions/shop-refunds/invoke", fn("shop-refunds"), 404]],
  ["demo-admin", ["s3:CreateBucket", "POST", "/api/v1/s3/buckets", bucket(NAMES.bucketAssets), 409]],
  ["demo-admin", ["sqs:CreateQueue", "POST", "/api/v1/sqs/queues", q(NAMES.ordersQueue), 409]],
  ["bob", ["ec2:RunInstances", "POST", "/api/v1/ec2/instances", inst("*"), 400]],
  ["alice", ["dynamodb:PutItem", "POST", "/api/v1/dynamodb/tables/shop-orders/items", arn("dynamodb", "table/shop-orders"), 400]],
  ["ci-deploy", ["ecs:UpdateService", "PUT", "/api/v1/ecs/services/shop-worker-canary", arn("ecs", `service/${NAMES.ecsCluster}/shop-worker-canary`), 404]],
]

function weighted(r: () => number, list: [number, Gen][]): Spec {
  const total = list.reduce((a, [w]) => a + w, 0)
  let x = r() * total
  for (const [w, g] of list) {
    if ((x -= w) <= 0) return g(r)
  }
  return list[0][1](r)
}

function build(): TrailEvent[] {
  const out: TrailEvent[] = []
  const now = Date.now()
  let n = 0
  const add = (t: number, user: string, spec: Spec, r: () => number) => {
    if (t > now - 30_000) return
    const a = ACTORS[user]
    const [action, method, path, resource, status = method === "POST" && !action.includes("Invoke") && !action.includes("Stop") && !action.includes("Start") ? 201 : 200] = spec
    out.push({
      id: hex16(`${user}${action}${t}${n++}`),
      time: new Date(t).toISOString(),
      user,
      user_arn: userArn(user),
      access_key: a.key,
      action,
      resource,
      method,
      path,
      status,
      source_ip: a.ip,
      user_agent: a.ua,
      latency_ms: status === 403 ? 2 + Math.floor(r() * 5) : action.includes("Invoke") ? 80 + Math.floor(r() * 900) : 4 + Math.floor(r() * (action.startsWith("ec2:Run") || action.startsWith("rds") ? 700 : 90)),
    })
  }

  // ordinary activity: each person works in their own hours
  const shifts: Record<string, { list: [number, Gen][]; perDay: number; from: number; to: number; weekend: number }> = {
    alice: { list: ALICE, perDay: 15, from: 9, to: 18, weekend: 0.1 },
    bob: { list: BOB, perDay: 12, from: 7, to: 16, weekend: 0.15 },
    "demo-admin": { list: ADMIN, perDay: 6, from: 8, to: 20, weekend: 0.3 },
  }
  const startOfToday = new Date(now)
  startOfToday.setUTCHours(0, 0, 0, 0)
  for (let d = 0; d < 7; d++) {
    const dayStart = startOfToday.getTime() - d * DAY
    const wd = new Date(dayStart).getUTCDay()
    const weekend = wd === 0 || wd === 6
    for (const [user, s] of Object.entries(shifts)) {
      const r = rng(`trail:${user}:${d}`)
      const count = Math.round(s.perDay * (weekend ? s.weekend : 0.75 + r() * 0.5))
      for (let i = 0; i < count; i++) {
        const t = dayStart + (s.from + r() * (s.to - s.from)) * HOUR
        add(t, user, weighted(r, s.list), r)
      }
    }
  }
  // CI deploys come in bursts
  ;[[5.9, 14.1], [3.95, 10.4], [2.1, 16.8], [1, 15.3]].forEach(([daysAgo, hour], k) => {
    const r = rng(`trail:deploy:${k}`)
    const base = startOfToday.getTime() - Math.floor(daysAgo) * DAY + hour * HOUR
    const steps: Spec[] = [
      ["ecr:PutImage", "PUT", "/api/v1/ecr/repositories/shop-web/images", arn("ecr", "repository/shop-web")],
      ["lambda:UpdateFunctionCode", "PUT", `/api/v1/lambda/functions/${NAMES.paymentsFn}/code`, fn(NAMES.paymentsFn)],
      ["lambda:PublishVersion", "POST", `/api/v1/lambda/functions/${NAMES.paymentsFn}/versions`, fn(NAMES.paymentsFn)],
      ["ecs:UpdateService", "PUT", `/api/v1/ecs/services/${NAMES.ecsWorker}`, arn("ecs", `service/${NAMES.ecsCluster}/${NAMES.ecsWorker}`)],
      ["s3:PutObject", "PUT", `/api/v1/s3/buckets/${NAMES.bucketAssets}/object`, bucket(`${NAMES.bucketAssets}/static/app.${hex16(`app${k}`).slice(0, 6)}.js`)],
      ["ssm:PutParameter", "PUT", "/api/v1/ssm/parameter", arn("ssm", "parameter/shop/release/current")],
    ]
    if (k % 2 === 0) steps.push(["cloudformation:UpdateStack", "PUT", "/api/v1/cloudformation/stacks/shop-app", stack("shop-app")])
    steps.forEach((s, i) => add(base + i * (20_000 + r() * 30_000), "ci-deploy", s, r))
  })
  // a few scheduled CI actions
  {
    const r = rng("trail:ci-misc")
    for (let d = 0; d < 7; d++) add(startOfToday.getTime() - d * DAY + 2 * HOUR + r() * 600_000, "ci-deploy", weighted(r, CI), r)
  }
  // denied and failed calls
  FAILURES.forEach(([user, spec], i) => {
    const r = rng(`trail:fail:${i}`)
    add(now - (0.2 + r() * 6.5) * DAY, user, spec, r)
  })
  // one incident window: bob restarts web-2 while the CPU alarm fires
  {
    const r = rng("trail:incident")
    add(now - 38 * MIN, "bob", ["ec2:RebootInstances", "POST", `/api/v1/ec2/instances/${INSTANCE.web2}/reboot`, inst(INSTANCE.web2)], r)
    add(now - 33 * MIN, "bob", ["autoscaling:SetDesiredCapacity", "PUT", `/api/v1/autoscaling/groups/${NAMES.asg}`, arn("autoscaling", `autoScalingGroup:*:autoScalingGroupName/${NAMES.asg}`)], r)
  }
  return out.sort((a, b) => Date.parse(b.time) - Date.parse(a.time))
}

function hex16(s: string): string {
  const r = rng(s)
  let o = ""
  for (let i = 0; i < 32; i++) o += "0123456789abcdef"[Math.floor(r() * 16)]
  return o
}

interface TrailState {
  events: TrailEvent[]
}

/** recordTrail lets other demo services log a mutating call (newest first). */
export function recordTrail(e: Omit<TrailEvent, "id" | "time" | "user_arn" | "source_ip" | "user_agent" | "latency_ms"> & Partial<TrailEvent>) {
  const s = getState().trail as TrailState | undefined
  if (!s) return
  const a = ACTORS[e.user] ?? ACTORS["demo-admin"]
  s.events.unshift({
    id: hex(32),
    time: new Date().toISOString(),
    user_arn: userArn(e.user),
    source_ip: a.ip,
    user_agent: a.ua,
    latency_ms: 5 + Math.floor(Math.random() * 40),
    ...e,
  })
}

const service: DemoService = {
  name: "trail",
  seed: (): TrailState => ({ events: build() }),
  routes: (r) => {
    r.get("/api/v1/cloudtrail/events", ({ query }) => {
      const s = getState().trail as TrailState
      const limit = Math.max(1, Number(query.limit) || 200)
      const user = query.user
      const action = (query.action ?? "").toLowerCase()
      const text = (query.q ?? "").toLowerCase()
      const onlyErrors = query.errors === "true"
      const out: TrailEvent[] = []
      for (const e of s.events) {
        if (user && e.user !== user) continue
        if (action && !e.action.toLowerCase().includes(action)) continue
        if (onlyErrors && e.status < 400) continue
        if (text && !`${e.action} ${e.resource} ${e.user}`.toLowerCase().includes(text)) continue
        out.push(e)
        if (out.length >= limit) break
      }
      return out
    })
  },
}

export default service
