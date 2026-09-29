"use client"

import Link from "next/link"
import { useRouter } from "next/navigation"
import { useMemo, useState } from "react"
import { Plus, ShieldCheck, Trash2, UsersRound } from "lucide-react"
import { toast } from "sonner"

import { Button } from "@/components/ui/button"
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs"
import { ActionsMenu } from "@/components/console/actions-menu"
import { ConfirmDialog } from "@/components/console/confirm-dialog"
import { CopyableText } from "@/components/console/copy-button"
import { DataTable, type Column } from "@/components/console/data-table"
import { EmptyState } from "@/components/console/empty-state"
import { ErrorState } from "@/components/console/error-state"
import { KeyValueGrid } from "@/components/console/key-value"
import { DetailSkeleton } from "@/components/console/loading"
import { PageHeader } from "@/components/console/page-header"
import { Section } from "@/components/console/section"
import { StatusBadge } from "@/components/console/status-badge"
import { TimeAgo } from "@/components/console/time-ago"
import { api, seg } from "@/lib/api"
import { formatDate, pluralize } from "@/lib/format"
import { revalidate, useApi, useQueryParam, useSetQueryParam } from "@/lib/hooks"
import type { IamGroup, IamUser, PolicySummary } from "@/lib/types"

import { AttachPoliciesDialog, PickDialog, runEach } from "./dialogs"
import { IAM, LINK, PolicyTypeBadge, groupHref, policyHref } from "./common"
import { PermissionsBoundarySection } from "./boundary"
import { InlinePoliciesSection } from "./inline-policies-section"
import { TagsSection } from "./tags-section"
import { UserCredentials } from "./user-credentials"

interface PermRow {
  id: string
  policy: string
  managed: boolean | undefined
  description: string
  via: string | null // null = directly
}

const TABS = ["permissions", "groups", "credentials", "tags"] as const

