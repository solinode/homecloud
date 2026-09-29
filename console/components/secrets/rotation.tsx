"use client"

import { useState } from "react"
import Link from "next/link"
import { AlertTriangle, Info, Loader2 } from "lucide-react"
import { toast } from "sonner"

import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert"
import { Button } from "@/components/ui/button"
import { Checkbox } from "@/components/ui/checkbox"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select"
import { ConfirmDialog } from "@/components/console/confirm-dialog"
import { Field } from "@/components/console/form-field"
import { JsonEditor, jsonError } from "@/components/console/json-editor"
import { KeyValueGrid } from "@/components/console/key-value"
import { Section } from "@/components/console/section"
import { StatusBadge } from "@/components/console/status-badge"
import { TimeAgo } from "@/components/console/time-ago"
import { FUNCTIONS_PATH, functionHref } from "@/components/lambda/common"
import { CodeBlock } from "@/components/s3/common"
import { formatDate } from "@/lib/format"
import { api, seg } from "@/lib/api"
import { useAction, useApi } from "@/lib/hooks"
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

/** RotationSection shows the secret's rotation configuration and status, and changes it. */
export function RotationSection({ secret, onChanged }: { secret: Secret; onChanged: () => void }) {
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
        <RotationControls secret={secret} onChanged={onChanged} />
      </div>
    </Section>
  )
}

type Action = "configure" | "rotate" | "cancel"

function RotationControls({ secret, onChanged }: { secret: Secret; onChanged: () => void }) {
  const fns = useApi<LambdaFunction[]>(FUNCTIONS_PATH)
  const [action, setAction] = useState<Action>(secret.rotation_enabled ? "rotate" : "configure")
  const [fn, setFn] = useState(secret.rotation_lambda_arn ? lambdaName(secret.rotation_lambda_arn) : "")
  const [days, setDays] = useState(String(secret.rotation_rules?.AutomaticallyAfterDays || /rate\((\d+) days?\)/.exec(secret.rotation_rules?.ScheduleExpression ?? "")?.[1] || 30))
  const [now, setNow] = useState(true)
  const { pending, run } = useAction()

  const fnList = fns.data ?? []
  const fnArn = fnList.find((f) => f.name === fn)?.arn ?? fn
  const n = Number(days)
  const daysErr = Number.isInteger(n) && n >= 1 && n <= 1000 ? null : "Enter a whole number of days between 1 and 1000."
  const base = `/api/v1/secrets/${seg(secret.name)}`
  const disabled = pending || !!secret.deletion_date

  const submit = async () => {
    let r: unknown
    if (action === "cancel") r = await run(() => api.post(`${base}/cancel-rotation`), "Rotation turned off")
    else if (action === "rotate") r = await run(() => api.post(`${base}/rotate`, { rotate_immediately: true }), "Rotation started")
    else
      r = await run(
        () => api.post(`${base}/rotate`, { rotation_lambda_arn: fnArn, schedule_expression: `rate(${n} days)`, rotate_immediately: now }),
        now ? "Rotation configured and started" : "Rotation configured",
      )
    if (r !== undefined) onChanged()
  }

  return (
    <div className="flex flex-col gap-3 rounded-md border p-3">
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
                  : "The function receives Step, SecretId and ClientRequestToken. You also need lambda:InvokeFunction on it."
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
      {action === "cancel" && !secret.rotation_enabled && <p className="text-muted-foreground text-xs">Rotation is already off.</p>}
      <div>
        <Button
          size="sm"
          variant={action === "cancel" ? "outline" : "default"}
          onClick={submit}
          disabled={
            disabled ||
            (action === "configure" && (!fn || !!daysErr)) ||
            (action === "rotate" && (!secret.rotation_lambda_arn || rotationPending(secret))) ||
            (action === "cancel" && !secret.rotation_enabled)
          }
        >
          {pending && <Loader2 className="animate-spin" />}
          {action === "configure" ? "Save rotation" : action === "rotate" ? "Rotate now" : "Turn off rotation"}
        </Button>
      </div>
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

const pretty = (doc: string) => {
  try {
    return doc ? JSON.stringify(JSON.parse(doc), null, 2) : ""
  } catch {
    return doc
  }
}

function templatePolicy(secret: Secret): string {
  const account = secret.arn.split(":")[4] ?? ""
  return JSON.stringify(
    {
      Version: "2012-10-17",
      Statement: [{ Effect: "Allow", Principal: { AWS: `arn:aws:iam::${account}:root` }, Action: "secretsmanager:GetSecretValue", Resource: "*" }],
    },
    null,
    2,
  )
}

/** ResourcePolicySection shows, edits and deletes the secret's resource-based policy. */
export function ResourcePolicySection({ secret, onChanged }: { secret: Secret; onChanged: () => void }) {
  const doc = pretty(secret.resource_policy ?? "")
  const [editing, setEditing] = useState(false)
  const [text, setText] = useState("")
  const [blockPublic, setBlockPublic] = useState(true)
  const [confirmDelete, setConfirmDelete] = useState(false)
  const { pending, run } = useAction()
  const path = `/api/v1/secrets/${seg(secret.name)}/policy`
  const err = editing ? jsonError(text) : null

  const start = () => {
    setText(doc || templatePolicy(secret))
    setBlockPublic(true)
    setEditing(true)
  }
  const save = async () => {
    const r = await run(() => api.put(path, { policy: JSON.stringify(JSON.parse(text)), block_public_policy: blockPublic }), "Resource policy saved")
    if (r !== undefined) {
      setEditing(false)
      onChanged()
    }
  }

  return (
    <Section
      title="Resource permissions"
      description="A resource-based policy grants other principals access to this secret, in addition to their IAM policies."
      actions={
        !editing && !secret.deletion_date ? (
          <>
            {doc && (
              <Button size="sm" variant="outline" onClick={() => setConfirmDelete(true)}>
                Delete policy
              </Button>
            )}
            <Button size="sm" variant="outline" onClick={start}>
              {doc ? "Edit policy" : "Attach policy"}
            </Button>
          </>
        ) : undefined
      }
    >
      {editing ? (
        <div className="flex flex-col gap-3">
          <JsonEditor value={text} onChange={setText} rows={14} />
          <div className="flex items-center gap-2">
            <Checkbox id="secret-block-public" checked={blockPublic} onCheckedChange={(v) => setBlockPublic(v === true)} />
            <Label htmlFor="secret-block-public" className="text-sm font-normal">
              Block public access (reject policies that grant access to everyone)
            </Label>
          </div>
          <div className="flex gap-2">
            <Button size="sm" onClick={save} disabled={pending || !!err}>
              {pending && <Loader2 className="animate-spin" />} Save policy
            </Button>
            <Button size="sm" variant="outline" onClick={() => setEditing(false)} disabled={pending}>
              Cancel
            </Button>
          </div>
        </div>
      ) : doc ? (
        <CodeBlock code={doc} className="max-h-96 overflow-y-auto" />
      ) : (
        <p className="text-muted-foreground text-sm">No resource policy is attached. Access is controlled by IAM policies only.</p>
      )}
      <ConfirmDialog
        open={confirmDelete}
        onOpenChange={setConfirmDelete}
        title="Delete resource policy?"
        description="Principals that were granted access only by this policy lose access to the secret."
        onConfirm={async () => {
          await api.del(path)
          toast.success("Resource policy deleted")
          onChanged()
        }}
      />
    </Section>
  )
}
