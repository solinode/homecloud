"use client"

import { useEffect, useState } from "react"
import { AlertTriangle, Download, KeyRound, Loader2, Plus, Settings2, Trash2 } from "lucide-react"
import { toast } from "sonner"

import { Button } from "@/components/ui/button"
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog"
import { Label } from "@/components/ui/label"
import { RadioGroup, RadioGroupItem } from "@/components/ui/radio-group"
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip"
import { ActionsMenu } from "@/components/console/actions-menu"
import { ConfirmDialog } from "@/components/console/confirm-dialog"
import { CopyButton, CopyableText } from "@/components/console/copy-button"
import { DataTable, type Column } from "@/components/console/data-table"
import { EmptyState } from "@/components/console/empty-state"
import { KeyValueGrid } from "@/components/console/key-value"
import { Section } from "@/components/console/section"
import { StatusBadge } from "@/components/console/status-badge"
import { TimeAgo } from "@/components/console/time-ago"
import { api, errorMessage, seg } from "@/lib/api"
import { revalidate } from "@/lib/hooks"
import type { AccessKey, IamUser, NewAccessKey } from "@/lib/types"

import {
  IAM,
  PasswordChooser,
  SecretValue,
  choiceError,
  choicePassword,
  csvLine,
  downloadText,
  newPasswordChoice,
  signInUrl,
  type PasswordChoice,
} from "./common"

const MAX_KEYS = 2

