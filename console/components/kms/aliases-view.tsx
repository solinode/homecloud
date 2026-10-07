"use client"

import { useEffect, useMemo, useState } from "react"
import { Loader2, Plus, Tags, Trash2 } from "lucide-react"
import { toast } from "sonner"

import { Button } from "@/components/ui/button"
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog"
import { ConfirmDialog } from "@/components/console/confirm-dialog"
import { CopyButton } from "@/components/console/copy-button"
import { CellLink, CellText, DataTable, type Column } from "@/components/console/data-table"
import { EmptyState } from "@/components/console/empty-state"
import { Field } from "@/components/console/form-field"
import { TimeAgo } from "@/components/console/time-ago"
import { api, errorMessage, seg } from "@/lib/api"
import { revalidate, useApi } from "@/lib/hooks"
import type { KmsAlias, KmsKey } from "@/lib/types"

import { AliasInput, KMS_PATH, KeyPicker, KeyStateBadge, KeyTypeTag, aliasError, isServiceManaged, keyHref, normalizeAlias } from "./shared"

const ALIASES_PATH = `${KMS_PATH}/aliases`

const reserved = (name: string) => name.startsWith("alias/hc/") || name.startsWith("alias/aws/")

/** AliasesView lists every alias in the account (GET /api/v1/kms/aliases) with its target key. */
export function AliasesView({ keys }: { keys: KmsKey[] | undefined }) {
  const { data, error, isLoading, isValidating, mutate } = useApi<KmsAlias[]>(ALIASES_PATH, { refreshInterval: 15_000 })
  const [creating, setCreating] = useState(false)
  const [deleting, setDeleting] = useState<string | null>(null)
  const byId = useMemo(() => new Map((keys ?? []).map((k) => [k.id, k])), [keys])

  const columns: Column<KmsAlias>[] = [
    {
      id: "name",
      header: "Alias name",
      cell: (a) => (
        <span className="inline-flex max-w-full items-center gap-1">
          <CellText mono>{a.name}</CellText>
          <CopyButton value={a.name} label="Copy alias" />
        </span>
      ),
      value: (a) => a.name,
    },
    {
      id: "key",
      header: "Target key",
      cell: (a) => (
        <CellLink href={keyHref(a.key_id)} mono title={a.key_id}>
          {a.key_id.slice(0, 8)}...
        </CellLink>
      ),
      value: (a) => a.key_id,
    },
    {
      id: "type",
      header: "Key type",
      cell: (a) => {
        const k = byId.get(a.key_id)
        return k ? <KeyTypeTag spec={k.key_spec} /> : <span className="text-muted-foreground">-</span>
      },
      value: (a) => byId.get(a.key_id)?.key_spec,
      hideBelow: "md",
    },
    {
      id: "state",
      header: "Key status",
      cell: (a) => {
        const k = byId.get(a.key_id)
        return k ? <KeyStateBadge state={k.state} /> : <span className="text-muted-foreground">-</span>
      },
      value: (a) => byId.get(a.key_id)?.state,
      hideBelow: "sm",
    },
    { id: "created", header: "Created", cell: (a) => (a.created_at ? <TimeAgo value={a.created_at} /> : "-"), value: (a) => a.created_at, hideBelow: "lg" },
    {
      id: "actions",
      header: "",
      className: "text-right",
      cell: (a) => (
        <Button
          variant="ghost"
          size="icon"
          className="text-muted-foreground hover:text-destructive size-8"
          disabled={reserved(a.name)}
          title={reserved(a.name) ? "Aliases of HomeCloud managed keys can't be deleted" : `Delete ${a.name}`}
          aria-label={`Delete ${a.name}`}
          onClick={(e) => {
            e.stopPropagation()
            setDeleting(a.name)
          }}
        >
          <Trash2 />
        </Button>
      ),
    },
  ]

  return (
    <>
      <DataTable
        title="Aliases"
        description="Friendly names for keys. Use an alias anywhere a key ID is accepted; point it at a new key to switch keys without changing callers."
        data={data}
        columns={columns}
        rowId={(a) => a.name}
        loading={isLoading}
        error={error}
        onRefresh={() => mutate()}
        refreshing={isValidating}
        searchPlaceholder="Filter by alias or key ID"
        defaultSort={{ id: "name" }}
        actions={
          <Button size="sm" onClick={() => setCreating(true)}>
            <Plus /> Create alias
          </Button>
        }
        empty={
          <EmptyState
            icon={Tags}
            title="No aliases"
            description="Create an alias to refer to a key by name."
            action={
              <Button size="sm" onClick={() => setCreating(true)}>
                <Plus /> Create alias
              </Button>
            }
          />
        }
      />

      <CreateAliasForKeyDialog open={creating} keys={keys} onClose={() => setCreating(false)} onCreated={() => mutate()} />
      <ConfirmDialog
        open={!!deleting}
        onOpenChange={(o) => !o && setDeleting(null)}
        title="Delete alias"
        description={
          <p>
            Delete <span className="text-foreground font-mono">{deleting}</span>? The key is not affected, but callers that use this alias fail until it is recreated.
          </p>
        }
        onConfirm={async () => {
          if (!deleting) return
          await api.del(`${ALIASES_PATH}/${seg(deleting)}`)
          toast.success(`Alias ${deleting} deleted`)
          await revalidate(KMS_PATH)
        }}
      />
    </>
  )
}

