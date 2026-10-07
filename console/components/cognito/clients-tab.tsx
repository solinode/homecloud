"use client"

import { useEffect, useState } from "react"
import { AppWindow, Loader2, Plus, Trash2 } from "lucide-react"
import { toast } from "sonner"

import { Button } from "@/components/ui/button"
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog"
import { Input } from "@/components/ui/input"
import { ConfirmDialog } from "@/components/console/confirm-dialog"
import { CopyButton } from "@/components/console/copy-button"
import { CellText, DataTable, type Column } from "@/components/console/data-table"
import { EmptyState } from "@/components/console/empty-state"
import { Field } from "@/components/console/form-field"
import { Tag } from "@/components/console/tag"
import { TimeAgo } from "@/components/console/time-ago"
import { api, errorMessage, seg } from "@/lib/api"
import { revalidate, useApi } from "@/lib/hooks"
import type { AppClient, CreateAppClientInput, CreatedAppClient, UserPool } from "@/lib/types"

import { COGNITO_PATH, SwitchRow, poolPath } from "./shared"
import { ShowOnceDialog } from "./show-once-dialog"

export function useClients(poolId: string) {
  return useApi<AppClient[]>(`${poolPath(poolId)}/clients`, { refreshInterval: 60_000 })
}

function formatMinutes(m: number) {
  if (m % 60 === 0) return `${m / 60} hour${m === 60 ? "" : "s"}`
  return `${m} minutes`
}

export function ClientsTab({ pool }: { pool: UserPool }) {
  const { data, error, isLoading, isValidating, mutate } = useClients(pool.id)
  const [creating, setCreating] = useState(false)
  const [created, setCreated] = useState<CreatedAppClient | null>(null)
  const [deleting, setDeleting] = useState<AppClient | null>(null)

  const columns: Column<AppClient>[] = [
    {
      id: "name",
      header: "App client name",
      cell: (c) =>
        c.name ? (
          <CellText max="16rem" className="font-medium">
            {c.name}
          </CellText>
        ) : (
          <span className="text-muted-foreground">(unnamed)</span>
        ),
      value: (c) => c.name,
    },
    {
      id: "id",
      header: "Client ID",
      cell: (c) => (
        <span className="inline-flex items-center gap-1 font-mono text-[13px] whitespace-nowrap">
          {c.id}
          <CopyButton value={c.id} label="Copy client ID" />
        </span>
      ),
      value: (c) => c.id,
    },
    {
      id: "secret",
      header: "Client secret",
      cell: (c) => (c.has_secret ? <Tag accent="brand" mono={false}>Confidential</Tag> : <Tag mono={false}>Public</Tag>),
      value: (c) => (c.has_secret ? 1 : 0),
      hideBelow: "sm",
    },
    { id: "access", header: "ID/access token", cell: (c) => formatMinutes(c.access_token_minutes), value: (c) => c.access_token_minutes, hideBelow: "md" },
    { id: "refresh", header: "Refresh token", cell: (c) => `${c.refresh_token_days} day${c.refresh_token_days === 1 ? "" : "s"}`, value: (c) => c.refresh_token_days, hideBelow: "md" },
    { id: "created", header: "Created", cell: (c) => <TimeAgo value={c.created_at} />, value: (c) => c.created_at, hideBelow: "lg" },
    {
      id: "actions",
      header: "",
      className: "w-12 text-right",
      cell: (c) => (
        <Button variant="ghost" size="icon" className="text-destructive hover:text-destructive size-8" onClick={() => setDeleting(c)} aria-label={`Delete app client ${c.name}`}>
          <Trash2 />
        </Button>
      ),
    },
  ]

  return (
    <>
      <DataTable
        title="App clients"
        description="Each application that signs users in uses an app client ID. Clients with a secret must send it too (for server-side apps)."
        data={data}
        columns={columns}
        rowId={(c) => c.id}
        loading={isLoading}
        error={error}
        onRefresh={() => mutate()}
        refreshing={isValidating}
        noSearch
        defaultSort={{ id: "name" }}
        actions={
          <Button size="sm" onClick={() => setCreating(true)}>
            <Plus /> Create app client
          </Button>
        }
        empty={
          <EmptyState
            icon={AppWindow}
            title="No app clients"
            description="Create an app client to call the sign-up and sign-in endpoints."
            action={
              <Button size="sm" onClick={() => setCreating(true)}>
                <Plus /> Create app client
              </Button>
            }
          />
        }
      />
      <CreateClientDialog pool={pool} open={creating} onOpenChange={setCreating} onCreated={setCreated} />
      <ShowOnceDialog
        open={!!created}
        onClose={() => setCreated(null)}
        title={`App client ${created?.name || ""} created`}
        description={created?.client_secret ? "Send both values when your app calls the sign-up and sign-in endpoints." : "Use this client ID when your app calls the sign-up and sign-in endpoints."}
        values={
          created
            ? [{ label: "Client ID", value: created.id }, ...(created.client_secret ? [{ label: "Client secret", value: created.client_secret, secret: true }] : [])]
            : []
        }
      />
      <ConfirmDialog
        open={!!deleting}
        onOpenChange={(o) => !o && setDeleting(null)}
        title={`Delete app client ${deleting?.name ?? ""}?`}
        description={
          <p>
            Applications using client <span className="text-foreground font-mono">{deleting?.id}</span> can no longer sign users in or refresh tokens. Tokens already
            issued stay valid until they expire.
          </p>
        }
        onConfirm={async () => {
          if (!deleting) return
          await api.del(`${poolPath(pool.id)}/clients/${seg(deleting.id)}`)
          toast.success(`Deleted app client ${deleting.name || deleting.id}`)
          await revalidate(COGNITO_PATH)
        }}
      />
    </>
  )
}