export function UserDetail() {
  const name = useQueryParam("name")
  const tabParam = useQueryParam("tab")
  const setParam = useSetQueryParam()
  const router = useRouter()
  const tab = (TABS as readonly string[]).includes(tabParam) ? tabParam : "permissions"

  const path = name ? `${IAM}/users/${seg(name)}` : null
  const { data: user, error, isLoading, mutate } = useApi<IamUser>(path)
  const groupsQ = useApi<IamGroup[]>(name ? `${IAM}/groups` : null)
  const policiesQ = useApi<PolicySummary[]>(name ? `${IAM}/policies` : null)
  const [confirmDelete, setConfirmDelete] = useState(false)

  const refresh = () => {
    mutate()
    revalidate(IAM)
  }

  const crumbs = [{ label: "IAM", href: "/iam/" }, { label: "Users", href: "/iam/users/" }, { label: name || "User" }]

  if (!name) return <ErrorState error={new Error("No user name given. Open a user from the Users list.")} />
  if (isLoading && !user)
    return (
      <div className="flex flex-col gap-4">
        <PageHeader title={name} breadcrumbs={crumbs} />
        <DetailSkeleton />
      </div>
    )
  if (error || !user)
    return (
      <div className="flex flex-col gap-4">
        <PageHeader title={name} breadcrumbs={crumbs} />
        <ErrorState error={error ?? new Error("User not found")} onRetry={() => mutate()} />
      </div>
    )

  const keys = user.access_keys ?? []
  const activeKeys = keys.filter((k) => k.status === "Active").length

  return (
    <div className="flex flex-col gap-4">
      <PageHeader
        title={user.name}
        breadcrumbs={crumbs}
        badge={user.root ? <StatusBadge status="root" tone="warning" label="Root user" /> : undefined}
        actions={
          !user.root && (
            <Button variant="outline" size="sm" onClick={() => setConfirmDelete(true)}>
              <Trash2 /> Delete
            </Button>
          )
        }
      />

      <Section title="Summary">
        <KeyValueGrid
          columns={3}
          items={[
            { label: "ARN", value: <CopyableText value={user.arn} className="text-[13px]" />, wide: true },
            { label: "Created", value: formatDate(user.created_at) },
            { label: "User ID", value: <CopyableText value={user.id} className="text-[13px]" /> },
            {
              label: "Console access",
              value: <StatusBadge status={user.console_access ? "enabled" : "disabled"} label={user.console_access ? "Enabled" : "Disabled"} />,
            },
            { label: "Last console sign-in", value: user.last_login ? <TimeAgo value={user.last_login} /> : "Never" },
            { label: "Password set", value: user.password_set_at ? <TimeAgo value={user.password_set_at} /> : "Not set" },
            {
              label: "Access keys",
              value: keys.length ? `${pluralize(keys.length, "key")} (${activeKeys} active)` : "None",
            },
          ]}
        />
      </Section>

      <Tabs value={tab} onValueChange={(v) => setParam("tab", v === "permissions" ? null : v)}>
        <TabsList>
          <TabsTrigger value="permissions">Permissions</TabsTrigger>
          <TabsTrigger value="groups">Groups ({user.groups.length})</TabsTrigger>
          <TabsTrigger value="credentials">Security credentials</TabsTrigger>
          <TabsTrigger value="tags">Tags</TabsTrigger>
        </TabsList>
        <TabsContent value="permissions">
          <PermissionsTab user={user} groups={groupsQ.data} policies={policiesQ.data} loading={groupsQ.isLoading || policiesQ.isLoading} onChanged={refresh} />
        </TabsContent>
        <TabsContent value="groups">
          <GroupsTab user={user} groups={groupsQ.data} loading={groupsQ.isLoading} error={groupsQ.error} onChanged={refresh} />
        </TabsContent>
        <TabsContent value="credentials">
          <UserCredentials user={user} onChanged={() => mutate()} />
        </TabsContent>
        <TabsContent value="tags">
          <TagsSection
            tags={user.tags}
            noun="users"
            onSave={async (tags) => {
              await api.put(`${IAM}/users/${seg(user.name)}/tags`, { tags })
              refresh()
            }}
          />
        </TabsContent>
      </Tabs>

      <ConfirmDialog
        open={confirmDelete}
        onOpenChange={setConfirmDelete}
        title={`Delete ${user.name}?`}
        description="Deleting the user permanently removes its console password, access keys, inline policies and group memberships. Applications using its access keys stop working immediately."
        confirmText={user.name}
        onConfirm={async () => {
          await api.del(`${IAM}/users/${seg(user.name)}`)
          toast.success(`User ${user.name} deleted`)
          revalidate(IAM)
          router.push("/iam/users/")
        }}
      />
    </div>
  )
}

