// A forgiving reader for CloudFormation templates (JSON or the common YAML subset),
// good enough to validate templates, list parameters/resources/outputs and derive
// the creation order for the demo. It does not evaluate intrinsic functions.

import type { CfnParamDef } from "@/lib/types"

export const RESOURCE_TYPES = [
  "HC::ACM::Certificate", "HC::ApiGateway::Api", "HC::AutoScaling::AutoScalingGroup", "HC::CloudWatch::Alarm", "HC::Cognito::UserPool", "HC::DynamoDB::Table",
  "HC::EC2::Instance", "HC::EC2::SecurityGroup", "HC::EC2::Subnet", "HC::EC2::VPC", "HC::EC2::Volume", "HC::ECR::Repository", "HC::ECS::Service",
  "HC::ECS::TaskDefinition", "HC::EFS::FileSystem", "HC::ELB::LoadBalancer", "HC::ELB::TargetGroup", "HC::Events::Rule", "HC::IAM::Group", "HC::IAM::Policy",
  "HC::IAM::User", "HC::KMS::Key", "HC::Lambda::EventSourceMapping", "HC::Lambda::Function", "HC::Logs::LogGroup", "HC::RDS::DBInstance",
  "HC::Route53::HostedZone", "HC::S3::Bucket", "HC::SNS::Topic", "HC::SQS::Queue", "HC::SSM::Parameter", "HC::SecretsManager::Secret", "HC::StepFunctions::StateMachine",
]

export interface ParsedResource {
  id: string
  type: string
  /** raw source text of the resource, used for name and dependency detection */
  text: string
}
export interface ParsedOutput {
  name: string
  text: string
}
export interface ParsedTemplate {
  description: string
  parameters: Record<string, CfnParamDef>
  resources: ParsedResource[]
  outputs: ParsedOutput[]
  /** creation order of logical IDs (dependencies first) */
  order: string[]
}

export class TemplateError extends Error {}