function CreateClientDialog({ pool, open, onOpenChange, onCreated }: { pool: UserPool; open: boolean; onOpenChange: (o: boolean) => void; onCreated: (c: CreatedAppClient) => void }) {
  const [name, setName] = useState("")
  const [secret, setSecret] = useState(false)
  const [access, setAccess] = useState("60")
  const [refresh, setRefresh] = useState("30")
  const [touched, setTouched] = useState(false)
  const [pending, setPending] = useState(false)
  useEffect(() => {
    if (!open) return
    setName("")
    setSecret(false)
    setAccess("60")
    setRefresh("30")
    setTouched(false)
  }, [open])

  const a = Number(access)
  const r = Number(refresh)
  const errors: Record<string, string> = {}
  if (!name.trim()) errors.name = "Enter a name"
  if (!Number.isInteger(a) || a < 5 || a > 1440) errors.access = "5-1440 minutes"
  if (!Number.isInteger(r) || r < 1 || r > 3650) errors.refresh = "1-3650 days"
  const err = (k: string) => (touched ? errors[k] : undefined)

  const submit = async (e: React.FormEvent) => {
    e.preventDefault()
    setTouched(true)
    if (Object.keys(errors).length) return
    const body: CreateAppClientInput = { name: name.trim(), generate_secret: secret, access_token_minutes: a, refresh_token_days: r }
    setPending(true)
    try {
      const c = await api.post<CreatedAppClient>(`${poolPath(pool.id)}/clients`, body)
      toast.success(`Created app client ${c.name}`)
      await revalidate(COGNITO_PATH)
      onOpenChange(false)
      onCreated(c)
    } catch (err) {
      toast.error(errorMessage(err))
    } finally {
      setPending(false)
    }
  }

  return (
    <Dialog open={open} onOpenChange={(o) => !pending && onOpenChange(o)}>
      <DialogContent className="sm:max-w-md">
        <form onSubmit={submit} className="flex flex-col gap-4">
          <DialogHeader>
            <DialogTitle>Create app client</DialogTitle>
            <DialogDescription>The client ID is public; a client secret is only for applications that can keep it confidential.</DialogDescription>
          </DialogHeader>
          <Field label="App client name" htmlFor="cl-name" error={err("name")}>
            <Input id="cl-name" autoFocus value={name} onChange={(e) => setName(e.target.value)} placeholder="web" autoComplete="off" />
          </Field>
          <SwitchRow
            id="cl-secret"
            label="Generate a client secret"
            description="For server-side apps. Browser and mobile apps cannot keep a secret; leave it off for them."
            checked={secret}
            onChange={setSecret}
          />
          <div className="grid grid-cols-2 gap-3">
            <Field label="ID/access token (minutes)" htmlFor="cl-access" error={err("access")} help="5-1440">
              <Input id="cl-access" inputMode="numeric" value={access} onChange={(e) => setAccess(e.target.value)} />
            </Field>
            <Field label="Refresh token (days)" htmlFor="cl-refresh" error={err("refresh")} help="1-3650">
              <Input id="cl-refresh" inputMode="numeric" value={refresh} onChange={(e) => setRefresh(e.target.value)} />
            </Field>
          </div>
          <DialogFooter>
            <Button type="button" variant="outline" onClick={() => onOpenChange(false)} disabled={pending}>
              Cancel
            </Button>
            <Button type="submit" disabled={pending}>
              {pending && <Loader2 className="animate-spin" />}
              Create app client
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}
