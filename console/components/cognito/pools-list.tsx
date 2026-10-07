"use client"

import { useEffect, useState } from "react"
import { useRouter } from "next/navigation"
import { Loader2, Plus, UsersRound } from "lucide-react"
import { toast } from "sonner"

import { Button } from "@/components/ui/button"
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog"
import { Input } from "@/components/ui/input"
import { ActionsMenu, type ActionItem } from "@/components/console/actions-menu"
import { ConfirmDialog } from "@/components/console/confirm-dialog"
import { CopyButton } from "@/components/console/copy-button"
import { CellLink, DataTable, type Column } from "@/components/console/data-table"
import { EmptyState } from "@/components/console/empty-state"
import { Field } from "@/components/console/form-field"
import { PageHeader } from "@/components/console/page-header"
import { TimeAgo } from "@/components/console/time-ago"
import { api, errorMessage } from "@/lib/api"
import { pluralize } from "@/lib/format"
import { revalidate, useQueryParam } from "@/lib/hooks"
import type { CreateUserPoolInput, PasswordPolicy, UserPool } from "@/lib/types"

import { COGNITO_PATH, DEFAULT_POLICY, NAME_RE, POOLS_PATH, PasswordPolicyFields, SignUpBadge, SwitchRow, minLengthError, poolHref, poolPath, usePools } from "./shared"

export function PoolsList() {
  const router = useRouter()
  const { data, error, isLoading, isValidating, mutate } = usePools()
  const [selected, setSelected] = useState<string[]>([])
  const [creating, setCreating] = useState(useQueryParam("create") === "1")
  const [deleting, setDeleting] = useState<UserPool | null>(null)
  const sel = (data ?? []).find((p) => selected.includes(p.id)) ?? null

  const columns: Column<UserPool>[] = [
    {
      id: "name",
      header: "User pool name",
      cell: (p) => <CellLink href={poolHref(p.id)}>{p.name}</CellLink>,
      value: (p) => p.name,
    },
    {
      id: "id",
      header: "User pool ID",
      cell: (p) => (
        <span className="inline-flex items-center gap-1 font-mono text-[13px] whitespace-nowrap">
          {p.id}
          <CopyButton value={p.id} label="Copy pool ID" />
        </span>
      ),
      value: (p) => p.id,
    },
    { id: "users", header: "Users", cell: (p) => <span className="tabular-nums">{p.users}</span>, value: (p) => p.users },
    { id: "clients", header: "App clients", cell: (p) => <span className="tabular-nums">{p.clients}</span>, value: (p) => p.clients, hideBelow: "sm" },
    {
      id: "signup",
      header: "Self sign-up",
      cell: (p) => <SignUpBadge pool={p} />,
      value: (p) => (p.self_sign_up ? 1 : 0),
      hideBelow: "md",
    },
    { id: "created", header: "Created", cell: (p) => <TimeAgo value={p.created_at} />, value: (p) => p.created_at, hideBelow: "sm" },
  ]

  const items: ActionItem[] = [
    { label: "View details", onSelect: () => sel && router.push(poolHref(sel.id)), disabled: !sel },
    { label: "Integration", onSelect: () => sel && router.push(poolHref(sel.id, "integration")), disabled: !sel },
    { separator: true },
    { label: "Delete user pool", destructive: true, onSelect: () => sel && setDeleting(sel), disabled: !sel },
  ]

  return (
    <div className="flex flex-col gap-4">
      <PageHeader
        title="User pools"
        description="User directories for your applications: sign-up and sign-in, groups, and signed JWTs that your code and API Gateway routes can verify."
        breadcrumbs={[{ label: "Cognito", href: "/cognito/" }, { label: "User pools" }]}
      />
      <DataTable
        title="User pools"
        data={data}
        columns={columns}
        rowId={(p) => p.id}
        loading={isLoading}
        error={error}
        onRefresh={() => mutate()}
        refreshing={isValidating}
        selection="single"
        selected={selected}
        onSelectedChange={setSelected}
        searchPlaceholder="Filter by name or ID"
        defaultSort={{ id: "name" }}
        actions={
          <>
            <ActionsMenu items={items} disabled={!sel} />
            <Button size="sm" onClick={() => setCreating(true)}>
              <Plus /> Create user pool
            </Button>
          </>
        }
        empty={
          <EmptyState
            icon={UsersRound}
            title="No user pools"
            description="Create a user pool, add an app client, and your application can sign users up and in with a couple of HTTP calls."
            action={
              <Button size="sm" onClick={() => setCreating(true)}>
                <Plus /> Create user pool
              </Button>
            }
          />
        }
      />
      <CreatePoolDialog open={creating} onOpenChange={setCreating} />
      <DeletePoolDialog pool={deleting} onClose={() => setDeleting(null)} />
    </div>
  )
}

