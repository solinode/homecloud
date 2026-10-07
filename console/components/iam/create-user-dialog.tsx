"use client"

import Link from "next/link"
import { useEffect, useState } from "react"
import { AlertTriangle, CheckCircle2, Download, Loader2 } from "lucide-react"
import { toast } from "sonner"

import { Button } from "@/components/ui/button"
import { Checkbox } from "@/components/ui/checkbox"
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog"
import { Input } from "@/components/ui/input"
import { Alert, AlertDescription } from "@/components/ui/alert"
import { CopyableText } from "@/components/console/copy-button"
import { Field } from "@/components/console/form-field"
import { KeyValueGrid } from "@/components/console/key-value"
import { Stepper } from "@/components/console/stepper"
import { TagsEditor, rowsToTags, type TagRow } from "@/components/console/tags-editor"
import { api, errorMessage } from "@/lib/api"
import { revalidate, useApi } from "@/lib/hooks"
import type { IamGroup, IamUser, PolicySummary } from "@/lib/types"

import {
  CheckList,
  IAM,
  PasswordChooser,
  PolicyPicker,
  SecretValue,
  choiceError,
  choicePassword,
  csvLine,
  downloadText,
  nameError,
  newPasswordChoice,
  policyNameFromArn,
  signInUrl,
  userHref,
  type PasswordChoice,
} from "./common"
import { BoundaryHelp, BoundarySelect } from "./boundary"

const STEPS = ["User details", "Permissions", "Review and create"]

interface Created {
  name: string
  password: string | null
}

