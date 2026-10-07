"use client"

import { useState } from "react"
import { toast } from "sonner"

import { ConfirmDialog } from "@/components/console/confirm-dialog"
import { StatusBadge, type Tone } from "@/components/console/status-badge"
import { api, seg } from "@/lib/api"
import { revalidate } from "@/lib/hooks"
import { pluralize } from "@/lib/format"

export const CFN_PATH = "/api/v1/cloudformation"
export const STACKS_PATH = "/api/v1/cloudformation/stacks"
export const VALIDATE_PATH = "/api/v1/cloudformation/validate"
export const TYPES_PATH = "/api/v1/cloudformation/resource-types"

export const stackPath = (name: string) => `${STACKS_PATH}/${seg(name)}`
export const stackHref = (name: string, tab?: string) => `/cloudformation/stack/?name=${encodeURIComponent(name)}${tab ? `&tab=${tab}` : ""}`
export const updateStackHref = (name: string) => `/cloudformation/create/?update=${encodeURIComponent(name)}`
export const CREATE_HREF = "/cloudformation/create/"

export const STACK_NAME_RE = /^[a-zA-Z][a-zA-Z0-9-]{0,127}$/

export function stackNameError(name: string): string | null {
  if (!name) return "Enter a stack name"
  if (name.length > 128) return "Stack names are at most 128 characters"
  if (!STACK_NAME_RE.test(name)) return "Start with a letter; use only letters, digits and hyphens (-)"
  return null
}

// ---- status ----

export const isInProgress = (status?: string) => !!status && status.endsWith("_IN_PROGRESS")

/** Stacks in these states can be updated. */
export const canUpdate = (status: string) => !isInProgress(status) && status !== "ROLLBACK_COMPLETE" && status !== "DELETE_COMPLETE"
/** Stacks in these states can be deleted (a stuck DELETE_IN_PROGRESS can be retried by the API, but not from here). */
export const canDelete = (status: string) => !isInProgress(status) && status !== "DELETE_COMPLETE"

/** pollInterval: 2 s while any stack/resource is in progress, 15 s otherwise. */
export const pollInterval = (statuses: (string | undefined)[]) => (statuses.some(isInProgress) ? 2000 : 15_000)

/**
 * stackStatusTone maps a CloudFormation status to a StatusBadge tone (statusTone
 * doesn't know these words): *_COMPLETE success, *_IN_PROGRESS warning,
 * *_FAILED and ROLLBACK_COMPLETE danger, a completed update rollback warning
 * (the stack is back on its previous, working configuration), deletes neutral.
 */
export function stackStatusTone(status: string): Tone {
  if (isInProgress(status)) return "warning"
  if (status.endsWith("_FAILED")) return "danger"
  // A plain ROLLBACK_COMPLETE means the creation failed and was undone; the stack can only be deleted.
  if (status === "ROLLBACK_COMPLETE") return "danger"
  if (status.endsWith("ROLLBACK_COMPLETE")) return "warning"
  if (status === "DELETE_COMPLETE" || status === "DELETE_SKIPPED") return "neutral"
  if (status.endsWith("_COMPLETE")) return "success"
  return "neutral"
}

/** stackStatusLabel: "CREATE_IN_PROGRESS" -> "Create in progress". */
export function stackStatusLabel(status: string): string {
  const w = status.toLowerCase().replace(/_+/g, " ").trim()
  return w.charAt(0).toUpperCase() + w.slice(1)
}

/** StackStatusBadge renders a stack, resource or event status; in-progress states spin. */
export function StackStatusBadge({ status, className }: { status: string; className?: string }) {
  if (!status) return <span className="text-muted-foreground">-</span>
  return (
    <span title={status} className="inline-flex">
      <StatusBadge
        // "updating" is one of StatusBadge's transitional states, which get a spinner.
        status={isInProgress(status) ? "updating" : status}
        tone={stackStatusTone(status)}
        label={stackStatusLabel(status)}
        className={className}
      />
    </span>
  )
}

