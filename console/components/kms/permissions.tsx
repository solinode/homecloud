"use client"

import { useState } from "react"
import { Loader2, Plus, Trash2 } from "lucide-react"
import { toast } from "sonner"

import { Button } from "@/components/ui/button"
import { Checkbox } from "@/components/ui/checkbox"
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { ConfirmDialog } from "@/components/console/confirm-dialog"
import { CopyableText } from "@/components/console/copy-button"
import { ErrorState } from "@/components/console/error-state"
import { Field } from "@/components/console/form-field"
import { JsonEditor, jsonError } from "@/components/console/json-editor"
import { Section } from "@/components/console/section"
import { TimeAgo } from "@/components/console/time-ago"
import { CodeBlock } from "@/components/s3/common"
import { api, seg } from "@/lib/api"
import { useAction, useApi } from "@/lib/hooks"
import type { KmsGrant, KmsKey } from "@/lib/types"

import { KEYS_PATH, keyKind } from "./shared"

const pretty = (doc: string) => {
  try {
    return JSON.stringify(JSON.parse(doc), null, 2)
  } catch {
    return doc
  }
}

/** KeyPolicySection shows and edits the key policy (the "default" policy). */
export function KeyPolicySection({ k, readOnly }: { k: KmsKey; readOnly: boolean }) {
  const path = `${KEYS_PATH}/${seg(k.id)}/policy`
  const { data, error, mutate } = useApi<{ policy: string }>(path)
  const [editing, setEditing] = useState(false)
  const [text, setText] = useState("")
  const { pending, run } = useAction()
  const doc = data ? pretty(data.policy) : ""

  const save = async () => {
    const r = await run(() => api.put(path, { policy: JSON.stringify(JSON.parse(text)) }), "Key policy saved")
    if (r !== undefined) {
      setEditing(false)
      mutate()
    }
  }

  return (
    <Section
      title="Key policy"
      description="The key policy controls who can use and manage the key. IAM policies grant access to the key only where the key policy allows it."
      actions={
        !editing && !readOnly && data ? (
          <Button
            size="sm"
            variant="outline"
            onClick={() => {
              setText(doc)
              setEditing(true)
            }}
          >
            Edit policy
          </Button>
        ) : undefined
      }
    >
      {error ? (
        <ErrorState error={error} onRetry={() => mutate()} />
      ) : editing ? (
        <div className="flex flex-col gap-3">
          <JsonEditor value={text} onChange={setText} rows={16} />
          <div className="flex gap-2">
            <Button size="sm" onClick={save} disabled={pending || !!jsonError(text)}>
              {pending && <Loader2 className="animate-spin" />} Save policy
            </Button>
            <Button size="sm" variant="outline" onClick={() => setEditing(false)} disabled={pending}>
              Cancel
            </Button>
          </div>
        </div>
      ) : data ? (
        <CodeBlock code={doc} className="max-h-96 overflow-y-auto" />
      ) : (
        <p className="text-muted-foreground text-sm">Loading policy...</p>
      )}
    </Section>
  )
}

const ENCRYPT_OPS = ["Encrypt", "Decrypt", "GenerateDataKey", "GenerateDataKeyWithoutPlaintext", "ReEncryptFrom", "ReEncryptTo", "GenerateDataKeyPair", "GenerateDataKeyPairWithoutPlaintext"]
const COMMON_OPS = ["DescribeKey", "CreateGrant", "RetireGrant"]

function grantOps(k: KmsKey): string[] {
  const kind = keyKind(k.key_spec)
  if (kind === "hmac") return ["GenerateMac", "VerifyMac", ...COMMON_OPS]
  if (k.key_usage === "SIGN_VERIFY") return ["Sign", "Verify", "GetPublicKey", ...COMMON_OPS]
  if (kind === "asymmetric") return ["Encrypt", "Decrypt", "GetPublicKey", ...COMMON_OPS]
  return [...ENCRYPT_OPS, ...COMMON_OPS]
}

