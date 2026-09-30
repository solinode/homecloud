import { badRequest, conflict, err, getState, later, notFound, unavailable, type DemoService, type Req, type Router, type State } from "../engine"
import { DAY, HOUR, MIN, ACCOUNT, REGION, ago, arn, clone, nowIso, stableId, uuid, hex, rng } from "../util"
import { NAMES, SG, SUBNET } from "../ids"
import type {
  EventInvokeConfig,
  EventSourceMapping,
  FunctionCode,
  InvokeResult,
  LambdaAccountSettings,
  LambdaAlias,
  LambdaFunction,
  LambdaRuntime,
  LayerVersion,
} from "@/lib/types"

const LATEST = "$LATEST"
const PUBLIC_BASE = "https://demo.homecloud.example"

// ---------------------------------------------------------------- state ----

export interface LambdaState {
  functions: LambdaFunction[]
  /** published versions, all functions */
  versions: LambdaFunction[]
  aliases: LambdaAlias[]
  /** function name -> path -> text */
  code: Record<string, Record<string, string>>
  /** function name -> extra file count for non-editable packages */
  bigPackage: Record<string, number>
  invokeConfigs: EventInvokeConfig[]
  layers: LayerVersion[]
  mappings: EventSourceMapping[]
  /** function name -> ISO time of the last invocation (drives Warm/Idle) */
  invoked: Record<string, string>
}

const S = (): LambdaState => getState().lambda as LambdaState

// ------------------------------------------------------------- runtimes ----

const PY_TEMPLATE = `import json

def lambda_handler(event, context):
    print("event:", json.dumps(event))
    name = (event.get("queryStringParameters") or {}).get("name") or event.get("name") or "world"
    return {
        "statusCode": 200,
        "headers": {"Content-Type": "application/json"},
        "body": json.dumps({"message": f"Hello, {name}!", "request_id": context.aws_request_id}),
    }
`

const NODE_TEMPLATE = `export const handler = async (event, context) => {
  console.log("event:", JSON.stringify(event));
  const name = event.queryStringParameters?.name ?? event.name ?? "world";
  return {
    statusCode: 200,
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ message: \`Hello, \${name}!\`, requestId: context.awsRequestId }),
  };
};
`

const RUBY_TEMPLATE = `require 'json'

def lambda_handler(event:, context:)
  puts "event: #{event.to_json}"
  { statusCode: 200, body: { message: "Hello, world!", request_id: context.aws_request_id }.to_json }
end
`

const rt = (name: string, label: string, image: string, handler: string, file: string, template: string, deprecated = false): LambdaRuntime => ({
  name,
  label,
  image: `public.ecr.aws/lambda/${image}`,
  default_handler: handler,
  default_file: file,
  template,
  ...(deprecated ? { deprecated: true } : {}),
})
const py = (v: string, dep = false) => rt(`python${v}`, `Python ${v}`, `python:${v}`, "lambda_function.lambda_handler", "lambda_function.py", PY_TEMPLATE, dep)
const node = (v: string, dep = false) => rt(`nodejs${v}.x`, `Node.js ${v}.x`, `nodejs:${v}`, "index.handler", "index.mjs", NODE_TEMPLATE, dep)

const RUNTIMES: LambdaRuntime[] = [
  py("3.13"),
  py("3.12"),
  py("3.11"),
  py("3.10"),
  py("3.9", true),
  node("22"),
  node("20"),
  node("18", true),
  rt("java21", "Java 21", "java:21", "example.Handler::handleRequest", "", ""),
  rt("java17", "Java 17", "java:17", "example.Handler::handleRequest", "", ""),
  rt("ruby3.3", "Ruby 3.3", "ruby:3.3", "lambda_function.lambda_handler", "lambda_function.rb", RUBY_TEMPLATE),
  rt("dotnet8", ".NET 8", "dotnet:8", "Function::Function.Handler::FunctionHandler", "", ""),
  rt("provided.al2023", "OS-only runtime (Amazon Linux 2023)", "provided:al2023", "bootstrap", "", ""),
  rt("provided.al2", "OS-only runtime (Amazon Linux 2)", "provided:al2", "bootstrap", "", ""),
]

const fnArn = (name: string) => arn("lambda", `function:${name}`)
const layerArn = (name: string) => arn("lambda", `layer:${name}`)
const roleArn = (name: string) => arn("iam", `role/${name}`, { region: null })
const sha = (seed: string) => btoa(stableId("", seed, 32)).slice(0, 44)

// ---------------------------------------------------------- source code ----

function pySource(name: string, body: string) {
  return `import json
import logging
import os

logger = logging.getLogger()
logger.setLevel(logging.INFO)


def handler(event, context):
    logger.info("received event: %s", json.dumps(event))
${body}
`
}

const CODE: Record<string, (fileBase: string) => Record<string, string>> = {
  [NAMES.paymentsFn]: () => ({
    "payments.py": pySource(
      NAMES.paymentsFn,
      `    order_id = event.get("order_id", "unknown")
    amount = int(event.get("amount", 0))
    if amount <= 0:
        raise ValueError("amount must be positive")
    # A real deployment would call the payment provider here.
    payment_id = "pay_" + context.aws_request_id[:8]
    logger.info("charged %s cents for order %s", amount, order_id)
    return {"status": "succeeded", "payment_id": payment_id, "order_id": order_id, "amount": amount, "currency": "usd"}`,
    ),
    "config.py": `import os

DB_HOST = os.environ.get("DB_HOST", "localhost")
PROVIDER_KEY = os.environ.get("PROVIDER_KEY", "")
ORDERS_QUEUE_URL = os.environ.get("ORDERS_QUEUE_URL", "")
`,
    "requirements.txt": "requests==2.32.3\npsycopg2-binary==2.9.9\n",
  }),
  "shop-order-processor": () => ({
    "index.mjs": `import { SQSClient, DeleteMessageCommand } from "@aws-sdk/client-sqs";

const sqs = new SQSClient({});

export const handler = async (event) => {
  const failures = [];
  for (const record of event.Records ?? []) {
    try {
      const order = JSON.parse(record.body);
      console.log("processing order", order.order_id, "items:", order.items?.length ?? 0);
      // reserve stock, charge the customer, start fulfillment ...
    } catch (err) {
      console.error("failed to process", record.messageId, err);
      failures.push({ itemIdentifier: record.messageId });
    }
  }
  return { batchItemFailures: failures };
};
`,
    "package.json": `{\n  "name": "shop-order-processor",\n  "version": "1.8.2",\n  "type": "module",\n  "dependencies": { "@aws-sdk/client-sqs": "^3.600.0" }\n}\n`,
  }),
  "shop-image-resizer": () => ({
    "resizer.py": `import io
import os

import boto3
from PIL import Image

s3 = boto3.client("s3")
SIZES = [(1600, 1600), (800, 800), (240, 240)]


def handler(event, context):
    for record in event["Records"]:
        bucket = record["s3"]["bucket"]["name"]
        key = record["s3"]["object"]["key"]
        body = s3.get_object(Bucket=bucket, Key=key)["Body"].read()
        img = Image.open(io.BytesIO(body))
        for w, h in SIZES:
            out = io.BytesIO()
            img.copy().resize((w, h)).save(out, format="WEBP", quality=82)
            s3.put_object(Bucket=os.environ["ASSETS_BUCKET"], Key=f"resized/{w}/{key}.webp", Body=out.getvalue())
    return {"resized": len(event["Records"])}
`,
  }),
  "shop-api-handler": () => ({
    "index.mjs": `const products = [
  { id: "p_100", name: "Canvas tote bag", price: 1800 },
  { id: "p_101", name: "Enamel mug", price: 1400 },
  { id: "p_102", name: "Sticker pack", price: 600 },
];

export const handler = async (event) => {
  const { rawPath, requestContext } = event;
  const method = requestContext?.http?.method ?? "GET";
  if (method === "GET" && rawPath === "/products") {
    return json(200, products);
  }
  if (method === "GET" && rawPath?.startsWith("/products/")) {
    const p = products.find((x) => x.id === rawPath.split("/").pop());
    return p ? json(200, p) : json(404, { message: "not found" });
  }
  return json(404, { message: "no route" });
};

const json = (statusCode, body) => ({ statusCode, headers: { "Content-Type": "application/json" }, body: JSON.stringify(body) });
`,
  }),
  "shop-nightly-report": () => ({
    "report.py": `import csv
import io
import os
from datetime import date, timedelta

import boto3

s3 = boto3.client("s3")


def handler(event, context):
    day = date.today() - timedelta(days=1)
    rows = [["order_id", "total_cents", "status"]]  # pulled from the orders database
    buf = io.StringIO()
    csv.writer(buf).writerows(rows)
    key = f"reports/daily/{day.isoformat()}.csv"
    s3.put_object(Bucket=os.environ["REPORT_BUCKET"], Key=key, Body=buf.getvalue().encode())
    return {"report": key, "rows": len(rows) - 1}
`,
  }),
  "shop-session-stream": () => ({
    "index.mjs": `export const handler = async (event) => {
  for (const record of event.Records) {
    if (record.eventName === "REMOVE") console.log("session expired", record.dynamodb.Keys.session_id.S);
  }
  return { processed: event.Records.length };
};
`,
  }),
}

