import type { Stack, StackEvent, StackInput, StackResource, StackSummary, ValidateTemplateResult } from "@/lib/types"
import { badRequest, err, getState, later, type DemoService, type State } from "../engine"
import { arn, nowIso, stableId, uid, uuid } from "../util"
import { seedStacks } from "./cfn-data"
import { RESOURCE_TYPES, TemplateError, guessName, parseTemplate, type ParsedTemplate } from "./cfn-template"

interface CfnState {
  stacks: Stack[]
}

const cs = () => getState().cfn as CfnState
const find = (name: string) => cs().stacks.find((s) => s.name === name)
const STACK = "HC::CloudFormation::Stack"
const isBusy = (s: string) => s.endsWith("_IN_PROGRESS")

const notFoundStack = (name: string) => err(404, "StackNotFound", `stack "${name}" does not exist`)

function summary(s: Stack): StackSummary {
  return {
    name: s.name,
    arn: s.arn,
    status: s.status,
    status_reason: s.status_reason ?? "",
    description: s.description ?? "",
    resources: Object.keys(s.resources ?? {}).length,
    created_at: s.created_at,
    updated_at: s.updated_at,
  }
}

function ev(s: Stack, logical: string, type: string, status: string, reason?: string) {
  const e: StackEvent = { time: nowIso(), logical_id: logical, type, status, ...(reason ? { reason } : {}) }
  s.events = [e, ...(s.events ?? [])]
  s.updated_at = e.time
}

/** validates supplied parameters against the template's declarations (defaults are filled in). */
function resolveParams(t: ParsedTemplate, input: Record<string, unknown> = {}, prev: Record<string, unknown> = {}): Record<string, unknown> {
  const out: Record<string, unknown> = {}
  for (const [name, def] of Object.entries(t.parameters)) {
    let v = input[name] ?? (def.NoEcho ? undefined : prev[name])
    if (v === undefined) v = prev[name] ?? def.Default
    if (v === undefined || v === "") throw badRequest(`parameter ${name} is required`)
    if (def.Type === "Number" && isNaN(Number(v))) throw badRequest(`parameter ${name} must be a number`)
    if (def.AllowedValues?.length && !def.AllowedValues.some((a) => String(a) === String(v))) throw badRequest(`parameter ${name} must be one of [${def.AllowedValues.join(" ")}]`)
    out[name] = v
  }
  for (const name of Object.keys(input)) if (!(name in t.parameters)) throw badRequest(`template has no parameter "${name}"`)
  return out
}

const ID_PREFIX: Record<string, string> = {
  "HC::EC2::VPC": "vpc-",
  "HC::EC2::Subnet": "subnet-",
  "HC::EC2::SecurityGroup": "sg-",
  "HC::EC2::Volume": "vol-",
  "HC::EC2::Instance": "i-",
  "HC::EFS::FileSystem": "fs-",
}