// ---- resource types ----

export interface TypeMeta {
  /** console service name */
  service: string
  /** console page for the resource (or its list) */
  href: (physicalId: string) => string
  /** true when href points at a list, not the resource itself */
  listOnly?: boolean
  /** what Ref returns (the physical ID) */
  ref: string
  /** the stack waits for this state before continuing */
  waits?: string
  /** the list page of the service */
  consoleHref: string
}

const q = (path: string, key: string) => (id: string) => `${path}?${key}=${encodeURIComponent(id)}`
const list = (path: string) => () => path

export const TYPE_META: Record<string, TypeMeta> = {
  "HC::ApiGateway::Api": { service: "API Gateway", href: q("/apigateway/api/", "id"), ref: "API ID", consoleHref: "/apigateway/" },
  "HC::CloudWatch::Alarm": { service: "CloudWatch", href: list("/cloudwatch/alarms/"), listOnly: true, ref: "Alarm name", consoleHref: "/cloudwatch/alarms/" },
  "HC::DynamoDB::Table": { service: "DynamoDB", href: q("/dynamodb/table/", "name"), ref: "Table name", consoleHref: "/dynamodb/" },
  "HC::EC2::Instance": { service: "EC2", href: q("/ec2/instance/", "id"), ref: "Instance ID", waits: "running", consoleHref: "/ec2/" },
  "HC::EC2::SecurityGroup": { service: "VPC", href: q("/vpc/security-group/", "id"), ref: "Security group ID", consoleHref: "/vpc/security-groups/" },
  "HC::EC2::Subnet": { service: "VPC", href: list("/vpc/subnets/"), listOnly: true, ref: "Subnet ID", consoleHref: "/vpc/subnets/" },
  "HC::EC2::VPC": { service: "VPC", href: list("/vpc/"), listOnly: true, ref: "VPC ID", consoleHref: "/vpc/" },
  "HC::EC2::Volume": { service: "EC2", href: list("/ec2/volumes/"), listOnly: true, ref: "Volume ID", consoleHref: "/ec2/volumes/" },
  "HC::ECR::Repository": { service: "ECR", href: q("/ecr/repository/", "name"), ref: "Repository name", consoleHref: "/ecr/" },
  "HC::ECS::Service": { service: "ECS", href: q("/ecs/service/", "name"), ref: "Service name", consoleHref: "/ecs/" },
  "HC::ECS::TaskDefinition": {
    service: "ECS",
    href: (id) => {
      const i = id.lastIndexOf(":")
      if (i <= 0) return "/ecs/task-definitions/"
      return `/ecs/task-definition/?family=${encodeURIComponent(id.slice(0, i))}&revision=${encodeURIComponent(id.slice(i + 1))}`
    },
    ref: "family:revision",
    consoleHref: "/ecs/task-definitions/",
  },
  "HC::EFS::FileSystem": { service: "EFS", href: q("/efs/file-system/", "id"), ref: "File system ID", consoleHref: "/efs/" },
  "HC::ELB::LoadBalancer": { service: "ELB", href: q("/elb/load-balancer/", "name"), ref: "Load balancer name", waits: "active", consoleHref: "/elb/" },
  "HC::ELB::TargetGroup": { service: "ELB", href: q("/elb/target-group/", "name"), ref: "Target group name", consoleHref: "/elb/target-groups/" },
  "HC::Events::Rule": { service: "EventBridge", href: q("/events/rule/", "name"), ref: "Rule name", consoleHref: "/events/" },
  "HC::IAM::Group": { service: "IAM", href: q("/iam/group/", "name"), ref: "Group name", consoleHref: "/iam/groups/" },
  "HC::IAM::Policy": { service: "IAM", href: q("/iam/policy/", "name"), ref: "Policy name", consoleHref: "/iam/policies/" },
  "HC::IAM::User": { service: "IAM", href: q("/iam/user/", "name"), ref: "User name", consoleHref: "/iam/users/" },
  "HC::KMS::Key": { service: "KMS", href: q("/kms/key/", "id"), ref: "Key ID", consoleHref: "/kms/" },
  "HC::Lambda::EventSourceMapping": { service: "Lambda", href: list("/lambda/triggers/"), listOnly: true, ref: "Mapping ID", consoleHref: "/lambda/triggers/" },
  "HC::Lambda::Function": { service: "Lambda", href: q("/lambda/function/", "name"), ref: "Function name", consoleHref: "/lambda/" },
  "HC::Logs::LogGroup": { service: "CloudWatch Logs", href: q("/cloudwatch/logs/group/", "name"), ref: "Log group name", consoleHref: "/cloudwatch/logs/" },
  "HC::RDS::DBInstance": { service: "RDS", href: q("/rds/instance/", "id"), ref: "DB instance ID", waits: "available", consoleHref: "/rds/" },
  "HC::S3::Bucket": { service: "S3", href: q("/s3/bucket/", "name"), ref: "Bucket name", consoleHref: "/s3/" },
  "HC::SNS::Topic": { service: "SNS", href: q("/sns/topic/", "name"), ref: "Topic name", consoleHref: "/sns/" },
  "HC::SQS::Queue": { service: "SQS", href: q("/sqs/queue/", "name"), ref: "Queue name", consoleHref: "/sqs/" },
  "HC::SSM::Parameter": { service: "Systems Manager", href: q("/ssm/parameter/", "name"), ref: "Parameter name", consoleHref: "/ssm/" },
  "HC::SecretsManager::Secret": { service: "Secrets Manager", href: q("/secrets/secret/", "name"), ref: "Secret name", consoleHref: "/secrets/" },
  "HC::StepFunctions::StateMachine": { service: "Step Functions", href: q("/sfn/state-machine/", "name"), ref: "State machine name", consoleHref: "/sfn/" },
}

