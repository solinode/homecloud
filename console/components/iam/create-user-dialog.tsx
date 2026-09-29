"use client"

import Link from "next/link"
import { useEffect, useState } from "react"
import { AlertTriangle, CheckCircle2, Download, Loader2 } from "lucide-react"
import { toast } from "sonner"

import { Button } from "@/components/ui/button"
import { Checkbox } from "@/components/ui/checkbox"
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { CopyableText } from "@/components/console/copy-button"
import { Field } from "@/components/console/form-field"
import { TagsEditor, rowsToTags, type TagRow } from "@/components/console/tags-editor"
import { api, errorMessage } from "@/lib/api"
import { revalidate, useApi } from "@/lib/hooks"
import type { IamGroup, IamUser, PolicySummary } from "@/lib/types"
import { cn } from "@/lib/utils"

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
  signInUrl,
  userHref,
  type PasswordChoice,
} from "./common"

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
      await api.post<IamUser>(`${IAM}/users`, { name, password: password ?? undefined, groups, policies, tags: rowsToTags(tags) })
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
                <CheckCircle2 className="size-5 text-emerald-600 dark:text-emerald-400" /> User created successfully
              </DialogTitle>
              <DialogDescription>
                {created.password ? "Retrieve the console password now. You can reset it later, but you cannot view it again." : "The user was created without console access."}
              </DialogDescription>
            </DialogHeader>
            {created.password && (
              <div className="flex flex-col gap-3">
                <div className="flex items-start gap-2 rounded-md border border-amber-500/40 bg-amber-50 p-3 text-sm text-amber-800 dark:bg-amber-500/10 dark:text-amber-200">
                  <AlertTriangle className="mt-0.5 size-4 shrink-0" />
                  This is the only time the password is shown. Copy it or download the .csv file and share it with {created.name} securely.
                </div>
                <div className="bg-muted/30 grid gap-3 rounded-md border p-3 text-sm">
                  <div>
                    <p className="text-muted-foreground mb-1 text-xs">Console sign-in URL</p>
                    <CopyableText value={url} />
                  </div>
                  <div>
                    <p className="text-muted-foreground mb-1 text-xs">User name</p>
                    <CopyableText value={created.name} />
                  </div>
                  <div>
                    <p className="text-muted-foreground mb-1 text-xs">Console password</p>
                    <SecretValue value={created.password} label="Password" />
                  </div>
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
              <ol className="mt-1 flex flex-wrap items-center gap-x-3 gap-y-1 text-xs">
                {STEPS.map((s, i) => (
                  <li key={s} className={cn("flex items-center gap-1.5", i === step ? "text-foreground font-medium" : "text-muted-foreground")}>
                    <span
                      className={cn(
                        "flex size-5 items-center justify-center rounded-full border text-[11px]",
                        i === step && "border-primary bg-primary text-primary-foreground",
                        i < step && "border-primary text-primary",
                      )}
                    >
                      {i + 1}
                    </span>
                    {s}
                  </li>
                ))}
              </ol>
            </DialogHeader>

            {step === 0 && (
              <div className="flex flex-col gap-4">
                <Field label="User name" htmlFor="new-user-name" error={touched ? nErr : null} help="Up to 64 characters: letters, digits and + = , . @ _ -">
                  <Input id="new-user-name" autoFocus autoComplete="off" value={name} onChange={(e) => setName(e.target.value.trim())} aria-invalid={touched && !!nErr} placeholder="e.g. jane" />
                </Field>
                <label className="flex items-start gap-3 rounded-md border p-3">
                  <Checkbox checked={consoleAccess} onCheckedChange={(v) => setConsoleAccess(v === true)} className="mt-0.5" />
                  <span className="flex flex-col gap-0.5">
                    <span className="text-sm font-medium">Provide user access to the HomeCloud console</span>
                    <span className="text-muted-foreground text-xs">
                      Without a password the user can only call the API with access keys, which you can create on the user&apos;s Security credentials tab.
                    </span>
                  </span>
                </label>
                {consoleAccess && (
                  <div className="flex flex-col gap-2 pl-1">
                    <Label>Console password</Label>
                    <PasswordChooser value={pw} onChange={setPw} showErrors={touched} />
                  </div>
                )}
              </div>
            )}

            {step === 1 && (
              <div className="flex flex-col gap-5">
                <div className="flex flex-col gap-2">
                  <div>
                    <p className="text-sm font-medium">Add user to groups</p>
                    <p className="text-muted-foreground text-xs">The user gets every policy attached to its groups. This is the recommended way to manage permissions.</p>
                  </div>
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
                </div>
                <div className="flex flex-col gap-2">
                  <div>
                    <p className="text-sm font-medium">Attach policies directly</p>
                    <p className="text-muted-foreground text-xs">Optional. Policies attached here apply only to this user.</p>
                  </div>
                  <PolicyPicker policies={policiesQ.data} loading={policiesQ.isLoading} selected={policies} onChange={setPolicies} maxHeight="max-h-60" />
                </div>
              </div>
            )}

            {step === 2 && (
              <div className="flex flex-col gap-4 text-sm">
                <div className="bg-muted/30 grid gap-2 rounded-md border p-3 sm:grid-cols-2">
                  <div>
                    <p className="text-muted-foreground text-xs">User name</p>
                    <p className="font-medium">{name}</p>
                  </div>
                  <div>
                    <p className="text-muted-foreground text-xs">Console access</p>
                    <p>{consoleAccess ? `Enabled (${pw.mode === "auto" ? "autogenerated" : "custom"} password)` : "Disabled"}</p>
                  </div>
                  <div>
                    <p className="text-muted-foreground text-xs">Groups</p>
                    <p>{groups.length ? groups.join(", ") : "None"}</p>
                  </div>
                  <div>
                    <p className="text-muted-foreground text-xs">Policies attached directly</p>
                    <p>{policies.length ? policies.join(", ") : "None"}</p>
                  </div>
                </div>
                {!groups.length && !policies.length && (
                  <p className="text-muted-foreground flex items-center gap-2 text-xs">
                    <AlertTriangle className="size-3.5 text-amber-600 dark:text-amber-400" /> The user will have no permissions until you add it to a group or attach a policy.
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