export function UserCredentials({ user, onChanged }: { user: IamUser; onChanged: () => void }) {
  const keys = user.access_keys ?? []
  const [manage, setManage] = useState(false)
  const [newKey, setNewKey] = useState<NewAccessKey | null>(null)
  const [creating, setCreating] = useState(false)
  const [selected, setSelected] = useState<string[]>([])
  const [confirmDelete, setConfirmDelete] = useState(false)
  const [busy, setBusy] = useState(false)
  const [password, setPassword] = useState<string | null>(null)
  const base = `${IAM}/users/${seg(user.name)}`
  const sel = keys.find((k) => k.access_key_id === selected[0])
  const url = signInUrl()

  const refresh = () => {
    onChanged()
    revalidate(IAM)
  }

  const createKey = async () => {
    setCreating(true)
    try {
      const k = await api.post<NewAccessKey>(`${base}/access-keys`)
      setNewKey(k)
      refresh()
    } catch (e) {
      toast.error(errorMessage(e))
    } finally {
      setCreating(false)
    }
  }

  const setStatus = async (k: AccessKey, status: "Active" | "Inactive") => {
    setBusy(true)
    try {
      await api.patch(`${base}/access-keys/${seg(k.access_key_id)}`, { status })
      toast.success(`Access key ${k.access_key_id} ${status === "Active" ? "activated" : "deactivated"}`)
      refresh()
    } catch (e) {
      toast.error(errorMessage(e))
    } finally {
      setBusy(false)
    }
  }

  const columns: Column<AccessKey>[] = [
    { id: "id", header: "Access key ID", value: (k) => k.access_key_id, cell: (k) => <CopyableText value={k.access_key_id} className="text-[13px]" /> },
    { id: "status", header: "Status", value: (k) => k.status, cell: (k) => <StatusBadge status={k.status} /> },
    { id: "created", header: "Created", value: (k) => k.created_at, cell: (k) => <TimeAgo value={k.created_at} />, hideBelow: "sm" },
    {
      id: "used",
      header: "Last used",
      value: (k) => k.last_used ?? "",
      cell: (k) => (k.last_used ? <TimeAgo value={k.last_used} /> : <span className="text-muted-foreground">Never</span>),
    },
  ]

  const atLimit = keys.length >= MAX_KEYS
  const createBtn = (
    <Button size="sm" onClick={createKey} disabled={atLimit || creating}>
      {creating ? <Loader2 className="animate-spin" /> : <Plus />} Create access key
    </Button>
  )

  return (
    <div className="flex flex-col gap-6">
      <Section
        title="Console sign-in"
        actions={
          <Button size="sm" variant="outline" onClick={() => setManage(true)}>
            <Settings2 /> Manage console access
          </Button>
        }
      >
        <KeyValueGrid
          columns={3}
          items={[
            {
              label: "Console access",
              value: <StatusBadge status={user.console_access ? "enabled" : "disabled"} label={user.console_access ? "Enabled" : "Disabled"} />,
            },
            { label: "Console password", value: user.password_set_at ? <>Set <TimeAgo value={user.password_set_at} /></> : "Not set" },
            { label: "Last console sign-in", value: user.last_login ? <TimeAgo value={user.last_login} /> : "Never" },
            { label: "Console sign-in link", value: <CopyableText value={url} />, wide: true },
          ]}
        />
      </Section>

      <DataTable
        title="Access keys"
        description={
          <>
            Use access keys to call the HomeCloud API from the CLI, SDKs or scripts. A user can have at most {MAX_KEYS} access keys. For security, rotate keys regularly and
            deactivate keys you no longer use.
          </>
        }
        data={keys}
        columns={columns}
        rowId={(k) => k.access_key_id}
        selection="single"
        selected={selected}
        onSelectedChange={setSelected}
        noSearch
        count={keys.length}
        actions={
          <>
            <ActionsMenu
              disabled={!sel || busy}
              items={[
                { label: "Activate", disabled: sel?.status === "Active", onSelect: () => sel && setStatus(sel, "Active") },
                { label: "Deactivate", disabled: sel?.status === "Inactive", onSelect: () => sel && setStatus(sel, "Inactive") },
                { separator: true },
                { label: "Delete", icon: <Trash2 />, destructive: true, onSelect: () => setConfirmDelete(true) },
              ]}
            />
            {atLimit ? (
              <Tooltip>
                <TooltipTrigger asChild>
                  <span tabIndex={0}>{createBtn}</span>
                </TooltipTrigger>
                <TooltipContent>This user already has {MAX_KEYS} access keys. Delete one to create a new key.</TooltipContent>
              </Tooltip>
            ) : (
              createBtn
            )}
          </>
        }
        empty={
          <EmptyState
            icon={KeyRound}
            title="No access keys"
            description="Create an access key to use HomeCloud programmatically."
            action={
              <Button size="sm" onClick={createKey} disabled={creating}>
                <Plus /> Create access key
              </Button>
            }
          />
        }
      />
      {atLimit && <p className="text-muted-foreground -mt-4 text-xs">This user has reached the limit of {MAX_KEYS} access keys. Delete an unused key to create a new one.</p>}

      <ConfirmDialog
        open={confirmDelete}
        onOpenChange={setConfirmDelete}
        title={`Delete access key ${sel?.access_key_id ?? ""}?`}
        description={
          <div className="flex flex-col gap-2">
            <p>Applications that use this key stop working immediately. This cannot be undone.</p>
            {sel?.status === "Active" && (
              <p className="flex items-start gap-2 text-amber-700 dark:text-amber-300">
                <AlertTriangle className="mt-0.5 size-4 shrink-0" /> This key is active. We recommend deactivating it first and deleting it once you are sure nothing uses it.
              </p>
            )}
          </div>
        }
        confirmText={sel?.access_key_id}
        onConfirm={async () => {
          if (!sel) return
          await api.del(`${base}/access-keys/${seg(sel.access_key_id)}`)
          toast.success(`Access key ${sel.access_key_id} deleted`)
          setSelected([])
          refresh()
        }}
      />

      <NewKeyDialog value={newKey} onClose={() => setNewKey(null)} />
      <ConsoleAccessDialog
        open={manage}
        onOpenChange={setManage}
        user={user}
        onDone={(pw) => {
          refresh()
          setPassword(pw)
        }}
      />
      <PasswordResultDialog user={user.name} password={password} onClose={() => setPassword(null)} />
    </div>
  )
}