/** typeParts: "HC::SQS::Queue" -> { service: "SQS", resource: "Queue" }. */
export function typeParts(type: string) {
  const p = type.split("::")
  return { service: p.length >= 3 ? p[1] : type, resource: p.length >= 3 ? p.slice(2).join("::") : "" }
}

/** resourceHref links a stack resource's physical ID to its console page, when there is one. */
export function resourceHref(type: string, physicalId: string): string | null {
  if (!physicalId) return null
  return TYPE_META[type]?.href(physicalId) ?? null
}

// ---- template inspection (display only; the server parses templates) ----

/** templateFormat guesses the template syntax for display. */
export function templateFormat(src: string): "JSON" | "YAML" {
  return src.trimStart().startsWith("{") ? "JSON" : "YAML"
}

function tryJson(src: string): Record<string, unknown> | null {
  if (!src.trimStart().startsWith("{")) return null
  try {
    const v = JSON.parse(src)
    return v && typeof v === "object" ? (v as Record<string, unknown>) : null
  } catch {
    return null
  }
}

const indentOf = (line: string) => line.length - line.trimStart().length
const isBlank = (line: string) => !line.trim() || line.trimStart().startsWith("#")
const unquote = (s: string) => s.trim().replace(/^["']|["']$/g, "")
const reEsc = (s: string) => s.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")

/** yamlSection returns the lines of a top-level YAML key ("Resources:"). */
function yamlSection(lines: string[], key: string): string[] {
  const start = lines.findIndex((l) => new RegExp(`^${reEsc(key)}\\s*:`).test(l))
  if (start < 0) return []
  const out: string[] = []
  for (let i = start + 1; i < lines.length; i++) {
    if (!isBlank(lines[i]) && indentOf(lines[i]) === 0) break
    out.push(lines[i])
  }
  return out
}

/** yamlChildren maps each direct child key of a section to its inline text and nested lines. */
function yamlChildren(section: string[]): Map<string, { inline: string; body: string[] }> {
  const out = new Map<string, { inline: string; body: string[] }>()
  const first = section.find((l) => !isBlank(l))
  if (!first) return out
  const ind = indentOf(first)
  let cur: { inline: string; body: string[] } | null = null
  for (const l of section) {
    if (isBlank(l)) continue
    if (indentOf(l) === ind) {
      const m = l.trim().match(/^["']?([A-Za-z0-9]+)["']?\s*:(.*)$/)
      if (!m) {
        cur = null
        continue
      }
      cur = { inline: m[2], body: [] }
      out.set(m[1], cur)
    } else if (cur && indentOf(l) > ind) {
      cur.body.push(l)
    }
  }
  return out
}

/** yamlField finds `Field: value` directly under a child (flow or block style). */
function yamlField(child: { inline: string; body: string[] }, field: string): string | undefined {
  const flow = child.inline.match(new RegExp(`[{,]\\s*${field}\\s*:\\s*("[^"]*"|'[^']*'|[^,}]+)`))
  if (flow) return unquote(flow[1])
  const first = child.body.find((l) => !isBlank(l))
  if (!first) return undefined
  const ind = indentOf(first)
  for (const l of child.body) {
    if (indentOf(l) !== ind) continue
    const m = l.trim().match(new RegExp(`^${field}\\s*:\\s*(.+)$`))
    if (m) return unquote(m[1])
  }
  return undefined
}

/** templateResourceTypes maps logical IDs to resource types, best effort. */
export function templateResourceTypes(src: string): Record<string, string> {
  const out: Record<string, string> = {}
  const json = tryJson(src)
  if (json) {
    const res = json.Resources as Record<string, { Type?: string }> | undefined
    for (const [id, r] of Object.entries(res ?? {})) if (r && typeof r.Type === "string") out[id] = r.Type
    return out
  }
  for (const [id, child] of yamlChildren(yamlSection(src.split("\n"), "Resources"))) {
    const t = yamlField(child, "Type")
    if (t) out[id] = t
  }
  return out
}

/** templateOutputDescriptions maps output names to their Description, best effort. */
export function templateOutputDescriptions(src: string): Record<string, string> {
  const out: Record<string, string> = {}
  const json = tryJson(src)
  if (json) {
    const outs = json.Outputs as Record<string, { Description?: string }> | undefined
    for (const [k, o] of Object.entries(outs ?? {})) if (o && typeof o.Description === "string") out[k] = o.Description
    return out
  }
  for (const [k, child] of yamlChildren(yamlSection(src.split("\n"), "Outputs"))) {
    const d = yamlField(child, "Description")
    if (d) out[k] = d
  }
  return out
}

/** valueText renders a parameter/output value (lists and objects as JSON). */
export function valueText(v: unknown): string {
  if (v === null || v === undefined) return ""
  if (typeof v === "string") return v
  if (Array.isArray(v) && v.every((x) => typeof x !== "object")) return v.join(",")
  return JSON.stringify(v)
}

// ---- delete ----

/** useDeleteStack returns an opener and the typed-confirmation dialog for deleting a stack. */
export function useDeleteStack(onDeleted?: (name: string) => void) {
  const [target, setTarget] = useState<{ name: string; resources: number } | null>(null)
  const dialog = (
    <ConfirmDialog
      open={!!target}
      onOpenChange={(o) => !o && setTarget(null)}
      title={`Delete stack ${target?.name ?? ""}?`}
      description={
        <>
          Deleting the stack deletes its {target ? pluralize(target.resources, "resource") : "resources"} in reverse creation order (S3 buckets and ECR
          repositories are emptied first). Resources with <span className="font-mono text-[13px]">DeletionPolicy: Retain</span> are kept. The deletion
          runs in the background; follow it on the Events tab.
        </>
      }
      confirmText={target?.name}
      onConfirm={async () => {
        if (!target) return
        await api.del(stackPath(target.name))
        toast.success(`Deleting stack ${target.name}`)
        await revalidate(STACKS_PATH)
        onDeleted?.(target.name)
      }}
    />
  )
  return { open: (name: string, resources: number) => setTarget({ name, resources }), dialog }
}
