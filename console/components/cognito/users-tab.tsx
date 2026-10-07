"use client"

import { useEffect, useState } from "react"
import { Loader2, Plus, UserRound } from "lucide-react"
import { toast } from "sonner"

import { Button } from "@/components/ui/button"
import { Checkbox } from "@/components/ui/checkbox"
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog"
import { Input } from "@/components/ui/input"
import { RadioGroup, RadioGroupItem } from "@/components/ui/radio-group"
import { ActionsMenu, type ActionItem } from "@/components/console/actions-menu"
import { ConfirmDialog } from "@/components/console/confirm-dialog"
import { CopyableText } from "@/components/console/copy-button"
import { CellText, DataTable, type Column } from "@/components/console/data-table"
import { EmptyState } from "@/components/console/empty-state"
import { Field } from "@/components/console/form-field"
import { KeyValueGrid } from "@/components/console/key-value"
import { Tag } from "@/components/console/tag"
import { TagsEditor, tagsToRows, type TagRow } from "@/components/console/tags-editor"
import { TimeAgo } from "@/components/console/time-ago"
import { api, errorMessage, seg } from "@/lib/api"
import { formatDate } from "@/lib/format"
import { revalidate, useApi } from "@/lib/hooks"
import type { CognitoUser, CreateCognitoUserInput, UpdateCognitoUserInput, UserPool } from "@/lib/types"

import { COGNITO_PATH, EnabledBadge, SwitchRow, UserStatusBadge, passwordProblems, policySummary, poolPath } from "./shared"
import { ShowOnceDialog } from "./show-once-dialog"

const RESERVED = new Set(["sub", "aud", "iss", "exp", "iat", "nbf", "auth_time", "token_use", "client_id", "username", "jti", "scope", "event_id", "origin_jti"])

/** attrKeyError mirrors checkAttrs in cognito.go. */
function attrKeyError(k: string): string | null {
  if (RESERVED.has(k) || k.startsWith("cognito:") || k.startsWith("hc:")) return `"${k}" is a reserved claim name`
  if (k.length > 64) return "Attribute names are at most 64 characters"
  return null
}

function rowsError(rows: TagRow[]): string | null {
  const keys = rows.map((r) => r.key.trim()).filter(Boolean)
  if (new Set(keys).size !== keys.length) return "Attribute names must be unique"
  if (rows.some((r) => !r.key.trim() && r.value.trim())) return "Every attribute with a value needs a name"
  for (const k of keys) {
    const e = attrKeyError(k)
    if (e) return e
  }
  if (rows.some((r) => r.value.length > 2048)) return "Attribute values are at most 2048 characters"
  return null
}

const userPath = (pool: string, username: string) => `${poolPath(pool)}/users/${seg(username)}`

