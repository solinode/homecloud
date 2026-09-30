import { badRequest, err, getState, notFound, type DemoService, type Router } from "../engine"
import { ACCOUNT, DAY, REGION, ago, ahead, hashStr, hex, stableId } from "../util"
import type { KmsAlias, KmsGrant, KmsKey } from "@/lib/types"

interface StoredKey extends Omit<KmsKey, "aliases"> {
  policy: string
  grants: KmsGrant[]
}
interface KmsState {
  keys: StoredKey[]
  aliases: KmsAlias[]
}
const st = () => getState().kms as KmsState

const keyId = (name: string) => {
  const h = stableId("", name, 32)
  return `${h.slice(0, 8)}-${h.slice(8, 12)}-4${h.slice(13, 16)}-a${h.slice(17, 20)}-${h.slice(20, 32)}`
}
const keyArn = (id: string) => `arn:aws:kms:${REGION}:${ACCOUNT}:key/${id}`

const defaultPolicy = () =>
  JSON.stringify(
    {
      Version: "2012-10-17",
      Id: "key-default-1",
      Statement: [
        { Sid: "Enable IAM User Permissions", Effect: "Allow", Principal: { AWS: `arn:aws:iam::${ACCOUNT}:root` }, Action: "kms:*", Resource: "*" },
        {
          Sid: "Allow use of the key by the shop roles", Effect: "Allow",
          Principal: { AWS: [`arn:aws:iam::${ACCOUNT}:role/shop-payments-lambda-role`, `arn:aws:iam::${ACCOUNT}:role/shop-web-ec2-role`] },
          Action: ["kms:Encrypt", "kms:Decrypt", "kms:ReEncrypt*", "kms:GenerateDataKey*", "kms:DescribeKey"], Resource: "*",
        },
      ],
    },
    null,
    2,
  )

const mk = (name: string, o: Partial<StoredKey> & { age: number }): StoredKey => {
  const id = keyId(name)
  const { age, ...rest } = o
  return {
    id, arn: keyArn(id), description: "", key_spec: "SYMMETRIC_DEFAULT", key_usage: "ENCRYPT_DECRYPT", state: "Enabled", managed: false, rotation_enabled: false, rotation_period_days: 365,
    next_rotation: null, last_rotated: null, deletion_date: null, created_at: ago(age), key_versions: 1, tags: {}, policy: "", grants: [], ...rest,
  }
}