function PermissionsTab({
  user,
  groups,
  policies,
  loading,
  onChanged,
}: {
  user: IamUser
  groups: IamGroup[] | undefined
  policies: PolicySummary[] | undefined
  loading: boolean
  onChanged: () => void
}) {
  const [selected, setSelected] = useState<string[]>([])
  const [attachOpen, setAttachOpen] = useState(false)
  const [confirmDetach, setConfirmDetach] = useState(false)
  const base = `${IAM}/users/${seg(user.name)}`

  const rows = useMemo<PermRow[]>(() => {
    const byName = new Map((policies ?? []).map((p) => [p.name, p]))
    const out: PermRow[] = user.attached_policies.map((p) => ({ id: `direct:${p}`, policy: p, managed: byName.get(p)?.managed, description: byName.get(p)?.description ?? "", via: null }))
    for (const g of groups ?? []) {
      if (!user.groups.includes(g.name)) continue
      for (const p of g.attached_policies)
        out.push({ id: `group:${g.name}:${p}`, policy: p, managed: byName.get(p)?.managed, description: byName.get(p)?.description ?? "", via: g.name })
    }
    return out
  }, [user, groups, policies])

  const sel = rows.filter((r) => selected.includes(r.id))
  const detachable = sel.filter((r) => r.via === null && !(user.root && r.policy === "AdministratorAccess"))

  const columns: Column<PermRow>[] = [
    {
      id: "policy",
      header: "Policy name",
      value: (r) => r.policy,
      cell: (r) => (
        <Link href={policyHref(r.policy)} className={LINK}>
          {r.policy}
        </Link>
      ),
    },
    { id: "type", header: "Type", value: (r) => (r.managed ? "AWS managed" : "Customer managed"), cell: (r) => (r.managed === undefined ? "-" : <PolicyTypeBadge managed={r.managed} />) },
    {
      id: "via",
      header: "Attached via",
      value: (r) => r.via ?? "Directly",
      cell: (r) =>
        r.via ? (
          <span>
            Group:{" "}
            <Link href={groupHref(r.via)} className="text-primary hover:underline">
              {r.via}
            </Link>
          </span>
        ) : (
          "Directly"
        ),
    },
    { id: "desc", header: "Description", value: (r) => r.description, cell: (r) => <span className="text-muted-foreground">{r.description || "-"}</span>, hideBelow: "lg" },
  ]

  return (
    <div className="flex flex-col gap-6">
      <DataTable
        title="Permissions policies"
        description="Managed policies attached to this user directly or through its groups."
        data={loading ? undefined : rows}
        loading={loading}
        columns={columns}
        rowId={(r) => r.id}
        selection="multi"
        selected={selected}
        onSelectedChange={setSelected}
        searchPlaceholder="Filter policies"
        actions={
          <>
            <ActionsMenu
              disabled={!selected.length}
              items={[
                {
                  label: "Remove",
                  icon: <Trash2 />,
                  destructive: true,
                  disabled: !detachable.length,
                  hint: "Only policies attached directly can be removed here. AdministratorAccess cannot be removed from root.",
                  onSelect: () => setConfirmDetach(true),
                },
              ]}
            />
            <Button size="sm" onClick={() => setAttachOpen(true)}>
              <Plus /> Add permissions
            </Button>
          </>
        }
        empty={
          <EmptyState
            icon={ShieldCheck}
            title="No permissions policies"
            description="This user cannot do anything until you attach a policy or add it to a group."
            action={
              <Button size="sm" onClick={() => setAttachOpen(true)}>
                <Plus /> Add permissions
              </Button>
            }
          />
        }
      />

      <InlinePoliciesSection kind="user" owner={user.name} policies={user.inline_policies} onChanged={onChanged} />

      <PermissionsBoundarySection
        kind="user"
        arn={user.permissions_boundary}
        onSet={async (arn) => {
          const p = `${IAM}/users/${seg(user.name)}/permissions-boundary`
          await (arn ? api.put(p, { policy: arn }) : api.del(p))
          onChanged()
        }}
      />

      <AttachPoliciesDialog
        open={attachOpen}
        onOpenChange={setAttachOpen}
        title={`Add permissions to ${user.name}`}
        description="Attach managed policies directly to the user. To give several users the same permissions, add them to a group instead."
        exclude={user.attached_policies}
        onAttach={async (names) => {
          const n = await runEach(names, (p) => api.post(`${base}/policies`, { policy: p }))
          if (n) toast.success(`${pluralize(n, "policy", "policies")} attached to ${user.name}`)
          onChanged()
          return n === names.length
        }}
      />

      <ConfirmDialog
        open={confirmDetach}
        onOpenChange={setConfirmDetach}
        title={detachable.length === 1 ? `Remove ${detachable[0]?.policy}?` : `Remove ${detachable.length} policies?`}
        description={
          <div className="flex flex-col gap-2">
            <p>
              The user loses the permissions granted by {detachable.map((r) => r.policy).join(", ")}. The policies themselves are not deleted.
            </p>
            {sel.length > detachable.length && <p>Policies attached through groups (or root&apos;s AdministratorAccess) are skipped.</p>}
          </div>
        }
        actionLabel="Remove"
        onConfirm={async () => {
          for (const r of detachable) await api.del(`${base}/policies/${seg(r.policy)}`)
          toast.success(`${pluralize(detachable.length, "policy", "policies")} removed from ${user.name}`)
          setSelected([])
          onChanged()
        }}
      />


    </div>
  )
}