function NewKeyDialog({ value, onClose }: { value: NewAccessKey | null; onClose: () => void }) {
  const origin = typeof window === "undefined" ? "" : window.location.origin
  const k = value
  const snippet = k
    ? `export AWS_ACCESS_KEY_ID=${k.access_key_id}\nexport AWS_SECRET_ACCESS_KEY=${k.secret_access_key}\n\ncurl -H "Authorization: Bearer $AWS_ACCESS_KEY_ID:$AWS_SECRET_ACCESS_KEY" \\\n  ${origin}/api/v1/auth/whoami`
    : ""
  return (
    <Dialog open={!!value} onOpenChange={() => undefined}>
      <DialogContent className="max-h-[92vh] overflow-y-auto sm:max-w-2xl" showCloseButton={false} onInteractOutside={(e) => e.preventDefault()} onEscapeKeyDown={(e) => e.preventDefault()}>
        {k && (
          <>
            <DialogHeader>
              <DialogTitle>Retrieve access key</DialogTitle>
              <DialogDescription>Access key created for {k.user_name}.</DialogDescription>
            </DialogHeader>
            <div className="flex items-start gap-2 rounded-md border border-amber-500/40 bg-amber-50 p-3 text-sm text-amber-800 dark:bg-amber-500/10 dark:text-amber-200">
              <AlertTriangle className="mt-0.5 size-4 shrink-0" />
              <span>
                This is the only time the secret access key can be viewed or downloaded. You cannot recover it later. You can create a new access key at any time.
              </span>
            </div>
            <div className="grid gap-3 rounded-md border p-3 text-sm">
              <div>
                <p className="text-muted-foreground mb-1 text-xs">Access key ID</p>
                <div className="flex items-center gap-1">
                  <code className="bg-muted flex-1 rounded px-2 py-1 font-mono text-[13px] break-all">{k.access_key_id}</code>
                  <CopyButton value={k.access_key_id} toastMessage="Access key ID copied" />
                </div>
              </div>
              <div>
                <p className="text-muted-foreground mb-1 text-xs">Secret access key</p>
                <SecretValue value={k.secret_access_key} label="Secret access key" />
              </div>
            </div>
            <div className="flex flex-col gap-1.5">
              <div className="flex items-center justify-between">
                <p className="text-sm font-medium">Use it from a terminal</p>
                <CopyButton value={snippet} size="sm" label="Copy" toastMessage="Snippet copied" />
              </div>
              <pre className="bg-muted/50 overflow-x-auto rounded-md border p-3 font-mono text-xs leading-5">{snippet}</pre>
            </div>
            <DialogFooter className="gap-2">
              <Button
                variant="outline"
                onClick={() =>
                  downloadText(`${k.user_name}_accessKeys.csv`, `${csvLine(["Access key ID", "Secret access key"])}\n${csvLine([k.access_key_id, k.secret_access_key])}\n`)
                }
              >
                <Download /> Download .csv file
              </Button>
              <Button onClick={onClose}>Done</Button>
            </DialogFooter>
          </>
        )}
      </DialogContent>
    </Dialog>
  )
}