function seed(): KmsState {
  const keys: StoredKey[] = [
    mk("shop-data", {
      age: 300 * DAY, description: "Encrypts the shop database, EBS volumes and S3 buckets", rotation_enabled: true, key_versions: 2, last_rotated: ago(65 * DAY), next_rotation: ahead(300 * DAY), tags: { app: "shop", env: "prod" },
      grants: [
        { grant_id: stableId("", "grant-rds", 64), name: "rds-storage", grantee_principal: `arn:aws:iam::${ACCOUNT}:role/aws-service-role/rds.amazonaws.com/AWSServiceRoleForRDS`, operations: ["Encrypt", "Decrypt", "GenerateDataKey", "CreateGrant", "DescribeKey"], created_at: ago(280 * DAY) },
        { grant_id: stableId("", "grant-ebs", 64), name: "ebs-volumes", grantee_principal: `arn:aws:iam::${ACCOUNT}:role/aws-service-role/ec2.amazonaws.com/AWSServiceRoleForEC2`, retiring_principal: `arn:aws:iam::${ACCOUNT}:root`, operations: ["Decrypt", "GenerateDataKeyWithoutPlaintext", "CreateGrant"], created_at: ago(120 * DAY) },
      ],
    }),
    mk("shop-secrets", { age: 240 * DAY, description: "Envelope key for application secrets", rotation_enabled: true, rotation_period_days: 180, key_versions: 3, last_rotated: ago(30 * DAY), next_rotation: ahead(150 * DAY), tags: { app: "shop", env: "prod" } }),
    mk("shop-jwt-signing", { age: 150 * DAY, description: "Signs customer session tokens (RS256)", key_spec: "RSA_2048", key_usage: "SIGN_VERIFY", tags: { app: "shop" } }),
    mk("shop-webhook-hmac", { age: 90 * DAY, description: "HMAC key for payment webhook verification", key_spec: "HMAC_256", key_usage: "GENERATE_VERIFY_MAC", tags: { app: "shop" } }),
    mk("dev-sandbox", { age: 200 * DAY, description: "Scratch key for local experiments", state: "Disabled", tags: { env: "dev" } }),
    mk("legacy-backups", { age: 500 * DAY, description: "Old backup key (retired after migration)", state: "PendingDeletion", deletion_date: ahead(6 * DAY), tags: { env: "prod", status: "retired" } }),
    mk("hc-s3", { age: 400 * DAY, description: "Default key that protects S3 objects when no other key is defined", managed: true, rotation_enabled: true, key_versions: 2, last_rotated: ago(35 * DAY), next_rotation: ahead(330 * DAY) }),
    mk("hc-secretsmanager", { age: 400 * DAY, description: "Default key that protects Secrets Manager secrets", managed: true, rotation_enabled: true, key_versions: 2, last_rotated: ago(35 * DAY), next_rotation: ahead(330 * DAY) }),
    mk("hc-ssm", { age: 400 * DAY, description: "Default key that protects Systems Manager parameters", managed: true, rotation_enabled: true }),
    mk("hc-rds", { age: 400 * DAY, description: "Default key that protects RDS database volumes", managed: true, rotation_enabled: true }),
  ]
  const al = (name: string, key: string, age: number): KmsAlias => ({ name, key_id: keyId(key), created_at: ago(age), updated_at: ago(age) })
  const aliases: KmsAlias[] = [
    al("alias/shop-data", "shop-data", 300 * DAY),
    al("alias/shop-secrets", "shop-secrets", 240 * DAY),
    al("alias/shop-jwt-signing", "shop-jwt-signing", 150 * DAY),
    al("alias/shop-webhook-hmac", "shop-webhook-hmac", 90 * DAY),
    al("alias/dev-sandbox", "dev-sandbox", 200 * DAY),
    al("alias/hc/s3", "hc-s3", 400 * DAY),
    al("alias/hc/secretsmanager", "hc-secretsmanager", 400 * DAY),
    al("alias/hc/rds", "hc-rds", 400 * DAY),
    al("alias/hc/ssm", "hc-ssm", 400 * DAY),
  ]
  return { keys, aliases }
}

const view = (k: StoredKey): KmsKey => {
  const { policy: _p, grants: _g, ...rest } = k
  return { ...rest, aliases: st().aliases.filter((a) => a.key_id === k.id).map((a) => a.name) }
}
const find = (ref: string): StoredKey => {
  let id = ref
  if (ref.startsWith("alias/") || ref.includes(":alias/")) id = st().aliases.find((a) => a.name === ref.slice(ref.indexOf("alias/")))?.key_id ?? ""
  else if (ref.startsWith("arn:")) id = ref.slice(ref.lastIndexOf("/") + 1)
  const k = st().keys.find((x) => x.id === id)
  if (!k) throw err(404, "NotFoundException", `Key '${ref}' does not exist`)
  return k
}
const usable = (k: StoredKey) => {
  if (k.state === "Disabled") throw err(409, "DisabledException", `${k.arn} is disabled.`)
  if (k.state === "PendingDeletion") throw err(409, "KMSInvalidStateException", `${k.arn} is pending deletion.`)
}
const customer = (k: StoredKey) => {
  if (k.managed) throw err(403, "NotAuthorizedException", "This operation is not allowed on a HomeCloud managed key.")
}

// Canned "ciphertext": readable base64 that round-trips so the demo crypto tool works.
const b64 = (s: string) => btoa(unescape(encodeURIComponent(s)))
const unb64 = (s: string) => {
  try {
    return decodeURIComponent(escape(atob(s)))
  } catch {
    return null
  }
}
const MAGIC = "DEMO-CIPHERTEXT"
const seal = (k: StoredKey, plaintextB64: string, ctx: unknown) => b64(`${MAGIC}|${k.id}|${b64(JSON.stringify(ctx ?? {}))}|${plaintextB64}`)
function unseal(blob: string, ctx: unknown) {
  const raw = unb64(blob)
  const parts = raw?.split("|")
  if (!parts || parts[0] !== MAGIC || parts.length !== 4) throw err(400, "InvalidCiphertextException", "The ciphertext is invalid.")
  if (unb64(parts[2]) !== JSON.stringify(ctx ?? {})) throw err(400, "InvalidCiphertextException", "The encryption context does not match the one used to encrypt.")
  return { key: find(parts[1]), plaintext: parts[3] }
}
const fakeTag = (label: string, k: StoredKey, msg: string) => b64(`${label}|${k.id}|${hashStr(msg).toString(16)}${hashStr(msg + k.id).toString(16)}`)