function genericSource(runtime: string, handler: string): Record<string, string> {
  const base = handler.split(".")[0] || "index"
  if (runtime.startsWith("python")) return { [`${base}.py`]: PY_TEMPLATE.replace("lambda_handler", handler.split(".").pop() || "handler") }
  if (runtime.startsWith("nodejs")) return { [`${base}.mjs`]: NODE_TEMPLATE }
  if (runtime.startsWith("ruby")) return { [`${base}.rb`]: RUBY_TEMPLATE }
  return {}
}

const totalSize = (files: Record<string, string>) => Object.values(files).reduce((n, s) => n + s.length, 0)

// ----------------------------------------------------------------- seed ----

interface FnSpec extends Partial<LambdaFunction> {
  name: string
  runtime: string
  handler: string
  created: number
  modified: number
  versions?: number
}

function baseFn(s: FnSpec): LambdaFunction {
  const { created, modified, versions, ...rest } = s
  const files = CODE[s.name]?.("") ?? genericSource(s.runtime, s.handler)
  const zipSize = s.package_type === "Image" ? 0 : Math.round(totalSize(files) * 0.55) + 1024
  return {
    arn: fnArn(s.name),
    description: "",
    memory_mb: 128,
    timeout_seconds: 3,
    environment: null,
    code_sha256: sha(`${s.name}:latest`),
    code_size: zipSize,
    state: "Active",
    function_url: { enabled: false, auth_type: "NONE" },
    log_group: `/aws/lambda/${s.name}`,
    last_update_status: "Successful",
    package_type: "Zip",
    architectures: ["x86_64"],
    layers: [],
    reserved_concurrency: null,
    version: LATEST,
    last_version: versions ?? 0,
    revision_id: uuid(),
    last_modified: ago(modified),
    created_at: ago(created),
    tags: {},
    ...rest,
  } as LambdaFunction
}
function seedFunctions(): FnSpec[] {
  const dlq = arn("sqs", NAMES.ordersDlq)
  return [
    {
      name: NAMES.paymentsFn,
      runtime: "python3.12",
      handler: "payments.handler",
      description: "Charges customers for orders and records the payment",
      memory_mb: 512,
      timeout_seconds: 30,
      environment: { DB_HOST: "shop-db.c9demoexample.us-east-1.rds.amazonaws.com", PROVIDER_KEY: "demo-not-a-real-secret", ORDERS_QUEUE_URL: `https://sqs.${REGION}.amazonaws.com/${ACCOUNT}/${NAMES.ordersQueue}`, LOG_LEVEL: "INFO" },
      role: roleArn("shop-payments-role"),
      layers: [`${layerArn("shop-deps")}:3`],
      subnet_id: SUBNET.privateA,
      security_group_ids: [SG.web],
      dead_letter_target: dlq,
      reserved_concurrency: 50,
      function_url: { enabled: true, auth_type: "HC_IAM", url: `${PUBLIC_BASE}/lambda-url/${NAMES.paymentsFn}/` },
      tags: { app: "shop", team: "payments", env: "prod" },
      created: 96 * DAY,
      modified: 3 * DAY,
      versions: 5,
    },
    {
      name: "shop-order-processor",
      runtime: "nodejs20.x",
      handler: "index.handler",
      description: "Consumes the orders queue and starts fulfillment",
      memory_mb: 1024,
      timeout_seconds: 60,
      environment: { ORDERS_TABLE: "shop-orders", STATE_MACHINE_ARN: arn("states", `stateMachine:${NAMES.stateMachine}`), NODE_OPTIONS: "--enable-source-maps" },
      role: roleArn("shop-order-processor-role"),
      layers: [`${layerArn("shop-node-common")}:4`],
      dead_letter_target: dlq,
      tags: { app: "shop", team: "orders", env: "prod" },
      created: 88 * DAY,
      modified: 9 * DAY,
      versions: 3,
    },
    {
      name: "shop-image-resizer",
      runtime: "python3.12",
      handler: "resizer.handler",
      description: "Generates WebP thumbnails for uploaded product photos",
      memory_mb: 1536,
      timeout_seconds: 45,
      environment: { ASSETS_BUCKET: NAMES.bucketAssets, UPLOADS_BUCKET: NAMES.bucketUploads },
      role: roleArn("shop-image-resizer-role"),
      layers: [`${layerArn("shop-pillow")}:2`],
      architectures: ["arm64"],
      tags: { app: "shop", team: "web", env: "prod" },
      created: 61 * DAY,
      modified: 21 * DAY,
      versions: 2,
    },
    {
      name: "shop-api-handler",
      runtime: "nodejs22.x",
      handler: "index.handler",
      description: "Serves the public product catalog API",
      memory_mb: 256,
      timeout_seconds: 10,
      environment: { CATALOG_TABLE: "shop-catalog", CACHE_TTL: "60" },
      role: roleArn("shop-api-role"),
      function_url: { enabled: true, auth_type: "NONE", url: `${PUBLIC_BASE}/lambda-url/shop-api-handler/` },
      tags: { app: "shop", team: "web", env: "prod" },
      created: 74 * DAY,
      modified: 5 * DAY,
      versions: 4,
    },
    {
      name: "shop-nightly-report",
      runtime: "python3.11",
      handler: "report.handler",
      description: "Builds the daily sales report CSV",
      memory_mb: 2048,
      timeout_seconds: 300,
      environment: { REPORT_BUCKET: NAMES.bucketBackups, TZ: "UTC" },
      role: roleArn("shop-report-role"),
      subnet_id: SUBNET.privateB,
      security_group_ids: [SG.web],
      tags: { app: "shop", team: "analytics", env: "prod" },
      created: 140 * DAY,
      modified: 40 * DAY,
      versions: 1,
    },
    {
      name: "shop-notifier",
      runtime: "",
      handler: "",
      description: "Sends order emails and push notifications (container image)",
      package_type: "Image",
      image_uri: `${ACCOUNT}.dkr.ecr.${REGION}.amazonaws.com/shop/notifier:1.4.0`,
      image_config: { EntryPoint: ["/lambda-entrypoint.sh"], Command: ["app.handler"], WorkingDirectory: "/var/task" },
      memory_mb: 1024,
      timeout_seconds: 30,
      environment: { SENDER: "orders@shop.example.com", TEMPLATE_BUCKET: NAMES.bucketAssets },
      role: roleArn("shop-notifier-role"),
      code_sha256: sha("notifier-image"),
      tags: { app: "shop", team: "orders", env: "prod" },
      created: 33 * DAY,
      modified: 6 * DAY,
      versions: 2,
    },
    {
      name: "shop-inventory-sync",
      runtime: "java21",
      handler: "com.shop.inventory.SyncHandler::handleRequest",
      description: "Synchronizes stock levels with the warehouse system",
      memory_mb: 2048,
      timeout_seconds: 120,
      environment: { WAREHOUSE_URL: "https://warehouse.example.com/api", WAREHOUSE_TOKEN: "demo-not-a-real-secret" },
      role: roleArn("shop-inventory-role"),
      architectures: ["arm64"],
      tags: { app: "shop", team: "logistics", env: "prod" },
      created: 118 * DAY,
      modified: 14 * DAY,
      versions: 3,
    },
    {
      name: "shop-session-stream",
      runtime: "nodejs20.x",
      handler: "index.handler",
      description: "Reacts to expired shopping sessions in DynamoDB",
      memory_mb: 128,
      timeout_seconds: 15,
      environment: { LOG_LEVEL: "info" },
      role: roleArn("shop-stream-role"),
      tags: { app: "shop", env: "prod" },
      created: 27 * DAY,
      modified: 27 * DAY,
      versions: 0,
    },
  ]
}

