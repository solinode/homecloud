"use client"

import { useEffect, useState } from "react"
import { Loader2, Plus, Trash2, Users } from "lucide-react"
import { toast } from "sonner"

import { Button } from "@/components/ui/button"
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog"
import { Input } from "@/components/ui/input"
import { ConfirmDialog } from "@/components/console/confirm-dialog"
import { CellText, DataTable, type Column } from "@/components/console/data-table"
import { EmptyState } from "@/components/console/empty-state"
import { Field } from "@/components/console/form-field"
import { api, errorMessage, seg } from "@/lib/api"
import { pluralize } from "@/lib/format"
import { revalidate, useApi } from "@/lib/hooks"
import type { CognitoUser, UserPool, UserPoolGroup } from "@/lib/types"

import { COGNITO_PATH, NAME_RE, poolPath } from "./shared"

export function GroupsTab({ pool, onChanged }: { pool: UserPool; onChanged: () => void }) {
  const users = useApi<CognitoUser[]>(`${poolPath(pool.id)}/users`)
  const [creating, setCreating] = useState(false)
  const [deleting, setDeleting] = useState<UserPoolGroup | null>(null)
  const members = (g: string) => (users.data ?? []).filter((u) => u.groups?.includes(g)).length

  const columns: Column<UserPoolGroup>[] = [
    {
      id: "name",
      header: "Group name",
      cell: (g) => (
        <CellText max="16rem" className="font-medium">
          {g.name}
        </CellText>
      ),
      value: (g) => g.name,
    },
    { id: "desc", header: "Description", cell: (g) => <CellText muted>{g.description}</CellText>, value: (g) => g.description, hideBelow: "md" },
    { id: "precedence", header: "Precedence", cell: (g) => <span className="tabular-nums">{g.precedence}</span>, value: (g) => g.precedence, hideBelow: "sm" },
    { id: "members", header: "Members", cell: (g) => (users.data ? <span className="tabular-nums">{members(g.name)}</span> : "..."), value: (g) => members(g.name) },
    {
      id: "actions",
      header: "",
      className: "w-12 text-right",
      cell: (g) => (
        <Button variant="ghost" size="icon" className="text-destructive hover:text-destructive size-8" onClick={() => setDeleting(g)} aria-label={`Delete group ${g.name}`}>
          <Trash2 />
        </Button>
      ),
    },
  ]

  return (
    <>
      <DataTable
        title="Groups"
        description="Groups are included in tokens as the cognito:groups claim, so your code can authorize by role."
        data={pool.groups}
        columns={columns}
        rowId={(g) => g.name}
        noSearch={pool.groups.length < 8}
        defaultSort={{ id: "precedence" }}
        actions={
          <Button size="sm" onClick={() => setCreating(true)}>
            <Plus /> Create group
          </Button>
        }
        empty={
          <EmptyState
            icon={Users}
            title="No groups"
            description="Create groups such as admins or customers and add users to them."
            action={
              <Button size="sm" onClick={() => setCreating(true)}>
                <Plus /> Create group
              </Button>
            }
          />
        }
      />
      <CreateGroupDialog pool={pool} open={creating} onOpenChange={setCreating} onCreated={onChanged} />
      <ConfirmDialog
        open={!!deleting}
        onOpenChange={(o) => !o && setDeleting(null)}
        title={`Delete group ${deleting?.name ?? ""}?`}
        description={
          deleting && (
            <p>
              {members(deleting.name) ? `${pluralize(members(deleting.name), "user")} will be removed from the group. ` : ""}Users themselves are not deleted. Tokens
              issued before the change still carry the group until they expire.
            </p>
          )
        }
        onConfirm={async () => {
          if (!deleting) return
          await api.del(`${poolPath(pool.id)}/groups/${seg(deleting.name)}`)
          toast.success(`Deleted group ${deleting.name}`)
          await revalidate(COGNITO_PATH)
          onChanged()
        }}
      />
    </>
  )
}

function CreateGroupDialog({ pool, open, onOpenChange, onCreated }: { pool: UserPool; open: boolean; onOpenChange: (o: boolean) => void; onCreated: () => void }) {
  const [name, setName] = useState("")
  const [description, setDescription] = useState("")
  const [precedence, setPrecedence] = useState("0")
  const [touched, setTouched] = useState(false)
  const [pending, setPending] = useState(false)
  useEffect(() => {
    if (!open) return
    setName("")
    setDescription("")
    setPrecedence("0")
    setTouched(false)
  }, [open])

  const nameErr = !name.trim()
    ? "Enter a group name"
    : !NAME_RE.test(name.trim())
      ? "1-128 letters, digits, spaces and + = , . @ _ -"
      : pool.groups.some((g) => g.name === name.trim())
        ? "A group with this name already exists"
        : undefined
  const precErr = /^\d+$/.test(precedence) ? undefined : "A whole number, 0 or more"

  const submit = async (e: React.FormEvent) => {
    e.preventDefault()
    setTouched(true)
    if (nameErr || precErr) return
    setPending(true)
    try {
      await api.post(`${poolPath(pool.id)}/groups`, { name: name.trim(), description: description.trim(), precedence: Number(precedence) })
      toast.success(`Created group ${name.trim()}`)
      await revalidate(COGNITO_PATH)
      onCreated()
      onOpenChange(false)
    } catch (err) {
      toast.error(errorMessage(err))
    } finally {
      setPending(false)
    }
  }

  return (
    <Dialog open={open} onOpenChange={(o) => !pending && onOpenChange(o)}>
      <DialogContent className="sm:max-w-md">
        <form onSubmit={submit} className="flex flex-col gap-4">
          <DialogHeader>
            <DialogTitle>Create group</DialogTitle>
            <DialogDescription>Add users to the group from the Users tab.</DialogDescription>
          </DialogHeader>
          <Field label="Group name" htmlFor="grp-name" error={touched ? nameErr : undefined}>
            <Input id="grp-name" autoFocus value={name} onChange={(e) => setName(e.target.value)} placeholder="admins" autoComplete="off" />
          </Field>
          <Field label="Description" htmlFor="grp-desc" optional>
            <Input id="grp-desc" value={description} onChange={(e) => setDescription(e.target.value)} />
          </Field>
          <Field label="Precedence" htmlFor="grp-prec" error={touched ? precErr : undefined} help="Lower values take priority when your application picks one group.">
            <Input id="grp-prec" inputMode="numeric" value={precedence} onChange={(e) => setPrecedence(e.target.value)} className="w-28" />
          </Field>
          <DialogFooter>
            <Button type="button" variant="outline" onClick={() => onOpenChange(false)} disabled={pending}>
              Cancel
            </Button>
            <Button type="submit" disabled={pending}>
              {pending && <Loader2 className="animate-spin" />}
              Create group
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}
