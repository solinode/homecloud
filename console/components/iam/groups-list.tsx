"use client"

import Link from "next/link"
import { useEffect, useState } from "react"
import { Loader2, Plus, Trash2, UsersRound } from "lucide-react"
import { toast } from "sonner"

import { Button } from "@/components/ui/button"
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog"
import { Input } from "@/components/ui/input"
import { ActionsMenu } from "@/components/console/actions-menu"
import { ConfirmDialog } from "@/components/console/confirm-dialog"
import { DataTable, type Column } from "@/components/console/data-table"
import { EmptyState } from "@/components/console/empty-state"
import { Field } from "@/components/console/form-field"
import { PageHeader } from "@/components/console/page-header"
import { TimeAgo } from "@/components/console/time-ago"
import { api, errorMessage, seg } from "@/lib/api"
import { pluralize } from "@/lib/format"
import { revalidate, useApi, useQueryParam, useSetQueryParam } from "@/lib/hooks"
import type { IamGroup, PolicySummary } from "@/lib/types"

import { IAM, LINK, PolicyPicker, groupHref, nameError } from "./common"

export function GroupsList() {
  const { data, error, isLoading, isValidating, mutate } = useApi<IamGroup[]>(`${IAM}/groups`)
  const [selected, setSelected] = useState<string[]>([])
  const [confirm, setConfirm] = useState(false)
  const [createOpen, setCreateOpen] = useState(false)
  const createParam = useQueryParam("create")
  const setParam = useSetQueryParam()

  useEffect(() => {
    if (createParam === "1") setCreateOpen(true)
  }, [createParam])
  const openCreate = (o: boolean) => {
    setCreateOpen(o)
    if (!o && createParam) setParam("create", null)
  }

  const sel = (data ?? []).filter((g) => selected.includes(g.name))

  const columns: Column<IamGroup>[] = [
    {
      id: "name",
      header: "Group name",
      value: (g) => g.name,
      cell: (g) => (
        <Link href={groupHref(g.name)} className={LINK}>
          {g.name}
        </Link>
      ),
    },
    { id: "users", header: "Users", value: (g) => g.members.length, cell: (g) => g.members.length },
    {
      id: "perms",
      header: "Permissions",
      value: (g) => g.attached_policies.length,
      cell: (g) => (g.attached_policies.length ? pluralize(g.attached_policies.length, "policy", "policies") : <span className="text-muted-foreground">Not defined</span>),
    },
    { id: "created", header: "Created", value: (g) => g.created_at, cell: (g) => <TimeAgo value={g.created_at} />, hideBelow: "sm" },
  ]

  return (
    <div className="flex flex-col gap-4">
      <PageHeader
        title="User groups"
        description="A user group is a collection of IAM users. Use groups to specify permissions for a collection of users."
        breadcrumbs={[{ label: "IAM", href: "/iam/" }, { label: "User groups" }]}
      />
      <DataTable
        title="User groups"
        data={data}
        columns={columns}
        rowId={(g) => g.name}
        loading={isLoading}
        error={error}
        onRetry={() => mutate()}
        onRefresh={() => mutate()}
        refreshing={isValidating}
        selection="multi"
        selected={selected}
        onSelectedChange={setSelected}
        searchPlaceholder="Search groups"
        defaultSort={{ id: "name" }}
        actions={
          <>
            <ActionsMenu disabled={!selected.length} items={[{ label: "Delete", icon: <Trash2 />, destructive: true, onSelect: () => setConfirm(true) }]} />
            <Button size="sm" onClick={() => openCreate(true)}>
              <Plus /> Create group
            </Button>
          </>
        }
        empty={
          <EmptyState
            icon={UsersRound}
            title="No user groups"
            description="Create a group, attach policies to it and add users who need the same permissions."
            action={
              <Button size="sm" onClick={() => openCreate(true)}>
                <Plus /> Create group
              </Button>
            }
          />
        }
      />
      <ConfirmDialog
        open={confirm}
        onOpenChange={setConfirm}
        title={sel.length === 1 ? `Delete ${sel[0].name}?` : `Delete ${sel.length} groups?`}
        description={
          <div className="flex flex-col gap-2">
            <p>Members are removed from the group and lose its permissions. The users and policies themselves are not deleted.</p>
            {sel.length > 1 && <p className="font-medium">{sel.map((g) => g.name).join(", ")}</p>}
          </div>
        }
        confirmText={sel.length === 1 ? sel[0].name : "delete"}
        onConfirm={async () => {
          for (const g of sel) await api.del(`${IAM}/groups/${seg(g.name)}`)
          toast.success(sel.length === 1 ? `Group ${sel[0].name} deleted` : `${sel.length} groups deleted`)
          setSelected([])
          revalidate(IAM)
        }}
      />
      <CreateGroupDialog open={createOpen} onOpenChange={openCreate} existing={(data ?? []).map((g) => g.name)} />
    </div>
  )
}

function CreateGroupDialog({ open, onOpenChange, existing }: { open: boolean; onOpenChange: (o: boolean) => void; existing: string[] }) {
  const policies = useApi<PolicySummary[]>(open ? `${IAM}/policies` : null)
  const [name, setName] = useState("")
  const [selected, setSelected] = useState<string[]>([])
  const [touched, setTouched] = useState(false)
  const [pending, setPending] = useState(false)
  useEffect(() => {
    if (open) {
      setName("")
      setSelected([])
      setTouched(false)
    }
  }, [open])
  const err = nameError("group", name) ?? (existing.includes(name) ? `A group named ${name} already exists.` : null)

  const submit = async (e: React.FormEvent) => {
    e.preventDefault()
    setTouched(true)
    if (err) return
    setPending(true)
    try {
      await api.post(`${IAM}/groups`, { name, policies: selected })
      toast.success(`Group ${name} created`)
      revalidate(IAM)
      onOpenChange(false)
    } catch (e) {
      toast.error(errorMessage(e))
    } finally {
      setPending(false)
    }
  }

  return (
    <Dialog open={open} onOpenChange={(o) => !pending && onOpenChange(o)}>
      <DialogContent className="max-h-[92vh] overflow-y-auto sm:max-w-2xl">
        <form onSubmit={submit} className="flex flex-col gap-4">
          <DialogHeader>
            <DialogTitle>Create user group</DialogTitle>
            <DialogDescription>Attach policies now; you can add users from the group&apos;s page.</DialogDescription>
          </DialogHeader>
          <Field label="User group name" htmlFor="group-name" error={touched ? err : null} help="Up to 64 characters: letters, digits and + = , . @ _ -">
            <Input id="group-name" autoFocus autoComplete="off" value={name} onChange={(e) => setName(e.target.value.trim())} placeholder="e.g. developers" aria-invalid={touched && !!err} />
          </Field>
          <Field label="Attach permissions policies" optional>
            <PolicyPicker policies={policies.data} loading={policies.isLoading} selected={selected} onChange={setSelected} maxHeight="max-h-64" />
          </Field>
          <DialogFooter>
            <Button type="button" variant="outline" onClick={() => onOpenChange(false)} disabled={pending}>
              Cancel
            </Button>
            <Button type="submit" disabled={pending}>
              {pending && <Loader2 className="animate-spin" />} Create group
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}
