"use client"

import { useEffect, useMemo, useState } from "react"
import Link from "next/link"
import { AlertTriangle, Info, Terminal } from "lucide-react"

import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert"
import { Button } from "@/components/ui/button"
import { Checkbox } from "@/components/ui/checkbox"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select"
import { Field } from "@/components/console/form-field"
import { KeyValueGrid } from "@/components/console/key-value"
import { Section } from "@/components/console/section"
import { StatusBadge } from "@/components/console/status-badge"
import { TimeAgo } from "@/components/console/time-ago"
import { apiOrigin } from "@/components/kms/shared"
import { FUNCTIONS_PATH, functionHref } from "@/components/lambda/common"
import { CodeBlock } from "@/components/s3/common"
import { formatDate } from "@/lib/format"
import { useApi } from "@/lib/hooks"
import type { LambdaFunction, Secret, SecretRotationRules } from "@/lib/types"

/** functionName extracts the function name from a Lambda ARN (dropping a qualifier). */
export function lambdaName(arn: string): string {
  const i = arn.indexOf(":function:")
  if (i < 0) return arn
  return arn.slice(i + 10).split(":")[0]
}

/** scheduleLabel describes rotation rules in words. */
export function scheduleLabel(r?: SecretRotationRules | null): string {
  if (!r) return ""
  let s = ""
  if (r.AutomaticallyAfterDays) s = `Every ${r.AutomaticallyAfterDays} day${r.AutomaticallyAfterDays === 1 ? "" : "s"}`
  else if (r.ScheduleExpression) {
    const m = /^rate\((\d+) (hour|hours|day|days)\)$/.exec(r.ScheduleExpression.trim())
    s = m ? `Every ${m[1]} ${m[2].replace(/s$/, "")}${m[1] === "1" ? "" : "s"}` : r.ScheduleExpression
  }
  if (r.Duration) s += `${s ? ", " : ""}window ${r.Duration}`
  return s
}

/** rotationPending: a version carries AWSPENDING without AWSCURRENT (a rotation that hasn't finished). */
export const rotationPending = (s: Secret) => s.versions.some((v) => v.stages.includes("AWSPENDING") && !v.stages.includes("AWSCURRENT"))