export function UsersTab({ pool }: { pool: UserPool }) {
  const { data, error, isLoading, isValidating, mutate } = useApi<CognitoUser[]>(`${poolPath(pool.id)}/users`, { refreshInterval: 30_000 })
  const [selected, setSelected] = useState<string[]>([])
  const [creating, setCreating] = useState(false)
  const [created, setCreated] = useState<CognitoUser | null>(null)
  const [pwFor, setPwFor] = useState<CognitoUser | null>(null)
  const [attrsFor, setAttrsFor] = useState<CognitoUser | null>(null)
  const [groupsFor, setGroupsFor] = useState<CognitoUser | null>(null)
  const [signingOut, setSigningOut] = useState<CognitoUser | null>(null)
  const [deleting, setDeleting] = useState<CognitoUser | null>(null)
  const [busy, setBusy] = useState(false)

  const sel = (data ?? []).find((u) => selected.includes(u.username)) ?? null

  const patch = async (u: CognitoUser, body: UpdateCognitoUserInput, success: string) => {
    setBusy(true)
    try {
      await api.patch(userPath(pool.id, u.username), body)
      toast.success(success)
      await revalidate(COGNITO_PATH)
    } catch (e) {
      toast.error(errorMessage(e))
    } finally {
      setBusy(false)
    }
  }

  const columns: Column<CognitoUser>[] = [
    {
      id: "username",
      header: "User name",
      cell: (u) => (
        <CellText max="16rem" className="font-medium">
          {u.username}
        </CellText>
      ),
      value: (u) => u.username,
    },
    { id: "email", header: "Email", cell: (u) => <CellText max="16rem">{u.attributes?.email}</CellText>, value: (u) => u.attributes?.email, hideBelow: "md" },
    { id: "status", header: "Confirmation status", cell: (u) => <UserStatusBadge status={u.status} />, value: (u) => u.status },
    { id: "enabled", header: "Status", cell: (u) => <EnabledBadge enabled={u.enabled} />, value: (u) => (u.enabled ? 1 : 0) },
    {
      id: "groups",
      header: "Groups",
      cell: (u) => <CellText max="12rem">{u.groups?.length ? u.groups.join(", ") : null}</CellText>,
      value: (u) => (u.groups ?? []).join(" "),
      hideBelow: "lg",
    },
    {
      id: "signin",
      header: "Last sign-in",
      cell: (u) => (u.last_sign_in ? <TimeAgo value={u.last_sign_in} /> : <span className="text-muted-foreground">Never</span>),
      value: (u) => u.last_sign_in ?? "",
      hideBelow: "lg",
    },
    { id: "created", header: "Created", cell: (u) => <TimeAgo value={u.created_at} />, value: (u) => u.created_at, hideBelow: "sm" },
  ]

  const items: ActionItem[] = sel
    ? [
        sel.enabled
          ? { label: "Disable user", onSelect: () => patch(sel, { enabled: false }, `Disabled ${sel.username}; their tokens stop working`), disabled: busy }
          : { label: "Enable user", onSelect: () => patch(sel, { enabled: true }, `Enabled ${sel.username}`), disabled: busy },
        ...(sel.status === "UNCONFIRMED" ? [{ label: "Confirm account", onSelect: () => patch(sel, { confirm: true }, `Confirmed ${sel.username}`), disabled: busy }] : []),
        { label: "Set password", onSelect: () => setPwFor(sel) },
        { label: "Edit attributes", onSelect: () => setAttrsFor(sel) },
        { label: "Add to / remove from groups", onSelect: () => setGroupsFor(sel), disabled: !pool.groups.length, hint: pool.groups.length ? undefined : "The pool has no groups" },
        { separator: true },
        { label: "Sign out everywhere", onSelect: () => setSigningOut(sel) },
        { label: "Delete user", destructive: true, onSelect: () => setDeleting(sel) },
      ]
    : [{ label: "Select a user", onSelect: () => undefined, disabled: true }]

  return (
    <div className="flex flex-col gap-4">
      <DataTable
        title="Users"
        description={`Password policy: ${policySummary(pool.password_policy)}.`}
        data={data}
        columns={columns}
        rowId={(u) => u.username}
        loading={isLoading}
        error={error}
        onRefresh={() => mutate()}
        refreshing={isValidating}
        selection="single"
        selected={selected}
        onSelectedChange={setSelected}
        searchPlaceholder="Filter by user name, email or group"
        defaultSort={{ id: "username" }}
        expanded={(u) => (selected.includes(u.username) ? <UserDetails user={u} /> : null)}
        actions={
          <>
            <ActionsMenu items={items} disabled={!sel} />
            <Button size="sm" onClick={() => setCreating(true)}>
              <Plus /> Create user
            </Button>
          </>
        }
        empty={
          <EmptyState
            icon={UserRound}
            title="No users"
            description={pool.self_sign_up ? "Users appear here when they sign up through an app client, or when you create them." : "Self sign-up is off: create users here."}
            action={
              <Button size="sm" onClick={() => setCreating(true)}>
                <Plus /> Create user
              </Button>
            }
          />
        }
      />

      <CreateUserDialog pool={pool} open={creating} onOpenChange={setCreating} onCreated={setCreated} />
      <ShowOnceDialog
        open={!!created?.temporary_password}
        onClose={() => setCreated(null)}
        title={`User ${created?.username ?? ""} created`}
        description="HomeCloud generated a temporary password. The user must choose a new password at first sign-in (NEW_PASSWORD_REQUIRED challenge)."
        values={created?.temporary_password ? [{ label: "Temporary password", value: created.temporary_password, secret: true }] : []}
      />
      <SetPasswordDialog pool={pool} user={pwFor} onClose={() => setPwFor(null)} />
      <AttributesDialog pool={pool} user={attrsFor} onClose={() => setAttrsFor(null)} />
      <GroupsDialog pool={pool} user={groupsFor} onClose={() => setGroupsFor(null)} />
      <ConfirmDialog
        open={!!signingOut}
        onOpenChange={(o) => !o && setSigningOut(null)}
        title={`Sign ${signingOut?.username ?? ""} out everywhere?`}
        actionLabel="Sign out"
        destructive={false}
        description="Revokes every refresh token of the user and invalidates their outstanding ID and access tokens. They must sign in again."
        onConfirm={async () => {
          if (!signingOut) return
          await api.post(`${userPath(pool.id, signingOut.username)}/sign-out`)
          toast.success(`Signed ${signingOut.username} out of all sessions`)
        }}
      />
      <ConfirmDialog
        open={!!deleting}
        onOpenChange={(o) => !o && setDeleting(null)}
        title={`Delete user ${deleting?.username ?? ""}`}
        confirmText={deleting?.username}
        description="The user and their sessions are deleted permanently. Their tokens stop verifying immediately."
        onConfirm={async () => {
          if (!deleting) return
          await api.del(userPath(pool.id, deleting.username))
          toast.success(`Deleted user ${deleting.username}`)
          setSelected([])
          await revalidate(COGNITO_PATH)
        }}
      />
    </div>
  )
}

