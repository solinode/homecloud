"use client"

import Link from "next/link"
import { useRouter } from "next/navigation"
import { useEffect, useState } from "react"
import { Loader2, Plus, Trash2, UserCog } from "lucide-react"
import { toast } from "sonner"

import { Button } from "@/components/ui/button"
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog"
import { ConfirmDialog } from "@/components/console/confirm-dialog"
import { CopyableText } from "@/components/console/copy-button"
import { EmptyState } from "@/components/console/empty-state"
import { ErrorState } from "@/components/console/error-state"
import { Field } from "@/components/console/form-field"
import { KeyValueGrid } from "@/components/console/key-value"
import { DetailSkeleton } from "@/components/console/loading"
import { PageHeader } from "@/components/console/page-header"
import { Section } from "@/components/console/section"
import { TagList } from "@/components/console/tags-editor"
import { api, errorMessage, seg } from "@/lib/api"
import { formatDate } from "@/lib/format"
import { revalidate, useApi, useQueryParam } from "@/lib/hooks"
import type { IamRole, InstanceProfile } from "@/lib/types"

import { IAM, LINK } from "./common"
import { RolePicker } from "./role-picker"
import { TrustedEntitiesList, roleHref } from "./role-common"

export function InstanceProfileDetail() {
  const name = useQueryParam("name")
  const router = useRouter()
  const path = name ? `${IAM}/instance-profiles/${seg(name)}` : null
  const { data: profile, error, isLoading, mutate } = useApi<InstanceProfile>(path)
  const roleName = profile?.roles?.[0]
  const roleQ = useApi<IamRole>(roleName ? `${IAM}/roles/${seg(roleName)}` : null)
  const [addOpen, setAddOpen] = useState(false)
  const [confirmRemove, setConfirmRemove] = useState(false)
  const [confirmDelete, setConfirmDelete] = useState(false)

  const refresh = () => {
    mutate()
    revalidate(IAM)
  }

  const crumbs = [{ label: "IAM", href: "/iam/" }, { label: "Instance profiles", href: "/iam/instance-profiles/" }, { label: name || "Instance profile" }]
  if (!name) return <ErrorState error={new Error("No instance profile name given. Open one from the Instance profiles list.")} />
  if (isLoading && !profile)
    return (
      <div className="flex flex-col gap-4">
        <PageHeader title={name} breadcrumbs={crumbs} />
        <DetailSkeleton />
      </div>
    )
  if (error || !profile)
    return (
      <div className="flex flex-col gap-4">
        <PageHeader title={name} breadcrumbs={crumbs} />
        <ErrorState error={error ?? new Error("Instance profile not found")} onRetry={() => mutate()} />
      </div>
    )

  const base = `${IAM}/instance-profiles/${seg(profile.name)}`
  const role = roleQ.data

  return (
    <div className="flex flex-col gap-4">
      <PageHeader
        title={profile.name}
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
            { label: "ARN", value: <CopyableText value={profile.arn} className="text-[13px]" />, wide: true },
            { label: "Created", value: formatDate(profile.created_at) },
            { label: "Instance profile ID", value: <CopyableText value={profile.id} className="text-[13px]" /> },
            { label: "Path", value: <span className="font-mono text-[13px]">{profile.path || "/"}</span> },
            { label: "Tags", value: <TagList tags={profile.tags} />, wide: true },
          ]}
        />
      </Section>

      <Section
        title="Role"
        description="An instance profile holds at most one role. Instances launched with this profile get temporary credentials for it."
        actions={
          roleName ? (
            <Button size="sm" variant="outline" onClick={() => setConfirmRemove(true)}>
              Remove role
            </Button>
          ) : (
            <Button size="sm" onClick={() => setAddOpen(true)}>
              <Plus /> Add role
            </Button>
          )
        }
      >
        {roleName ? (
          <div className="flex flex-col gap-3 text-sm">
            <div className="flex items-center gap-2">
              <UserCog className="text-muted-foreground size-4" />
              <Link href={roleHref(roleName)} className={LINK}>
                {roleName}
              </Link>
            </div>
            {role && (
              <KeyValueGrid
                columns={2}
                items={[
                  { label: "Role ARN", value: <CopyableText value={role.arn} className="text-[13px]" />, wide: true },
                  { label: "Trusted entities", value: <TrustedEntitiesList role={role} /> },
                  { label: "Description", value: role.description },
                ]}
              />
            )}
            {role && !role.trusted_services?.includes("ec2.amazonaws.com") && (
              <p className="rounded-md border border-amber-500/40 bg-amber-50 p-3 text-sm text-amber-800 dark:bg-amber-500/10 dark:text-amber-200">
                The role&apos;s trust policy does not allow ec2.amazonaws.com, so instances cannot assume it. Edit the role&apos;s trust relationships to add EC2.
              </p>
            )}
            {roleQ.error && <p className="text-destructive text-xs">{errorMessage(roleQ.error)}</p>}
          </div>
        ) : (
          <EmptyState
            icon={UserCog}
            title="No role"
            description="Instances launched with this profile get no credentials until you add a role."
            action={
              <Button size="sm" onClick={() => setAddOpen(true)}>
                <Plus /> Add role
              </Button>
            }
          />
        )}
      </Section>

      <AddRoleDialog open={addOpen} onOpenChange={setAddOpen} profile={profile.name} onAdded={refresh} />
      <ConfirmDialog
        open={confirmRemove}
        onOpenChange={setConfirmRemove}
        title={`Remove ${roleName} from ${profile.name}?`}
        description="Instances launched with this profile stop getting new credentials for the role. The role itself is not deleted."
        actionLabel="Remove role"
        onConfirm={async () => {
          await api.del(`${base}/roles/${seg(roleName!)}`)
          toast.success(`Role ${roleName} removed from ${profile.name}`)
          refresh()
        }}
      />
      <ConfirmDialog
        open={confirmDelete}
        onOpenChange={setConfirmDelete}
        title={`Delete ${profile.name}?`}
        description={
          roleName
            ? `The role ${roleName} is removed from the profile first. Instances launched with it can no longer get credentials. The role is not deleted.`
            : "Instances launched with this profile can no longer get credentials."
        }
        confirmText={profile.name}
        onConfirm={async () => {
          if (roleName) await api.del(`${base}/roles/${seg(roleName)}`)
          await api.del(base)
          toast.success(`Instance profile ${profile.name} deleted`)
          revalidate(IAM)
          router.push("/iam/instance-profiles/")
        }}
      />
    </div>
  )
}