function physicalFor(stack: string, type: string, id: string, text: string, params: Record<string, unknown>): { physical: string; attrs: Record<string, unknown> } {
  const name = guessName(text, params, stack)
  const fallback = `${stack}-${id.toLowerCase()}`.slice(0, 60)
  const n = name || fallback
  if (ID_PREFIX[type]) return { physical: uid(ID_PREFIX[type], type === "HC::EFS::FileSystem" ? 8 : 17), attrs: { name: n } }
  switch (type) {
    case "HC::Lambda::EventSourceMapping":
      return { physical: uuid(), attrs: {} }
    case "HC::KMS::Key":
      return { physical: uuid(), attrs: { arn: arn("kms", `key/${stableId("", stack + id, 8)}`) } }
    case "HC::ApiGateway::Api":
      return { physical: uid("", 10), attrs: { name: n } }
    case "HC::Route53::HostedZone":
      return { physical: "Z" + uid("", 12).toUpperCase(), attrs: { name: n } }
    case "HC::ACM::Certificate":
      return { physical: uuid(), attrs: {} }
    case "HC::Cognito::UserPool":
      return { physical: `us-east-1_${uid("", 9)}`, attrs: { name: n } }
    case "HC::ECS::TaskDefinition":
      return { physical: `${n}:1`, attrs: { family: n, revision: 1 } }
    case "HC::S3::Bucket":
      return { physical: n, attrs: { arn: `arn:aws:s3:::${n}` } }
    case "HC::SQS::Queue":
      return { physical: n, attrs: { arn: arn("sqs", n), url: `http://sqs.us-east-1.homecloud.local/123456789012/${n}` } }
    case "HC::SNS::Topic":
      return { physical: n, attrs: { arn: arn("sns", n) } }
    case "HC::Lambda::Function":
      return { physical: n, attrs: { arn: arn("lambda", `function:${n}`) } }
    case "HC::DynamoDB::Table":
      return { physical: n, attrs: { arn: arn("dynamodb", `table/${n}`) } }
    default:
      return { physical: n, attrs: { arn: arn("cloudformation", `${type.split("::").pop()?.toLowerCase()}/${n}`) } }
  }
}