function GroupsTab({
  user,
  groups,
  loading,
  error,
  onChanged,
}: {
  user: IamUser
  groups: IamGroup[] | undefined
  loading: boolean
  error: unknown
  onChanged: () => void
}) {
  const [selected, setSelected] = useState<string[]>([])
  const [addOpen, setAddOpen] = useState(false)
  const [confirm, setConfirm] = useState(false)
  const mine = useMemo(() => (groups ?? []).filter((g) => user.groups.includes(g.name)), [groups, user.groups])
  const others = (groups ?? []).filter((g) => !user.groups.includes(g.name))

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
    {
      id: "policies",
      header: "Attached policies",
      value: (g) => g.attached_policies.length,
      cell: (g) => (g.attached_policies.length ? g.attached_policies.join(", ") : <span className="text-muted-foreground">None</span>),
    },
    { id: "users", header: "Users", value: (g) => g.members.length, cell: (g) => g.members.length, hideBelow: "sm" },
  ]

  return (
    <>
      <DataTable
        title="User groups membership"
        description="The user gets the permissions of every group it belongs to."
        data={loading ? undefined : mine}
        loading={loading}
        error={error}
        columns={columns}
        rowId={(g) => g.name}
        selection="multi"
        selected={selected}
        onSelectedChange={setSelected}
        searchPlaceholder="Filter groups"
        actions={
          <>
            <Button size="sm" variant="outline" disabled={!selected.length} onClick={() => setConfirm(true)}>
              Remove from group
            </Button>
            <Button size="sm" onClick={() => setAddOpen(true)}>
              <Plus /> Add user to groups
            </Button>
          </>
        }
        empty={
          <EmptyState
            icon={UsersRound}
            title="Not a member of any group"
            description="Add the user to groups to grant permissions shared by a team."
            action={
              <Button size="sm" onClick={() => setAddOpen(true)}>
                <Plus /> Add user to groups
              </Button>
            }
          />
        }
      />
      <PickDialog
        open={addOpen}
        onOpenChange={setAddOpen}
        title={`Add ${user.name} to groups`}
        items={others.map((g) => ({ id: g.name, label: g.name, sub: `${pluralize(g.attached_policies.length, "policy", "policies")}, ${pluralize(g.members.length, "user")}` }))}
        empty={
          <>
            {groups?.length ? "The user already belongs to every group. " : "No groups exist yet. "}
            <Link href="/iam/groups/?create=1" className="text-primary hover:underline">
              Create a group
            </Link>
          </>
        }
        actionLabel="Add to groups"
        onSubmit={async (ids) => {
          const n = await runEach(ids, (g) => api.post(`${IAM}/groups/${seg(g)}/members`, { user: user.name }))
          if (n) toast.success(`${user.name} added to ${pluralize(n, "group")}`)
          onChanged()
          return n === ids.length
        }}
      />
      <ConfirmDialog
        open={confirm}
        onOpenChange={setConfirm}
        title={`Remove ${user.name} from ${pluralize(selected.length, "group")}?`}
        description={<>The user loses the permissions of {selected.join(", ")}.</>}
        actionLabel="Remove"
        onConfirm={async () => {
          for (const g of selected) await api.del(`${IAM}/groups/${seg(g)}/members/${seg(user.name)}`)
          toast.success(`${user.name} removed from ${pluralize(selected.length, "group")}`)
          setSelected([])
          onChanged()
        }}
      />
    </>
  )
}