function AddRoleDialog({ open, onOpenChange, profile, onAdded }: { open: boolean; onOpenChange: (o: boolean) => void; profile: string; onAdded: () => void }) {
  const [role, setRole] = useState("")
  const [pending, setPending] = useState(false)
  useEffect(() => {
    if (open) setRole("")
  }, [open])
  const submit = async () => {
    setPending(true)
    try {
      await api.post(`${IAM}/instance-profiles/${seg(profile)}/roles`, { role })
      toast.success(`Role ${role} added to ${profile}`)
      onAdded()
      onOpenChange(false)
    } catch (e) {
      toast.error(errorMessage(e))
    } finally {
      setPending(false)
    }
  }
  return (
    <Dialog open={open} onOpenChange={(o) => !pending && onOpenChange(o)}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>Add role to {profile}</DialogTitle>
          <DialogDescription>Adding a role needs iam:PassRole on it: whoever launches an instance with this profile acts as the role.</DialogDescription>
        </DialogHeader>
        <Field label="Role">
          <RolePicker value={role} valueType="name" service="ec2.amazonaws.com" onChange={(v) => setRole(v)} />
        </Field>
        <DialogFooter>
          <Button variant="outline" onClick={() => onOpenChange(false)} disabled={pending}>
            Cancel
          </Button>
          <Button onClick={submit} disabled={pending || !role}>
            {pending && <Loader2 className="animate-spin" />} Add role
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