function outputsFor(t: ParsedTemplate, res: Record<string, StackResource>, params: Record<string, unknown>): Record<string, unknown> {
  const out: Record<string, unknown> = {}
  for (const o of t.outputs) {
    const ref = /!Ref\s+(\w+)/.exec(o.text) ?? /"Ref"\s*:\s*"(\w+)"/.exec(o.text)
    const ga = /!GetAtt\s+(\w+)\.([\w.]+)/.exec(o.text)
    const sub = /!Sub\s+["']([^"']*)["']/.exec(o.text)
    if (ga) out[o.name] = (res[ga[1]]?.attributes?.[ga[2]] as unknown) ?? res[ga[1]]?.physical_id ?? ""
    else if (ref) out[o.name] = res[ref[1]]?.physical_id ?? String(params[ref[1]] ?? "")
    else if (sub) out[o.name] = sub[1].replace(/\$\{(\w+)\}/g, (_, k) => res[k]?.physical_id ?? String(params[k] ?? k))
    else {
      const v = scalar(o.text, "Value")
      if (v !== undefined) out[o.name] = v
    }
  }
  return out
}
const scalar = (text: string, key: string) => {
  const m = new RegExp(`${key}"?\\s*:\\s*("[^"]*"|'[^']*'|[^,}\\n]+)`).exec(text)
  return m ? m[1].trim().replace(/^["']|["']$/g, "") : undefined
}

const stepMs = (type: string) => (type === "HC::RDS::DBInstance" || type === "HC::ELB::LoadBalancer" || type === "HC::EC2::Instance" ? 1400 : 600)

/** creates the resources one by one (each takes a moment), then completes the stack. */
function simulateDeploy(name: string, t: ParsedTemplate, params: Record<string, unknown>, kind: "CREATE" | "UPDATE") {
  let at = 500
  const texts = new Map(t.resources.map((r) => [r.id, r]))
  // resources that already exist and are unchanged in an update are left alone
  const todo = t.order.filter((id) => {
    const cur = find(name)?.resources?.[id]
    return !(kind === "UPDATE" && cur && cur.type === texts.get(id)?.type)
  })
  for (const id of todo) {
    const r = texts.get(id)!
    const begin = at
    later(begin, () => {
      const s = find(name)
      if (!s || !isBusy(s.status)) return
      s.resources = s.resources ?? {}
      s.order = s.order ?? []
      const existing = s.resources[id]
      s.resources[id] = { logical_id: id, type: r.type, physical_id: existing?.physical_id ?? "", status: kind === "UPDATE" && existing ? "UPDATE_IN_PROGRESS" : "CREATE_IN_PROGRESS", updated_at: nowIso() }
      if (!s.order.includes(id)) s.order.push(id)
      ev(s, id, r.type, s.resources[id].status)
    })
    at += stepMs(r.type)
    later(at, () => {
      const s = find(name)
      if (!s || !isBusy(s.status) || !s.resources?.[id]) return
      const p = physicalFor(name, r.type, id, r.text, params)
      const cur = s.resources[id]
      cur.physical_id = p.physical
      cur.attributes = p.attrs
      cur.status = cur.status === "UPDATE_IN_PROGRESS" ? "UPDATE_COMPLETE" : "CREATE_COMPLETE"
      cur.updated_at = nowIso()
      ev(s, id, r.type, cur.status)
    })
    at += 150
  }
  later(at + 400, () => finalize(name, t, params, kind))
}

function finalize(name: string, t: ParsedTemplate, params: Record<string, unknown>, kind: "CREATE" | "UPDATE") {
  const s = find(name)
  if (!s || !isBusy(s.status)) return
  s.resources = s.resources ?? {}
  // resources dropped from the template are removed by an update
  const keep = new Set(t.order)
  for (const id of Object.keys(s.resources)) {
    if (!keep.has(id)) {
      ev(s, id, s.resources[id].type, "DELETE_COMPLETE", "removed from the template")
      delete s.resources[id]
    }
  }
  s.order = t.order.filter((id) => s.resources?.[id])
  s.outputs = outputsFor(t, s.resources, params)
  s.status = `${kind}_COMPLETE`
  s.status_reason = ""
  ev(s, name, STACK, s.status)
}

function view(s: Stack): Stack {
  // NoEcho parameters are masked, like the real API
  let params = s.parameters
  try {
    const t = parseTemplate(s.template)
    if (params) {
      params = { ...params }
      for (const [k, d] of Object.entries(t.parameters)) if (d.NoEcho && k in params) params[k] = "****"
    }
  } catch {
    // keep as is
  }
  return { ...s, parameters: params }
}

function seedTypes() {
  return [...RESOURCE_TYPES].sort()
}

function onLoad(state: State) {
  const st = state.cfn as CfnState
  for (const s of [...st.stacks]) {
    if (!isBusy(s.status)) continue
    if (s.status === "DELETE_IN_PROGRESS") {
      st.stacks.splice(st.stacks.indexOf(s), 1)
      continue
    }
    try {
      const t = parseTemplate(s.template)
      const kind = s.status.startsWith("UPDATE") ? "UPDATE" : "CREATE"
      const params = s.parameters ?? {}
      for (const id of t.order) {
        const r = t.resources.find((x) => x.id === id)!
        const cur = s.resources?.[id]
        if (cur?.status.endsWith("_COMPLETE")) continue
        const p = physicalFor(s.name, r.type, id, r.text, params)
        s.resources = s.resources ?? {}
        s.resources[id] = { logical_id: id, type: r.type, physical_id: p.physical, status: `${kind}_COMPLETE`, attributes: p.attrs, updated_at: nowIso() }
      }
      s.status = `${kind}_COMPLETE`
      s.order = t.order
      s.outputs = outputsFor(t, s.resources ?? {}, params)
    } catch {
      s.status = "CREATE_COMPLETE"
    }
    s.status_reason = ""
  }
}

const service: DemoService = {
  name: "cfn",
  seed: (): CfnState => ({ stacks: seedStacks() }),
  onLoad,
  routes: (r) => {
    r.get("/api/v1/cloudformation/stacks", () => cs().stacks.filter((s) => s.status !== "DELETE_COMPLETE").map(summary))
    r.get("/api/v1/cloudformation/stacks/:name", ({ params }) => {
      const s = find(params.name)
      if (!s) throw notFoundStack(params.name)
      return view(s)
    })
    r.get("/api/v1/cloudformation/resource-types", seedTypes)
    r.post("/api/v1/cloudformation/validate", ({ body }): ValidateTemplateResult => {
      try {
        const t = parseTemplate(String(body?.template ?? ""))
        return { valid: true, description: t.description, parameters: Object.keys(t.parameters).length ? t.parameters : null, creation_order: t.order }
      } catch (e) {
        if (e instanceof TemplateError) return { valid: false, error: e.message }
        throw e
      }
    })
    r.post("/api/v1/cloudformation/stacks", ({ body }) => {
      const b = (body ?? {}) as StackInput
      const name = String(b.name ?? "")
      if (!/^[a-zA-Z][a-zA-Z0-9-]{0,127}$/.test(name)) throw badRequest("stack names start with a letter and contain letters, digits and hyphens")
      let t: ParsedTemplate
      try {
        t = parseTemplate(b.template ?? "")
      } catch (e) {
        throw badRequest(e instanceof Error ? e.message : String(e))
      }
      const params = resolveParams(t, b.parameters)
      if (find(name)) throw err(409, "AlreadyExistsException", `stack "${name}" already exists`)
      const now = nowIso()
      const s: Stack = {
        name,
        arn: arn("cloudformation", `stack/${name}/${stableId("", name + now, 8)}-${stableId("", name, 4)}`),
        status: "CREATE_IN_PROGRESS",
        status_reason: "",
        description: t.description,
        template: b.template,
        parameters: params,
        resources: {},
        order: [],
        outputs: {},
        events: [{ time: now, logical_id: name, type: STACK, status: "CREATE_IN_PROGRESS", reason: "user initiated" }],
        created_at: now,
        updated_at: now,
      }
      cs().stacks.unshift(s)
      simulateDeploy(name, t, params, "CREATE")
      return view(s)
    })
    r.put("/api/v1/cloudformation/stacks/:name", ({ params, body }) => {
      const s = find(params.name)
      if (!s) throw notFoundStack(params.name)
      if (!["CREATE_COMPLETE", "UPDATE_COMPLETE", "UPDATE_FAILED"].includes(s.status)) throw err(409, "ValidationError", `a stack in ${s.status} cannot be updated`)
      const b = (body ?? {}) as StackInput
      const template = b.template || s.template
      let t: ParsedTemplate
      try {
        t = parseTemplate(template)
      } catch (e) {
        throw badRequest(e instanceof Error ? e.message : String(e))
      }
      const merged = resolveParams(t, b.parameters, s.parameters ?? {})
      s.template = template
      s.parameters = merged
      s.description = t.description
      s.status = "UPDATE_IN_PROGRESS"
      s.status_reason = ""
      ev(s, s.name, STACK, "UPDATE_IN_PROGRESS", "user initiated")
      simulateDeploy(s.name, t, merged, "UPDATE")
      return view(s)
    })
    r.del("/api/v1/cloudformation/stacks/:name", ({ params }) => {
      const s = find(params.name)
      if (!s) throw notFoundStack(params.name)
      if (isBusy(s.status)) throw err(409, "StackBusy", `stack is ${s.status}`)
      const name = s.name
      s.status = "DELETE_IN_PROGRESS"
      s.status_reason = ""
      ev(s, name, STACK, "DELETE_IN_PROGRESS", "user initiated")
      const ids = [...(s.order ?? [])].reverse()
      let at = 400
      for (const id of ids) {
        const type = s.resources?.[id]?.type ?? ""
        later(at, () => {
          const c = find(name)
          if (!c || c.status !== "DELETE_IN_PROGRESS" || !c.resources?.[id]) return
          c.resources[id].status = "DELETE_IN_PROGRESS"
          ev(c, id, type, "DELETE_IN_PROGRESS")
        })
        at += 500
        later(at, () => {
          const c = find(name)
          if (!c || c.status !== "DELETE_IN_PROGRESS") return
          if (c.resources) delete c.resources[id]
          ev(c, id, type, "DELETE_COMPLETE")
        })
        at += 100
      }
      later(at + 300, () => {
        const st = cs()
        const i = st.stacks.findIndex((x) => x.name === name && x.status === "DELETE_IN_PROGRESS")
        if (i >= 0) st.stacks.splice(i, 1)
      })
      return view(s)
    })
  },
}

export default service
