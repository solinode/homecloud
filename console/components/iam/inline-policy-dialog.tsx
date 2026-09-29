"use client"

import { useEffect, useState } from "react"
import { Loader2 } from "lucide-react"
import { toast } from "sonner"

import { Button } from "@/components/ui/button"
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog"
import { Input } from "@/components/ui/input"
import { Field } from "@/components/console/form-field"
import { JsonEditor, jsonError } from "@/components/console/json-editor"
import { api, errorMessage, seg } from "@/lib/api"
import type { PolicyDocument } from "@/lib/types"

import { IAM, POLICY_TEMPLATE, PolicyStatementsTable, nameError, policyJson, validatePolicy } from "./common"

/**
 * InlinePolicyDialog creates, views or edits an inline policy of a user or
 * role or group (`kind`). `initial` null = create.
 */
export function InlinePolicyDialog({
  open,
  onOpenChange,
  owner,
  kind = "user",
  initial,
  existing,
  readOnly,
  onSaved,
}: {
  open: boolean
  onOpenChange: (o: boolean) => void
  /** Name of the user, role or group the policy is embedded in. */
  owner: string
  kind?: "user" | "role" | "group"
  initial: { name: string; doc: PolicyDocument } | null
  existing: string[]
  readOnly?: boolean
  onSaved: () => void
}) {
  const [name, setName] = useState("")
  const [text, setText] = useState("")
  const [touched, setTouched] = useState(false)
  const [pending, setPending] = useState(false)

  useEffect(() => {
    if (open) {
      setName(initial?.name ?? "")
      setText(policyJson(initial?.doc ?? POLICY_TEMPLATE))
      setTouched(false)
    }
  }, [open, initial])

  const creating = !initial
  const nErr = creating ? (nameError("policy", name) ?? (existing.includes(name) ? `An inline policy named ${name} already exists.` : null)) : null
  const docErr = jsonError(text) ?? validatePolicy(JSON.parse(jsonError(text) ? "null" : text))

  const save = async () => {
    setTouched(true)
    if (nErr || docErr) return
    setPending(true)
    try {
      await api.put(`${IAM}/${kind}s/${seg(owner)}/inline-policies/${seg(name)}`, JSON.parse(text))
      toast.success(creating ? `Inline policy ${name} created` : `Inline policy ${name} updated`)
      onSaved()
      onOpenChange(false)
    } catch (e) {
      toast.error(errorMessage(e))
    } finally {
      setPending(false)
    }
  }

  let parsed: PolicyDocument | null = null
  if (readOnly) {
    try {
      parsed = JSON.parse(text) as PolicyDocument
    } catch {
      parsed = null
    }
  }

  return (
    <Dialog open={open} onOpenChange={(o) => !pending && onOpenChange(o)}>
      <DialogContent className="max-h-[92vh] overflow-y-auto sm:max-w-3xl">
        <DialogHeader>
          <DialogTitle>{creating ? "Create inline policy" : readOnly ? `Inline policy ${initial?.name}` : `Edit inline policy ${initial?.name}`}</DialogTitle>
          <DialogDescription>
            An inline policy is embedded in the {kind} <span className="text-foreground font-medium">{owner}</span> and is deleted with it.
          </DialogDescription>
        </DialogHeader>
        {creating && (
          <Field label="Policy name" htmlFor="inline-name" error={touched ? nErr : null}>
            <Input id="inline-name" autoFocus value={name} onChange={(e) => setName(e.target.value.trim())} placeholder="e.g. AllowReportsBucket" aria-invalid={touched && !!nErr} />
          </Field>
        )}
        {readOnly && parsed && <PolicyStatementsTable doc={parsed} />}
        <JsonEditor value={text} onChange={readOnly ? undefined : setText} readOnly={readOnly} validate={validatePolicy} rows={14} />
        <DialogFooter>
          <Button variant="outline" onClick={() => onOpenChange(false)} disabled={pending}>
            {readOnly ? "Close" : "Cancel"}
          </Button>
          {!readOnly && (
            <Button onClick={save} disabled={pending || !!docErr}>
              {pending && <Loader2 className="animate-spin" />} {creating ? "Create policy" : "Save changes"}
            </Button>
          )}
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