function seed(): LambdaState {
  const specs = seedFunctions()
  const functions: LambdaFunction[] = []
  const versions: LambdaFunction[] = []
  const code: Record<string, Record<string, string>> = {}
  const bigPackage: Record<string, number> = {}
  for (const sp of specs) {
    const f = baseFn(sp)
    functions.push(f)
    if (f.package_type !== "Image") code[f.name] = CODE[f.name]?.("") ?? genericSource(f.runtime, f.handler)
    if (f.name === "shop-inventory-sync") {
      code[f.name] = {}
      bigPackage[f.name] = 214
      f.code_size = 48_300_000
    }
    const n = sp.versions ?? 0
    for (let i = 1; i <= n; i++) {
      const v: LambdaFunction = clone(f)
      v.version = String(i)
      v.arn = `${f.arn}:${i}`
      v.last_version = 0
      v.function_url = { enabled: false, auth_type: "NONE" }
      v.reserved_concurrency = null
      v.code_sha256 = i === n ? f.code_sha256 : sha(`${f.name}:v${i}`)
      v.version_description = ["Initial release", "Fix retry handling", "Bump dependencies", "Tune memory and timeout", "Add structured logging"][i - 1] ?? `Release ${i}`
      v.description = f.description
      v.last_modified = ago(f.name === NAMES.paymentsFn ? Math.max(3 * DAY + 4 * HOUR, (n - i + 1) * 17 * DAY) : Math.max(sp.modified + HOUR, (n - i + 1) * 12 * DAY))
      v.created_at = f.created_at
      v.revision_id = uuid()
      versions.push(v)
    }
  }
  const mkAlias = (fn: string, name: string, version: string, description: string, weights?: Record<string, number>): LambdaAlias => ({
    function: fn,
    name,
    arn: `${fnArn(fn)}:${name}`,
    function_version: version,
    description,
    additional_version_weights: weights ?? null,
    revision_id: uuid(),
  })
  const aliases = [
    mkAlias(NAMES.paymentsFn, "prod", "5", "Production traffic"),
    mkAlias(NAMES.paymentsFn, "canary", "4", "Weighted rollout of version 5", { "5": 0.1 }),
    mkAlias(NAMES.paymentsFn, "staging", LATEST, "Always the latest code"),
    mkAlias("shop-api-handler", "live", "4", "Serves the public API"),
    mkAlias("shop-order-processor", "prod", "3", "Production"),
  ]
  const invokeConfigs: EventInvokeConfig[] = [
    {
      function: NAMES.paymentsFn,
      qualifier: LATEST,
      maximum_retry_attempts: 1,
      maximum_event_age_seconds: 3600,
      on_failure: arn("sqs", NAMES.ordersDlq),
      last_modified: ago(30 * DAY),
    },
  ]
  const layer = (name: string, v: number, desc: string, rts: string[], archs: string[], size: number, age: number): LayerVersion => ({
    name,
    version: v,
    arn: `${layerArn(name)}:${v}`,
    layer_arn: layerArn(name),
    description: desc,
    compatible_runtimes: rts,
    compatible_architectures: archs,
    license_info: "MIT",
    code_sha256: sha(`${name}:${v}`),
    code_size: size,
    created_at: ago(age),
  })
  const layers = [
    layer("shop-deps", 1, "requests and psycopg2", ["python3.11", "python3.12"], ["x86_64"], 8_400_000, 120 * DAY),
    layer("shop-deps", 2, "Add pydantic", ["python3.11", "python3.12"], ["x86_64"], 12_900_000, 70 * DAY),
    layer("shop-deps", 3, "Pin psycopg2-binary 2.9.9", ["python3.11", "python3.12"], ["x86_64"], 13_100_000, 26 * DAY),
    layer("shop-pillow", 1, "Pillow 10.3", ["python3.12"], ["arm64"], 9_800_000, 58 * DAY),
    layer("shop-pillow", 2, "Pillow 10.4 with WebP support", ["python3.12"], ["arm64"], 10_600_000, 34 * DAY),
    layer("shop-node-common", 1, "Logging helpers", ["nodejs20.x"], ["x86_64", "arm64"], 410_000, 100 * DAY),
    layer("shop-node-common", 2, "Add retry helper", ["nodejs20.x"], ["x86_64", "arm64"], 520_000, 80 * DAY),
    layer("shop-node-common", 3, "Tracing", ["nodejs20.x", "nodejs22.x"], ["x86_64", "arm64"], 890_000, 45 * DAY),
    layer("shop-node-common", 4, "AWS SDK v3 clients", ["nodejs20.x", "nodejs22.x"], ["x86_64", "arm64"], 2_150_000, 12 * DAY),
  ]
  const ordersArn = arn("sqs", NAMES.ordersQueue)
  const mappings: EventSourceMapping[] = [
    {
      id: uuid(),
      function_name: "shop-order-processor",
      queue_name: NAMES.ordersQueue,
      event_source_arn: ordersArn,
      batch_size: 10,
      batching_window_seconds: 5,
      function_response_types: ["ReportBatchItemFailures"],
      enabled: true,
      last_processing_result: "OK",
      last_invoked_at: ago(2 * MIN),
      created_at: ago(85 * DAY),
    },
    {
      id: uuid(),
      function_name: NAMES.paymentsFn,
      queue_name: "shop-payments-requests",
      event_source_arn: arn("sqs", "shop-payments-requests"),
      batch_size: 1,
      batching_window_seconds: 0,
      function_response_types: [],
      enabled: true,
      last_processing_result: "OK",
      last_invoked_at: ago(9 * MIN),
      created_at: ago(60 * DAY),
    },
    {
      id: uuid(),
      function_name: "shop-session-stream",
      queue_name: "",
      event_source_arn: arn("dynamodb", `table/shop-sessions/stream/${new Date(Date.now() - 27 * DAY).toISOString().slice(0, 19)}`),
      batch_size: 100,
      batching_window_seconds: 1,
      function_response_types: [],
      enabled: true,
      last_processing_result: "OK",
      last_invoked_at: ago(4 * MIN),
      created_at: ago(27 * DAY),
      starting_position: "LATEST",
      checkpoint: hex(20),
      maximum_retry_attempts: 3,
      bisect_batch_on_function_error: true,
      on_failure: arn("sqs", NAMES.ordersDlq),
    },
    {
      id: uuid(),
      function_name: "shop-image-resizer",
      queue_name: "shop-image-jobs",
      event_source_arn: arn("sqs", "shop-image-jobs"),
      batch_size: 5,
      batching_window_seconds: 10,
      function_response_types: [],
      enabled: false,
      last_processing_result: "No records processed",
      last_invoked_at: ago(6 * DAY),
      created_at: ago(40 * DAY),
    },
  ]
  const invoked: Record<string, string> = {
    [NAMES.paymentsFn]: ago(3 * MIN),
    "shop-order-processor": ago(2 * MIN),
    "shop-api-handler": ago(40 * 1000),
    "shop-session-stream": ago(4 * MIN),
    "shop-notifier": ago(11 * MIN),
    "shop-image-resizer": ago(6 * HOUR),
    "shop-nightly-report": ago(5 * HOUR),
    "shop-inventory-sync": ago(2 * HOUR),
  }
  return { functions, versions, aliases, code, bigPackage, invokeConfigs, layers, mappings, invoked }
}

