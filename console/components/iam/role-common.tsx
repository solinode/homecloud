"use client"

import { Building2, Cloud, Globe, User, UserCog } from "lucide-react"

import type { IamRole, TrustPolicyDocument, TrustPrincipal } from "@/lib/types"

import { asList } from "./common"

export const roleHref = (n: string) => `/iam/role/?name=${encodeURIComponent(n)}`
export const createRoleHref = (service?: string) => `/iam/roles/create/${service ? `?service=${encodeURIComponent(service)}` : ""}`

/** Service principals offered by the create role wizard ("AWS service" trust). */
export const SERVICE_PRINCIPALS: { principal: string; name: string; description: string }[] = [
  { principal: "lambda.amazonaws.com", name: "Lambda", description: "Allows Lambda functions to call HomeCloud services on your behalf." },
  { principal: "ec2.amazonaws.com", name: "EC2", description: "Allows EC2 instances to call HomeCloud services on your behalf." },
  { principal: "ecs-tasks.amazonaws.com", name: "Elastic Container Service Task", description: "Allows ECS tasks to call HomeCloud services on your behalf." },
  { principal: "states.amazonaws.com", name: "Step Functions", description: "Allows state machines to invoke Lambda functions and other services." },
  { principal: "events.amazonaws.com", name: "EventBridge", description: "Allows EventBridge rules to deliver events to targets." },
  { principal: "apigateway.amazonaws.com", name: "API Gateway", description: "Allows API Gateway to call integrations and push logs to CloudWatch." },
  { principal: "cloudformation.amazonaws.com", name: "CloudFormation", description: "Allows CloudFormation to create and manage stack resources on your behalf." },
  { principal: "scheduler.amazonaws.com", name: "EventBridge Scheduler", description: "Allows schedules to invoke their targets." },
]

export const serviceName = (principal: string) => SERVICE_PRINCIPALS.find((s) => s.principal === principal)?.name ?? principal.replace(/\.amazonaws\.com$/, "")

export const TRUST_TEMPLATE: TrustPolicyDocument = {
  Version: "2012-10-17",
  Statement: [{ Effect: "Allow", Principal: { Service: "lambda.amazonaws.com" }, Action: "sts:AssumeRole" }],
}

const one = (v: string[]) => (v.length === 1 ? v[0] : v)

/** serviceTrust is the trust policy for "AWS service" roles. */
export function serviceTrust(services: string[]): TrustPolicyDocument {
  return { Version: "2012-10-17", Statement: [{ Effect: "Allow", Principal: { Service: one(services) }, Action: "sts:AssumeRole" }] }
}

/** accountTrust is the trust policy for "This account" roles: the account root, or specific principal ARNs. */
export function accountTrust(accountId: string, principals: string[]): TrustPolicyDocument {
  const aws = principals.length ? principals : [`arn:aws:iam::${accountId}:root`]
  return { Version: "2012-10-17", Statement: [{ Effect: "Allow", Principal: { AWS: one(aws) }, Action: "sts:AssumeRole" }] }
}

const strList = (v: unknown) =>
  (typeof v === "string" && v.length > 0) || (Array.isArray(v) && v.length > 0 && v.every((x) => typeof x === "string" && x.length > 0))

/** validateTrustPolicy mirrors PolicyDocument.ValidateTrust in the API. */
export function validateTrustPolicy(parsed: unknown): string | null {
  if (!parsed || typeof parsed !== "object" || Array.isArray(parsed)) return "The trust policy must be a JSON object"
  const st = (parsed as { Statement?: unknown }).Statement
  if (!Array.isArray(st) || st.length === 0) return "Statement must be a non-empty array"
  for (let i = 0; i < st.length; i++) {
    const s = st[i] as Record<string, unknown> | null
    if (!s || typeof s !== "object" || Array.isArray(s)) return `Statement[${i}] must be an object`
    if (s.Effect !== "Allow" && s.Effect !== "Deny") return `Statement[${i}].Effect must be "Allow" or "Deny"`
    const p = s.Principal
    if (p === undefined) return `Statement[${i}].Principal is required in a trust policy`
    if (typeof p === "string") {
      if (p !== "*") return `Statement[${i}].Principal must be "*" or an object`
    } else if (!p || typeof p !== "object" || Array.isArray(p)) {
      return `Statement[${i}].Principal must be "*" or an object`
    } else {
      const keys = Object.keys(p)
      const bad = keys.find((k) => !["AWS", "Service", "Federated"].includes(k))
      if (bad) return `Statement[${i}].Principal.${bad}: use AWS, Service or Federated`
      if (!keys.length) return `Statement[${i}].Principal must name at least one principal`
      for (const k of keys) if (!strList((p as Record<string, unknown>)[k])) return `Statement[${i}].Principal.${k} must be a string or a non-empty array of strings`
    }
    if (s.Resource !== undefined) return `Statement[${i}].Resource is not allowed in a trust policy`
    if (!strList(s.Action)) return `Statement[${i}].Action must be a string or a non-empty array of strings`
    const bad = asList(s.Action as string | string[]).find((a) => !a.toLowerCase().startsWith("sts:"))
    if (bad) return `Statement[${i}].Action "${bad}": trust policies only grant sts: actions`
  }
  return null
}

