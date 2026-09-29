"use client"

import Link from "next/link"
import { useRouter } from "next/navigation"
import { useState } from "react"
import { Eye, FilePen, Info, Loader2, Pencil, Star, Trash2, UserCog, Users, UsersRound } from "lucide-react"
import { toast } from "sonner"

import { Button } from "@/components/ui/button"
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog"
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs"
import { Textarea } from "@/components/ui/textarea"
import { ActionsMenu } from "@/components/console/actions-menu"
import { ConfirmDialog } from "@/components/console/confirm-dialog"
import { CopyButton, CopyableText } from "@/components/console/copy-button"
import { DataTable, type Column } from "@/components/console/data-table"
import { EmptyState } from "@/components/console/empty-state"
import { ErrorState } from "@/components/console/error-state"
import { Field } from "@/components/console/form-field"
import { JsonEditor } from "@/components/console/json-editor"
import { KeyValueGrid } from "@/components/console/key-value"
import { DetailSkeleton } from "@/components/console/loading"
import { PageHeader } from "@/components/console/page-header"
import { Section } from "@/components/console/section"
import { StatusBadge } from "@/components/console/status-badge"
import { TimeAgo } from "@/components/console/time-ago"
import { api, errorMessage, seg } from "@/lib/api"
import { formatDate, pluralize } from "@/lib/format"
import { revalidate, useApi, useQueryParam, useSetQueryParam } from "@/lib/hooks"
import type { PolicyDetail as PolicyDetailT, PolicyVersion } from "@/lib/types"

import { IAM, LINK, PolicyStatementsTable, PolicyTypeBadge, groupHref, policyJson, policyTypeLabel, statementCount, userHref } from "./common"
import { PolicyEditor, policyTextError } from "./policy-editor"
import { roleHref } from "./role-common"

const TABS = ["permissions", "entities", "versions"] as const
const MAX_VERSIONS = 5

