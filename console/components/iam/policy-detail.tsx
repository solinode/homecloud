"use client"

import Link from "next/link"
import { useRouter } from "next/navigation"
import { useState } from "react"
import { Info, Loader2, Pencil, Trash2, Users, UsersRound } from "lucide-react"
import { toast } from "sonner"

import { Button } from "@/components/ui/button"
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs"
import { Textarea } from "@/components/ui/textarea"
import { ConfirmDialog } from "@/components/console/confirm-dialog"
import { CopyButton, CopyableText } from "@/components/console/copy-button"
import { EmptyState } from "@/components/console/empty-state"
import { ErrorState } from "@/components/console/error-state"
import { Field } from "@/components/console/form-field"
import { JsonEditor } from "@/components/console/json-editor"
import { KeyValueGrid } from "@/components/console/key-value"
import { DetailSkeleton } from "@/components/console/loading"
import { PageHeader } from "@/components/console/page-header"
import { Section } from "@/components/console/section"
import { api, errorMessage, seg } from "@/lib/api"
import { formatDate } from "@/lib/format"
import { revalidate, useApi, useQueryParam, useSetQueryParam } from "@/lib/hooks"
import type { PolicyDetail as PolicyDetailT } from "@/lib/types"

import { IAM, PolicyStatementsTable, PolicyTypeBadge, groupHref, policyJson, userHref } from "./common"
import { PolicyEditor, policyTextError } from "./policy-editor"

export function PolicyDetail() {
  const name = useQueryParam("name")
  const tabParam = useQueryParam("tab")
  const setParam = useSetQueryParam()
  const router = useRouter()
  const tab = tabParam === "entities" ? "entities" : "permissions"
  const path = name ? `${IAM}/policies/${seg(name)}` : null
  const { data, error, isLoading, mutate } = useApi<PolicyDetailT>(path)
  const [editing, setEditing] = useState(false)
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
  const attachedCount = users.length + groups.length
  const json = policyJson(policy.document)

  const startEdit = () => {
    setText(json)
    setDescription(policy.description)
    setEditing(true)
    setParam("tab", null)
  }

  const docErr = editing ? policyTextError(text) : null
  const save = async () => {
    if (docErr) return
    setSaving(true)
    try {
      await api.put(`${IAM}/policies/${seg(policy.name)}`, { description, document: JSON.parse(text) })
      toast.success(`Policy ${policy.name} updated`)
      setEditing(false)
      mutate()
      revalidate(IAM)
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
                <Button size="sm" variant="outline" onClick={startEdit}>
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
            { label: "Type", value: policy.managed ? "HomeCloud managed" : "Customer managed" },
            { label: "Created", value: formatDate(policy.created_at) },
            { label: "Edited", value: formatDate(policy.updated_at) },
            { label: "ARN", value: <CopyableText value={policy.arn} className="text-[13px]" />, wide: true },
            { label: "Description", value: policy.description || "-", wide: true },
          ]}
        />
      </Section>

      <Tabs value={editing ? "permissions" : tab} onValueChange={(v) => setParam("tab", v === "permissions" ? null : v)}>
        <TabsList>
          <TabsTrigger value="permissions">Permissions</TabsTrigger>
          <TabsTrigger value="entities" disabled={editing}>
            Entities attached ({attachedCount})
          </TabsTrigger>
        </TabsList>
        <TabsContent value="permissions" className="flex flex-col gap-4">
          {editing ? (
            <Section title="Edit policy" description="Changes apply immediately to every user and group this policy is attached to.">
              <div className="flex flex-col gap-4">
                <Field label="Description" htmlFor="edit-desc" optional>
                  <Textarea id="edit-desc" rows={2} value={description} onChange={(e) => setDescription(e.target.value)} maxLength={1000} />
                </Field>
                <PolicyEditor value={text} onChange={setText} />
                <div className="flex justify-end gap-2 border-t pt-3">
                  <Button variant="outline" onClick={() => setEditing(false)} disabled={saving}>
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
                  This is a HomeCloud managed policy. It cannot be edited or deleted. To customize it, copy its JSON into a{" "}
                  <Link href="/iam/policies/create/" className="text-primary hover:underline">
                    new policy
                  </Link>
                  .
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
          <Section title="Entities attached" description="Users and user groups this policy is attached to as a permissions policy." flush>
            {!attachedCount ? (
              <EmptyState icon={Users} title="Not attached" description="Attach this policy from a user's Permissions tab or a group's Permissions tab." />
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
                    {users.map((u) => (
                      <tr key={`u-${u}`} className="border-b last:border-0">
                        <td className="px-4 py-2">
                          <Link href={userHref(u)} className="text-primary font-medium hover:underline">
                            {u}
                          </Link>
                        </td>
                        <td className="text-muted-foreground px-4 py-2">
                          <span className="flex items-center gap-1.5">
                            <Users className="size-3.5" /> User
                          </span>
                        </td>
                      </tr>
                    ))}
                    {groups.map((g) => (
                      <tr key={`g-${g}`} className="border-b last:border-0">
                        <td className="px-4 py-2">
                          <Link href={groupHref(g)} className="text-primary font-medium hover:underline">
                            {g}
                          </Link>
                        </td>
                        <td className="text-muted-foreground px-4 py-2">
                          <span className="flex items-center gap-1.5">
                            <UsersRound className="size-3.5" /> User group
                          </span>
                        </td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            )}
          </Section>
        </TabsContent>
      </Tabs>

      <ConfirmDialog
        open={confirmDelete}
        onOpenChange={setConfirmDelete}
        title={`Delete ${policy.name}?`}
        description={
          attachedCount
            ? `This policy is attached to ${users.length} user(s) and ${groups.length} group(s). Detach it from every entity first; the API rejects deleting an attached policy.`
            : "The policy is permanently deleted. This cannot be undone."
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
