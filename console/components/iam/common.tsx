"use client"

import { useMemo, useState } from "react"
import useSWR from "swr"
import { Eye, EyeOff, RefreshCw, Search } from "lucide-react"

import { Button } from "@/components/ui/button"
import { Checkbox } from "@/components/ui/checkbox"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { RadioGroup, RadioGroupItem } from "@/components/ui/radio-group"
import { Skeleton } from "@/components/ui/skeleton"
import { CopyButton } from "@/components/console/copy-button"
import { StatusBadge } from "@/components/console/status-badge"
import { Tag } from "@/components/console/tag"
import { api, seg, type ApiError } from "@/lib/api"
import type { AccessKey, PolicyDocument, PolicyStatement, PolicySummary } from "@/lib/types"
import { cn } from "@/lib/utils"

export const IAM = "/api/v1/iam"
export const NAME_RE = /^[\w+=,.@-]{1,64}$/

export const userHref = (n: string) => `/iam/user/?name=${encodeURIComponent(n)}`
export const groupHref = (n: string) => `/iam/group/?name=${encodeURIComponent(n)}`
export const policyHref = (n: string) => `/iam/policy/?name=${encodeURIComponent(n)}`
export const instanceProfileHref = (n: string) => `/iam/instance-profile/?name=${encodeURIComponent(n)}`

export const LINK = "text-primary font-medium hover:underline"

export function nameError(kind: string, v: string): string | null {
  if (!v) return `Enter a ${kind} name.`
  if (!NAME_RE.test(v)) return `Use 1-64 letters, digits or + = , . @ _ - characters.`
  return null
}

/** signInUrl is where IAM users of this HomeCloud sign in to the console. */
export function signInUrl(): string {
  if (typeof window === "undefined") return "/login/"
  return `${window.location.origin}/login/`
}

/** generatePassword returns a random password with every character class. */
export function generatePassword(length = 16): string {
  const sets = ["ABCDEFGHJKLMNPQRSTUVWXYZ", "abcdefghijkmnopqrstuvwxyz", "23456789", "!@#$%^&*-_=+"]
  const all = sets.join("")
  const rnd = (n: number) => {
    const a = new Uint32Array(1)
    crypto.getRandomValues(a)
    return a[0] % n
  }
  const chars = sets.map((s) => s[rnd(s.length)])
  while (chars.length < length) chars.push(all[rnd(all.length)])
  for (let i = chars.length - 1; i > 0; i--) {
    const j = rnd(i + 1)
    ;[chars[i], chars[j]] = [chars[j], chars[i]]
  }
  return chars.join("")
}