// -------------------------------------------------------------- helpers ----

const WARM_MS = 15 * MIN

function getFn(name: string): LambdaFunction {
  const f = S().functions.find((x) => x.name === name)
  if (!f) throw err(404, "ResourceNotFound", `Function not found: ${fnArn(name)}`)
  return f
}

function touch(f: LambdaFunction) {
  f.last_modified = nowIso()
  f.revision_id = uuid()
}

/** view is the function as the API returns it (with derived fields). */
function view(f: LambdaFunction): LambdaFunction {
  return clone(f)
}

function resolveQualifier(name: string, q?: string): LambdaFunction {
  const latest = getFn(name)
  if (!q || q === LATEST) return latest
  if (/^\d+$/.test(q)) {
    const v = S().versions.find((x) => x.name === name && x.version === q)
    if (!v) throw err(404, "ResourceNotFound", `Function not found: ${fnArn(name)}:${q}`)
    return v
  }
  const a = S().aliases.find((x) => x.function === name && x.name === q)
  if (!a) throw err(404, "ResourceNotFound", `Function not found: ${fnArn(name)}:${q}`)
  return a.function_version === LATEST ? latest : (S().versions.find((x) => x.name === name && x.version === a.function_version) ?? latest)
}

function warmCount(name: string): number {
  const t = S().invoked[name]
  if (!t) return 0
  const age = Date.now() - new Date(t).getTime()
  if (age > WARM_MS) return 0
  return 1 + Math.floor(rng(name)() * 3)
}

function accountSettings(): LambdaAccountSettings {
  const st = S()
  const total = st.functions.reduce((n, f) => n + f.code_size, 0) + st.layers.reduce((n, l) => n + l.code_size, 0)
  return {
    AccountLimit: { TotalCodeSize: 80_530_636_800, CodeSizeUnzipped: 262_144_000, CodeSizeZipped: 52_428_800, ConcurrentExecutions: 1000, UnreservedConcurrentExecutions: 1000 - st.functions.reduce((n, f) => n + (f.reserved_concurrency ?? 0), 0) },
    AccountUsage: { TotalCodeSize: total, FunctionCount: st.functions.length },
  }
}

const NAME_RE = /^[a-zA-Z0-9_-]{1,64}$/

function validateConfig(f: Partial<LambdaFunction>, image: boolean) {
  if (f.memory_mb !== undefined && (f.memory_mb < 128 || f.memory_mb > 10240)) throw badRequest("Memory size must be between 128 and 10240 MB")
  if (f.timeout_seconds !== undefined && (f.timeout_seconds < 1 || f.timeout_seconds > 900)) throw badRequest("Timeout must be between 1 and 900 seconds")
  if (!image && f.runtime !== undefined && f.runtime && !RUNTIMES.some((r) => r.name === f.runtime)) throw badRequest(`Unsupported runtime "${f.runtime}"`)
  if (!image && f.runtime !== undefined && /^(python|nodejs|ruby)/.test(f.runtime) && f.handler !== undefined) {
    const i = f.handler.lastIndexOf(".")
    if (i <= 0 || i === f.handler.length - 1) throw badRequest("Handler must have the form file.function")
  }
}

function checkLayers(layers: string[]) {
  if (layers.length > 5) throw badRequest("A function can use at most 5 layers")
  for (const l of layers) {
    if (!S().layers.some((x) => x.arn === l)) throw notFound("layer version", l)
  }
}

/** settle marks an in-flight function (create or update) ready. */
function settle(name: string) {
  const f = S().functions.find((x) => x.name === name)
  if (!f) return
  f.state = "Active"
  f.state_reason = undefined
  f.state_reason_code = undefined
  f.last_update_status = "Successful"
}

function markUpdating(f: LambdaFunction) {
  f.last_update_status = "InProgress"
  later(1500, () => settle(f.name))
}

// ------------------------------------------------------------ canned run ----

const pad = (n: number) => String(n).padStart(2, "0")
const stamp = () => {
  const d = new Date()
  return `${d.getUTCFullYear()}-${pad(d.getUTCMonth() + 1)}-${pad(d.getUTCDate())}T${pad(d.getUTCHours())}:${pad(d.getUTCMinutes())}:${pad(d.getUTCSeconds())}.${String(d.getUTCMilliseconds()).padStart(3, "0")}Z`
}

// eslint-disable-next-line @typescript-eslint/no-explicit-any
type Json = any

function isHttpEvent(e: Json) {
  return e && typeof e === "object" && (e.requestContext || e.httpMethod || e.rawPath)
}