const indentOf = (l: string) => l.length - l.trimStart().length
const unquote = (s: string) => s.trim().replace(/^(["'])([\s\S]*)\1$/, "$2")

/** blockChildren returns the direct children (key -> source text) of a top level YAML section. */
function children(lines: string[]): { key: string; text: string }[] {
  const body = lines.filter((l) => l.trim() && !l.trim().startsWith("#"))
  if (!body.length) return []
  const ind = Math.min(...body.map(indentOf))
  const out: { key: string; text: string }[] = []
  for (const l of body) {
    if (indentOf(l) === ind) {
      const m = /^\s*["']?([A-Za-z0-9_.:-]+)["']?\s*:\s*(.*)$/.exec(l)
      if (m) out.push({ key: m[1], text: m[2] })
      else if (out.length) out[out.length - 1].text += "\n" + l
    } else if (out.length) out[out.length - 1].text += "\n" + l
  }
  return out
}

function scalarOf(text: string, key: string): string | undefined {
  const m = new RegExp(`(?:^|[\\s{,])${key}\\s*:\\s*("[^"]*"|'[^']*'|[^,}\\n]+)`).exec(text)
  return m ? unquote(m[1]) : undefined
}

function paramDef(text: string): CfnParamDef {
  const def: CfnParamDef = { Type: scalarOf(text, "Type") ?? "String" }
  const d = scalarOf(text, "Default")
  if (d !== undefined && d !== "") def.Default = def.Type === "Number" && d !== "" && !isNaN(Number(d)) ? Number(d) : d
  const desc = scalarOf(text, "Description")
  if (desc) def.Description = desc
  if (/NoEcho\s*:\s*true/i.test(text)) def.NoEcho = true
  const av = /AllowedValues\s*:\s*\[([^\]]*)\]/.exec(text)
  if (av) def.AllowedValues = av[1].split(",").map((x) => unquote(x)).filter(Boolean)
  else {
    const block = /AllowedValues\s*:\s*\n((?:\s*-\s*.+\n?)+)/.exec(text)
    if (block) def.AllowedValues = block[1].split("\n").map((l) => unquote(l.replace(/^\s*-\s*/, ""))).filter(Boolean)
  }
  return def
}

function fromYaml(src: string): { description: string; parameters: Record<string, CfnParamDef>; resources: ParsedResource[]; outputs: ParsedOutput[] } {
  const lines = src.replace(/\r/g, "").split("\n")
  const sections: Record<string, { head: string; lines: string[] }> = {}
  let cur: string | null = null
  for (const l of lines) {
    if (!l.trim() || l.trim().startsWith("#")) {
      if (cur) sections[cur].lines.push(l)
      continue
    }
    const m = /^([A-Za-z][A-Za-z0-9]*)\s*:\s*(.*)$/.exec(l)
    if (m && indentOf(l) === 0) {
      cur = m[1]
      sections[cur] = { head: m[2], lines: [] }
    } else if (cur) sections[cur].lines.push(l)
    else throw new TemplateError("template is not valid YAML or JSON: line 1 must be a top level key")
  }
  if (!Object.keys(sections).length) throw new TemplateError("template is not valid YAML or JSON")
  const description = unquote((sections.Description?.head ?? "").replace(/^[>|][-+]?$/, "") || sections.Description?.lines.map((l) => l.trim()).join(" ").trim() || "")
  const parameters: Record<string, CfnParamDef> = {}
  if (sections.Parameters) for (const c of children(sections.Parameters.lines)) parameters[c.key] = paramDef(c.text)
  const resources: ParsedResource[] = []
  if (sections.Resources) {
    for (const c of children(sections.Resources.lines)) {
      const type = scalarOf(c.text, "Type")
      if (!type) throw new TemplateError(`resource ${c.key} has no Type`)
      resources.push({ id: c.key, type, text: c.text })
    }
  }
  const outputs: ParsedOutput[] = []
  if (sections.Outputs) for (const c of children(sections.Outputs.lines)) outputs.push({ name: c.key, text: c.text })
  return { description, parameters, resources, outputs }
}

function fromJson(src: string) {
  let doc: Record<string, unknown>
  try {
    doc = JSON.parse(src)
  } catch (e) {
    throw new TemplateError(`template is not valid YAML or JSON: ${(e as Error).message}`)
  }
  const parameters: Record<string, CfnParamDef> = {}
  for (const [k, v] of Object.entries((doc.Parameters ?? {}) as Record<string, CfnParamDef>)) parameters[k] = { ...v, Type: v.Type ?? "String" }
  const resources: ParsedResource[] = Object.entries((doc.Resources ?? {}) as Record<string, { Type?: string }>).map(([id, r]) => {
    if (!r.Type) throw new TemplateError(`resource ${id} has no Type`)
    return { id, type: r.Type, text: JSON.stringify(r) }
  })
  const outputs: ParsedOutput[] = Object.entries((doc.Outputs ?? {}) as Record<string, unknown>).map(([name, o]) => ({ name, text: JSON.stringify(o) }))
  return { description: typeof doc.Description === "string" ? doc.Description : "", parameters, resources, outputs }
}

export function parseTemplate(src: string): ParsedTemplate {
  if (!src.trim()) throw new TemplateError("template is empty")
  if (src.length > 1 << 20) throw new TemplateError("template is larger than 1 MB")
  const t = src.trimStart().startsWith("{") ? fromJson(src) : fromYaml(src)
  if (!t.resources.length) throw new TemplateError("template must declare at least one resource")
  for (const r of t.resources) {
    if (!/^[A-Za-z0-9]{1,255}$/.test(r.id)) throw new TemplateError(`logical ID "${r.id}" must be alphanumeric`)
    if (!RESOURCE_TYPES.includes(r.type)) throw new TemplateError(`resource ${r.id} has unsupported type "${r.type}"`)
  }
  const ids = t.resources.map((r) => r.id)
  const deps = new Map<string, string[]>()
  for (const r of t.resources) {
    deps.set(
      r.id,
      ids.filter((o) => o !== r.id && new RegExp(`(^|[^A-Za-z0-9_])${o}([^A-Za-z0-9_]|$)`).test(r.text)),
    )
  }
  const order: string[] = []
  const state = new Map<string, number>()
  const visit = (id: string, path: string[]) => {
    if (state.get(id) === 2) return
    if (state.get(id) === 1) throw new TemplateError(`dependency cycle: ${[...path, id].join(" -> ")}`)
    state.set(id, 1)
    for (const d of deps.get(id) ?? []) visit(d, [...path, id])
    state.set(id, 2)
    order.push(id)
  }
  for (const id of ids) visit(id, [])
  return { ...t, order }
}

/** guessName pulls the resource's `name` property out of its source and substitutes parameters. */
export function guessName(text: string, params: Record<string, unknown>, stackName: string): string | undefined {
  const m = /(?:^|[\s{,"'])(?:name|Name|family|table_name)\s*["']?\s*:\s*(?:!Sub\s+|!Join\s+)?("[^"]*"|'[^']*'|[^\s,}\n]+)/.exec(text)
  if (!m) return undefined
  const v = unquote(m[1]).replace(/\$\{AWS::StackName\}/g, stackName).replace(/\$\{AWS::Region\}/g, "us-east-1")
  const out = v.replace(/\$\{([A-Za-z0-9]+)\}/g, (_, p) => String(params[p] ?? p))
  if (/^!/.test(out)) {
    const ref = /!Ref\s+(\w+)/.exec(out)
    return ref ? String(params[ref[1]] ?? "") || undefined : undefined
  }
  return out
}