export function DeletePoolDialog({ pool, onClose, redirect }: { pool: UserPool | null; onClose: () => void; redirect?: boolean }) {
  const router = useRouter()
  return (
    <ConfirmDialog
      open={!!pool}
      onOpenChange={(o) => !o && onClose()}
      title={`Delete user pool ${pool?.name ?? ""}`}
      confirmText={pool?.name}
      description={
        <p>
          The pool is deleted with its {pluralize(pool?.users ?? 0, "user")}, {pluralize(pool?.clients ?? 0, "app client")} and signing key. Tokens it issued stop
          verifying, and API routes that use it as their authorizer return 401.
        </p>
      }
      onConfirm={async () => {
        if (!pool) return
        await api.del(poolPath(pool.id))
        toast.success(`Deleted user pool ${pool.name}`)
        if (redirect) router.push("/cognito/")
        await revalidate(COGNITO_PATH)
      }}
    />
  )
}

function CreatePoolDialog({ open, onOpenChange }: { open: boolean; onOpenChange: (o: boolean) => void }) {
  const router = useRouter()
  const [name, setName] = useState("")
  const [policy, setPolicy] = useState<PasswordPolicy>(DEFAULT_POLICY)
  const [autoConfirm, setAutoConfirm] = useState(true)
  const [selfSignUp, setSelfSignUp] = useState(true)
  const [touched, setTouched] = useState(false)
  const [pending, setPending] = useState(false)

  useEffect(() => {
    if (!open) return
    setName("")
    setPolicy(DEFAULT_POLICY)
    setAutoConfirm(true)
    setSelfSignUp(true)
    setTouched(false)
  }, [open])

  const nameErr = !name.trim() ? "Enter a name" : !NAME_RE.test(name.trim()) ? "1-128 letters, digits, spaces and + = , . @ _ -" : undefined
  const minErr = minLengthError(policy.min_length)

  const submit = async (e: React.FormEvent) => {
    e.preventDefault()
    setTouched(true)
    if (nameErr || minErr) return
    const body: CreateUserPoolInput = { name: name.trim(), password_policy: policy, auto_confirm: autoConfirm, self_sign_up: selfSignUp }
    setPending(true)
    try {
      const p = await api.post<UserPool>(POOLS_PATH, body)
      toast.success(`Created user pool ${p.name}`)
      await revalidate(COGNITO_PATH)
      onOpenChange(false)
      router.push(poolHref(p.id))
    } catch (err) {
      toast.error(errorMessage(err))
    } finally {
      setPending(false)
    }
  }

  return (
    <Dialog open={open} onOpenChange={(o) => !pending && onOpenChange(o)}>
      <DialogContent className="max-h-[90vh] overflow-y-auto sm:max-w-lg">
        <form onSubmit={submit} className="flex flex-col gap-5">
          <DialogHeader>
            <DialogTitle>Create user pool</DialogTitle>
            <DialogDescription>Each pool has its own users, groups, app clients and RS256 signing key.</DialogDescription>
          </DialogHeader>
          <Field label="User pool name" htmlFor="pool-name" error={touched ? nameErr : undefined}>
            <Input id="pool-name" autoFocus value={name} onChange={(e) => setName(e.target.value)} placeholder="shop-users" autoComplete="off" />
          </Field>
          <div className="flex flex-col gap-2">
            <span className="text-sm font-semibold">Password policy</span>
            <PasswordPolicyFields value={policy} onChange={setPolicy} idPrefix="pool-pp" error={touched ? minErr : undefined} />
          </div>
          <div className="flex flex-col gap-2">
            <span className="text-sm font-semibold">Sign-up</span>
            <SwitchRow
              id="pool-self"
              label="Self sign-up"
              description="Let anyone create an account through the public sign-up endpoint. Turn off to create users only from the console or API."
              checked={selfSignUp}
              onChange={setSelfSignUp}
            />
            <SwitchRow
              id="pool-auto"
              label="Auto-confirm new users"
              description="Self-registered users can sign in immediately. Otherwise they stay unconfirmed until an administrator confirms them."
              checked={autoConfirm}
              onChange={setAutoConfirm}
            />
          </div>
          <DialogFooter>
            <Button type="button" variant="outline" onClick={() => onOpenChange(false)} disabled={pending}>
              Cancel
            </Button>
            <Button type="submit" disabled={pending}>
              {pending && <Loader2 className="animate-spin" />}
              Create user pool
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}
