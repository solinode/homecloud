"use client"

import Link from "next/link"
import { useRouter } from "next/navigation"
import { useEffect, useState } from "react"
import { Loader2, Plus, Server, Trash2 } from "lucide-react"
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
import { revalidate, useApi, useQueryParam } from "@/lib/hooks"
import type { InstanceProfile } from "@/lib/types"

import { IAM, LINK, instanceProfileHref, nameError } from "./common"
import { RolePicker } from "./role-picker"
import { roleHref } from "./role-common"

const PATH_RE = /^\/([\x21-\x7e]*\/)?$/

export function pathError(p: string): string | null {
  if (!p) return null
  if (p.length > 512 || !PATH_RE.test(p)) return "A path starts and ends with / and uses printable ASCII characters, e.g. /app/."
  return null
}

export function InstanceProfilesList() {
  const { data, error, isLoading, isValidating, mutate } = useApi<InstanceProfile[]>(`${IAM}/instance-profiles`)
  const [selected, setSelected] = useState<string[]>([])
  const [confirm, setConfirm] = useState(false)
  const createParam = useQueryParam("create")
  const roleParam = useQueryParam("role")
  const router = useRouter()
  const [createOpen, setCreateOpen] = useState(false)

  useEffect(() => {
    if (createParam === "1") setCreateOpen(true)
  }, [createParam])
  const openCreate = (o: boolean) => {
    setCreateOpen(o)
    if (!o && (createParam || roleParam)) router.replace("/iam/instance-profiles/", { scroll: false })
  }

  const sel = (data ?? []).filter((p) => selected.includes(p.name))

  const columns: Column<InstanceProfile>[] = [
    {
      id: "name",
      header: "Instance profile name",
      value: (p) => p.name,
      cell: (p) => (
        <Link href={instanceProfileHref(p.name)} className={LINK}>
          {p.name}
        </Link>
      ),
    },
    {
      id: "role",
      header: "Role",
      value: (p) => p.roles?.[0] ?? "",
      cell: (p) =>
        p.roles?.length ? (
          <Link href={roleHref(p.roles[0])} className="text-primary hover:underline">
            {p.roles[0]}
          </Link>
        ) : (
          <span className="text-muted-foreground">No role</span>
        ),
    },
    { id: "path", header: "Path", value: (p) => p.path, cell: (p) => <span className="font-mono text-xs">{p.path || "/"}</span>, hideBelow: "md" },
    {
      id: "arn",
      header: "ARN",
      value: (p) => p.arn,
      cell: (p) => (
        <span className="text-muted-foreground block max-w-[24rem] truncate font-mono text-xs" title={p.arn}>
          {p.arn}
        </span>
      ),
      hideBelow: "lg",
    },
    { id: "created", header: "Created", value: (p) => p.created_at, cell: (p) => <TimeAgo value={p.created_at} />, hideBelow: "sm" },
  ]

  return (
    <div className="flex flex-col gap-4">
      <PageHeader
        title="Instance profiles"
        description="An instance profile passes a role to EC2 instances: software on an instance launched with the profile gets temporary credentials for the role."
        breadcrumbs={[{ label: "IAM", href: "/iam/" }, { label: "Instance profiles" }]}
      />
      <DataTable
        title="Instance profiles"
        data={data}
        columns={columns}
        rowId={(p) => p.name}
        loading={isLoading}
        error={error}
        onRetry={() => mutate()}
        onRefresh={() => mutate()}
        refreshing={isValidating}
        selection="multi"
        selected={selected}
        onSelectedChange={setSelected}
        searchPlaceholder="Filter instance profiles"
        defaultSort={{ id: "name" }}
        actions={
          <>
            <ActionsMenu disabled={!selected.length} items={[{ label: "Delete", icon: <Trash2 />, destructive: true, onSelect: () => setConfirm(true) }]} />
            <Button size="sm" onClick={() => openCreate(true)}>
              <Plus /> Create instance profile
            </Button>
          </>
        }
        empty={
          <EmptyState
            icon={Server}
            title="No instance profiles"
            description="Create an instance profile for a role that trusts ec2.amazonaws.com, then choose it when you launch an instance."
            action={
              <Button size="sm" onClick={() => openCreate(true)}>
                <Plus /> Create instance profile
              </Button>
            }
          />
        }
      />
      <ConfirmDialog
        open={confirm}
        onOpenChange={setConfirm}
        title={sel.length === 1 ? `Delete ${sel[0].name}?` : `Delete ${sel.length} instance profiles?`}
        description={
          <div className="flex flex-col gap-2">
            <p>Instances launched with {sel.length === 1 ? "this profile" : "these profiles"} can no longer get credentials for the role. The roles themselves are not deleted.</p>
            {sel.length > 1 && <p className="font-medium">{sel.map((p) => p.name).join(", ")}</p>}
          </div>
        }
        confirmText={sel.length === 1 ? sel[0].name : "delete"}
        onConfirm={async () => {
          for (const p of sel) {
            // The role is removed from the profile first, as AWS requires.
            for (const r of p.roles ?? []) await api.del(`${IAM}/instance-profiles/${seg(p.name)}/roles/${seg(r)}`)
            await api.del(`${IAM}/instance-profiles/${seg(p.name)}`)
          }
          toast.success(sel.length === 1 ? `Instance profile ${sel[0].name} deleted` : `${sel.length} instance profiles deleted`)
          setSelected([])
          revalidate(IAM)
        }}
      />
      <CreateInstanceProfileDialog open={createOpen} onOpenChange={openCreate} existing={(data ?? []).map((p) => p.name)} initialRole={roleParam} />
    </div>
  )
}

