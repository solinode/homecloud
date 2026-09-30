import { badRequest, conflict, notFound, unavailable, type DemoService, type Router, getState } from "../engine"
import type { Bucket, BucketDetail, LifecycleRule, ObjectListing, ObjectMeta, PresignResult, S3Credentials, S3Object, S3Status } from "@/lib/types"
import { ACCOUNT, DAY, HOUR, REGION, FAKE_ACCESS_KEY, FAKE_SECRET, ago, ahead, hex, rng } from "../util"
import { NAMES } from "../ids"

interface Obj {
  key: string
  size: number
  last_modified: string
  etag: string
  content_type: string
  metadata: Record<string, string>
  /** inline text content for small text objects */
  body?: string
}

interface BucketState {
  name: string
  created_at: string
  versioning: string
  public: boolean
  policy: string
  website: boolean
  index_document: string
  error_document: string
  lifecycle_rules: LifecycleRule[]
  tags: Record<string, string>
  objects: Obj[]
}

interface S3State {
  buckets: BucketState[]
}

const st = () => getState().s3 as S3State

const CT: Record<string, string> = {
  html: "text/html",
  css: "text/css",
  js: "application/javascript",
  json: "application/json",
  txt: "text/plain",
  md: "text/markdown",
  csv: "text/csv",
  log: "text/plain",
  png: "image/png",
  jpg: "image/jpeg",
  webp: "image/webp",
  svg: "image/svg+xml",
  gz: "application/gzip",
  zip: "application/zip",
  tf: "text/plain",
  tfstate: "application/json",
  pdf: "application/pdf",
  woff2: "font/woff2",
  ico: "image/x-icon",
}
const contentType = (key: string) => CT[key.slice(key.lastIndexOf(".") + 1).toLowerCase()] ?? "application/octet-stream"
const isText = (ct: string) => ct.startsWith("text/") || ct === "application/json" || ct === "application/javascript" || ct === "image/svg+xml"

function obj(key: string, size: number, age: number, body?: string, metadata: Record<string, string> = {}): Obj {
  return { key, size: body !== undefined ? new TextEncoder().encode(body).length : size, last_modified: ago(age), etag: `"${hex(32)}"`, content_type: contentType(key), metadata, body }
}

function publicPolicy(bucket: string) {
  return JSON.stringify({ Version: "2012-10-17", Statement: [{ Effect: "Allow", Principal: { AWS: ["*"] }, Action: ["s3:GetObject"], Resource: [`arn:aws:s3:::${bucket}/*`] }] })
}