function ConsoleAccessDialog({ open, onOpenChange, user, onDone }: { open: boolean; onOpenChange: (o: boolean) => void; user: IamUser; onDone: (password: string | null) => void }) {
  const [mode, setMode] = useState<"enable" | "disable">("enable")
  const [pw, setPw] = useState<PasswordChoice>(() => ({ mode: "auto", custom: "", generated: "" }))
  const [touched, setTouched] = useState(false)
  const [pending, setPending] = useState(false)
  useEffect(() => {
    if (open) {
      setMode("enable")
      setPw(newPasswordChoice())
      setTouched(false)
    }
  }, [open])

  const base = `${IAM}/users/${seg(user.name)}`
  const submit = async () => {
    setTouched(true)
    if (mode === "enable" && choiceError(pw)) return
    setPending(true)
    try {
      if (mode === "enable") {
        const p = choicePassword(pw)
        await api.put(`${base}/password`, { password: p })
        toast.success(user.console_access ? `Console password for ${user.name} reset` : `Console access enabled for ${user.name}`)
        onOpenChange(false)
        onDone(p)
      } else {
        await api.del(`${base}/password`)
        toast.success(`Console access disabled for ${user.name}`)
        onOpenChange(false)
        onDone(null)
      }
    } catch (e) {
      toast.error(errorMessage(e))
    } finally {
      setPending(false)
    }
  }

  return (
    <Dialog open={open} onOpenChange={(o) => !pending && onOpenChange(o)}>
      <DialogContent className="max-h-[92vh] overflow-y-auto">
        <DialogHeader>
          <DialogTitle>Manage console access</DialogTitle>
          <DialogDescription>
            Console access for <span className="text-foreground font-medium">{user.name}</span> is currently {user.console_access ? "enabled" : "disabled"}.
          </DialogDescription>
        </DialogHeader>
        <RadioGroup value={mode} onValueChange={(v) => setMode(v as typeof mode)} className="gap-3">
          <div className="flex items-start gap-2">
            <RadioGroupItem value="enable" id="ca-enable" className="mt-0.5" />
            <div className="flex flex-col gap-0.5">
              <Label htmlFor="ca-enable">{user.console_access ? "Reset password" : "Enable console access"}</Label>
              <span className="text-muted-foreground text-xs">Set a new console password. Existing sessions stay signed in.</span>
            </div>
          </div>
          <div className="flex items-start gap-2">
            <RadioGroupItem value="disable" id="ca-disable" className="mt-0.5" disabled={user.root || !user.console_access} />
            <div className="flex flex-col gap-0.5">
              <Label htmlFor="ca-disable" className={user.root || !user.console_access ? "opacity-50" : undefined}>
                Disable console access
              </Label>
              <span className="text-muted-foreground text-xs">
                {user.root ? "The root user must keep console access." : "Removes the console password. Access keys keep working."}
              </span>
            </div>
          </div>
        </RadioGroup>
        {mode === "enable" && (
          <div className="flex flex-col gap-2 rounded-md border p-3">
            <Label>Console password</Label>
            <PasswordChooser value={pw} onChange={setPw} showErrors={touched} />
          </div>
        )}
        <DialogFooter>
          <Button variant="outline" onClick={() => onOpenChange(false)} disabled={pending}>
            Cancel
          </Button>
          <Button variant={mode === "disable" ? "destructive" : "default"} onClick={submit} disabled={pending}>
            {pending && <Loader2 className="animate-spin" />}
            {mode === "disable" ? "Disable console access" : user.console_access ? "Reset password" : "Enable console access"}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

function PasswordResultDialog({ user, password, onClose }: { user: string; password: string | null; onClose: () => void }) {
  const url = signInUrl()
  return (
    <Dialog open={!!password} onOpenChange={() => undefined}>
      <DialogContent showCloseButton={false} onInteractOutside={(e) => e.preventDefault()} onEscapeKeyDown={(e) => e.preventDefault()}>
        {password && (
          <>
            <DialogHeader>
              <DialogTitle>Console password</DialogTitle>
              <DialogDescription>Share these sign-in details with {user} securely. The password is not shown again.</DialogDescription>
            </DialogHeader>
            <div className="bg-muted/30 grid gap-3 rounded-md border p-3 text-sm">
              <div>
                <p className="text-muted-foreground mb-1 text-xs">Console sign-in URL</p>
                <CopyableText value={url} />
              </div>
              <div>
                <p className="text-muted-foreground mb-1 text-xs">User name</p>
                <CopyableText value={user} />
              </div>
              <div>
                <p className="text-muted-foreground mb-1 text-xs">Console password</p>
                <SecretValue value={password} label="Password" />
              </div>
            </div>
            <DialogFooter className="gap-2">
              <Button variant="outline" onClick={() => downloadText(`${user}_credentials.csv`, `${csvLine(["User name", "Password", "Console sign-in URL"])}\n${csvLine([user, password, url])}\n`)}>
                <Download /> Download .csv file
              </Button>
              <Button onClick={onClose}>Done</Button>
            </DialogFooter>
          </>
        )}
      </DialogContent>
    </Dialog>
  )
}