const csvCell = (v: string) => (/[",\n]/.test(v) ? `"${v.replace(/"/g, '""')}"` : v)

export function csvLine(values: string[]): string {
  return values.map(csvCell).join(",")
}

/** downloadText saves a string as a file via a Blob URL. */
export function downloadText(filename: string, text: string, type = "text/csv") {
  const url = URL.createObjectURL(new Blob([text], { type }))
  const a = document.createElement("a")
  a.href = url
  a.download = filename
  document.body.appendChild(a)
  a.click()
  a.remove()
  setTimeout(() => URL.revokeObjectURL(url), 1000)
}

// ---- policy documents ----

export const POLICY_TEMPLATE: PolicyDocument = {
  Version: "2012-10-17",
  Statement: [{ Effect: "Allow", Action: ["s3:GetObject"], Resource: ["*"] }],
}

export const policyJson = (d: unknown) => JSON.stringify(d, null, 2)

const strList = (v: unknown) =>
  (typeof v === "string" && v.length > 0) || (Array.isArray(v) && v.length > 0 && v.every((x) => typeof x === "string" && x.length > 0))

/** statementsOf returns a document's statements; AWS allows Statement to be a single object. */
export function statementsOf<S>(doc: { Statement?: S[] | S } | null | undefined): S[] {
  const st = doc?.Statement
  if (st === undefined || st === null) return []
  return Array.isArray(st) ? st : [st]
}

/** statementCount is the number of statements of a (possibly malformed) document. */
export const statementCount = (doc: { Statement?: unknown } | null | undefined) => statementsOf(doc as { Statement?: unknown }).length

/** validatePolicy mirrors PolicyDocument.Validate in the API. */
export function validatePolicy(parsed: unknown): string | null {
  if (!parsed || typeof parsed !== "object" || Array.isArray(parsed)) return "The policy must be a JSON object"
  const doc = parsed as { Version?: unknown; Statement?: unknown }
  if (doc.Version !== undefined && doc.Version !== "2012-10-17" && doc.Version !== "2008-10-17") return 'Version must be "2012-10-17" or "2008-10-17"'
  const raw = doc.Statement
  if (raw === undefined || raw === null) return "Statement is required"
  const st = Array.isArray(raw) ? raw : [raw]
  if (st.length === 0) return "Statement must not be empty"
  for (let i = 0; i < st.length; i++) {
    const s = st[i] as Record<string, unknown> | null
    if (!s || typeof s !== "object" || Array.isArray(s)) return `Statement[${i}] must be an object`
    if (s.Effect !== "Allow" && s.Effect !== "Deny") return `Statement[${i}].Effect must be "Allow" or "Deny"`
    if (s.Action !== undefined && s.NotAction !== undefined) return `Statement[${i}] cannot have both Action and NotAction`
    if (s.Action === undefined && s.NotAction === undefined) return `Statement[${i}] needs an Action or NotAction`
    const ak = s.Action !== undefined ? "Action" : "NotAction"
    if (!strList(s[ak])) return `Statement[${i}].${ak} must be a string or a non-empty array of strings`
    const badAction = asList(s[ak] as string | string[]).find((a) => a !== "*" && !a.includes(":"))
    if (badAction) return `Statement[${i}].${ak} "${badAction}" must be <service>:<action>`
    if (s.Resource !== undefined && s.NotResource !== undefined) return `Statement[${i}] cannot have both Resource and NotResource`
    if (s.Resource === undefined && s.NotResource === undefined) return `Statement[${i}] needs a Resource or NotResource`
    const rk = s.Resource !== undefined ? "Resource" : "NotResource"
    if (!strList(s[rk])) return `Statement[${i}].${rk} must be a string or a non-empty array of strings`
    if (s.Principal !== undefined) return `Statement[${i}]: Principal is only allowed in role trust policies`
    if (s.Condition !== undefined && (!s.Condition || typeof s.Condition !== "object" || Array.isArray(s.Condition)))
      return `Statement[${i}].Condition must be an object of {operator: {key: value}}`
  }
  return null
}

export const asList = (v: string | string[] | undefined | null) => (v === undefined || v === null ? [] : Array.isArray(v) ? v : [v])

/** conditionLines flattens a Condition block into "operator key: values" rows. */
export function conditionLines(cond: PolicyStatement["Condition"] | null | undefined): { op: string; key: string; values: string[] }[] {
  const out: { op: string; key: string; values: string[] }[] = []
  if (!cond || typeof cond !== "object") return out
  for (const [op, kv] of Object.entries(cond)) {
    if (!kv || typeof kv !== "object") continue
    for (const [key, v] of Object.entries(kv)) out.push({ op, key, values: (Array.isArray(v) ? v : [v]).map((x) => String(x)) })
  }
  return out
}

/** PolicyStatementsTable is a readable summary of a policy's statements. */
export function PolicyStatementsTable({ doc }: { doc: PolicyDocument | undefined | null }) {
  const statements = statementsOf<PolicyStatement>(doc).filter((s) => s && typeof s === "object")
  if (!statements.length) return <p className="text-muted-foreground text-sm">This policy has no statements.</p>
  const hasCond = statements.some((s) => conditionLines(s.Condition).length > 0)
  return (
    <div className="overflow-x-auto rounded-lg border">
      <table className="w-full text-sm">
        <thead>
          <tr className="bg-muted/50 text-muted-foreground border-b text-left text-xs">
            <th className="px-3 py-2 font-semibold">Effect</th>
            <th className="px-3 py-2 font-semibold">Actions</th>
            <th className="px-3 py-2 font-semibold">Resources</th>
            {hasCond && <th className="px-3 py-2 font-semibold">Conditions</th>}
          </tr>
        </thead>
        <tbody>
          {statements.map((s, i) => {
            const notAction = s.Action === undefined && s.NotAction !== undefined
            const notResource = s.Resource === undefined && s.NotResource !== undefined
            const conds = conditionLines(s.Condition)
            return (
              <tr key={i} className="border-b align-top last:border-0">
                <td className="px-3 py-2">
                  <StatusBadge status={s.Effect === "Allow" ? "allowed" : "explicitDeny"} label={s.Effect} />
                  {s.Sid && <div className="text-muted-foreground mt-1 text-xs">{s.Sid}</div>}
                </td>
                <td className="px-3 py-2">
                  {notAction && <div className="mb-1 text-warning text-xs font-medium">All actions except (NotAction)</div>}
                  <div className="flex flex-wrap gap-1">
                    {asList(notAction ? s.NotAction : s.Action).map((a) => (
                      <Tag key={a} accent={a === "*" ? "warning" : "neutral"}>
                        {a === "*" ? "* (all actions)" : a}
                      </Tag>
                    ))}
                  </div>
                </td>
                <td className="px-3 py-2">
                  {notResource && <div className="mb-1 text-warning text-xs font-medium">All resources except (NotResource)</div>}
                  <div className="flex flex-col gap-0.5">
                    {asList(notResource ? s.NotResource : s.Resource).map((r) => (
                      <span key={r} className="font-mono text-xs break-all">
                        {r === "*" ? "* (all resources)" : r}
                      </span>
                    ))}
                  </div>
                </td>
                {hasCond && (
                  <td className="px-3 py-2">
                    {conds.length ? (
                      <div className="flex flex-col gap-1">
                        {conds.map((c) => (
                          <span key={`${c.op}:${c.key}`} className="font-mono text-xs break-all">
                            <span className="text-muted-foreground">{c.op}</span> {c.key}: {c.values.join(", ")}
                          </span>
                        ))}
                      </div>
                    ) : (
                      <span className="text-muted-foreground text-xs">None</span>
                    )}
                  </td>
                )}
              </tr>
            )
          })}
        </tbody>
      </table>
    </div>
  )
}

/** policyTypeLabel is the AWS console's name for a policy's type. */
export const policyTypeLabel = (managed: boolean | undefined) => (managed ? "AWS managed" : "Customer managed")

/** policyNameFromArn returns the policy name of a policy ARN (or the input when it is a name). */
export const policyNameFromArn = (arn: string) => arn.slice(arn.lastIndexOf("/") + 1)

export function PolicyTypeBadge({ managed }: { managed: boolean }) {
  return managed ? <StatusBadge status="managed" tone="info" label="AWS managed" /> : <StatusBadge status="customer" tone="neutral" label="Customer managed" />
}

/** Common actions, grouped by service, for pickers and the policy editor. */
export const COMMON_ACTIONS: { service: string; actions: string[] }[] = [
  { service: "s3", actions: ["s3:ListAllMyBuckets", "s3:ListBucket", "s3:GetObject", "s3:PutObject", "s3:DeleteObject", "s3:CreateBucket", "s3:DeleteBucket"] },
  { service: "ec2", actions: ["ec2:DescribeInstances", "ec2:RunInstances", "ec2:StartInstances", "ec2:StopInstances", "ec2:TerminateInstances", "ec2:DescribeVpcs"] },
  { service: "iam", actions: ["iam:ListUsers", "iam:GetUser", "iam:CreateUser", "iam:DeleteUser", "iam:AttachUserPolicy", "iam:CreateAccessKey", "iam:ListPolicies"] },
  { service: "secretsmanager", actions: ["secretsmanager:ListSecrets", "secretsmanager:GetSecretValue", "secretsmanager:CreateSecret", "secretsmanager:DeleteSecret"] },
  { service: "cloudwatch", actions: ["cloudwatch:ListMetrics", "cloudwatch:GetMetricStatistics", "cloudwatch:PutMetricAlarm", "logs:DescribeLogGroups", "logs:FilterLogEvents"] },
  { service: "other", actions: ["cloudtrail:LookupEvents", "lambda:InvokeFunction", "dynamodb:GetItem", "sqs:SendMessage", "sns:Publish", "rds:DescribeDBInstances"] },
]

// ---- pickers ----

/** PolicyPicker is a searchable checkbox list of policies. */
export function PolicyPicker({
  policies,
  loading,
  selected,
  onChange,
  exclude = [],
  maxHeight = "max-h-72",
}: {
  policies: PolicySummary[] | undefined
  loading?: boolean
  selected: string[]
  onChange: (names: string[]) => void
  exclude?: string[]
  maxHeight?: string
}) {
  const [q, setQ] = useState("")
  const [type, setType] = useState<"all" | "managed" | "local">("all")
  const rows = useMemo(() => {
    const s = q.trim().toLowerCase()
    return (policies ?? [])
      .filter((p) => !exclude.includes(p.name))
      .filter((p) => (type === "all" ? true : type === "managed" ? p.managed : !p.managed))
      .filter((p) => !s || p.name.toLowerCase().includes(s) || p.description.toLowerCase().includes(s))
      .sort((a, b) => a.name.localeCompare(b.name))
  }, [policies, q, type, exclude])
  const toggle = (n: string, on: boolean) => onChange(on ? [...selected, n] : selected.filter((x) => x !== n))

  return (
    <div className="flex flex-col gap-2">
      <div className="flex flex-wrap items-center gap-2">
        <div className="relative min-w-48 flex-1">
          <Search className="text-muted-foreground absolute top-1/2 left-2.5 size-4 -translate-y-1/2" />
          <Input value={q} onChange={(e) => setQ(e.target.value)} placeholder="Filter policies by name or description" className="h-8 pl-8" />
        </div>
        <div className="flex rounded-md border p-0.5 text-xs">
          {(
            [
              ["all", "All types"],
              ["managed", "AWS managed"],
              ["local", "Customer managed"],
            ] as const
          ).map(([k, l]) => (
            <button
              key={k}
              type="button"
              onClick={() => setType(k)}
              className={cn("rounded px-2 py-1", type === k ? "bg-primary text-primary-foreground" : "text-muted-foreground hover:bg-accent")}
            >
              {l}
            </button>
          ))}
        </div>
      </div>
      <div className={cn("overflow-y-auto rounded-md border", maxHeight)}>
        {loading ? (
          <div className="flex flex-col gap-2 p-3">
            {Array.from({ length: 4 }, (_, i) => (
              <Skeleton key={i} className="h-8 w-full" />
            ))}
          </div>
        ) : !rows.length ? (
          <p className="text-muted-foreground p-6 text-center text-sm">No policies match.</p>
        ) : (
          rows.map((p) => {
            const on = selected.includes(p.name)
            return (
              <label key={p.name} className={cn("hover:bg-accent/50 flex cursor-pointer items-start gap-3 border-b px-3 py-2 last:border-0", on && "bg-primary/5")}>
                <Checkbox checked={on} onCheckedChange={(v) => toggle(p.name, v === true)} className="mt-0.5" />
                <span className="flex min-w-0 flex-1 flex-col gap-0.5">
                  <span className="flex flex-wrap items-center gap-2 text-sm font-medium">
                    {p.name} <PolicyTypeBadge managed={p.managed} />
                  </span>
                  {p.description && <span className="text-muted-foreground text-xs">{p.description}</span>}
                </span>
              </label>
            )
          })
        )}
      </div>
      <p className="text-muted-foreground text-xs">{selected.length} selected</p>
    </div>
  )
}

/** CheckList is a simple checkbox list (groups, users). */
export function CheckList({
  items,
  selected,
  onChange,
  empty,
  loading,
}: {
  items: { id: string; label: React.ReactNode; sub?: React.ReactNode }[]
  selected: string[]
  onChange: (ids: string[]) => void
  empty: React.ReactNode
  loading?: boolean
}) {
  if (loading)
    return (
      <div className="flex flex-col gap-2">
        <Skeleton className="h-8 w-full" />
        <Skeleton className="h-8 w-full" />
      </div>
    )
  if (!items.length) return <p className="text-muted-foreground rounded-md border border-dashed p-4 text-center text-sm">{empty}</p>
  return (
    <div className="max-h-64 overflow-y-auto rounded-md border">
      {items.map((it) => {
        const on = selected.includes(it.id)
        return (
          <label key={it.id} className={cn("hover:bg-accent/50 flex cursor-pointer items-center gap-3 border-b px-3 py-2 last:border-0", on && "bg-primary/5")}>
            <Checkbox checked={on} onCheckedChange={(v) => onChange(v === true ? [...selected, it.id] : selected.filter((x) => x !== it.id))} />
            <span className="flex min-w-0 flex-1 items-center justify-between gap-2 text-sm">
              <span className="font-medium">{it.label}</span>
              {it.sub && <span className="text-muted-foreground text-xs">{it.sub}</span>}
            </span>
          </label>
        )
      })}
    </div>
  )
}

// ---- passwords ----

export interface PasswordChoice {
  mode: "auto" | "custom"
  custom: string
  generated: string
}

export const newPasswordChoice = (): PasswordChoice => ({ mode: "auto", custom: "", generated: generatePassword() })
export const choicePassword = (c: PasswordChoice) => (c.mode === "auto" ? c.generated : c.custom)
export const choiceError = (c: PasswordChoice) => (c.mode === "custom" && c.custom.length < 8 ? "The password must be at least 8 characters." : null)

/** PasswordChooser: autogenerated vs custom console password. */
export function PasswordChooser({ value, onChange, showErrors }: { value: PasswordChoice; onChange: (c: PasswordChoice) => void; showErrors?: boolean }) {
  const [show, setShow] = useState(false)
  const err = choiceError(value)
  return (
    <RadioGroup value={value.mode} onValueChange={(m) => onChange({ ...value, mode: m as PasswordChoice["mode"] })} className="gap-3">
      <div className="flex items-start gap-2">
        <RadioGroupItem value="auto" id="pw-auto" className="mt-0.5" />
        <div className="flex min-w-0 flex-1 flex-col gap-1">
          <Label htmlFor="pw-auto">Autogenerated password</Label>
          <span className="text-muted-foreground text-xs">You can view the password after you create the user.</span>
          {value.mode === "auto" && (
            <div className="flex items-center gap-1">
              <code className="bg-muted rounded px-2 py-1 text-xs">{show ? value.generated : "•".repeat(value.generated.length)}</code>
              <Button type="button" size="icon" variant="ghost" className="size-7" onClick={() => setShow(!show)} aria-label={show ? "Hide password" : "Show password"}>
                {show ? <EyeOff /> : <Eye />}
              </Button>
              <Button type="button" size="icon" variant="ghost" className="size-7" onClick={() => onChange({ ...value, generated: generatePassword() })} aria-label="Regenerate">
                <RefreshCw />
              </Button>
            </div>
          )}
        </div>
      </div>
      <div className="flex items-start gap-2">
        <RadioGroupItem value="custom" id="pw-custom" className="mt-0.5" />
        <div className="flex min-w-0 flex-1 flex-col gap-1.5">
          <Label htmlFor="pw-custom">Custom password</Label>
          {value.mode === "custom" && (
            <>
              <div className="relative max-w-sm">
                <Input
                  type={show ? "text" : "password"}
                  autoComplete="new-password"
                  value={value.custom}
                  onChange={(e) => onChange({ ...value, custom: e.target.value })}
                  placeholder="At least 8 characters"
                  aria-invalid={showErrors && !!err}
                  className="pr-9"
                />
                <Button type="button" size="icon" variant="ghost" className="absolute top-0.5 right-0.5 size-8" onClick={() => setShow(!show)} aria-label={show ? "Hide password" : "Show password"}>
                  {show ? <EyeOff /> : <Eye />}
                </Button>
              </div>
              {showErrors && err ? <span className="text-destructive text-xs">{err}</span> : <span className="text-muted-foreground text-xs">Must be at least 8 characters.</span>}
            </>
          )}
        </div>
      </div>
    </RadioGroup>
  )
}

/** SecretValue shows a masked secret with show and copy buttons. */
export function SecretValue({ value, label }: { value: string; label: string }) {
  const [show, setShow] = useState(false)
  return (
    <div className="flex min-w-0 items-center gap-1">
      <code className="bg-muted min-w-0 flex-1 rounded px-2 py-1 font-mono text-[13px] break-all">{show ? value : "•".repeat(Math.min(value.length, 24))}</code>
      <Button type="button" size="icon" variant="ghost" className="size-8 shrink-0" onClick={() => setShow(!show)} aria-label={show ? `Hide ${label}` : `Show ${label}`}>
        {show ? <EyeOff /> : <Eye />}
      </Button>
      <CopyButton value={value} toastMessage={`${label} copied`} />
    </div>
  )
}

// ---- data ----

/**
 * useAccessKeysByUser fetches the access keys of every listed user in one
 * cached request (the user list endpoint does not include keys).
 */
export function useAccessKeysByUser(names: string[] | undefined) {
  const key = names ? `${IAM}/users?access-keys=${[...names].sort().join(",")}` : null
  return useSWR<Record<string, AccessKey[]>, ApiError>(
    key,
    async () => {
      const entries = await Promise.all(
        (names ?? []).map(async (n) => {
          try {
            return [n, await api.get<AccessKey[]>(`${IAM}/users/${seg(n)}/access-keys`)] as const
          } catch {
            return [n, [] as AccessKey[]] as const
          }
        }),
      )
      return Object.fromEntries(entries)
    },
    { keepPreviousData: true },
  )
}

export const DAY = 86_400_000

/** keyAgeDays is the number of days since a key was last used (or created, if never used). */
export function keyIdleDays(k: AccessKey, now = Date.now()) {
  return Math.floor((now - new Date(k.last_used ?? k.created_at).getTime()) / DAY)
}