function CreateAliasForKeyDialog({ open, keys, onClose, onCreated }: { open: boolean; keys: KmsKey[] | undefined; onClose: () => void; onCreated: () => void }) {
  const [name, setName] = useState("")
  const [keyId, setKeyId] = useState("")
  const [pending, setPending] = useState(false)
  const [touched, setTouched] = useState(false)

  useEffect(() => {
    if (open) {
      setName("")
      setKeyId("")
      setTouched(false)
    }
  }, [open])

  const customer = (keys ?? []).filter((k) => !isServiceManaged(k) && k.state !== "PendingDeletion")
  const full = normalizeAlias(name)
  const nameErr = full ? aliasError(full) : "Enter an alias name."
  const keyErr = keyId ? null : "Choose a key."

  const submit = async (e: React.FormEvent) => {
    e.preventDefault()
    setTouched(true)
    if (nameErr || keyErr) return
    setPending(true)
    try {
      await api.post(ALIASES_PATH, { name: full, key_id: keyId })
      toast.success(`Alias ${full} created`)
      await revalidate(KMS_PATH)
      onCreated()
      onClose()
    } catch (err) {
      toast.error(errorMessage(err))
    } finally {
      setPending(false)
    }
  }

  return (
    <Dialog open={open} onOpenChange={(o) => !o && !pending && onClose()}>
      <DialogContent className="sm:max-w-md">
        <form onSubmit={submit} className="flex flex-col gap-4">
          <DialogHeader>
            <DialogTitle>Create alias</DialogTitle>
            <DialogDescription>Alias names are unique in the account. Only customer managed keys can be alias targets.</DialogDescription>
          </DialogHeader>
          <Field label="Alias name" htmlFor="kms-alias-name" error={touched || name ? nameErr : undefined}>
            <AliasInput id="kms-alias-name" autoFocus value={name} onChange={setName} invalid={!!nameErr && (touched || !!name)} />
          </Field>
          <Field label="Target key" htmlFor="kms-alias-key" error={touched ? keyErr : undefined}>
            <KeyPicker id="kms-alias-key" keys={keys ? customer : undefined} value={keyId} onChange={setKeyId} />
          </Field>
          <DialogFooter>
            <Button type="button" variant="outline" onClick={onClose} disabled={pending}>
              Cancel
            </Button>
            <Button type="submit" disabled={pending || (touched && (!!nameErr || !!keyErr))}>
              {pending && <Loader2 className="animate-spin" />}
              Create alias
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}