const validAlias = (a: unknown) => {
  if (typeof a !== "string" || !/^alias\/[a-zA-Z0-9/_-]+$/.test(a)) throw err(400, "ValidationException", 'Alias must start with the prefix "alias/" and contain only alphanumeric characters, /, _ and -')
  if (a.startsWith("alias/hc/") || a.startsWith("alias/aws/")) throw err(403, "NotAuthorizedException", "Cannot create alias with prefix 'alias/aws/' or 'alias/hc/' (reserved for managed keys)")
  return a
}
const macAlg = (k: StoredKey) => `HMAC_SHA_${k.key_spec.replace("HMAC_", "")}`

function routes(r: Router) {
  const P = "/api/v1/kms"
  r.get(`${P}/keys`, () => st().keys.map(view))
  r.post(`${P}/keys`, ({ body }) => {
    if (body?.alias) {
      validAlias(body.alias)
      if (st().aliases.some((a) => a.name === body.alias)) throw err(409, "AlreadyExistsException", `alias "${body.alias}" already exists`)
    }
    const spec = body?.key_spec || "SYMMETRIC_DEFAULT"
    const id = keyId(`new-${hex(12)}`)
    const usage = body?.key_usage || (spec.startsWith("HMAC") ? "GENERATE_VERIFY_MAC" : "ENCRYPT_DECRYPT")
    const k: StoredKey = {
      id, arn: keyArn(id), description: body?.description ?? "", key_spec: spec, key_usage: usage, state: "Enabled", managed: false, rotation_enabled: false, rotation_period_days: 365,
      next_rotation: null, last_rotated: null, deletion_date: null, created_at: new Date().toISOString(), key_versions: 1, tags: body?.tags ?? {}, policy: "", grants: [],
    }
    if (body?.rotation_enabled && spec === "SYMMETRIC_DEFAULT") {
      k.rotation_enabled = true
      k.next_rotation = ahead(365 * DAY)
    }
    st().keys.push(k)
    if (body?.alias) st().aliases.push({ name: body.alias, key_id: id, created_at: k.created_at, updated_at: k.created_at })
    return view(k)
  })
  r.get(`${P}/keys/:id`, ({ params }) => view(find(params.id)))
  r.patch(`${P}/keys/:id`, ({ params, body }) => {
    const k = find(params.id)
    if (typeof body?.description === "string") {
      customer(k)
      k.description = body.description
    }
    if (typeof body?.rotation_enabled === "boolean") {
      customer(k)
      usable(k)
      if (k.key_spec !== "SYMMETRIC_DEFAULT") throw err(400, "UnsupportedOperationException", `${k.arn} is not a symmetric encryption key; rotation is not supported.`)
      const p = Number(body.rotation_period_days) || k.rotation_period_days || 365
      if (p < 90 || p > 2560) throw err(400, "ValidationException", "rotation_period_days must be between 90 and 2560")
      k.rotation_enabled = body.rotation_enabled
      k.rotation_period_days = p
      k.next_rotation = body.rotation_enabled ? ahead(p * DAY) : null
    }
    return view(k)
  })
  const setState = (on: boolean) => ({ params }: { params: Record<string, string> }) => {
    const k = find(params.id)
    customer(k)
    if (k.state === "PendingDeletion") throw err(409, "KMSInvalidStateException", `${k.arn} is pending deletion.`)
    k.state = on ? "Enabled" : "Disabled"
    return view(k)
  }
  r.post(`${P}/keys/:id/enable`, setState(true))
  r.post(`${P}/keys/:id/disable`, setState(false))
  r.post(`${P}/keys/:id/rotate`, ({ params }) => {
    const k = find(params.id)
    customer(k)
    usable(k)
    if (k.key_spec !== "SYMMETRIC_DEFAULT") throw err(400, "UnsupportedOperationException", "Only symmetric encryption keys can be rotated.")
    k.key_versions += 1
    k.last_rotated = new Date().toISOString()
    if (k.rotation_enabled) k.next_rotation = ahead((k.rotation_period_days ?? 365) * DAY)
    return view(k)
  })
  r.post(`${P}/keys/:id/schedule-deletion`, ({ params, body }) => {
    const k = find(params.id)
    customer(k)
    const n = Number(body?.pending_window_days) || 30
    if (n < 7 || n > 30) throw err(400, "ValidationException", "pending_window_days must be between 7 and 30")
    if (k.state === "PendingDeletion") throw err(409, "KMSInvalidStateException", `${k.arn} is already pending deletion.`)
    k.state = "PendingDeletion"
    k.deletion_date = ahead(n * DAY)
    k.rotation_enabled = false
    k.next_rotation = null
    return view(k)
  })
  r.post(`${P}/keys/:id/cancel-deletion`, ({ params }) => {
    const k = find(params.id)
    if (k.state !== "PendingDeletion") throw err(409, "KMSInvalidStateException", `${k.arn} is not pending deletion.`)
    k.state = "Disabled"
    k.deletion_date = null
    return view(k)
  })
  r.get(`${P}/keys/:id/policy`, ({ params }) => {
    const k = find(params.id)
    return { policy_name: "default", policy: k.policy || defaultPolicy() }
  })
  r.put(`${P}/keys/:id/policy`, ({ params, body }) => {
    const k = find(params.id)
    customer(k)
    try {
      const d = JSON.parse(body?.policy)
      if (!d?.Statement) throw new Error("no statement")
    } catch {
      throw err(400, "MalformedPolicyDocumentException", "The new key policy is not a valid JSON policy document.")
    }
    k.policy = body.policy
    return { policy_name: "default", policy: k.policy }
  })
  r.get(`${P}/keys/:id/grants`, ({ params }) => find(params.id).grants)
  r.post(`${P}/keys/:id/grants`, ({ params, body }) => {
    const k = find(params.id)
    customer(k)
    usable(k)
    if (!body?.grantee_principal) throw badRequest("grantee_principal is required")
    if (!Array.isArray(body.operations) || !body.operations.length) throw badRequest("operations is required")
    const g: KmsGrant = {
      grant_id: hex(64), name: body.name || undefined, grantee_principal: body.grantee_principal, retiring_principal: body.retiring_principal || undefined,
      operations: body.operations, created_at: new Date().toISOString(),
    }
    k.grants.push(g)
    return { ...g, grant_token: "demo-not-a-real-grant-token-" + hex(24) }
  })
  r.del(`${P}/keys/:id/grants/:grant`, ({ params }) => {
    const k = find(params.id)
    if (!k.grants.some((g) => g.grant_id === params.grant)) throw err(404, "NotFoundException", `Grant ${params.grant} not found`)
    k.grants = k.grants.filter((g) => g.grant_id !== params.grant)
  })
  r.get(`${P}/keys/:id/public-key`, ({ params }) => {
    const k = find(params.id)
    if (k.key_spec === "SYMMETRIC_DEFAULT" || k.key_spec.startsWith("HMAC")) throw err(400, "UnsupportedOperationException", `${k.arn} is not an asymmetric key`)
    usable(k)
    const der = b64(`demo-not-a-real-public-key:${k.id}`).padEnd(392, "A")
    const lines = der.match(/.{1,64}/g) ?? []
    return {
      key_id: k.arn, public_key: der, pem: `-----BEGIN PUBLIC KEY-----\n${lines.join("\n")}\n-----END PUBLIC KEY-----\n`, key_spec: k.key_spec, key_usage: k.key_usage,
      encryption_algorithms: k.key_usage === "ENCRYPT_DECRYPT" ? ["RSAES_OAEP_SHA_1", "RSAES_OAEP_SHA_256"] : null,
      signing_algorithms: k.key_usage === "SIGN_VERIFY" ? ["RSASSA_PKCS1_V1_5_SHA_256", "RSASSA_PKCS1_V1_5_SHA_384", "RSASSA_PKCS1_V1_5_SHA_512", "RSASSA_PSS_SHA_256", "RSASSA_PSS_SHA_384", "RSASSA_PSS_SHA_512"] : null,
    }
  })

  // aliases
  r.get(`${P}/aliases`, () => st().aliases)
  r.post(`${P}/aliases`, ({ body }) => {
    const name = validAlias(body?.name)
    if (st().aliases.some((a) => a.name === name)) throw err(409, "AlreadyExistsException", `An alias with the name ${name} already exists`)
    const k = find(body?.key_id ?? "")
    customer(k)
    const now = new Date().toISOString()
    const a: KmsAlias = { name, key_id: k.id, created_at: now, updated_at: now }
    st().aliases.push(a)
    return a
  })
  r.del(`${P}/aliases/*name`, ({ params }) => {
    const a = st().aliases.find((x) => x.name === params.name)
    if (!a) throw notFound("Alias", params.name)
    if (a.name.startsWith("alias/hc/")) throw err(403, "NotAuthorizedException", "Managed aliases cannot be deleted.")
    st().aliases = st().aliases.filter((x) => x !== a)
  })

  // crypto (canned, reversible fake ciphertext)
  r.post(`${P}/encrypt`, ({ body }) => {
    const k = find(body?.key_id ?? "")
    usable(k)
    if (k.key_usage !== "ENCRYPT_DECRYPT") throw err(400, "InvalidKeyUsageException", `${k.arn} key usage is ${k.key_usage}, which is not valid for Encrypt.`)
    if (typeof body.plaintext !== "string" || unb64(body.plaintext) === null) throw badRequest("plaintext must be base64")
    if (body.plaintext.length > 5500) throw err(400, "ValidationException", "Plaintext must be at most 4096 bytes.")
    return { ciphertext_blob: seal(k, body.plaintext, body.encryption_context), key_id: k.arn, encryption_algorithm: body.encryption_algorithm || "SYMMETRIC_DEFAULT" }
  })
  r.post(`${P}/decrypt`, ({ body }) => {
    const { key, plaintext } = unseal(String(body?.ciphertext_blob ?? ""), body?.encryption_context)
    if (body?.key_id && find(body.key_id).id !== key.id) throw err(400, "IncorrectKeyException", "The key ID in the request does not identify a CMK that can perform this operation.")
    usable(key)
    return { plaintext, key_id: key.arn, encryption_algorithm: body?.encryption_algorithm || "SYMMETRIC_DEFAULT" }
  })
  r.post(`${P}/generate-data-key`, ({ body }) => {
    const k = find(body?.key_id ?? "")
    usable(k)
    const n = body?.key_spec === "AES_128" ? 16 : Number(body?.number_of_bytes) || 32
    const plain = btoa(String.fromCharCode(...Array.from({ length: n }, (_, i) => (hashStr(`${k.id}${i}`) & 0x7f) | 0x20)))
    return { plaintext: plain, ciphertext_blob: seal(k, plain, body?.encryption_context), key_id: k.arn }
  })
  r.post(`${P}/generate-random`, ({ body }) => {
    const n = Number(body?.number_of_bytes)
    if (!(n >= 1 && n <= 1024)) throw badRequest("number_of_bytes must be 1-1024")
    return { plaintext: btoa(String.fromCharCode(...Array.from({ length: n }, () => Math.floor(Math.random() * 256)))) }
  })
  r.post(`${P}/sign`, ({ body }) => {
    const k = find(body?.key_id ?? "")
    usable(k)
    if (k.key_usage !== "SIGN_VERIFY") throw err(400, "InvalidKeyUsageException", `${k.arn} key usage is ${k.key_usage}, which is not valid for Sign.`)
    return { signature: fakeTag("DEMO-SIGNATURE", k, String(body.message) + body.algorithm), key_id: k.arn, algorithm: body.algorithm }
  })
  r.post(`${P}/verify`, ({ body }) => {
    const k = find(body?.key_id ?? "")
    usable(k)
    return { valid: body?.signature === fakeTag("DEMO-SIGNATURE", k, String(body.message) + body.algorithm), key_id: k.arn }
  })
  r.post(`${P}/generate-mac`, ({ body }) => {
    const k = find(body?.key_id ?? "")
    usable(k)
    if (k.key_usage !== "GENERATE_VERIFY_MAC") throw err(400, "InvalidKeyUsageException", `${k.arn} key usage is ${k.key_usage}, which is not valid for GenerateMac.`)
    return { mac: fakeTag("DEMO-MAC", k, String(body.message) + (body.algorithm || macAlg(k))), key_id: k.arn }
  })
  r.post(`${P}/verify-mac`, ({ body }) => {
    const k = find(body?.key_id ?? "")
    usable(k)
    return { valid: body?.mac === fakeTag("DEMO-MAC", k, String(body.message) + (body.algorithm || macAlg(k))), key_id: k.arn }
  })
}

const service: DemoService = { name: "kms", seed, routes }
export default service