function cannedResult(f: LambdaFunction, event: Json, rid: string): { payload: unknown; lines: string[] } {
  const ev = event && typeof event === "object" ? event : {}
  const nm = f.name
  if (isHttpEvent(ev)) {
    const path: string = ev.rawPath ?? ev.path ?? "/"
    const body = nm === "shop-api-handler" && path.startsWith("/products") ? [{ id: "p_100", name: "Canvas tote bag", price: 1800 }, { id: "p_101", name: "Enamel mug", price: 1400 }] : { message: `Hello from ${nm}`, request_id: rid }
    return { payload: { statusCode: 200, headers: { "Content-Type": "application/json" }, body: JSON.stringify(body) }, lines: [`handled ${ev.requestContext?.http?.method ?? ev.httpMethod ?? "GET"} ${path}`] }
  }
  if (Array.isArray(ev.Records)) {
    const n = ev.Records.length
    if (nm === "shop-order-processor") return { payload: { batchItemFailures: [] }, lines: [`processing ${n} record(s)`, `processed ${n} order(s)`] }
    return { payload: { processed: n }, lines: [`processing ${n} record(s)`] }
  }
  switch (nm) {
    case NAMES.paymentsFn: {
      const amount = Number(ev.amount ?? 4999)
      const order = String(ev.order_id ?? "ord_10432")
      return { payload: { status: "succeeded", payment_id: `pay_${rid.slice(0, 8)}`, order_id: order, amount, currency: "usd" }, lines: [`charged ${amount} cents for order ${order}`] }
    }
    case "shop-nightly-report":
      return { payload: { report: `reports/daily/${new Date(Date.now() - DAY).toISOString().slice(0, 10)}.csv`, rows: 318 }, lines: ["queried 318 orders", "uploaded report to s3"] }
    case "shop-inventory-sync":
      return { payload: { synced: 1204, updated: 37, failed: 0 }, lines: ["fetched 1204 SKUs from warehouse", "updated 37 stock levels"] }
    case "shop-notifier":
      return { payload: { sent: true, channel: "email", message_id: `msg_${hex(10)}` }, lines: ["rendered template order-confirmation", "sent email"] }
    case "shop-image-resizer":
      return { payload: { resized: 1 }, lines: ["resized image to 3 variants"] }
    default:
      return { payload: { message: `Hello from ${nm}`, echo: ev }, lines: ["invoked"] }
  }
}

function invokeFn(f: LambdaFunction, event: Json, executed: string): InvokeResult {
  const st = S()
  const rid = uuid()
  const cold = warmCount(f.name) === 0
  const fail = !!(event && typeof event === "object" && (event.fail || event.error || event.simulate_error))
  const initMs = cold ? 180 + Math.floor(Math.random() * 300) : 0
  const dur = Math.round((f.runtime.startsWith("java") ? 120 : 8) + Math.random() * 90 + (cold ? 40 : 0))
  const py = f.runtime.startsWith("python")
  const log = (level: string, msg: string) => (py ? `[${level}]\t${stamp()}\t${rid}\t${msg}` : `${stamp()}\t${rid}\t${level}\t${msg}`)
  const lines = [`START RequestId: ${rid} Version: ${executed}`]
  let payload: unknown
  let functionError: string | undefined
  if (fail) {
    const msg = String(event.error ?? "Simulated failure requested by the test event")
    functionError = "Unhandled"
    payload = py
      ? { errorMessage: msg, errorType: "RuntimeError", requestId: rid, stackTrace: ['  File "/var/task/handler.py", line 14, in handler\n    raise RuntimeError(msg)\n'] }
      : { errorType: "Error", errorMessage: msg, trace: [`Error: ${msg}`, "    at Runtime.handler (file:///var/task/index.mjs:12:11)", "    at Runtime.handleOnceNonStreaming (file:///var/runtime/index.mjs:1173:29)"] }
    lines.push(log("ERROR", `Invoke Error ${JSON.stringify(payload)}`))
  } else {
    const c = cannedResult(f, event, rid)
    payload = c.payload
    lines.push(log("INFO", `received event: ${JSON.stringify(event ?? {}).slice(0, 200)}`))
    for (const l of c.lines) lines.push(log("INFO", l))
  }
  lines.push(`END RequestId: ${rid}`)
  const billed = Math.ceil((dur + initMs) / 1) || 1
  lines.push(`REPORT RequestId: ${rid}\tDuration: ${dur}.${Math.floor(Math.random() * 90 + 10)} ms\tBilled Duration: ${billed} ms\tMemory Size: ${f.memory_mb} MB\tMax Memory Used: ${Math.round(f.memory_mb * (0.18 + Math.random() * 0.2))} MB${cold ? `\tInit Duration: ${initMs}.${Math.floor(Math.random() * 90 + 10)} ms` : ""}`)
  st.invoked[f.name] = nowIso()
  return {
    request_id: rid,
    status_code: 200,
    payload,
    ...(functionError ? { function_error: functionError } : {}),
    logs: lines.join("\n") + "\n",
    duration_ms: dur,
    billed_duration_ms: billed,
    cold_start: cold,
    executed_version: executed,
  }
}

// --------------------------------------------------------------- routes ----

const q = (r: Req, k: string) => r.query[k]

function createFunction(r: Req) {
  const b = r.body ?? {}
  const name = String(b.name ?? "").trim()
  if (!NAME_RE.test(name)) throw badRequest("Function names are 1-64 letters, digits, hyphens or underscores")
  const st = S()
  if (st.functions.some((f) => f.name === name)) throw conflict(`Function already exist: ${fnArn(name)}`)
  const image = b.package_type === "Image"
  if (image && !b.image_uri) throw badRequest("image_uri is required for package type Image")
  const runtime: string = image ? "" : (b.runtime ?? "python3.12")
  const rtDef = RUNTIMES.find((x) => x.name === runtime)
  const handler: string = image ? "" : (b.handler ?? rtDef?.default_handler ?? "index.handler")
  validateConfig({ runtime, handler, memory_mb: b.memory_mb, timeout_seconds: b.timeout_seconds }, image)
  if (b.layers) checkLayers(b.layers)
  let files: Record<string, string> = {}
  if (!image) {
    files = b.code?.files && Object.keys(b.code.files).length ? { ...b.code.files } : rtDef?.template ? { [rtDef.default_file]: rtDef.template } : {}
  }
  const now = nowIso()
  const f: LambdaFunction = {
    name,
    arn: fnArn(name),
    runtime,
    handler,
    description: b.description ?? "",
    memory_mb: b.memory_mb ?? 128,
    timeout_seconds: b.timeout_seconds ?? 3,
    environment: b.environment && Object.keys(b.environment).length ? b.environment : null,
    code_sha256: sha(`${name}:${now}`),
    code_size: image ? 0 : b.code?.zip_base64 ? Math.round(String(b.code.zip_base64).length * 0.75) : totalSize(files) + 512,
    state: "Pending",
    state_reason: "The function is being created.",
    state_reason_code: "Creating",
    function_url: { enabled: false, auth_type: "NONE" },
    log_group: `/aws/lambda/${name}`,
    subnet_id: b.subnet_id || undefined,
    security_group_ids: b.security_group_ids,
    last_update_status: "InProgress",
    role: b.role || roleArn(`${name}-role`),
    package_type: image ? "Image" : "Zip",
    image_uri: image ? b.image_uri : undefined,
    architectures: b.architectures?.length ? b.architectures : ["x86_64"],
    layers: b.layers ?? [],
    dead_letter_target: b.dead_letter_target || undefined,
    reserved_concurrency: null,
    version: LATEST,
    last_version: 0,
    revision_id: uuid(),
    last_modified: now,
    created_at: now,
    tags: b.tags ?? {},
  }
  st.functions.push(f)
  if (!image) st.code[name] = files
  later(2500, () => settle(name))
  if (b.publish) {
    later(2600, () => publish(name, ""))
  }
  return view(f)
}

