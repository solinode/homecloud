"use client"

import Link from "next/link"
import { useEffect, useMemo, useState } from "react"
import { Plus, Trash2, Users } from "lucide-react"
import { toast } from "sonner"

import { Button } from "@/components/ui/button"
import { ActionsMenu } from "@/components/console/actions-menu"
import { ConfirmDialog } from "@/components/console/confirm-dialog"
import { DataTable, type Column } from "@/components/console/data-table"
import { EmptyState } from "@/components/console/empty-state"
import { PageHeader } from "@/components/console/page-header"
import { StatusBadge } from "@/components/console/status-badge"
import { TimeAgo } from "@/components/console/time-ago"
import { api, seg } from "@/lib/api"
import { revalidate, useApi, useQueryParam, useSetQueryParam } from "@/lib/hooks"
import type { AccessKey, IamUser } from "@/lib/types"

import { CreateUserDialog } from "./create-user-dialog"
import { IAM, LINK, groupHref, useAccessKeysByUser, userHref } from "./common"

function KeysCell({ keys }: { keys: AccessKey[] | undefined }) {
  if (!keys) return <span className="text-muted-foreground">-</span>
  if (!keys.length) return <span className="text-muted-foreground">None</span>
  const active = keys.filter((k) => k.status === "Active").length
  return (
    <span className="flex items-center gap-2">
      <StatusBadge status={active ? "active" : "inactive"} label={active ? `${active} active` : "Inactive"} />
      {keys.length > active && active > 0 && <span className="text-muted-foreground text-xs">{keys.length - active} inactive</span>}
    </span>
  )
}

export function UsersList() {
  const { data, error, isLoading, isValidating, mutate } = useApi<IamUser[]>(`${IAM}/users`)
  const keys = useAccessKeysByUser(data?.map((u) => u.name))
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

  const rows = useMemo(() => data ?? [], [data])
  const sel = rows.filter((u) => selected.includes(u.name))
  const deletable = sel.filter((u) => !u.root)

  const columns: Column<IamUser>[] = [
    {
      id: "name",
      header: "User name",
      value: (u) => u.name,
      cell: (u) => (
        <span className="flex items-center gap-2">
          <Link href={userHref(u.name)} className={LINK}>
            {u.name}
          </Link>
          {u.root && <StatusBadge status="root" tone="warning" label="Root" />}
        </span>
      ),
    },
    {
      id: "groups",
      header: "Groups",
      value: (u) => u.groups.join(" "),
      cell: (u) =>
        u.groups.length ? (
          <span className="flex flex-wrap gap-x-1">
            {u.groups.map((g, i) => (
              <span key={g}>
                <Link href={groupHref(g)} className="text-primary hover:underline">
                  {g}
                </Link>
                {i < u.groups.length - 1 && ","}
              </span>
            ))}
          </span>
        ) : (
          <span className="text-muted-foreground">None</span>
        ),
      hideBelow: "md",
    },
    {
      id: "console",
      header: "Console access",
      value: (u) => (u.console_access ? "Enabled" : "Disabled"),
      cell: (u) => <StatusBadge status={u.console_access ? "enabled" : "disabled"} label={u.console_access ? "Enabled" : "Disabled"} />,
    },
    {
      id: "last_login",
      header: "Last console sign-in",
      value: (u) => u.last_login ?? "",
      cell: (u) => (u.last_login ? <TimeAgo value={u.last_login} /> : <span className="text-muted-foreground">Never</span>),
      hideBelow: "sm",
    },
    {
      id: "keys",
      header: "Access keys",
      value: (u) => (keys.data?.[u.name] ?? []).filter((k) => k.status === "Active").length,
      cell: (u) => <KeysCell keys={keys.data?.[u.name]} />,
      hideBelow: "md",
    },
    {
      id: "policies",
      header: "Direct policies",
      value: (u) => u.attached_policies.length + Object.keys(u.inline_policies ?? {}).length,
      cell: (u) => u.attached_policies.length + Object.keys(u.inline_policies ?? {}).length,
      hideBelow: "lg",
    },
    {
      id: "created",
      header: "Created",
      value: (u) => u.created_at,
      cell: (u) => <TimeAgo value={u.created_at} />,
      hideBelow: "lg",
    },
  ]

  const doDelete = async () => {
    for (const u of deletable) {
      await api.del(`${IAM}/users/${seg(u.name)}`)
    }
    toast.success(deletable.length === 1 ? `User ${deletable[0].name} deleted` : `${deletable.length} users deleted`)
    setSelected([])
    revalidate(IAM)
  }

  return (
    <div className="flex flex-col gap-4">
      <PageHeader
        title="Users"
        description="An IAM user is an identity with long-term credentials used to interact with HomeCloud in the console or through the API."
        breadcrumbs={[{ label: "IAM", href: "/iam/" }, { label: "Users" }]}
      />
      <DataTable
        title="Users"
        data={data}
        columns={columns}
        rowId={(u) => u.name}
        loading={isLoading}
        error={error}
        onRetry={() => mutate()}
        onRefresh={() => {
          mutate()
          keys.mutate()
        }}
        refreshing={isValidating || keys.isValidating}
        selection="multi"
        selected={selected}
        onSelectedChange={setSelected}
        searchPlaceholder="Search users"
        defaultSort={{ id: "name" }}
        actions={
          <>
            <ActionsMenu
              disabled={!selected.length}
              items={[
                {
                  label: "Delete",
                  icon: <Trash2 />,
                  destructive: true,
                  disabled: !deletable.length,
                  hint: sel.some((u) => u.root) ? "The root user cannot be deleted" : undefined,
                  onSelect: () => setConfirm(true),
                },
              ]}
            />
            <Button size="sm" onClick={() => openCreate(true)}>
              <Plus /> Create user
            </Button>
          </>
        }
        empty={
          <EmptyState
            icon={Users}
            title="No users"
            description="Create an IAM user for each person or application that needs access."
            action={
              <Button size="sm" onClick={() => openCreate(true)}>
                <Plus /> Create user
              </Button>
            }
          />
        }
      />

      <ConfirmDialog
        open={confirm}
        onOpenChange={setConfirm}
        title={deletable.length === 1 ? `Delete ${deletable[0]?.name}?` : `Delete ${deletable.length} users?`}
        description={
          <div className="flex flex-col gap-2">
            <p>
              Deleting a user permanently removes its console password, access keys and inline policies. Applications using its access keys stop working immediately.
            </p>
            {deletable.length > 1 && <p className="font-medium">{deletable.map((u) => u.name).join(", ")}</p>}
            {sel.some((u) => u.root) && <p>The root user is selected and will not be deleted.</p>}
          </div>
        }
        confirmText={deletable.length === 1 ? deletable[0]?.name : "delete"}
        onConfirm={doDelete}
      />

      <CreateUserDialog open={createOpen} onOpenChange={openCreate} existing={rows.map((u) => u.name)} />
    </div>
  )
}