export function CreateInstanceProfileDialog({
  open,
  onOpenChange,
  existing,
  initialRole,
  onCreated,
}: {
  open: boolean
  onOpenChange: (o: boolean) => void
  existing: string[]
  initialRole?: string
  onCreated?: (p: InstanceProfile) => void
}) {
  const [name, setName] = useState("")
  const [path, setPath] = useState("")
  const [role, setRole] = useState("")
  const [touched, setTouched] = useState(false)
  const [pending, setPending] = useState(false)
  useEffect(() => {
    if (open) {
      setName(initialRole ?? "")
      setPath("")
      setRole(initialRole ?? "")
      setTouched(false)
    }
  }, [open, initialRole])
  const nErr = nameError("instance profile", name) ?? (existing.includes(name) ? `An instance profile named ${name} already exists.` : null)
  const pErr = pathError(path)

  const submit = async (e: React.FormEvent) => {
    e.preventDefault()
    setTouched(true)
    if (nErr || pErr) return
    setPending(true)
    try {
      const p = await api.post<InstanceProfile>(`${IAM}/instance-profiles`, { name, path: path || undefined, role: role || undefined })
      toast.success(`Instance profile ${name} created`)
      revalidate(IAM)
      onCreated?.(p)
      onOpenChange(false)
    } catch (e) {
      toast.error(errorMessage(e))
    } finally {
      setPending(false)
    }
  }

  return (
    <Dialog open={open} onOpenChange={(o) => !pending && onOpenChange(o)}>
      <DialogContent className="sm:max-w-lg">
        <form onSubmit={submit} className="flex flex-col gap-4">
          <DialogHeader>
            <DialogTitle>Create instance profile</DialogTitle>
            <DialogDescription>An instance profile holds one role. Choose it when you launch an EC2 instance to give the instance that role&apos;s permissions.</DialogDescription>
          </DialogHeader>
          <Field label="Instance profile name" htmlFor="ip-name" error={touched ? nErr : null} help="Up to 64 characters: letters, digits and + = , . @ _ -. Often the same as the role's name.">
            <Input id="ip-name" autoFocus autoComplete="off" value={name} onChange={(e) => setName(e.target.value.trim())} placeholder="e.g. web-server" aria-invalid={touched && !!nErr} />
          </Field>
          <Field label="Role" optional help="Only roles that trust ec2.amazonaws.com are listed unless you show all roles. You can add or change the role later.">
            <RolePicker
              value={role}
              valueType="name"
              service="ec2.amazonaws.com"
              allowNone
              placeholder="No role"
              onChange={(v) => {
                setRole(v)
                if (!name && v) setName(v)
              }}
            />
          </Field>
          <Field label="Path" htmlFor="ip-path" optional error={touched ? pErr : null} help="Defaults to /.">
            <Input id="ip-path" autoComplete="off" value={path} onChange={(e) => setPath(e.target.value.trim())} placeholder="/" aria-invalid={touched && !!pErr} className="font-mono" />
          </Field>
          <DialogFooter>
            <Button type="button" variant="outline" onClick={() => onOpenChange(false)} disabled={pending}>
              Cancel
            </Button>
            <Button type="submit" disabled={pending}>
              {pending && <Loader2 className="animate-spin" />} Create instance profile
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}