const q = (s: string) => `'${s.replace(/'/g, `'\\''`)}'`

/**
 * RotationSection shows the secret's rotation configuration and status.
 * The native API has no rotation routes, so configuring and starting a
 * rotation is done with the AWS API; the builder below writes that command.
 */
export function RotationSection({ secret }: { secret: Secret }) {
  const pending = rotationPending(secret)
  const fnName = secret.rotation_lambda_arn ? lambdaName(secret.rotation_lambda_arn) : ""
  const status = secret.rotation_error ? (
    <StatusBadge status="failed" label="Last rotation failed" tone="danger" />
  ) : pending ? (
    <StatusBadge status="rotating" label="Rotation in progress" tone="info" />
  ) : secret.rotation_enabled ? (
    <StatusBadge status="enabled" label="Enabled" tone="success" />
  ) : (
    <StatusBadge status="disabled" label="Disabled" tone="neutral" />
  )

  return (
    <Section
      title="Rotation"
      description="A Lambda rotation function creates a new AWSPENDING version, tests it and promotes it to AWSCURRENT (createSecret, setSecret, testSecret, finishSecret)."
    >
      <div className="flex flex-col gap-4">
        {secret.rotation_error && (
          <Alert variant="destructive" className="border-destructive/40 bg-destructive/5">
            <AlertTriangle />
            <AlertTitle>The last rotation failed</AlertTitle>
            <AlertDescription className="break-words">{secret.rotation_error}</AlertDescription>
          </Alert>
        )}
        <KeyValueGrid
          columns={3}
          items={[
            { label: "Rotation status", value: status },
            {
              label: "Rotation function",
              value: fnName ? (
                <Link href={functionHref(fnName)} className="text-primary font-mono text-[13px] break-all hover:underline" title={secret.rotation_lambda_arn}>
                  {fnName}
                </Link>
              ) : (
                ""
              ),
            },
            { label: "Schedule", value: scheduleLabel(secret.rotation_rules) || (secret.rotation_enabled ? "On demand only" : "") },
            { label: "Last rotated", value: secret.last_rotated ? <span>{formatDate(secret.last_rotated)} (<TimeAgo value={secret.last_rotated} />)</span> : "Never" },
            { label: "Next rotation", value: secret.rotation_enabled && secret.next_rotation ? formatDate(secret.next_rotation) : "" },
          ]}
        />
        <RotationCommandBuilder secret={secret} />
      </div>
    </Section>
  )
}

type Action = "configure" | "rotate" | "cancel"

function RotationCommandBuilder({ secret }: { secret: Secret }) {
  const fns = useApi<LambdaFunction[]>(FUNCTIONS_PATH)
  const [action, setAction] = useState<Action>(secret.rotation_enabled ? "rotate" : "configure")
  const [fn, setFn] = useState(secret.rotation_lambda_arn ? lambdaName(secret.rotation_lambda_arn) : "")
  const [days, setDays] = useState(String(secret.rotation_rules?.AutomaticallyAfterDays || /rate\((\d+) days?\)/.exec(secret.rotation_rules?.ScheduleExpression ?? "")?.[1] || 30))
  const [now, setNow] = useState(true)
  const [origin, setOrigin] = useState("")
  useEffect(() => setOrigin(apiOrigin()), [])

  const fnList = fns.data ?? []
  const fnArn = fnList.find((f) => f.name === fn)?.arn ?? fn
  const n = Number(days)
  const daysErr = Number.isInteger(n) && n >= 1 && n <= 1000 ? null : "Enter a whole number of days between 1 and 1000."

  const cmd = useMemo(() => {
    const base = `aws --endpoint-url ${origin} secretsmanager`
    const id = `--secret-id ${q(secret.name)}`
    if (action === "cancel") return `${base} cancel-rotate-secret ${id}`
    if (action === "rotate") return `${base} rotate-secret ${id}`
    const lines = [`${base} rotate-secret ${id}`]
    if (fnArn) lines.push(`  --rotation-lambda-arn ${fnArn}`)
    lines.push(`  --rotation-rules ${q(JSON.stringify({ ScheduleExpression: `rate(${daysErr ? 30 : n} days)` }))}`)
    lines.push(now ? "  --rotate-immediately" : "  --no-rotate-immediately")
    return lines.join(" \\\n")
  }, [action, origin, secret.name, fnArn, n, daysErr, now])

  return (
    <div className="flex flex-col gap-3 rounded-md border p-3">
      <div className="flex items-start gap-2 text-sm">
        <Terminal className="text-muted-foreground mt-0.5 size-4 shrink-0" />
        <div>
          <p className="font-medium">Configure rotation</p>
          <p className="text-muted-foreground text-xs">
            Rotation is configured through the Secrets Manager API. Choose what to do and run the generated AWS CLI command with credentials that allow
            secretsmanager:RotateSecret and lambda:InvokeFunction.
          </p>
        </div>
      </div>
      <div className="flex flex-wrap gap-1.5" role="radiogroup" aria-label="Rotation action">
        {(
          [
            ["configure", secret.rotation_enabled ? "Change schedule or function" : "Turn on rotation"],
            ["rotate", "Rotate now"],
            ["cancel", "Turn off rotation"],
          ] as [Action, string][]
        ).map(([a, label]) => (
          <Button key={a} type="button" size="sm" variant={action === a ? "secondary" : "ghost"} role="radio" aria-checked={action === a} onClick={() => setAction(a)} className="h-7">
            {label}
          </Button>
        ))}
      </div>
      {action === "configure" && (
        <div className="grid grid-cols-1 gap-3 sm:grid-cols-[minmax(0,1fr)_10rem]">
          <Field
            label="Rotation function"
            htmlFor="secret-rot-fn"
            help={
              fns.error
                ? "Lambda functions couldn't be listed."
                : fnList.length === 0 && fns.data
                  ? "No Lambda functions yet. Create one that implements the four rotation steps."
                  : "The function receives Step, SecretId and ClientRequestToken."
            }
          >
            <Select value={fn} onValueChange={setFn} disabled={!fns.data}>
              <SelectTrigger id="secret-rot-fn" className="w-full">
                <SelectValue placeholder={fns.data ? "Choose a function" : "Loading functions..."} />
              </SelectTrigger>
              <SelectContent>
                {fnList.map((f) => (
                  <SelectItem key={f.name} value={f.name}>
                    <span className="font-mono text-[13px]">{f.name}</span>
                    <span className="text-muted-foreground text-xs">{f.runtime || f.package_type}</span>
                  </SelectItem>
                ))}
                {fn && !fnList.some((f) => f.name === fn) && (
                  <SelectItem value={fn}>
                    <span className="font-mono text-[13px]">{fn}</span>
                    <span className="text-muted-foreground text-xs">not found</span>
                  </SelectItem>
                )}
              </SelectContent>
            </Select>
          </Field>
          <Field label="Rotate every (days)" htmlFor="secret-rot-days" error={daysErr}>
            <Input id="secret-rot-days" type="number" min={1} max={1000} value={days} onChange={(e) => setDays(e.target.value)} aria-invalid={!!daysErr} />
          </Field>
          <div className="flex items-center gap-2 sm:col-span-2">
            <Checkbox id="secret-rot-now" checked={now} onCheckedChange={(v) => setNow(v === true)} />
            <Label htmlFor="secret-rot-now" className="text-sm font-normal">
              Rotate immediately after saving
            </Label>
          </div>
        </div>
      )}
      {action === "rotate" && !secret.rotation_lambda_arn && (
        <p className="text-destructive text-xs">This secret has no rotation function yet; turn on rotation first.</p>
      )}
      {action === "rotate" && pendingNote(secret)}
      <CodeBlock code={cmd} />
    </div>
  )
}

function pendingNote(secret: Secret) {
  if (!rotationPending(secret)) return null
  return (
    <p className="text-muted-foreground flex items-center gap-1.5 text-xs">
      <Info className="size-3.5 shrink-0" /> A rotation is still in progress; a new one is rejected until it finishes.
    </p>
  )
}

/** ResourcePolicySection shows the secret's resource-based policy. */
export function ResourcePolicySection({ secret }: { secret: Secret }) {
  const [origin, setOrigin] = useState("")
  useEffect(() => setOrigin(apiOrigin()), [])
  let doc = secret.resource_policy ?? ""
  try {
    if (doc) doc = JSON.stringify(JSON.parse(doc), null, 2)
  } catch {
    // show as stored
  }
  const put = `aws --endpoint-url ${origin} secretsmanager put-resource-policy \\\n  --secret-id ${q(secret.name)} \\\n  --block-public-policy --resource-policy file://policy.json`
  return (
    <Section
      title="Resource permissions"
      description="A resource-based policy grants other principals access to this secret, in addition to their IAM policies."
    >
      <div className="flex flex-col gap-3">
        {doc ? (
          <CodeBlock code={doc} className="max-h-96 overflow-y-auto" />
        ) : (
          <p className="text-muted-foreground text-sm">No resource policy is attached. Access is controlled by IAM policies only.</p>
        )}
        <details className="group text-sm">
          <summary className="text-primary cursor-pointer text-xs hover:underline">{doc ? "Replace or delete the policy" : "Attach a policy"}</summary>
          <div className="mt-2 flex flex-col gap-2">
            <p className="text-muted-foreground text-xs">
              Resource policies are managed through the Secrets Manager API (PutResourcePolicy, DeleteResourcePolicy). --block-public-policy rejects policies that
              grant access to everyone.
            </p>
            <CodeBlock code={doc ? `${put}\n\naws --endpoint-url ${origin} secretsmanager delete-resource-policy --secret-id ${q(secret.name)}` : put} />
          </div>
        </details>
      </div>
    </Section>
  )
}