export function PolicyDetail() {
  const name = useQueryParam("name")
  const tabParam = useQueryParam("tab")
  const setParam = useSetQueryParam()
  const router = useRouter()
  const tab = (TABS as readonly string[]).includes(tabParam) ? tabParam : "permissions"
  const path = name ? `${IAM}/policies/${seg(name)}` : null
  const { data, error, isLoading, mutate } = useApi<PolicyDetailT>(path)
  const versionsQ = useApi<PolicyVersion[]>(path ? `${path}/versions` : null)
  const [editing, setEditing] = useState<{ from: string } | null>(null)
  const [text, setText] = useState("")
  const [description, setDescription] = useState("")
  const [saving, setSaving] = useState(false)
  const [confirmDelete, setConfirmDelete] = useState(false)

  const crumbs = [{ label: "IAM", href: "/iam/" }, { label: "Policies", href: "/iam/policies/" }, { label: name || "Policy" }]
  if (!name) return <ErrorState error={new Error("No policy name given. Open a policy from the Policies list.")} />
  if (isLoading && !data)
    return (
      <div className="flex flex-col gap-4">
        <PageHeader title={name} breadcrumbs={crumbs} />
        <DetailSkeleton />
      </div>
    )
  if (error || !data)
    return (
      <div className="flex flex-col gap-4">
        <PageHeader title={name} breadcrumbs={crumbs} />
        <ErrorState error={error ?? new Error("Policy not found")} onRetry={() => mutate()} />
      </div>
    )

  const { policy, attachments } = data
  const users = attachments?.users ?? []
  const groups = attachments?.groups ?? []
  const roles = attachments?.roles ?? []
  const attachedCount = users.length + groups.length + roles.length
  const json = policyJson(policy.document)
  const versions = versionsQ.data ?? []
  const versionCount = versionsQ.data?.length ?? policy.versions?.length ?? 1
  const defaultVersion = policy.default_version || versions.find((v) => v.is_default)?.version_id || "v1"
  const oldestNonDefault = [...versions].filter((v) => !v.is_default).sort((a, b) => a.created_at.localeCompare(b.created_at))[0]

  const refresh = () => {
    mutate()
    versionsQ.mutate()
    revalidate(IAM)
  }

  const startEdit = (doc = policy.document, from = defaultVersion) => {
    setText(policyJson(doc))
    setDescription(policy.description)
    setEditing({ from })
    setParam("tab", null)
  }

  const docErr = editing ? policyTextError(text) : null
  const save = async () => {
    if (docErr) return
    setSaving(true)
    try {
      // PUT creates a new version and makes it the default (the oldest
      // non-default version makes room when there are five).
      await api.put(`${IAM}/policies/${seg(policy.name)}`, { description, document: JSON.parse(text) })
      toast.success(`Policy ${policy.name} updated with a new default version`)
      setEditing(null)
      refresh()
    } catch (e) {
      toast.error(errorMessage(e))
    } finally {
      setSaving(false)
    }
  }

  return (
    <div className="flex flex-col gap-4">
      <PageHeader
        title={policy.name}
        breadcrumbs={crumbs}
        badge={<PolicyTypeBadge managed={policy.managed} />}
        actions={
          !policy.managed && (
            <>
              {!editing && (
                <Button size="sm" variant="outline" onClick={() => startEdit()}>
                  <Pencil /> Edit
                </Button>
              )}
              <Button size="sm" variant="outline" onClick={() => setConfirmDelete(true)}>
                <Trash2 /> Delete
              </Button>
            </>
          )
        }
      />
      <Section title="Policy details">
        <KeyValueGrid
          columns={3}
          items={[
            { label: "Type", value: policyTypeLabel(policy.managed) },
            { label: "Created", value: formatDate(policy.created_at) },
            { label: "Edited", value: formatDate(policy.updated_at) },
            { label: "Default version", value: defaultVersion },
            { label: "Path", value: <span className="font-mono text-[13px]">{policy.path || "/"}</span> },
            { label: "Policy ID", value: policy.id ? <CopyableText value={policy.id} className="text-[13px]" /> : "" },
            { label: "ARN", value: <CopyableText value={policy.arn} className="text-[13px]" />, wide: true },
            { label: "Description", value: policy.description || "-", wide: true },
          ]}
        />
      </Section>

      <Tabs value={editing ? "permissions" : tab} onValueChange={(v) => setParam("tab", v === "permissions" ? null : v)}>
        <TabsList className="max-w-full overflow-x-auto">
          <TabsTrigger value="permissions">Permissions</TabsTrigger>
          <TabsTrigger value="entities" disabled={!!editing}>
            Entities attached ({attachedCount})
          </TabsTrigger>
          <TabsTrigger value="versions" disabled={!!editing}>
            Policy versions ({versionCount})
          </TabsTrigger>
        </TabsList>
        <TabsContent value="permissions" className="flex flex-col gap-4">
          {editing ? (
            <Section
              title="Edit policy"
              description={
                <>
                  Saving creates a new version from {editing.from === defaultVersion ? "the current default" : `version ${editing.from}`} and makes it the default. Changes apply immediately to every
                  user, group and role this policy is attached to.
                </>
              }
            >
              <div className="flex flex-col gap-4">
                {versionCount >= MAX_VERSIONS && (
                  <p className="flex items-start gap-2 rounded-md border border-amber-500/40 bg-amber-50 p-3 text-sm text-amber-800 dark:bg-amber-500/10 dark:text-amber-200">
                    <Info className="mt-0.5 size-4 shrink-0" />
                    <span>
                      This policy already has {MAX_VERSIONS} versions, the maximum. Saving deletes the oldest non-default version
                      {oldestNonDefault ? ` (${oldestNonDefault.version_id})` : ""}.
                    </span>
                  </p>
                )}
                <Field label="Description" htmlFor="edit-desc" optional>
                  <Textarea id="edit-desc" rows={2} value={description} onChange={(e) => setDescription(e.target.value)} maxLength={1000} />
                </Field>
                <PolicyEditor value={text} onChange={setText} />
                <div className="flex justify-end gap-2 border-t pt-3">
                  <Button variant="outline" onClick={() => setEditing(null)} disabled={saving}>
                    Cancel
                  </Button>
                  <Button onClick={save} disabled={saving || !!docErr}>
                    {saving && <Loader2 className="animate-spin" />} Save changes
                  </Button>
                </div>
              </div>
            </Section>
          ) : (
            <>
              {policy.managed && (
                <p className="text-muted-foreground flex items-start gap-2 rounded-md border px-3 py-2 text-sm">
                  <Info className="mt-0.5 size-4 shrink-0 text-blue-600 dark:text-blue-400" />
                  <span>
                    This is an AWS managed policy. It cannot be edited or deleted. To customize it, copy its JSON into a{" "}
                    <Link href="/iam/policies/create/" className="text-primary hover:underline">
                      new policy
                    </Link>
                    .
                  </span>
                </p>
              )}
              <Section title="Permissions defined in this policy">
                <PolicyStatementsTable doc={policy.document} />
              </Section>
              <Section title="Policy document" actions={<CopyButton value={json} size="sm" label="Copy JSON" toastMessage="Policy JSON copied" />}>
                <JsonEditor value={json} readOnly rows={Math.min(json.split("\n").length, 30)} />
              </Section>
            </>
          )}
        </TabsContent>
        <TabsContent value="entities">
          <EntitiesTab users={users} groups={groups} roles={roles} />
        </TabsContent>
        <TabsContent value="versions">
          <VersionsTab
            policyName={policy.name}
            managed={policy.managed}
            versions={versionsQ.data}
            loading={versionsQ.isLoading}
            error={versionsQ.error}
            onRetry={() => versionsQ.mutate()}
            onChanged={refresh}
            onEditFrom={(v) => startEdit(v.document, v.version_id)}
          />
        </TabsContent>
      </Tabs>

      <ConfirmDialog
        open={confirmDelete}
        onOpenChange={setConfirmDelete}
        title={`Delete ${policy.name}?`}
        description={
          attachedCount
            ? `This policy is attached to ${users.length} user(s), ${groups.length} group(s) and ${roles.length} role(s). Detach it from every entity first; the API rejects deleting an attached policy.`
            : `The policy and all ${pluralize(versionCount, "version")} are permanently deleted. This cannot be undone.`
        }
        confirmText={policy.name}
        onConfirm={async () => {
          await api.del(`${IAM}/policies/${seg(policy.name)}`)
          toast.success(`Policy ${policy.name} deleted`)
          revalidate(IAM)
          router.push("/iam/policies/")
        }}
      />
    </div>
  )
}