/** GrantsSection lists, creates and revokes the key's grants. */
export function GrantsSection({ k, readOnly }: { k: KmsKey; readOnly: boolean }) {
  const path = `${KEYS_PATH}/${seg(k.id)}/grants`
  const { data, error, mutate } = useApi<KmsGrant[]>(path)
  const [creating, setCreating] = useState(false)
  const [revoking, setRevoking] = useState<KmsGrant | null>(null)
  const grants = data ?? []

  return (
    <Section
      title={`Grants (${grants.length})`}
      description="A grant lets a principal use the key for the listed operations without changing the key policy."
      actions={
        !readOnly && k.state !== "PendingDeletion" ? (
          <Button size="sm" variant="outline" onClick={() => setCreating(true)}>
            <Plus /> Create grant
          </Button>
        ) : undefined
      }
      flush
    >
      {error ? (
        <div className="p-4">
          <ErrorState error={error} onRetry={() => mutate()} />
        </div>
      ) : (
        <div className="overflow-x-auto">
          <table className="w-full text-sm">
            <thead>
              <tr className="bg-muted/40 text-muted-foreground border-b text-left text-xs">
                <th className="px-4 py-2 font-semibold">Grantee principal</th>
                <th className="px-3 py-2 font-semibold">Operations</th>
                <th className="hidden px-3 py-2 font-semibold md:table-cell">Grant ID</th>
                <th className="hidden px-3 py-2 font-semibold sm:table-cell">Created</th>
                <th className="px-4 py-2" />
              </tr>
            </thead>
            <tbody>
              {grants.map((g) => (
                <tr key={g.grant_id} className="border-b last:border-0">
                  <td className="px-4 py-2 font-mono text-[13px] break-all">
                    {g.grantee_principal}
                    {g.name && <div className="text-muted-foreground font-sans text-xs">{g.name}</div>}
                  </td>
                  <td className="px-3 py-2 text-xs">{g.operations.join(", ")}</td>
                  <td className="hidden max-w-[12rem] px-3 py-2 md:table-cell">
                    <CopyableText value={g.grant_id} className="truncate text-[12px]" />
                  </td>
                  <td className="hidden px-3 py-2 whitespace-nowrap sm:table-cell">
                    <TimeAgo value={g.created_at} />
                  </td>
                  <td className="px-4 py-2 text-right">
                    {!readOnly && (
                      <Button variant="ghost" size="sm" onClick={() => setRevoking(g)} aria-label="Revoke grant">
                        <Trash2 /> Revoke
                      </Button>
                    )}
                  </td>
                </tr>
              ))}
              {data && grants.length === 0 && (
                <tr>
                  <td colSpan={5} className="text-muted-foreground px-4 py-3">
                    No grants.
                  </td>
                </tr>
              )}
            </tbody>
          </table>
        </div>
      )}
      <CreateGrantDialog open={creating} onOpenChange={setCreating} k={k} path={path} onCreated={() => mutate()} />
      <ConfirmDialog
        open={!!revoking}
        onOpenChange={(o) => !o && setRevoking(null)}
        title="Revoke grant?"
        description={revoking ? `${revoking.grantee_principal} loses the permissions this grant gives it (${revoking.operations.join(", ")}).` : undefined}
        actionLabel="Revoke"
        onConfirm={async () => {
          if (!revoking) return
          await api.del(`${path}/${seg(revoking.grant_id)}`)
          toast.success("Grant revoked")
          setRevoking(null)
          mutate()
        }}
      />
    </Section>
  )
}

function CreateGrantDialog({ open, onOpenChange, k, path, onCreated }: { open: boolean; onOpenChange: (o: boolean) => void; k: KmsKey; path: string; onCreated: () => void }) {
  const all = grantOps(k)
  const [grantee, setGrantee] = useState("")
  const [retiring, setRetiring] = useState("")
  const [name, setName] = useState("")
  const [ops, setOps] = useState<string[]>([])
  const [token, setToken] = useState<string | null>(null)
  const { pending, run } = useAction()

  const reset = () => {
    setGrantee("")
    setRetiring("")
    setName("")
    setOps([])
    setToken(null)
  }
  const create = async () => {
    const g = await run(
      () => api.post<KmsGrant>(path, { grantee_principal: grantee.trim(), retiring_principal: retiring.trim() || undefined, name: name.trim() || undefined, operations: ops }),
      "Grant created",
    )
    if (g) {
      setToken(g.grant_token ?? "")
      onCreated()
    }
  }

  return (
    <Dialog
      open={open}
      onOpenChange={(o) => {
        if (pending) return
        if (!o) reset()
        onOpenChange(o)
      }}
    >
      <DialogContent className="sm:max-w-lg">
        <DialogHeader>
          <DialogTitle>{token !== null ? "Grant created" : "Create grant"}</DialogTitle>
          <DialogDescription>
            {token !== null ? "Use the grant token to use the grant before it is fully propagated, or to retire it." : "Allow a principal to use this key for selected operations."}
          </DialogDescription>
        </DialogHeader>
        {token !== null ? (
          <CopyableText value={token} className="text-[12px] break-all" />
        ) : (
          <div className="flex flex-col gap-3">
            <Field label="Grantee principal" htmlFor="grant-grantee" help="An IAM user or role ARN, an account root ARN or a service principal.">
              <Input id="grant-grantee" value={grantee} onChange={(e) => setGrantee(e.target.value)} placeholder="arn:aws:iam::123456789012:role/app" autoFocus />
            </Field>
            <Field label="Operations">
              <div className="grid grid-cols-1 gap-1.5 sm:grid-cols-2">
                {all.map((op) => (
                  <div key={op} className="flex items-center gap-2">
                    <Checkbox
                      id={`grant-op-${op}`}
                      checked={ops.includes(op)}
                      onCheckedChange={(v) => setOps((cur) => (v === true ? [...cur, op] : cur.filter((x) => x !== op)))}
                    />
                    <Label htmlFor={`grant-op-${op}`} className="font-mono text-[12.5px] font-normal">
                      {op}
                    </Label>
                  </div>
                ))}
              </div>
            </Field>
            <Field label="Retiring principal" htmlFor="grant-retiring" optional>
              <Input id="grant-retiring" value={retiring} onChange={(e) => setRetiring(e.target.value)} />
            </Field>
            <Field label="Name" htmlFor="grant-name" optional help="Retrying with the same name and parameters returns the existing grant.">
              <Input id="grant-name" value={name} onChange={(e) => setName(e.target.value)} />
            </Field>
          </div>
        )}
        <DialogFooter>
          {token !== null ? (
            <Button
              onClick={() => {
                reset()
                onOpenChange(false)
              }}
            >
              Done
            </Button>
          ) : (
            <>
              <Button variant="outline" onClick={() => onOpenChange(false)} disabled={pending}>
                Cancel
              </Button>
              <Button onClick={create} disabled={pending || !grantee.trim() || ops.length === 0}>
                {pending && <Loader2 className="animate-spin" />} Create grant
              </Button>
            </>
          )}
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