function publish(name: string, description: string): LambdaFunction {
  const st = S()
  const cur = getFn(name)
  if (cur.last_update_status === "InProgress") throw conflict(`The operation cannot be performed at this time. An update is in progress for resource: ${cur.arn}`)
  const prev = st.versions.filter((v) => v.name === name).sort((a, b) => Number(b.version) - Number(a.version))[0]
  if (prev && prev.code_sha256 === cur.code_sha256 && JSON.stringify(prev.environment) === JSON.stringify(cur.environment) && prev.memory_mb === cur.memory_mb && prev.timeout_seconds === cur.timeout_seconds && prev.handler === cur.handler) return clone(prev)
  const n = (cur.last_version ?? 0) + 1
  const v: LambdaFunction = clone(cur)
  v.version = String(n)
  v.arn = `${cur.arn}:${n}`
  v.last_version = 0
  v.function_url = { enabled: false, auth_type: "NONE" }
  v.reserved_concurrency = null
  v.version_description = description
  if (description) v.description = description
  v.revision_id = uuid()
  v.last_modified = nowIso()
  st.versions.push(v)
  cur.last_version = n
  return clone(v)
}

function deleteFunction(name: string, qualifier?: string) {
  const st = S()
  getFn(name)
  if (qualifier && qualifier !== LATEST) {
    if (!/^\d+$/.test(qualifier)) throw badRequest("qualifier must be a published version number")
    const i = st.versions.findIndex((v) => v.name === name && v.version === qualifier)
    if (i < 0) throw err(404, "ResourceNotFound", `Function not found: ${fnArn(name)}:${qualifier}`)
    const a = st.aliases.find((x) => x.function === name && (x.function_version === qualifier || qualifier in (x.additional_version_weights ?? {})))
    if (a) throw conflict(`Version ${qualifier} is used by alias ${a.name}; delete or repoint the alias first`)
    st.versions.splice(i, 1)
    return
  }
  st.functions = st.functions.filter((f) => f.name !== name)
  st.versions = st.versions.filter((v) => v.name !== name)
  st.aliases = st.aliases.filter((a) => a.function !== name)
  st.invokeConfigs = st.invokeConfigs.filter((c) => c.function !== name)
  st.mappings = st.mappings.filter((m) => m.function_name.split(":")[0] !== name)
  delete st.code[name]
  delete st.invoked[name]
  // remove API Gateway routes and function-targeted rules that pointed at it
  const apigw = getState().apigateway as { apis?: { routes: { function_name: string }[] | null }[] } | undefined
  for (const a of apigw?.apis ?? []) a.routes = (a.routes ?? []).filter((r) => r.function_name !== name)
}

function aliasBody(name: string, b: Json, existing?: LambdaAlias): LambdaAlias {
  const st = S()
  const ver = String(b.function_version ?? existing?.function_version ?? "")
  if (!ver) throw badRequest("function_version is required")
  if (ver !== LATEST && !st.versions.some((v) => v.name === name && v.version === ver)) throw err(404, "ResourceNotFound", `Function not found: ${fnArn(name)}:${ver}`)
  let weights: Record<string, number> | null = existing?.additional_version_weights ?? null
  if ("additional_version_weights" in b) weights = b.additional_version_weights && Object.keys(b.additional_version_weights).length ? b.additional_version_weights : null
  if (weights) {
    if (Object.keys(weights).length > 1) throw badRequest("Number of items in AdditionalVersionWeights must be less than or equal to 1")
    for (const [v, w] of Object.entries(weights)) {
      if (typeof w !== "number" || w < 0 || w > 1) throw badRequest("AdditionalVersionWeights values must be between 0.0 and 1.0")
      if (!st.versions.some((x) => x.name === name && x.version === v)) throw err(404, "ResourceNotFound", `Function not found: ${fnArn(name)}:${v}`)
    }
  }
  return {
    function: name,
    name: existing?.name ?? String(b.name ?? ""),
    arn: existing?.arn ?? `${fnArn(name)}:${b.name}`,
    function_version: ver,
    description: b.description ?? existing?.description ?? "",
    additional_version_weights: weights,
    revision_id: uuid(),
  }
}

