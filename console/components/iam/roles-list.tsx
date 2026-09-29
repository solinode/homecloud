"use client"

import Link from "next/link"
import { useEffect, useState } from "react"
import { Plus, Trash2, UserCog } from "lucide-react"
import { toast } from "sonner"

import { Button } from "@/components/ui/button"
import { Checkbox } from "@/components/ui/checkbox"
import { ActionsMenu } from "@/components/console/actions-menu"
import { ConfirmDialog } from "@/components/console/confirm-dialog"
import { DataTable, type Column } from "@/components/console/data-table"
import { EmptyState } from "@/components/console/empty-state"
import { PageHeader } from "@/components/console/page-header"
import { TimeAgo } from "@/components/console/time-ago"
import { api, errorMessage, seg } from "@/lib/api"
import { pluralize } from "@/lib/format"
import { revalidate, useApi } from "@/lib/hooks"
import type { IamRole } from "@/lib/types"

import { IAM, LINK } from "./common"
import { TrustedEntitiesList, createRoleHref, roleHref, trustedEntities } from "./role-common"

const policyCount = (r: IamRole) => r.attached_policies.length + Object.keys(r.inline_policies ?? {}).length

/**
 * DeleteRolesDialog confirms deleting roles. Roles with policies can only be
 * deleted after the policies are removed; "detach policies and delete" does
 * that first (DELETE ?force=true).
 */
export function DeleteRolesDialog({
  open,
  onOpenChange,
  roles,
  onDeleted,
}: {
  open: boolean
  onOpenChange: (o: boolean) => void
  roles: IamRole[]
  onDeleted: () => void
}) {
  const [force, setForce] = useState(true)
  useEffect(() => {
    if (open) setForce(true)
  }, [open])
  const withPolicies = roles.filter((r) => policyCount(r) > 0)
  const single = roles.length === 1 ? roles[0] : null

  return (
    <ConfirmDialog
      open={open}
      onOpenChange={onOpenChange}
      title={single ? `Delete ${single.name}?` : `Delete ${roles.length} roles?`}
      description={
        <div className="flex flex-col gap-2">
          <p>
            Deleting a role revokes its active sessions immediately. Services, functions and instances configured with the role can no longer assume it.
          </p>
          {!single && <p className="text-foreground font-medium break-words">{roles.map((r) => r.name).join(", ")}</p>}
        </div>
      }
      confirmText={single ? single.name : "delete"}
      onConfirm={async () => {
        let n = 0
        try {
          for (const r of roles) {
            await api.del(`${IAM}/roles/${seg(r.name)}`, force ? { force: "true" } : undefined)
            n++
          }
        } catch (e) {
          if (n) onDeleted()
          throw new Error(single ? errorMessage(e) : `${roles[n].name}: ${errorMessage(e)}`)
        }
        toast.success(single ? `Role ${single.name} deleted` : `${pluralize(n, "role")} deleted`)
        onDeleted()
      }}
    >
      {withPolicies.length > 0 && (
        <label className="flex items-start gap-3 rounded-md border p-3">
          <Checkbox checked={force} onCheckedChange={(v) => setForce(v === true)} className="mt-0.5" />
          <span className="flex flex-col gap-0.5">
            <span className="text-sm font-medium">Detach policies and delete</span>
            <span className="text-muted-foreground text-xs">
              {single
                ? `${single.name} has ${pluralize(policyCount(single), "policy", "policies")}.`
                : `${pluralize(withPolicies.length, "selected role")} ${withPolicies.length === 1 ? "has" : "have"} policies.`}{" "}
              Managed policies are detached and inline policies deleted first. Without this, a role with policies cannot be deleted.
            </span>
          </span>
        </label>
      )}
    </ConfirmDialog>
  )
}

export function RolesList() {
  const { data, error, isLoading, isValidating, mutate } = useApi<IamRole[]>(`${IAM}/roles`)
  const [selected, setSelected] = useState<string[]>([])
  const [confirm, setConfirm] = useState(false)
  const sel = (data ?? []).filter((r) => selected.includes(r.name))

  const columns: Column<IamRole>[] = [
    {
      id: "name",
      header: "Role name",
      value: (r) => r.name,
      cell: (r) => (
        <Link href={roleHref(r.name)} className={LINK}>
          {r.name}
        </Link>
      ),
      className: "whitespace-nowrap",
    },
    {
      id: "trusted",
      header: "Trusted entities",
      value: (r) =>
        trustedEntities(r.assume_role_policy)
          .map((e) => `${e.label} ${e.value}`)
          .join(" "),
      cell: (r) => <TrustedEntitiesList role={r} />,
      className: "max-w-72",
    },
    {
      id: "description",
      header: "Description",
      value: (r) => r.description,
      cell: (r) => <span className="text-muted-foreground line-clamp-2 max-w-48">{r.description || "-"}</span>,
      hideBelow: "lg",
    },
    {
      id: "last_used",
      header: "Last activity",
      value: (r) => r.last_used ?? "",
      cell: (r) => (r.last_used ? <TimeAgo value={r.last_used} /> : <span className="text-muted-foreground">Never</span>),
      hideBelow: "sm",
    },
    {
      id: "created",
      header: "Created",
      value: (r) => r.created_at,
      cell: (r) => <TimeAgo value={r.created_at} />,
      hideBelow: "md",
    },
  ]

  const createButton = (
    <Button size="sm" asChild>
      <Link href={createRoleHref()}>
        <Plus /> Create role
      </Link>
    </Button>
  )

  return (
    <div className="flex flex-col gap-4">
      <PageHeader
        title="Roles"
        description="A role is an identity with permissions that trusted entities - HomeCloud services such as Lambda or EC2, or users in this account - assume to get temporary credentials."
        breadcrumbs={[{ label: "IAM", href: "/iam/" }, { label: "Roles" }]}
      />
      <DataTable
        title="Roles"
        data={data}
        columns={columns}
        rowId={(r) => r.name}
        loading={isLoading}
        error={error}
        onRetry={() => mutate()}
        onRefresh={() => mutate()}
        refreshing={isValidating}
        selection="multi"
        selected={selected}
        onSelectedChange={setSelected}
        searchPlaceholder="Search roles by name or trusted entity"
        defaultSort={{ id: "name" }}
        actions={
          <>
            <ActionsMenu disabled={!selected.length} items={[{ label: "Delete", icon: <Trash2 />, destructive: true, onSelect: () => setConfirm(true) }]} />
            {createButton}
          </>
        }
        empty={
          <EmptyState
            icon={UserCog}
            title="No roles"
            description="Create a role for each HomeCloud service or workload that needs to call other services, instead of embedding access keys."
            action={createButton}
          />
        }
      />
      <DeleteRolesDialog
        open={confirm}
        onOpenChange={setConfirm}
        roles={sel}
        onDeleted={() => {
          setSelected([])
          revalidate(IAM)
        }}
      />
    </div>
  )
}