function EntitiesTab({ users, groups, roles }: { users: string[]; groups: string[]; roles: string[] }) {
  const rows = [
    ...users.map((n) => ({ n, href: userHref(n), icon: Users, type: "User" })),
    ...groups.map((n) => ({ n, href: groupHref(n), icon: UsersRound, type: "User group" })),
    ...roles.map((n) => ({ n, href: roleHref(n), icon: UserCog, type: "Role" })),
  ]
  return (
    <Section title="Entities attached" description="Users, user groups and roles this policy is attached to as a permissions policy." flush>
      {!rows.length ? (
        <EmptyState icon={Users} title="Not attached" description="Attach this policy from the Permissions tab of a user, group or role." />
      ) : (
        <div className="overflow-x-auto">
          <table className="w-full text-sm">
            <thead>
              <tr className="bg-muted/40 text-muted-foreground border-b text-left text-xs">
                <th className="px-4 py-2 font-semibold">Entity name</th>
                <th className="px-4 py-2 font-semibold">Entity type</th>
              </tr>
            </thead>
            <tbody>
              {rows.map((r) => (
                <tr key={`${r.type}-${r.n}`} className="border-b last:border-0">
                  <td className="px-4 py-2">
                    <Link href={r.href} className={LINK}>
                      {r.n}
                    </Link>
                  </td>
                  <td className="text-muted-foreground px-4 py-2">
                    <span className="flex items-center gap-1.5">
                      <r.icon className="size-3.5" /> {r.type}
                    </span>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </Section>
  )
}

function VersionsTab({
  policyName,
  managed,
  versions,
  loading,
  error,
  onRetry,
  onChanged,
  onEditFrom,
}: {
  policyName: string
  managed: boolean
  versions: PolicyVersion[] | undefined
  loading: boolean
  error: unknown
  onRetry: () => void
  onChanged: () => void
  onEditFrom: (v: PolicyVersion) => void
}) {
  const [selected, setSelected] = useState<string[]>([])
  const [viewing, setViewing] = useState<PolicyVersion | null>(null)
  const [confirmDefault, setConfirmDefault] = useState(false)
  const [confirmDelete, setConfirmDelete] = useState(false)
  const sel = (versions ?? []).find((v) => v.version_id === selected[0])
  const base = `${IAM}/policies/${seg(policyName)}`

  const columns: Column<PolicyVersion>[] = [
    {
      id: "version",
      header: "Version",
      value: (v) => Number(v.version_id.replace(/\D/g, "")) || 0,
      cell: (v) => (
        <span className="flex flex-wrap items-center gap-2">
          <button type="button" className={LINK} onClick={() => setViewing(v)}>
            {v.version_id}
          </button>
          {v.is_default && <StatusBadge status="default" tone="success" label="Default" />}
        </span>
      ),
    },
    { id: "created", header: "Created", value: (v) => v.created_at, cell: (v) => <TimeAgo value={v.created_at} /> },
    {
      id: "statements",
      header: "Statements",
      value: (v) => statementCount(v.document),
      cell: (v) => statementCount(v.document),
      hideBelow: "sm",
    },
  ]

  return (
    <>
      <DataTable
        title="Policy versions"
        description={
          managed
            ? "AWS managed policies are versioned by HomeCloud. The default version is the one in effect."
            : `The default version is the one in effect. A customer managed policy keeps up to ${MAX_VERSIONS} versions; editing the policy creates a new default version.`
        }
        data={versions}
        loading={loading}
        error={error}
        onRetry={onRetry}
        onRefresh={onRetry}
        columns={columns}
        rowId={(v) => v.version_id}
        selection={managed ? "none" : "single"}
        selected={selected}
        onSelectedChange={setSelected}
        noSearch
        defaultSort={{ id: "version", desc: true }}
        actions={
          <>
            <Button size="sm" variant="outline" disabled={!sel} onClick={() => sel && setViewing(sel)}>
              <Eye /> View
            </Button>
            {!managed && (
              <ActionsMenu
                disabled={!sel}
                items={[
                  {
                    label: "Set as default",
                    icon: <Star />,
                    disabled: !sel || sel.is_default,
                    hint: sel?.is_default ? "This version is already the default" : undefined,
                    onSelect: () => setConfirmDefault(true),
                  },
                  { label: "Edit as new version", icon: <FilePen />, onSelect: () => sel && onEditFrom(sel) },
                  {
                    label: "Delete",
                    icon: <Trash2 />,
                    destructive: true,
                    disabled: !sel || sel.is_default,
                    hint: sel?.is_default ? "The default version cannot be deleted. Make another version the default first." : undefined,
                    onSelect: () => setConfirmDelete(true),
                  },
                ]}
              />
            )}
          </>
        }
      />

      <Dialog open={!!viewing} onOpenChange={(o) => !o && setViewing(null)}>
        <DialogContent className="max-h-[92vh] overflow-y-auto sm:max-w-3xl">
          <DialogHeader>
            <DialogTitle className="flex flex-wrap items-center gap-2">
              {policyName} {viewing?.version_id}
              {viewing?.is_default && <StatusBadge status="default" tone="success" label="Default" />}
            </DialogTitle>
            <DialogDescription>Created {viewing ? formatDate(viewing.created_at) : ""}</DialogDescription>
          </DialogHeader>
          {viewing && (
            <>
              <PolicyStatementsTable doc={viewing.document} />
              <JsonEditor value={policyJson(viewing.document)} readOnly rows={Math.min(policyJson(viewing.document).split("\n").length, 22)} />
            </>
          )}
          <DialogFooter>
            {viewing && <CopyButton value={policyJson(viewing.document)} size="sm" label="Copy JSON" toastMessage="Policy JSON copied" />}
            {viewing && !managed && (
              <Button
                variant="outline"
                onClick={() => {
                  const v = viewing
                  setViewing(null)
                  onEditFrom(v)
                }}
              >
                <FilePen /> Edit as new version
              </Button>
            )}
            <Button onClick={() => setViewing(null)}>Close</Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      <ConfirmDialog
        open={confirmDefault}
        onOpenChange={setConfirmDefault}
        title={`Set ${sel?.version_id} as the default version?`}
        description={`The permissions in ${sel?.version_id} apply immediately to every user, group and role ${policyName} is attached to.`}
        actionLabel="Set as default"
        destructive={false}
        onConfirm={async () => {
          if (!sel) return
          await api.put(`${base}/default-version`, { version: sel.version_id })
          toast.success(`${sel.version_id} is now the default version of ${policyName}`)
          onChanged()
        }}
      />
      <ConfirmDialog
        open={confirmDelete}
        onOpenChange={setConfirmDelete}
        title={`Delete version ${sel?.version_id}?`}
        description="The version is permanently deleted. The default version is not affected."
        onConfirm={async () => {
          if (!sel) return
          await api.del(`${base}/versions/${seg(sel.version_id)}`)
          toast.success(`Version ${sel.version_id} deleted`)
          setSelected([])
          onChanged()
        }}
      />
    </>
  )
}