export function CreateUserDialog({ open, onOpenChange, existing }: { open: boolean; onOpenChange: (o: boolean) => void; existing: string[] }) {
  const [step, setStep] = useState(0)
  const [name, setName] = useState("")
  const [consoleAccess, setConsoleAccess] = useState(true)
  const [pw, setPw] = useState<PasswordChoice>(() => ({ mode: "auto", custom: "", generated: "" }))
  const [groups, setGroups] = useState<string[]>([])
  const [policies, setPolicies] = useState<string[]>([])
  const [tags, setTags] = useState<TagRow[]>([])
  const [boundary, setBoundary] = useState("")
  const [touched, setTouched] = useState(false)
  const [pending, setPending] = useState(false)
  const [created, setCreated] = useState<Created | null>(null)

  const groupsQ = useApi<IamGroup[]>(open ? `${IAM}/groups` : null)
  const policiesQ = useApi<PolicySummary[]>(open ? `${IAM}/policies` : null)

  useEffect(() => {
    if (open) {
      setStep(0)
      setName("")
      setConsoleAccess(true)
      setPw(newPasswordChoice())
      setGroups([])
      setPolicies([])
      setTags([])
      setBoundary("")
      setTouched(false)
      setCreated(null)
    }
  }, [open])

  const nErr = nameError("user", name) ?? (existing.includes(name) ? `A user named ${name} already exists.` : null)
  const pErr = consoleAccess ? choiceError(pw) : null
  const tagErr = tags.some((t) => !t.key.trim() && t.value.trim()) ? "Every tag needs a key." : null

  const next = () => {
    setTouched(true)
    if (step === 0 && (nErr || pErr)) return
    setStep(step + 1)
  }

  const create = async () => {
    if (tagErr) return
    setPending(true)
    const password = consoleAccess ? choicePassword(pw) : null
    try {
      await api.post<IamUser>(`${IAM}/users`, { name, password: password ?? undefined, groups, policies, tags: rowsToTags(tags), permissions_boundary: boundary || undefined })
      toast.success(`User ${name} created`)
      revalidate(IAM)
      setCreated({ name, password })
    } catch (e) {
      toast.error(errorMessage(e))
    } finally {
      setPending(false)
    }
  }

  const url = signInUrl()

  return (
    <Dialog open={open} onOpenChange={(o) => !pending && onOpenChange(o)}>
      <DialogContent className="max-h-[92vh] overflow-y-auto sm:max-w-2xl" onInteractOutside={(e) => e.preventDefault()}
        onEscapeKeyDown={(e) => created && e.preventDefault()}
        showCloseButton={!created}
      >
        {created ? (
          <>
            <DialogHeader>
              <DialogTitle className="flex items-center gap-2">
                <CheckCircle2 className="text-success size-5" /> User created successfully
              </DialogTitle>
              <DialogDescription>
                {created.password ? "Retrieve the console password now. You can reset it later, but you cannot view it again." : "The user was created without console access."}
              </DialogDescription>
            </DialogHeader>
            {created.password && (
              <div className="flex flex-col gap-3">
                <Alert variant="warning">
                  <AlertTriangle />
                  <AlertDescription>
                    This is the only time the password is shown. Copy it or download the .csv file and share it with {created.name} securely.
                  </AlertDescription>
                </Alert>
                <div className="bg-muted/40 rounded-lg border p-4">
                  <KeyValueGrid
                    columns={2}
                    items={[
                      { label: "Console sign-in URL", value: <CopyableText value={url} />, wide: true },
                      { label: "User name", value: <CopyableText value={created.name} /> },
                      { label: "Console password", value: <SecretValue value={created.password} label="Password" /> },
                    ]}
                  />
                </div>
              </div>
            )}
            <DialogFooter className="gap-2">
              {created.password && (
                <Button
                  variant="outline"
                  onClick={() =>
                    downloadText(`${created.name}_credentials.csv`, `${csvLine(["User name", "Password", "Console sign-in URL"])}\n${csvLine([created.name, created.password!, url])}\n`)
                  }
                >
                  <Download /> Download .csv file
                </Button>
              )}
              <Button variant="outline" asChild>
                <Link href={userHref(created.name)}>View user</Link>
              </Button>
              <Button onClick={() => onOpenChange(false)}>Done</Button>
            </DialogFooter>
          </>
        ) : (
          <>
            <DialogHeader>
              <DialogTitle>Create user</DialogTitle>
              <DialogDescription className="sr-only">Create an IAM user in three steps.</DialogDescription>
              <Stepper steps={STEPS} current={step} onStepClick={(i) => i < step && !pending && setStep(i)} className="mt-2" />
            </DialogHeader>

            {step === 0 && (
              <div className="flex flex-col gap-4">
                <Field label="User name" htmlFor="new-user-name" error={touched ? nErr : null} help="Up to 64 characters: letters, digits and + = , . @ _ -">
                  <Input id="new-user-name" autoFocus autoComplete="off" value={name} onChange={(e) => setName(e.target.value.trim())} aria-invalid={touched && !!nErr} placeholder="e.g. jane" />
                </Field>
                <label className="bg-card hover:bg-muted/50 flex cursor-pointer items-start gap-3 rounded-lg border p-3.5 transition-colors">
                  <Checkbox checked={consoleAccess} onCheckedChange={(v) => setConsoleAccess(v === true)} className="mt-0.5" />
                  <span className="flex flex-col gap-0.5">
                    <span className="text-sm font-medium">Provide user access to the HomeCloud console</span>
                    <span className="text-muted-foreground text-xs">
                      Without a password the user can only call the API with access keys, which you can create on the user&apos;s Security credentials tab.
                    </span>
                  </span>
                </label>
                {consoleAccess && (
                  <Field label="Console password">
                    <PasswordChooser value={pw} onChange={setPw} showErrors={touched} />
                  </Field>
                )}
              </div>
            )}

            {step === 1 && (
              <div className="flex flex-col gap-5">
                <Field label="Add user to groups" help="The user gets every policy attached to its groups. This is the recommended way to manage permissions.">
                  <CheckList
                    loading={groupsQ.isLoading}
                    items={(groupsQ.data ?? []).map((g) => ({ id: g.name, label: g.name, sub: `${g.attached_policies.length} policies, ${g.members.length} users` }))}
                    selected={groups}
                    onChange={setGroups}
                    empty={
                      <>
                        No user groups yet.{" "}
                        <Link href="/iam/groups/?create=1" className="text-primary hover:underline">
                          Create a group
                        </Link>
                      </>
                    }
                  />
                </Field>
                <Field label="Attach policies directly" optional help="Policies attached here apply only to this user.">
                  <PolicyPicker policies={policiesQ.data} loading={policiesQ.isLoading} selected={policies} onChange={setPolicies} maxHeight="max-h-60" />
                </Field>
                <Field label="Permissions boundary" optional htmlFor="new-user-boundary" help={<BoundaryHelp kind="user" />}>
                  <BoundarySelect id="new-user-boundary" value={boundary} onChange={setBoundary} />
                </Field>
              </div>
            )}

            {step === 2 && (
              <div className="flex flex-col gap-4 text-sm">
                <div className="bg-muted/40 rounded-lg border p-4">
                  <KeyValueGrid
                    columns={2}
                    items={[
                      { label: "User name", value: <span className="font-medium">{name}</span> },
                      { label: "Console access", value: consoleAccess ? `Enabled (${pw.mode === "auto" ? "autogenerated" : "custom"} password)` : "Disabled" },
                      { label: "Groups", value: groups.length ? groups.join(", ") : "None" },
                      { label: "Policies attached directly", value: policies.length ? policies.join(", ") : "None" },
                      { label: "Permissions boundary", value: boundary ? policyNameFromArn(boundary) : "Not set" },
                    ]}
                  />
                </div>
                {!groups.length && !policies.length && (
                  <p className="text-muted-foreground flex items-center gap-2 text-xs">
                    <AlertTriangle className="text-warning size-3.5" /> The user will have no permissions until you add it to a group or attach a policy.
                  </p>
                )}
                <Field label="Tags" optional help="Key-value pairs to organize and find users. You can change them later on the user's Tags tab." error={tagErr}>
                  <TagsEditor rows={tags} onChange={setTags} addLabel="Add tag" />
                </Field>
              </div>
            )}

            <DialogFooter className="gap-2">
              <Button type="button" variant="outline" onClick={() => (step === 0 ? onOpenChange(false) : setStep(step - 1))} disabled={pending}>
                {step === 0 ? "Cancel" : "Previous"}
              </Button>
              {step < 2 ? (
                <Button type="button" onClick={next}>
                  Next
                </Button>
              ) : (
                <Button type="button" onClick={create} disabled={pending || !!tagErr}>
                  {pending && <Loader2 className="animate-spin" />} Create user
                </Button>
              )}
            </DialogFooter>
          </>
        )}
      </DialogContent>
    </Dialog>
  )
}