export interface TrustedEntity {
  kind: "service" | "account" | "user" | "role" | "federated" | "any" | "other"
  label: string
  value: string
}

function awsEntity(arn: string): TrustedEntity {
  if (arn === "*") return { kind: "any", label: "Any AWS principal", value: arn }
  if (/^\d{12}$/.test(arn)) return { kind: "account", label: `Account: ${arn}`, value: arn }
  const m = /^arn:[\w-]+:(iam|sts)::(\d*):(.*)$/.exec(arn)
  if (m) {
    const [, , acct, rest] = m
    if (rest === "root") return { kind: "account", label: `Account: ${acct}`, value: arn }
    if (rest.startsWith("user/")) return { kind: "user", label: `User: ${rest.slice(rest.lastIndexOf("/") + 1)}`, value: arn }
    if (rest.startsWith("role/")) return { kind: "role", label: `Role: ${rest.slice(rest.lastIndexOf("/") + 1)}`, value: arn }
  }
  return { kind: "other", label: arn, value: arn }
}

/** trustedEntities lists the principals the Allow statements of a trust policy name. */
export function trustedEntities(doc: TrustPolicyDocument | null | undefined): TrustedEntity[] {
  const out: TrustedEntity[] = []
  const seen = new Set<string>()
  const push = (e: TrustedEntity) => {
    const k = `${e.kind}:${e.value}`
    if (!seen.has(k)) {
      seen.add(k)
      out.push(e)
    }
  }
  for (const st of doc?.Statement ?? []) {
    if (!st || st.Effect !== "Allow") continue
    const p: TrustPrincipal | undefined = st.Principal
    if (p === "*") {
      push({ kind: "any", label: "Anyone", value: "*" })
      continue
    }
    if (!p || typeof p !== "object") continue
    for (const s of asList(p.Service)) push({ kind: "service", label: `AWS service: ${serviceName(s)}`, value: s })
    for (const a of asList(p.AWS)) push(awsEntity(a))
    for (const f of asList(p.Federated)) push({ kind: "federated", label: `Identity provider: ${f}`, value: f })
  }
  return out
}

const ENTITY_ICON = { service: Cloud, account: Building2, user: User, role: UserCog, federated: Globe, any: Globe, other: Globe }

/** TrustedEntitiesList renders the trusted entities of a role compactly. */
export function TrustedEntitiesList({ role, max = 3 }: { role: Pick<IamRole, "assume_role_policy">; max?: number }) {
  const list = trustedEntities(role.assume_role_policy)
  if (!list.length) return <span className="text-muted-foreground">None</span>
  const shown = list.slice(0, max)
  return (
    <span className="flex flex-col gap-0.5">
      {shown.map((e) => {
        const Icon = ENTITY_ICON[e.kind]
        return (
          <span key={`${e.kind}:${e.value}`} className="flex min-w-0 items-center gap-1.5" title={e.value}>
            <Icon className="text-muted-foreground size-3.5 shrink-0" />
            <span className="truncate">{e.label}</span>
          </span>
        )
      })}
      {list.length > max && <span className="text-muted-foreground text-xs">and {list.length - max} more</span>}
    </span>
  )
}

/** formatSessionDuration renders a role's max session duration (seconds). */
export function formatSessionDuration(seconds: number): string {
  const h = Math.floor(seconds / 3600)
  const m = Math.round((seconds % 3600) / 60)
  if (!m) return h === 1 ? "1 hour" : `${h} hours`
  return `${h} h ${m} min`
}

export const SESSION_DURATIONS = [1, 2, 4, 6, 8, 12].map((h) => ({ value: h * 3600, label: h === 1 ? "1 hour" : `${h} hours` }))