function seedBuckets(): BucketState[] {
  const r = rng("s3-seed")
  const tag = (extra: Record<string, string> = {}) => ({ Project: "shop", Environment: "prod", ...extra })

  const assets: Obj[] = [
    obj("index.html", 0, 3 * DAY, `<!doctype html>\n<html lang="en">\n<head><meta charset="utf-8"><title>Shop</title><link rel="stylesheet" href="/css/app.css"></head>\n<body>\n  <div id="root"></div>\n  <script src="/js/app.3f9a1c.js"></script>\n</body>\n</html>\n`),
    obj("404.html", 0, 40 * DAY, `<!doctype html>\n<html><head><title>Not found</title></head><body><h1>404</h1><p>That page does not exist.</p></body></html>\n`),
    obj("robots.txt", 0, 60 * DAY, "User-agent: *\nAllow: /\nSitemap: https://shop.example.com/sitemap.xml\n"),
    obj("css/app.css", 0, 3 * DAY, "body{font-family:system-ui,sans-serif;margin:0}\n.btn{padding:.5rem 1rem;border-radius:6px}\n"),
    obj("css/vendor.css", 41230, 3 * DAY),
    obj("js/app.3f9a1c.js", 284113, 3 * DAY),
    obj("js/vendor.77be02.js", 912456, 3 * DAY),
    obj("fonts/inter-var.woff2", 112400, 90 * DAY),
    obj("favicon.ico", 15086, 120 * DAY),
  ]
  for (let i = 1; i <= 12; i++) {
    assets.push(obj(`img/products/sku-${1000 + i}.webp`, 40000 + Math.floor(r() * 180000), (8 + i * 6) * DAY, undefined, { "cache-control": "max-age=31536000" }))
  }
  assets.push(obj("img/hero.jpg", 731204, 30 * DAY), obj("img/logo.svg", 0, 120 * DAY, `<svg xmlns="http://www.w3.org/2000/svg" width="96" height="32"><rect width="96" height="32" rx="6" fill="#0f766e"/><text x="12" y="21" fill="#fff" font-size="14">Shop</text></svg>\n`))

  const uploads: Obj[] = [
    obj("README.md", 0, 45 * DAY, "# Customer uploads\n\nThis bucket holds files uploaded by customers (return labels, invoices, avatars).\n\n- `avatars/` profile pictures, resized by the image-resize Lambda\n- `invoices/` generated PDF invoices\n- `returns/` return authorization photos\n\nObjects under `tmp/` expire after 7 days.\n"),
  ]
  for (let i = 0; i < 9; i++) uploads.push(obj(`avatars/user-${4100 + i}.jpg`, 20000 + Math.floor(r() * 90000), (i * 5 + 1) * DAY))
  for (let i = 0; i < 8; i++) uploads.push(obj(`invoices/2026/INV-${20260 + i}.pdf`, 60000 + Math.floor(r() * 40000), (i * 3 + 1) * DAY, undefined, { "order-id": `ORD-${88100 + i}` }))
  for (let i = 0; i < 4; i++) uploads.push(obj(`returns/RMA-${3300 + i}/photo-${i}.png`, 800000 + Math.floor(r() * 900000), (i * 2 + 1) * DAY))
  uploads.push(obj("tmp/import-2026-09-28.csv", 0, 2 * DAY, "sku,name,price,stock\nSKU-1001,Canvas tote,24.00,120\nSKU-1002,Enamel mug,12.50,340\nSKU-1003,Desk mat,39.00,58\nSKU-1004,Sticker pack,4.00,900\n"))
  uploads.push(obj("tmp/", 0, 2 * DAY))

  const backups: Obj[] = []
  for (let i = 0; i < 14; i++) {
    const d = new Date(Date.now() - i * DAY).toISOString().slice(0, 10)
    backups.push(obj(`postgres/daily/shop-${d}.sql.gz`, 380_000_000 + Math.floor(r() * 25_000_000), i * DAY + 3 * HOUR, undefined, { "pg-version": "16.4" }))
  }
  backups.push(obj("postgres/weekly/shop-2026-09-21.sql.gz", 402_113_004, 9 * DAY), obj("postgres/weekly/shop-2026-09-14.sql.gz", 397_020_411, 16 * DAY))
  backups.push(obj("redis/dump-2026-09-29.rdb", 52_004_300, 1 * DAY))

  const logs: Obj[] = []
  for (let i = 0; i < 24; i++) {
    logs.push(obj(`AWSLogs/${ACCOUNT}/elasticloadbalancing/${REGION}/2026/09/29/${ACCOUNT}_elasticloadbalancing_${REGION}_app.shop-alb_20260929T${String(i).padStart(2, "0")}00Z_10.0.1.${10 + i}_${hex(8)}.log.gz`, 90_000 + Math.floor(r() * 400_000), (24 - i) * HOUR))
  }
  logs.push(obj("README.txt", 0, 100 * DAY, "ALB access logs are delivered here every 5 minutes.\nRetention is 90 days (see lifecycle rules).\n"))

  const tf: Obj[] = [
    obj("shop/prod/terraform.tfstate", 0, 6 * HOUR, JSON.stringify({ version: 4, terraform_version: "1.9.5", serial: 212, lineage: "0b1c2d3e-demo-0000-0000-000000000000", outputs: { alb_dns: { value: "shop-alb-1234567890.us-east-1.elb.amazonaws.com", type: "string" } }, resources: [] }, null, 2) + "\n"),
    obj("shop/staging/terraform.tfstate", 0, 12 * DAY, JSON.stringify({ version: 4, terraform_version: "1.9.5", serial: 57, outputs: {}, resources: [] }, null, 2) + "\n"),
  ]

  const mk = (name: string, age: number, o: Partial<BucketState>, objects: Obj[]): BucketState => ({
    name, created_at: ago(age), versioning: "", public: false, policy: "", website: false, index_document: "", error_document: "", lifecycle_rules: [], tags: tag(), objects, ...o,
  })
  return [
    mk(NAMES.bucketAssets, 210 * DAY, { public: true, policy: publicPolicy(NAMES.bucketAssets), website: true, index_document: "index.html", error_document: "404.html", tags: tag({ Team: "web" }) }, assets),
    mk(NAMES.bucketUploads, 210 * DAY, { versioning: "Enabled", tags: tag({ Team: "web" }), lifecycle_rules: [{ id: "expire-tmp", prefix: "tmp/", expiration_days: 7, status: "Enabled" }] }, uploads),
    mk(NAMES.bucketBackups, 180 * DAY, { versioning: "Enabled", tags: tag({ Team: "platform", Backup: "true" }), lifecycle_rules: [{ id: "expire-daily", prefix: "postgres/daily/", expiration_days: 30, status: "Enabled" }] }, backups),
    mk(NAMES.bucketLogs, 200 * DAY, { tags: tag({ Team: "platform" }), lifecycle_rules: [{ id: "expire-logs", prefix: "AWSLogs/", expiration_days: 90, status: "Enabled" }], policy: JSON.stringify({ Version: "2012-10-17", Statement: [{ Sid: "AllowELBLogDelivery", Effect: "Allow", Principal: { AWS: "arn:aws:iam::127311923021:root" }, Action: "s3:PutObject", Resource: `arn:aws:s3:::${NAMES.bucketLogs}/AWSLogs/${ACCOUNT}/*` }] }) }, logs),
    mk(NAMES.bucketTfState, 240 * DAY, { versioning: "Enabled", tags: tag({ Team: "platform" }) }, tf),
    mk("shop-dev-scratch", 20 * DAY, { tags: { Environment: "dev" } }, [obj("notes.txt", 0, 5 * DAY, "scratch space for the dev team\n")]),
  ]
}