function UserDetails({ user }: { user: CognitoUser }) {
  const attrs = Object.entries(user.attributes ?? {}).sort(([a], [b]) => a.localeCompare(b))
  return (
    <KeyValueGrid
      columns={4}
      items={[
        { label: "sub", value: <CopyableText value={user.sub} /> },
        { label: "Created", value: formatDate(user.created_at) },
        { label: "Last sign-in", value: user.last_sign_in ? formatDate(user.last_sign_in) : "Never" },
        { label: "Groups", value: user.groups?.length ? user.groups.join(", ") : "" },
        {
          label: "Attributes",
          wide: true,
          value: attrs.length ? (
            <span className="flex flex-wrap gap-1.5">
              {attrs.map(([k, v]) => (
                <Tag key={k} title={`${k}=${v}`} className="max-w-full">
                  <span className="truncate">
                    {k}={v}
                  </span>
                </Tag>
              ))}
            </span>
          ) : (
            ""
          ),
        },
      ]}
    />
  )
}

type PwMode = "generate" | "set"

function CreateUserDialog({ pool, open, onOpenChange, onCreated }: { pool: UserPool; open: boolean; onOpenChange: (o: boolean) => void; onCreated: (u: CognitoUser) => void }) {
  const [username, setUsername] = useState("")
  const [email, setEmail] = useState("")
  const [attrs, setAttrs] = useState<TagRow[]>([])
  const [mode, setMode] = useState<PwMode>("generate")
  const [password, setPassword] = useState("")
  const [temporary, setTemporary] = useState(true)
  const [groups, setGroups] = useState<string[]>([])
  const [touched, setTouched] = useState(false)
  const [pending, setPending] = useState(false)

  useEffect(() => {
    if (!open) return
    setUsername("")
    setEmail("")
    setAttrs([])
    setMode("generate")
    setPassword("")
    setTemporary(true)
    setGroups([])
    setTouched(false)
  }, [open])

  const errors: Record<string, string> = {}
  if (!username.trim()) errors.username = "Enter a user name"
  else if (/\s/.test(username.trim()) || username.trim().length > 128) errors.username = "1-128 characters without spaces"
  if (email && !/^[^\s@]+@[^\s@]+$/.test(email.trim())) errors.email = "Enter a valid email address"
  const aErr = rowsError(attrs)
  if (aErr) errors.attrs = aErr
  if (mode === "set") {
    const p = passwordProblems(password, pool.password_policy)
    if (p) errors.password = p
  }
  const err = (k: string) => (touched ? errors[k] : undefined)

  const submit = async (e: React.FormEvent) => {
    e.preventDefault()
    setTouched(true)
    if (Object.keys(errors).length) return
    const attributes: Record<string, string> = {}
    for (const r of attrs) if (r.key.trim()) attributes[r.key.trim()] = r.value
    if (email.trim()) attributes.email = email.trim()
    const body: CreateCognitoUserInput = {
      username: username.trim(),
      password: mode === "set" ? password : undefined,
      temporary_password: mode === "set" ? temporary : undefined,
      attributes: Object.keys(attributes).length ? attributes : undefined,
      groups: groups.length ? groups : undefined,
    }
    setPending(true)
    try {
      const u = await api.post<CognitoUser>(`${poolPath(pool.id)}/users`, body)
      toast.success(`Created user ${u.username}`)
      await revalidate(COGNITO_PATH)
      onOpenChange(false)
      onCreated(u)
    } catch (err) {
      toast.error(errorMessage(err))
    } finally {
      setPending(false)
    }
  }

  return (
    <Dialog open={open} onOpenChange={(o) => !pending && onOpenChange(o)}>
      <DialogContent className="max-h-[90vh] overflow-y-auto sm:max-w-lg">
        <form onSubmit={submit} className="flex flex-col gap-4">
          <DialogHeader>
            <DialogTitle>Create user</DialogTitle>
            <DialogDescription>Administrator-created users are confirmed. With a temporary password they must set a new one at first sign-in.</DialogDescription>
          </DialogHeader>
          <Field label="User name" htmlFor="cu-name" error={err("username")} help="Case-insensitive and unique in the pool.">
            <Input id="cu-name" autoFocus value={username} onChange={(e) => setUsername(e.target.value)} placeholder="ann" autoComplete="off" />
          </Field>
          <Field label="Email" htmlFor="cu-email" optional error={err("email")} help="Stored as the email attribute and included in ID tokens.">
            <Input id="cu-email" type="email" value={email} onChange={(e) => setEmail(e.target.value)} placeholder="ann@example.com" autoComplete="off" />
          </Field>
          <Field label="Password">
            <RadioGroup value={mode} onValueChange={(v) => setMode(v as PwMode)} className="gap-2">
              <label className="flex items-start gap-2 text-sm">
                <RadioGroupItem value="generate" className="mt-0.5" />
                <span>
                  Generate a temporary password
                  <span className="text-muted-foreground block text-xs">Shown once after the user is created.</span>
                </span>
              </label>
              <label className="flex items-start gap-2 text-sm">
                <RadioGroupItem value="set" className="mt-0.5" />
                <span>Set a password</span>
              </label>
            </RadioGroup>
            {mode === "set" && (
              <div className="flex flex-col gap-2 pl-6">
                <Field label="Password" htmlFor="cu-pw" error={err("password")} help={policySummary(pool.password_policy)}>
                  <Input id="cu-pw" type="password" autoComplete="new-password" value={password} onChange={(e) => setPassword(e.target.value)} />
                </Field>
                <label className="flex items-center gap-2 text-sm">
                  <Checkbox checked={temporary} onCheckedChange={(c) => setTemporary(!!c)} />
                  Temporary: require a new password at first sign-in
                </label>
              </div>
            )}
          </Field>
          {pool.groups.length > 0 && (
            <Field label="Groups" optional>
              <div className="flex flex-wrap gap-x-4 gap-y-2">
                {pool.groups.map((g) => (
                  <label key={g.name} className="flex items-center gap-2 text-sm">
                    <Checkbox checked={groups.includes(g.name)} onCheckedChange={(c) => setGroups(c ? [...groups, g.name] : groups.filter((x) => x !== g.name))} />
                    {g.name}
                  </label>
                ))}
              </div>
            </Field>
          )}
          <Field label="Other attributes" optional error={err("attrs")} help="Custom claims, e.g. name, locale, custom:tenant. They appear in ID tokens.">
            <TagsEditor rows={attrs} onChange={setAttrs} keyPlaceholder="Attribute" valuePlaceholder="Value" addLabel="Add attribute" />
          </Field>
          <DialogFooter>
            <Button type="button" variant="outline" onClick={() => onOpenChange(false)} disabled={pending}>
              Cancel
            </Button>
            <Button type="submit" disabled={pending}>
              {pending && <Loader2 className="animate-spin" />}
              Create user
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}

function SetPasswordDialog({ pool, user, onClose }: { pool: UserPool; user: CognitoUser | null; onClose: () => void }) {
  const [password, setPassword] = useState("")
  const [permanent, setPermanent] = useState(true)
  const [touched, setTouched] = useState(false)
  const [pending, setPending] = useState(false)
  useEffect(() => {
    if (!user) return
    setPassword("")
    setPermanent(true)
    setTouched(false)
  }, [user])
  const pErr = passwordProblems(password, pool.password_policy)

  const submit = async (e: React.FormEvent) => {
    e.preventDefault()
    setTouched(true)
    if (!user || pErr) return
    setPending(true)
    try {
      await api.post(`${userPath(pool.id, user.username)}/password`, { password, permanent })
      toast.success(permanent ? `Set a new password for ${user.username}` : `Set a temporary password for ${user.username}; they must change it at next sign-in`)
      await revalidate(COGNITO_PATH)
      onClose()
    } catch (err) {
      toast.error(errorMessage(err))
    } finally {
      setPending(false)
    }
  }

  return (
    <Dialog open={!!user} onOpenChange={(o) => !o && !pending && onClose()}>
      <DialogContent className="sm:max-w-md">
        <form onSubmit={submit} className="flex flex-col gap-4">
          <DialogHeader>
            <DialogTitle>Set password for {user?.username}</DialogTitle>
            <DialogDescription>Existing sessions stay signed in; use Sign out everywhere to end them.</DialogDescription>
          </DialogHeader>
          <Field label="New password" htmlFor="sp-pw" error={touched ? pErr : undefined} help={policySummary(pool.password_policy)}>
            <Input id="sp-pw" type="password" autoFocus autoComplete="new-password" value={password} onChange={(e) => setPassword(e.target.value)} />
          </Field>
          <SwitchRow
            id="sp-perm"
            label="Permanent password"
            description={permanent ? "The user is confirmed and can sign in with this password." : "Temporary: the user must choose a new password at next sign-in."}
            checked={permanent}
            onChange={setPermanent}
          />
          <DialogFooter>
            <Button type="button" variant="outline" onClick={onClose} disabled={pending}>
              Cancel
            </Button>
            <Button type="submit" disabled={pending}>
              {pending && <Loader2 className="animate-spin" />}
              Set password
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}

function AttributesDialog({ pool, user, onClose }: { pool: UserPool; user: CognitoUser | null; onClose: () => void }) {
  const [rows, setRows] = useState<TagRow[]>([])
  const [pending, setPending] = useState(false)
  useEffect(() => {
    if (user) setRows(tagsToRows(user.attributes ?? {}))
  }, [user])
  const rErr = rowsError(rows)

  const submit = async (e: React.FormEvent) => {
    e.preventDefault()
    if (!user || rErr) return
    const attributes: Record<string, string> = {}
    for (const k of Object.keys(user.attributes ?? {})) attributes[k] = "" // removed unless kept below
    for (const r of rows) if (r.key.trim()) attributes[r.key.trim()] = r.value
    setPending(true)
    try {
      await api.patch(userPath(pool.id, user.username), { attributes })
      toast.success(`Saved attributes of ${user.username}`)
      await revalidate(COGNITO_PATH)
      onClose()
    } catch (err) {
      toast.error(errorMessage(err))
    } finally {
      setPending(false)
    }
  }

  return (
    <Dialog open={!!user} onOpenChange={(o) => !o && !pending && onClose()}>
      <DialogContent className="max-h-[90vh] overflow-y-auto sm:max-w-lg">
        <form onSubmit={submit} className="flex flex-col gap-4">
          <DialogHeader>
            <DialogTitle>Attributes of {user?.username}</DialogTitle>
            <DialogDescription>Attributes become claims in new ID tokens. Remove a row to delete the attribute. An empty value also deletes it.</DialogDescription>
          </DialogHeader>
          <Field label="Attributes" error={rErr} help="Custom claims, e.g. email, name, locale, custom:tenant.">
            <TagsEditor rows={rows} onChange={setRows} keyPlaceholder="Attribute" valuePlaceholder="Value" addLabel="Add attribute" />
          </Field>
          <DialogFooter>
            <Button type="button" variant="outline" onClick={onClose} disabled={pending}>
              Cancel
            </Button>
            <Button type="submit" disabled={pending || !!rErr}>
              {pending && <Loader2 className="animate-spin" />}
              Save
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}

function GroupsDialog({ pool, user, onClose }: { pool: UserPool; user: CognitoUser | null; onClose: () => void }) {
  const [groups, setGroups] = useState<string[]>([])
  const [pending, setPending] = useState(false)
  useEffect(() => {
    if (user) setGroups(user.groups ?? [])
  }, [user])

  const submit = async (e: React.FormEvent) => {
    e.preventDefault()
    if (!user) return
    setPending(true)
    try {
      await api.patch(userPath(pool.id, user.username), { groups })
      toast.success(`Saved group membership of ${user.username}`)
      await revalidate(COGNITO_PATH)
      onClose()
    } catch (err) {
      toast.error(errorMessage(err))
    } finally {
      setPending(false)
    }
  }

  return (
    <Dialog open={!!user} onOpenChange={(o) => !o && !pending && onClose()}>
      <DialogContent className="sm:max-w-md">
        <form onSubmit={submit} className="flex flex-col gap-4">
          <DialogHeader>
            <DialogTitle>Groups of {user?.username}</DialogTitle>
            <DialogDescription>Group names appear in the cognito:groups claim of new tokens.</DialogDescription>
          </DialogHeader>
          <Field label="Groups" help={`${groups.length} of ${pool.groups.length} selected`}>
            <div className="divide-y rounded-md border">
              {pool.groups.map((g) => (
                <label key={g.name} className="hover:bg-accent/50 flex cursor-pointer items-start gap-3 px-3 py-2 text-sm transition-colors">
                  <Checkbox className="mt-0.5" checked={groups.includes(g.name)} onCheckedChange={(c) => setGroups(c ? [...groups, g.name] : groups.filter((x) => x !== g.name))} />
                  <span className="min-w-0">
                    <span className="block truncate font-medium">{g.name}</span>
                    {g.description && <span className="text-muted-foreground block text-xs">{g.description}</span>}
                  </span>
                </label>
              ))}
            </div>
          </Field>
          <DialogFooter>
            <Button type="button" variant="outline" onClick={onClose} disabled={pending}>
              Cancel
            </Button>
            <Button type="submit" disabled={pending}>
              {pending && <Loader2 className="animate-spin" />}
              Save
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}