function routes(r: Router) {
  r.get("/api/v1/lambda/runtimes", () => RUNTIMES)
  r.get("/api/v1/lambda/account", () => accountSettings())

  // ---- functions
  r.get("/api/v1/lambda/functions", () => [...S().functions].sort((a, b) => a.name.localeCompare(b.name)).map(view))
  r.post("/api/v1/lambda/functions", (req) => createFunction(req))
  r.get("/api/v1/lambda/functions/:name", ({ params }) => {
    const st = S()
    const f = getFn(params.name)
    const n = warmCount(f.name)
    const cfg = st.invokeConfigs.find((c) => c.function === f.name && c.qualifier === LATEST)
    return {
      configuration: view(f),
      environment_state: n > 0 ? "Warm" : "Idle",
      environments: n,
      concurrent_executions: n > 0 && Date.now() - new Date(st.invoked[f.name]).getTime() < 30_000 ? 1 : 0,
      concurrency_limit: f.reserved_concurrency ?? 1000 - st.functions.reduce((s, x) => s + (x.reserved_concurrency ?? 0), 0),
      aliases: st.aliases.filter((a) => a.function === f.name),
      event_invoke_config: cfg ?? null,
    }
  })
  r.patch("/api/v1/lambda/functions/:name", ({ params, body: b }) => {
    const f = getFn(params.name)
    b = b ?? {}
    const image = f.package_type === "Image"
    if (image && b.runtime) throw badRequest("Runtime is not supported for functions with package type Image")
    validateConfig({ runtime: b.runtime, handler: b.handler, memory_mb: b.memory_mb, timeout_seconds: b.timeout_seconds }, image)
    if (b.runtime && b.handler === undefined && /^(python|nodejs|ruby)/.test(b.runtime) && !/^(python|nodejs|ruby)/.test(f.runtime)) throw badRequest("Handler is required when changing to this runtime")
    if (b.layers) checkLayers(b.layers)
    if (b.revision_id && b.revision_id !== f.revision_id) throw err(412, "PreconditionFailed", "The Revision Id provided does not match the latest Revision Id.")
    if (f.last_update_status === "InProgress") throw conflict(`The operation cannot be performed at this time. An update is in progress for resource: ${f.arn}`)
    for (const k of ["runtime", "handler", "description", "memory_mb", "timeout_seconds", "role", "layers", "architectures", "image_config", "subnet_id", "security_group_ids", "dead_letter_target", "tags"] as const) {
      if (b[k] !== undefined && b[k] !== null) (f as unknown as Record<string, unknown>)[k] = b[k]
    }
    if (b.subnet_id === "") {
      f.subnet_id = undefined
      f.security_group_ids = undefined
    }
    if (b.dead_letter_target === "") f.dead_letter_target = undefined
    if (b.environment !== undefined) f.environment = b.environment && Object.keys(b.environment).length ? b.environment : null
    touch(f)
    markUpdating(f)
    return view(f)
  })
  r.del("/api/v1/lambda/functions/:name", ({ params, query }) => {
    deleteFunction(params.name, query.qualifier)
  })
  r.get("/api/v1/lambda/functions/:name/code", ({ params, query }) => {
    const f = resolveQualifier(params.name, query.qualifier)
    if (query.format === "zip") throw unavailable("Downloading the deployment package")
    if (f.package_type === "Image") return { files: {}, editable: false, file_count: 0, image_uri: f.image_uri }
    const st = S()
    const big = st.bigPackage[f.name]
    if (big) return { files: {}, editable: false, file_count: big, sha256_hex: stableId("", `${f.name}:code`, 64) } as FunctionCode
    const files = st.code[f.name] ?? {}
    return { files: clone(files), editable: true, file_count: Object.keys(files).length, sha256_hex: stableId("", `${f.name}:${f.code_sha256}`, 64) } as FunctionCode
  })
  r.put("/api/v1/lambda/functions/:name/code", ({ params, body: b }) => {
    const f = getFn(params.name)
    const st = S()
    b = b ?? {}
    if (f.last_update_status === "InProgress") throw conflict(`The operation cannot be performed at this time. An update is in progress for resource: ${f.arn}`)
    if (b.image_uri) {
      if (f.package_type !== "Image") throw badRequest("image_uri can only be used with functions of package type Image")
      f.image_uri = b.image_uri
      f.code_sha256 = sha(`${f.name}:${b.image_uri}`)
    } else if (b.files && typeof b.files === "object") {
      st.code[f.name] = { ...b.files }
      delete st.bigPackage[f.name]
      f.code_size = totalSize(b.files) + 512
      f.code_sha256 = sha(`${f.name}:${nowIso()}`)
    } else if (b.zip_base64) {
      f.code_size = Math.round(String(b.zip_base64).length * 0.75)
      f.code_sha256 = sha(`${f.name}:${nowIso()}`)
    } else throw badRequest("provide zip_base64, files or image_uri")
    touch(f)
    markUpdating(f)
    if (b.publish) later(1600, () => publish(f.name, ""))
    return view(f)
  })
  r.post("/api/v1/lambda/functions/:name/invoke", ({ params, query, body }) => {
    const f = resolveQualifier(params.name, query.qualifier)
    if (f.state !== "Active") throw err(409, "ResourceConflict", `The function is currently in the ${f.state} state and cannot be invoked`)
    const executed = f.version ?? LATEST
    if (query.invocation_type === "Event") {
      S().invoked[f.name] = nowIso()
      return { status_code: 202, request_id: uuid() }
    }
    return invokeFn(f, body, executed)
  })
  r.get("/api/v1/lambda/functions/:name/policy", ({ params }) => {
    getFn(params.name)
    return { Version: "2012-10-17", Id: "default", Statement: [] }
  })
  r.put("/api/v1/lambda/functions/:name/url", ({ params, body: b }) => {
    const f = getFn(params.name)
    b = b ?? {}
    const auth = b.auth_type === "AWS_IAM" || b.auth_type === "HC_IAM" ? "HC_IAM" : b.auth_type === "NONE" || !b.auth_type ? "NONE" : null
    if (!auth) throw badRequest("AuthType must be NONE or AWS_IAM")
    f.function_url = b.enabled ? { enabled: true, auth_type: auth, url: `${PUBLIC_BASE}/lambda-url/${f.name}/` } : { enabled: false, auth_type: auth }
    touch(f)
    return view(f)
  })

  // ---- versions and aliases
  r.get("/api/v1/lambda/functions/:name/versions", ({ params }) => {
    const f = getFn(params.name)
    const vs = S().versions.filter((v) => v.name === f.name).sort((a, b) => Number(a.version) - Number(b.version))
    return [view(f), ...vs.map(clone)]
  })
  r.post("/api/v1/lambda/functions/:name/versions", ({ params, body }) => publish(params.name, String(body?.description ?? "")))
  r.get("/api/v1/lambda/functions/:name/aliases", ({ params }) => {
    getFn(params.name)
    return S().aliases.filter((a) => a.function === params.name).sort((a, b) => a.name.localeCompare(b.name))
  })
  r.post("/api/v1/lambda/functions/:name/aliases", ({ params, body: b }) => {
    getFn(params.name)
    const name = String(b?.name ?? "")
    if (!/^[a-zA-Z][a-zA-Z0-9_-]{0,127}$/.test(name) || /^\d+$/.test(name)) throw badRequest("Alias names start with a letter and use letters, digits, hyphens or underscores")
    if (S().aliases.some((a) => a.function === params.name && a.name === name)) throw conflict(`Alias already exists: ${fnArn(params.name)}:${name}`)
    const a = aliasBody(params.name, b)
    S().aliases.push(a)
    return a
  })
  r.patch("/api/v1/lambda/functions/:name/aliases/:alias", ({ params, body: b }) => {
    getFn(params.name)
    const st = S()
    const i = st.aliases.findIndex((a) => a.function === params.name && a.name === params.alias)
    if (i < 0) throw err(404, "ResourceNotFound", `Alias not found: ${fnArn(params.name)}:${params.alias}`)
    if (b?.revision_id && b.revision_id !== st.aliases[i].revision_id) throw err(412, "PreconditionFailed", "The Revision Id provided does not match the latest Revision Id.")
    st.aliases[i] = aliasBody(params.name, b ?? {}, st.aliases[i])
    return st.aliases[i]
  })
  r.del("/api/v1/lambda/functions/:name/aliases/:alias", ({ params }) => {
    getFn(params.name)
    const st = S()
    const before = st.aliases.length
    st.aliases = st.aliases.filter((a) => !(a.function === params.name && a.name === params.alias))
    if (st.aliases.length === before) throw err(404, "ResourceNotFound", `Alias not found: ${fnArn(params.name)}:${params.alias}`)
  })

  // ---- concurrency and async config
  r.put("/api/v1/lambda/functions/:name/concurrency", ({ params, body }) => {
    const f = getFn(params.name)
    const n = body?.reserved_concurrent_executions
    if (typeof n !== "number") throw badRequest("reserved_concurrent_executions is required")
    const others = S().functions.filter((x) => x.name !== f.name).reduce((s, x) => s + (x.reserved_concurrency ?? 0), 0)
    if (n < 0 || n > 1000 - 100 - others) throw badRequest(`Specified ReservedConcurrentExecutions for function decreases account's UnreservedConcurrentExecution below its minimum value of [100]`)
    f.reserved_concurrency = n
    touch(f)
    return { reserved_concurrent_executions: n }
  })
  r.del("/api/v1/lambda/functions/:name/concurrency", ({ params }) => {
    const f = getFn(params.name)
    f.reserved_concurrency = null
    touch(f)
  })
  const invokeCfg = (name: string, qual?: string) => {
    getFn(name)
    if (qual && qual !== LATEST) resolveQualifier(name, qual)
    return S().invokeConfigs.find((c) => c.function === name && c.qualifier === (qual || LATEST))
  }
  r.get("/api/v1/lambda/functions/:name/event-invoke-config", ({ params, query }) => {
    const c = invokeCfg(params.name, query.qualifier)
    if (!c) throw err(404, "ResourceNotFound", `The function ${fnArn(params.name)} doesn't have an EventInvokeConfig`)
    return c
  })
  r.put("/api/v1/lambda/functions/:name/event-invoke-config", ({ params, query, body: b }) => {
    invokeCfg(params.name, query.qualifier)
    b = b ?? {}
    if (b.maximum_retry_attempts !== undefined && (b.maximum_retry_attempts < 0 || b.maximum_retry_attempts > 2)) throw badRequest("MaximumRetryAttempts must be between 0 and 2")
    if (b.maximum_event_age_seconds !== undefined && (b.maximum_event_age_seconds < 60 || b.maximum_event_age_seconds > 21600)) throw badRequest("MaximumEventAgeInSeconds must be between 60 and 21600")
    const st = S()
    const qual = query.qualifier || LATEST
    st.invokeConfigs = st.invokeConfigs.filter((c) => !(c.function === params.name && c.qualifier === qual))
    const c: EventInvokeConfig = {
      function: params.name,
      qualifier: qual,
      maximum_retry_attempts: b.maximum_retry_attempts,
      maximum_event_age_seconds: b.maximum_event_age_seconds,
      on_success: b.on_success || undefined,
      on_failure: b.on_failure || undefined,
      last_modified: nowIso(),
    }
    st.invokeConfigs.push(c)
    return c
  })
  r.del("/api/v1/lambda/functions/:name/event-invoke-config", ({ params, query }) => {
    if (!invokeCfg(params.name, query.qualifier)) throw err(404, "ResourceNotFound", `The function ${fnArn(params.name)} doesn't have an EventInvokeConfig`)
    const qual = query.qualifier || LATEST
    const st = S()
    st.invokeConfigs = st.invokeConfigs.filter((c) => !(c.function === params.name && c.qualifier === qual))
  })

  // ---- layers
  r.get("/api/v1/lambda/layers", ({ query }) => {
    const latest = new Map<string, LayerVersion>()
    for (const l of S().layers) if ((latest.get(l.name)?.version ?? 0) < l.version) latest.set(l.name, l)
    return [...latest.values()]
      .filter((l) => (!query.runtime || (l.compatible_runtimes ?? []).includes(query.runtime)) && (!query.architecture || (l.compatible_architectures ?? []).includes(query.architecture)))
      .sort((a, b) => a.name.localeCompare(b.name))
  })
  r.post("/api/v1/lambda/layers", ({ body: b }) => {
    b = b ?? {}
    const name = String(b.name ?? "")
    if (!NAME_RE.test(name)) throw badRequest("Layer names are 1-64 letters, digits, hyphens or underscores")
    if (!b.zip_base64) throw badRequest("zip_base64 is required")
    const st = S()
    const v = Math.max(0, ...st.layers.filter((l) => l.name === name).map((l) => l.version)) + 1
    const lv: LayerVersion = {
      name,
      version: v,
      arn: `${layerArn(name)}:${v}`,
      layer_arn: layerArn(name),
      description: b.description ?? "",
      compatible_runtimes: b.compatible_runtimes ?? [],
      compatible_architectures: b.compatible_architectures ?? [],
      license_info: b.license_info ?? "",
      code_sha256: sha(`${name}:${v}:${nowIso()}`),
      code_size: Math.round(String(b.zip_base64).length * 0.75),
      created_at: nowIso(),
    }
    st.layers.push(lv)
    return lv
  })
  r.get("/api/v1/lambda/layers/:layer/versions", ({ params, query }) =>
    S()
      .layers.filter((l) => l.name === params.layer)
      .filter((l) => (!query.runtime || (l.compatible_runtimes ?? []).includes(query.runtime)) && (!query.architecture || (l.compatible_architectures ?? []).includes(query.architecture)))
      .sort((a, b) => b.version - a.version),
  )
  r.del("/api/v1/lambda/layers/:layer/versions/:version", ({ params }) => {
    const st = S()
    const v = Number(params.version)
    if (!Number.isInteger(v)) throw badRequest(`invalid version "${params.version}"`)
    const i = st.layers.findIndex((l) => l.name === params.layer && l.version === v)
    if (i < 0) throw err(404, "ResourceNotFound", `Layer version not found: ${layerArn(params.layer)}:${v}`)
    st.layers.splice(i, 1)
  })

  // ---- event source mappings
  r.get("/api/v1/lambda/event-source-mappings", ({ query }) => S().mappings.filter((m) => !query.function || m.function_name === query.function))
  r.post("/api/v1/lambda/event-source-mappings", ({ body: b }) => {
    b = b ?? {}
    const fname = String(b.function_name ?? "")
    getFn(fname.split(":")[0])
    const st = S()
    const stream = typeof b.event_source_arn === "string" && b.event_source_arn.includes(":dynamodb:") && b.event_source_arn.includes("/stream/")
    let srcArn: string
    let queue = ""
    if (stream) srcArn = b.event_source_arn
    else {
      queue = String(b.queue_name ?? "")
      if (!queue && typeof b.event_source_arn === "string") queue = b.event_source_arn.split(":").pop() ?? ""
      if (!queue) throw badRequest("queue_name is required")
      srcArn = arn("sqs", queue)
    }
    if (st.mappings.some((m) => m.function_name === fname && m.event_source_arn === srcArn)) throw conflict("An event source mapping with this event source and function already exists")
    const bs = Number(b.batch_size ?? (stream ? 100 : 10))
    if (!(bs >= 1 && bs <= (stream ? 10000 : 10000))) throw badRequest("batch_size is out of range")
    const m: EventSourceMapping = {
      id: uuid(),
      function_name: fname,
      queue_name: queue,
      event_source_arn: srcArn,
      batch_size: bs,
      batching_window_seconds: b.batching_window_seconds ?? 0,
      function_response_types: b.function_response_types ?? [],
      enabled: b.enabled !== false,
      last_processing_result: "No records processed",
      created_at: nowIso(),
      ...(stream
        ? {
            starting_position: b.starting_position ?? "LATEST",
            maximum_retry_attempts: b.maximum_retry_attempts,
            bisect_batch_on_function_error: !!b.bisect_batch_on_function_error,
            on_failure: b.on_failure || undefined,
          }
        : {}),
    }
    st.mappings.push(m)
    return m
  })
  r.patch("/api/v1/lambda/event-source-mappings/:id", ({ params, body: b }) => {
    const m = S().mappings.find((x) => x.id === params.id)
    if (!m) throw notFound("event source mapping", params.id)
    if (b?.enabled !== undefined) m.enabled = !!b.enabled
    if (b?.batch_size !== undefined) {
      if (!(b.batch_size >= 1 && b.batch_size <= 10000)) throw badRequest("batch_size is out of range")
      m.batch_size = Number(b.batch_size)
    }
    if (b?.batching_window_seconds !== undefined) m.batching_window_seconds = Number(b.batching_window_seconds)
    return m
  })
  r.del("/api/v1/lambda/event-source-mappings/:id", ({ params }) => {
    const st = S()
    if (!st.mappings.some((m) => m.id === params.id)) throw notFound("event source mapping", params.id)
    st.mappings = st.mappings.filter((m) => m.id !== params.id)
  })
}

function onLoad(s: State) {
  const st = s.lambda as LambdaState
  for (const f of st.functions) {
    if (f.state !== "Active") {
      f.state = "Active"
      f.state_reason = undefined
      f.state_reason_code = undefined
    }
    if (f.last_update_status === "InProgress") f.last_update_status = "Successful"
  }
}

const service: DemoService = {
  name: "lambda",
  seed,
  routes,
  onLoad,
}

export default service