const arnOf = (n: string) => `arn:aws:s3:::${n}`

function findBucket(name: string): BucketState {
  const b = st().buckets.find((x) => x.name === name)
  if (!b) throw notFound("Bucket", name)
  return b
}

function summary(b: BucketState): Bucket {
  return { name: b.name, arn: arnOf(b.name), created_at: b.created_at, region: REGION, website: b.website, public: b.public }
}

function detail(b: BucketState): BucketDetail {
  const real = b.objects.filter((o) => !o.key.endsWith("/") || o.size > 0)
  return {
    name: b.name,
    arn: arnOf(b.name),
    region: REGION,
    versioning: b.versioning,
    public: b.public,
    policy: b.policy,
    object_count: real.length,
    size_bytes: real.reduce((n, o) => n + o.size, 0),
    stats_truncated: false,
    website: b.website,
    index_document: b.index_document,
    error_document: b.error_document,
    lifecycle_rules: b.lifecycle_rules.length ? b.lifecycle_rules : null,
    tags: Object.keys(b.tags).length ? b.tags : null,
    website_url: `http://homecloud.local:8080/website/${b.name}/`,
  }
}

function toObject(o: Obj): S3Object {
  return { key: o.key, size: o.size, last_modified: o.last_modified, etag: o.etag, storage_class: "STANDARD", version_id: "", is_latest: true, delete_marker: false }
}

function listing(b: BucketState, prefix: string, recursive: boolean): ObjectListing {
  const objects: S3Object[] = []
  const prefixes = new Set<string>()
  const keys = [...b.objects].sort((x, y) => (x.key < y.key ? -1 : 1))
  for (const o of keys) {
    if (!o.key.startsWith(prefix)) continue
    const rest = o.key.slice(prefix.length)
    if (!recursive) {
      const i = rest.indexOf("/")
      if (i >= 0) {
        prefixes.add(prefix + rest.slice(0, i + 1))
        continue
      }
    }
    if (o.key === prefix) continue
    objects.push(toObject(o))
  }
  return { bucket: b.name, prefix, prefixes: [...prefixes], objects, truncated: false }
}

