"use client"

import Link from "next/link"
import { useRouter } from "next/navigation"
import { useMemo, useState } from "react"
import { Plus, ShieldCheck, Trash2, Users } from "lucide-react"
import { toast } from "sonner"

import { Button } from "@/components/ui/button"
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs"
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
import { IAM, LINK, PolicyTypeBadge, policyHref, userHref } from "./common"
import { InlinePoliciesSection } from "./inline-policies-section"

export function GroupDetail() {
  const name = useQueryParam("name")
  const tabParam = useQueryParam("tab")
  const setParam = useSetQueryParam()
  const router = useRouter()
  const tab = tabParam === "permissions" ? "permissions" : "users"

  const { data: group, error, isLoading, mutate } = useApi<IamGroup>(name ? `${IAM}/groups/${seg(name)}` : null)
  const usersQ = useApi<IamUser[]>(name ? `${IAM}/users` : null)
  const policiesQ = useApi<PolicySummary[]>(name ? `${IAM}/policies` : null)
  const [confirmDelete, setConfirmDelete] = useState(false)
  const [memberSel, setMemberSel] = useState<string[]>([])
  const [policySel, setPolicySel] = useState<string[]>([])
  const [addUsers, setAddUsers] = useState(false)
  const [attach, setAttach] = useState(false)
  const [confirmRemove, setConfirmRemove] = useState(false)
  const [confirmDetach, setConfirmDetach] = useState(false)

  const members = useMemo(() => {
    const byName = new Map((usersQ.data ?? []).map((u) => [u.name, u]))
    return (group?.members ?? []).map((m) => byName.get(m) ?? ({ name: m } as IamUser))
  }, [group, usersQ.data])
  const attached = useMemo(() => {
    const byName = new Map((policiesQ.data ?? []).map((p) => [p.name, p]))
    return (group?.attached_policies ?? []).map((p) => byName.get(p) ?? ({ name: p, description: "", managed: false } as PolicySummary))
  }, [group, policiesQ.data])

  const refresh = () => {
    mutate()
    revalidate(IAM)
  }

  const crumbs = [{ label: "IAM", href: "/iam/" }, { label: "User groups", href: "/iam/groups/" }, { label: name || "Group" }]
  if (!name) return <ErrorState error={new Error("No group name given. Open a group from the User groups list.")} />
  if (isLoading && !group)
    return (
      <div className="flex flex-col gap-4">
        <PageHeader title={name} breadcrumbs={crumbs} />
        <DetailSkeleton />
      </div>
    )
  if (error || !group)
    return (
      <div className="flex flex-col gap-4">
        <PageHeader title={name} breadcrumbs={crumbs} />
        <ErrorState error={error ?? new Error("Group not found")} onRetry={() => mutate()} />
      </div>
    )

  const base = `${IAM}/groups/${seg(group.name)}`
  const inlineCount = Object.keys(group.inline_policies ?? {}).length

  const memberCols: Column<IamUser>[] = [
    {
      id: "name",
      header: "User name",
      value: (u) => u.name,
      cell: (u) => (
        <Link href={userHref(u.name)} className={LINK}>
          {u.name}
        </Link>
      ),
    },
    {
      id: "console",
      header: "Console access",
      value: (u) => (u.console_access ? "Enabled" : "Disabled"),
      cell: (u) => (u.console_access === undefined ? "-" : <StatusBadge status={u.console_access ? "enabled" : "disabled"} label={u.console_access ? "Enabled" : "Disabled"} />),
    },
    {
      id: "login",
      header: "Last console sign-in",
      value: (u) => u.last_login ?? "",
      cell: (u) => (u.last_login ? <TimeAgo value={u.last_login} /> : <span className="text-muted-foreground">Never</span>),
      hideBelow: "sm",
    },
    { id: "created", header: "Created", value: (u) => u.created_at ?? "", cell: (u) => <TimeAgo value={u.created_at} />, hideBelow: "md" },
  ]

  const policyCols: Column<PolicySummary>[] = [
    {
      id: "name",
      header: "Policy name",
      value: (p) => p.name,
      cell: (p) => (
        <Link href={policyHref(p.name)} className={LINK}>
          {p.name}
        </Link>
      ),
    },
    { id: "type", header: "Type", value: (p) => (p.managed ? "AWS managed" : "Customer managed"), cell: (p) => <PolicyTypeBadge managed={p.managed} /> },
    { id: "desc", header: "Description", value: (p) => p.description, cell: (p) => <span className="text-muted-foreground">{p.description || "-"}</span>, hideBelow: "md" },
  ]

  return (
    <div className="flex flex-col gap-4">
      <PageHeader
        title={group.name}
        breadcrumbs={crumbs}
        actions={
          <Button variant="outline" size="sm" onClick={() => setConfirmDelete(true)}>
            <Trash2 /> Delete
          </Button>
        }
      />
      <Section title="Summary">
        <KeyValueGrid
          columns={3}
          items={[
            { label: "User group name", value: group.name },
            { label: "Created", value: formatDate(group.created_at) },
            { label: "Path", value: <span className="font-mono text-[13px]">{group.path ?? "/"}</span> },
            { label: "ARN", value: <CopyableText value={group.arn} className="text-[13px]" />, wide: true },
          ]}
        />
      </Section>

      <Tabs value={tab} onValueChange={(v) => setParam("tab", v === "users" ? null : v)}>
        <TabsList>
          <TabsTrigger value="users">Users ({group.members.length})</TabsTrigger>
          <TabsTrigger value="permissions">Permissions ({group.attached_policies.length + inlineCount})</TabsTrigger>
        </TabsList>
        <TabsContent value="users">
          <DataTable
            title="Users in this group"
            description="Users in the group get every policy attached to it."
            data={members}
            loading={usersQ.isLoading}
            columns={memberCols}
            rowId={(u) => u.name}
            selection="multi"
            selected={memberSel}
            onSelectedChange={setMemberSel}
            searchPlaceholder="Filter users"
            actions={
              <>
                <Button size="sm" variant="outline" disabled={!memberSel.length} onClick={() => setConfirmRemove(true)}>
                  Remove
                </Button>
                <Button size="sm" onClick={() => setAddUsers(true)}>
                  <Plus /> Add users
                </Button>
              </>
            }
            empty={
              <EmptyState
                icon={Users}
                title="No users in this group"
                action={
                  <Button size="sm" onClick={() => setAddUsers(true)}>
                    <Plus /> Add users
                  </Button>
                }
              />
            }
          />
        </TabsContent>
        <TabsContent value="permissions" className="flex flex-col gap-6">
          <DataTable
            title="Permissions policies"
            description="Policies attached to the group apply to all of its users."
            data={attached}
            loading={policiesQ.isLoading}
            columns={policyCols}
            rowId={(p) => p.name}
            selection="multi"
            selected={policySel}
            onSelectedChange={setPolicySel}
            searchPlaceholder="Filter policies"
            actions={
              <>
                <Button size="sm" variant="outline" disabled={!policySel.length} onClick={() => setConfirmDetach(true)}>
                  Detach
                </Button>
                <Button size="sm" onClick={() => setAttach(true)}>
                  <Plus /> Attach policies
                </Button>
              </>
            }
            empty={
              <EmptyState
                icon={ShieldCheck}
                title="No managed policies attached"
                description="Attach managed policies, or add an inline policy below, to give the group's users permissions."
                action={
                  <Button size="sm" onClick={() => setAttach(true)}>
                    <Plus /> Attach policies
                  </Button>
                }
              />
            }
          />
          <InlinePoliciesSection kind="group" owner={group.name} policies={group.inline_policies} onChanged={refresh} description="Policies embedded in this group only. Every user in the group gets their permissions." />
        </TabsContent>
      </Tabs>

      <PickDialog
        open={addUsers}
        onOpenChange={setAddUsers}
        title={`Add users to ${group.name}`}
        loading={usersQ.isLoading}
        items={(usersQ.data ?? []).filter((u) => !group.members.includes(u.name)).map((u) => ({ id: u.name, label: u.name, sub: u.root ? "root" : `${pluralize(u.groups.length, "group")}` }))}
        empty="Every user is already in this group."
        actionLabel="Add users"
        onSubmit={async (ids) => {
          const n = await runEach(ids, (u) => api.post(`${base}/members`, { user: u }))
          if (n) toast.success(`${pluralize(n, "user")} added to ${group.name}`)
          refresh()
          return n === ids.length
        }}
      />
      <AttachPoliciesDialog
        open={attach}
        onOpenChange={setAttach}
        title={`Attach policies to ${group.name}`}
        exclude={group.attached_policies}
        onAttach={async (names) => {
          const n = await runEach(names, (p) => api.post(`${base}/policies`, { policy: p }))
          if (n) toast.success(`${pluralize(n, "policy", "policies")} attached to ${group.name}`)
          refresh()
          return n === names.length
        }}
      />
      <ConfirmDialog
        open={confirmRemove}
        onOpenChange={setConfirmRemove}
        title={`Remove ${pluralize(memberSel.length, "user")} from ${group.name}?`}
        description={<>{memberSel.join(", ")} will lose the permissions of this group.</>}
        actionLabel="Remove"
        onConfirm={async () => {
          for (const u of memberSel) await api.del(`${base}/members/${seg(u)}`)
          toast.success(`${pluralize(memberSel.length, "user")} removed from ${group.name}`)
          setMemberSel([])
          refresh()
        }}
      />
      <ConfirmDialog
        open={confirmDetach}
        onOpenChange={setConfirmDetach}
        title={`Detach ${pluralize(policySel.length, "policy", "policies")}?`}
        description={<>Users in {group.name} lose the permissions granted by {policySel.join(", ")}.</>}
        actionLabel="Detach"
        onConfirm={async () => {
          for (const p of policySel) await api.del(`${base}/policies/${seg(p)}`)
          toast.success(`${pluralize(policySel.length, "policy", "policies")} detached from ${group.name}`)
          setPolicySel([])
          refresh()
        }}
      />
      <ConfirmDialog
        open={confirmDelete}
        onOpenChange={setConfirmDelete}
        title={`Delete ${group.name}?`}
        description={`The ${pluralize(group.members.length, "member")} of this group lose its permissions. Users and policies are not deleted.`}
        confirmText={group.name}
        onConfirm={async () => {
          await api.del(base)
          toast.success(`Group ${group.name} deleted`)
          revalidate(IAM)
          router.push("/iam/groups/")
        }}
      />
    </div>
  )
}