async function readBody(req: { body: unknown; raw?: unknown }): Promise<string> {
  const src = req.raw ?? req.body
  if (typeof src === "string") return src
  if (src && typeof (src as Blob).text === "function") return await (src as Blob).text()
  if (src && typeof src === "object") return JSON.stringify(src)
  return ""
}

function queryKey(q: Record<string, string>): string {
  if (!q.key) throw badRequest("key query parameter is required (max 1024 bytes)")
  return q.key
}

function findObj(b: BucketState, key: string): Obj {
  const o = b.objects.find((x) => x.key === key)
  if (!o) throw notFound("Object", key)
  return o
}

function putObj(b: BucketState, o: Obj) {
  const i = b.objects.findIndex((x) => x.key === o.key)
  if (i >= 0) b.objects[i] = o
  else b.objects.push(o)
}

const seg = "/api/v1/s3/buckets/:bucket"

function routes(r: Router) {
  r.get("/api/v1/s3/status", (): S3Status => ({ status: "available", endpoint: "http://homecloud.local:9000", region: REGION, console_url: "http://homecloud.local:9001" }))
  r.get("/api/v1/s3/credentials", (): S3Credentials => ({ endpoint: "http://homecloud.local:9000", region: REGION, access_key_id: FAKE_ACCESS_KEY, secret_access_key: FAKE_SECRET }))
  r.get("/api/v1/s3/buckets", () => st().buckets.map(summary))
  r.post("/api/v1/s3/buckets", ({ body }) => {
    const name = String(body?.name ?? "")
    if (name.length < 3 || name.length > 63) throw badRequest("bucket names must be 3-63 characters")
    if (!/^[a-z0-9][a-z0-9.-]*[a-z0-9]$/.test(name)) throw badRequest("bucket names may contain only lowercase letters, digits, dots and hyphens, and must start and end with a letter or digit")
    if (st().buckets.some((b) => b.name === name)) throw conflict("Your previous request to create the named bucket succeeded and you already own it.")
    st().buckets.push({
      name, created_at: new Date().toISOString(), versioning: body?.versioning ? "Enabled" : "", public: !!body?.public,
      policy: body?.public ? publicPolicy(name) : "", website: false, index_document: "", error_document: "", lifecycle_rules: [],
      tags: { ...(body?.tags ?? {}) }, objects: [],
    })
    return { name, arn: arnOf(name) }
  })
  r.get(seg, ({ params }) => detail(findBucket(params.bucket)))
  r.del(seg, ({ params, query }) => {
    const b = findBucket(params.bucket)
    if (b.objects.length && query.force !== "true") throw conflict("The bucket you tried to delete is not empty")
    st().buckets = st().buckets.filter((x) => x !== b)
  })
  r.put(`${seg}/versioning`, ({ params, body }) => {
    findBucket(params.bucket).versioning = body?.enabled ? "Enabled" : "Suspended"
  })
  r.put(`${seg}/access`, ({ params, body }) => {
    const b = findBucket(params.bucket)
    b.public = !!body?.public
    b.policy = b.public ? publicPolicy(b.name) : ""
  })
  r.put(`${seg}/policy`, ({ params, body }) => {
    const b = findBucket(params.bucket)
    const p = String(body?.policy ?? "")
    if (p) {
      try {
        JSON.parse(p)
      } catch {
        throw badRequest("policy must be a JSON document")
      }
    }
    b.policy = p
    b.public = p.includes('"*"') && p.includes("s3:GetObject")
  })
  r.put(`${seg}/website`, ({ params, body }) => {
    const b = findBucket(params.bucket)
    b.website = !!body?.enabled
    b.index_document = body?.index_document || "index.html"
    b.error_document = body?.error_document ?? ""
    return { name: b.name, website: b.website, index_document: b.index_document, error_document: b.error_document, tags: b.tags }
  })
  r.put(`${seg}/lifecycle`, ({ params, body }) => {
    const b = findBucket(params.bucket)
    const rules = (body?.rules ?? []) as { id?: string; prefix?: string; expiration_days: number }[]
    b.lifecycle_rules = rules.map((x, i) => {
      if (!(x.expiration_days >= 1)) throw badRequest(`rule ${i}: expiration_days must be at least 1`)
      return { id: x.id || `rule-${i + 1}`, prefix: x.prefix ?? "", expiration_days: x.expiration_days, status: "Enabled" }
    })
  })
  r.put(`${seg}/tags`, ({ params, body }) => {
    const b = findBucket(params.bucket)
    b.tags = { ...(body?.tags ?? {}) }
    return { name: b.name, tags: b.tags }
  })

  r.get(`${seg}/objects`, ({ params, query }) => listing(findBucket(params.bucket), query.prefix ?? "", query.recursive === "true"))
  r.put(`${seg}/object`, async (req) => {
    const b = findBucket(req.params.bucket)
    const key = queryKey(req.query)
    const text = await readBody(req)
    let ct = req.headers["Content-Type"] || req.headers["content-type"] || ""
    if (!ct || ct === "application/octet-stream") ct = contentType(key)
    const o = obj(key, 0, 0, text)
    o.content_type = ct
    if (!isText(ct)) o.body = undefined
    putObj(b, o)
    return { bucket: b.name, key, etag: o.etag, size: o.size, version_id: b.versioning === "Enabled" ? hex(32) : "" }
  })
  r.get(`${seg}/object`, ({ params, query }) => {
    const b = findBucket(params.bucket)
    const o = findObj(b, queryKey(query))
    if (query.inline === "true" && o.body !== undefined) return { key: o.key, content_type: o.content_type, size: o.size, body: o.body }
    throw unavailable("Downloading objects")
  })
  r.get(`${seg}/object/meta`, ({ params, query }): ObjectMeta => {
    const b = findBucket(params.bucket)
    const o = findObj(b, queryKey(query))
    return {
      key: o.key, size: o.size, content_type: o.content_type, etag: o.etag, last_modified: o.last_modified,
      version_id: b.versioning === "Enabled" ? hex(32) : "", metadata: Object.keys(o.metadata).length ? o.metadata : null,
      arn: `${arnOf(b.name)}/${o.key}`, url: `http://homecloud.local:9000/${b.name}/${o.key.split("/").map(encodeURIComponent).join("/")}`,
    }
  })
  r.del(`${seg}/object`, ({ params, query }) => {
    const b = findBucket(params.bucket)
    const key = queryKey(query)
    if (query.recursive === "true") {
      b.objects = b.objects.filter((o) => !o.key.startsWith(key))
      return { deleted_prefix: key }
    }
    b.objects = b.objects.filter((o) => o.key !== key)
  })
  r.post(`${seg}/folders`, ({ params, body }) => {
    const b = findBucket(params.bucket)
    let key = String(body?.key ?? "")
    if (!key) throw badRequest("key is required")
    if (!key.endsWith("/")) key += "/"
    putObj(b, obj(key, 0, 0, ""))
    return { key }
  })
  r.post(`${seg}/copy`, ({ params, body }) => {
    const b = findBucket(params.bucket)
    const src = findBucket(body?.source_bucket || params.bucket)
    const from = findObj(src, String(body?.source_key ?? ""))
    if (!body?.key) throw badRequest("key is required")
    const copy: Obj = { ...from, metadata: { ...from.metadata }, key: body.key, last_modified: new Date().toISOString(), etag: `"${hex(32)}"` }
    putObj(b, copy)
    return { key: copy.key, etag: copy.etag }
  })
  r.post(`${seg}/presign`, ({ params, body }): PresignResult => {
    const b = findBucket(params.bucket)
    if (!body?.key) throw badRequest("key is required")
    const secs = Number(body.expires_seconds) || 3600
    if (secs > 7 * 24 * 3600) throw badRequest("expires_seconds may not exceed 7 days")
    const key = String(body.key).split("/").map(encodeURIComponent).join("/")
    return {
      url: `http://homecloud.local:9000/${b.name}/${key}?X-Amz-Algorithm=AWS4-HMAC-SHA256&X-Amz-Credential=${FAKE_ACCESS_KEY}%2F20260930%2F${REGION}%2Fs3%2Faws4_request&X-Amz-Expires=${secs}&X-Amz-Signature=demo${hex(24)}`,
      expires_at: ahead(secs * 1000),
    }
  })
}

const service: DemoService = {
  name: "s3",
  seed: (): S3State => ({ buckets: seedBuckets() }),
  routes,
}

export default service
