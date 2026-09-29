"use client"

import { useEffect, useState } from "react"
import Link from "next/link"
import { AlertCircle, AlertTriangle, ArrowLeft, Check, Loader2, Lock, Pencil, Plus, RefreshCw, RotateCcw, Trash2, X } from "lucide-react"
import { toast } from "sonner"

import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert"
import { Button } from "@/components/ui/button"
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { Switch } from "@/components/ui/switch"
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs"
import { ActionsMenu, type ActionItem } from "@/components/console/actions-menu"
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
import { TimeAgo } from "@/components/console/time-ago"
import { ApiError, api, errorMessage, seg } from "@/lib/api"
import { formatDate } from "@/lib/format"
import { revalidate, useAction, useApi, useQueryParam, useSetQueryParam } from "@/lib/hooks"
import type { KmsKey } from "@/lib/types"
import { cn } from "@/lib/utils"

import { CryptoTool } from "./crypto-tool"
import { AliasInput, KEYS_PATH, KEY_POLL, KMS_PATH, KeyStateBadge, ManagedBadge, aliasError, isServiceManaged, keyLabel, normalizeAlias, useKeyActions } from "./shared"

const TABS = ["details", "aliases", "crypto"] as const
type Tab = (typeof TABS)[number]

const READ_ONLY_REASON = "HomeCloud managed keys are created and rotated by the service that owns them and cannot be changed."

function BackButton() {
  return (
    <Button variant="outline" size="sm" asChild>
      <Link href="/kms/">
        <ArrowLeft /> Back to keys
      </Link>
    </Button>
  )
}

export function KeyDetail() {
  const id = useQueryParam("id")
  const tabParam = useQueryParam("tab")
  const setParam = useSetQueryParam()
  const tab: Tab = (TABS as readonly string[]).includes(tabParam) ? (tabParam as Tab) : "details"

  const { data: key, error, isLoading, isValidating, mutate } = useApi<KmsKey>(id ? `${KEYS_PATH}/${seg(id)}` : null, { refreshInterval: KEY_POLL })
  const actions = useKeyActions(() => mutate())

  const crumbs = [{ label: "KMS", href: "/kms/" }, { label: key && isServiceManaged(key) ? "HomeCloud managed keys" : "Customer managed keys", href: key && isServiceManaged(key) ? "/kms/?view=managed" : "/kms/" }, { label: key ? keyLabel(key) : id || "Key" }]

  if (!id) {
    return (
      <>
        <PageHeader title="Key" breadcrumbs={crumbs} />
        <EmptyState title="No key selected" description="Open a key from the keys list." action={<BackButton />} />
      </>
    )
  }
  if (error && !key) {
    const notFound = error instanceof ApiError && error.status === 404
    return (
      <div className="flex flex-col gap-4">
        <PageHeader title={id} breadcrumbs={crumbs} />
        {notFound ? (
          <Section>
            <EmptyState icon={AlertCircle} title="Key not found" description={`Key ${id} does not exist. Keys are removed when their scheduled deletion date passes.`} action={<BackButton />} />
          </Section>
        ) : (
          <ErrorState error={error} onRetry={() => mutate()} />
        )}
      </div>
    )
  }
  if (isLoading || !key) return <DetailSkeleton />

  const managed = isServiceManaged(key)
  const locked = managed || actions.busy
  const pending = key.state === "PendingDeletion"

  const items: ActionItem[] = [
    { label: "Enable", onSelect: () => actions.enable(key), disabled: locked || key.state !== "Disabled", hint: managed ? READ_ONLY_REASON : undefined },
    { label: "Disable", onSelect: () => actions.disable(key), disabled: locked || key.state !== "Enabled", hint: managed ? READ_ONLY_REASON : undefined },
    { label: "Rotate key material now", onSelect: () => actions.rotate(key), disabled: locked || pending, hint: managed ? READ_ONLY_REASON : undefined },
    { separator: true },
    pending
      ? { label: "Cancel key deletion", onSelect: () => actions.cancelDeletion(key), disabled: locked }
      : { label: "Schedule key deletion", destructive: true, onSelect: () => actions.scheduleDeletion(key), disabled: locked, hint: managed ? READ_ONLY_REASON : undefined },
  ]

  return (
    <div className="flex flex-col gap-4">
      <PageHeader
        title={<span className="break-all">{keyLabel(key)}</span>}
        badge={
          <>
            <KeyStateBadge state={key.state} />
            {managed && <ManagedBadge />}
          </>
        }
        description={key.aliases?.length ? <span className="font-mono text-[13px] break-all">{key.id}</span> : key.description || undefined}
        breadcrumbs={crumbs}
        actions={
          <>
            <Button variant="outline" size="sm" onClick={() => mutate()} aria-label="Refresh">
              <RefreshCw className={cn(isValidating && "animate-spin")} />
            </Button>
            {pending && !managed ? (
              <Button size="sm" onClick={() => actions.cancelDeletion(key)} disabled={actions.busy}>
                {actions.busy ? <Loader2 className="animate-spin" /> : <RotateCcw />} Cancel deletion
              </Button>
            ) : null}
            <ActionsMenu label="Key actions" items={items} disabled={managed} />
          </>
        }
      />

      {managed && (
        <Alert>
          <Lock />
          <AlertTitle>Read-only HomeCloud managed key</AlertTitle>
          <AlertDescription>
            {READ_ONLY_REASON} You can still use it to encrypt and decrypt data, for example from the Encrypt / decrypt tab.
          </AlertDescription>
        </Alert>
      )}
      {pending && key.deletion_date && (
        <Alert variant="destructive" className="border-destructive/40 bg-destructive/5">
          <AlertTriangle />
          <AlertTitle>This key is scheduled for deletion on {formatDate(key.deletion_date)}</AlertTitle>
          <AlertDescription>
            <p>It can&apos;t encrypt or decrypt until you cancel the deletion. After the deletion date, data encrypted under it is unrecoverable.</p>
          </AlertDescription>
        </Alert>
      )}

      <Tabs value={tab} onValueChange={(v) => setParam("tab", v === "details" ? null : v)}>
        <TabsList>
          <TabsTrigger value="details">Details</TabsTrigger>
          <TabsTrigger value="aliases">
            Aliases <span className="text-muted-foreground text-xs">({key.aliases?.length ?? 0})</span>
          </TabsTrigger>
          <TabsTrigger value="crypto">Encrypt / decrypt</TabsTrigger>
        </TabsList>
        <TabsContent value="details">
          <DetailsTab k={key} managed={managed} actions={actions} onSaved={() => mutate()} />
        </TabsContent>
        <TabsContent value="aliases">
          <AliasesTab k={key} managed={managed} onChanged={() => mutate()} />
        </TabsContent>
        <TabsContent value="crypto">
          <CryptoTool keyId={key.id} lockKey />
        </TabsContent>
      </Tabs>

      {actions.dialogs}
    </div>
  )
}

function DescriptionField({ k, editable, onSaved }: { k: KmsKey; editable: boolean; onSaved: () => void }) {
  const [editing, setEditing] = useState(false)
  const [value, setValue] = useState(k.description)
  const { pending, run } = useAction()
  useEffect(() => setValue(k.description), [k.description, editing])

  const save = async () => {
    const ok = await run(async () => {
      await api.patch(`${KEYS_PATH}/${seg(k.id)}`, { description: value })
      return true
    }, "Description updated")
    if (ok) {
      setEditing(false)
      revalidate(KMS_PATH)
      onSaved()
    }
  }

  if (!editing)
    return (
      <span className="inline-flex items-start gap-1">
        <span className={cn(!k.description && "text-muted-foreground")}>{k.description || "No description"}</span>
        {editable && (
          <button
            type="button"
            onClick={() => setEditing(true)}
            aria-label="Edit description"
            className="text-muted-foreground hover:text-foreground hover:bg-accent inline-flex size-6 shrink-0 items-center justify-center rounded"
          >
            <Pencil className="size-3.5" />
          </button>
        )}
      </span>
    )
  return (
    <form
      className="flex items-center gap-1"
      onSubmit={(e) => {
        e.preventDefault()
        save()
      }}
    >
      <Input autoFocus value={value} onChange={(e) => setValue(e.target.value)} className="h-8" onKeyDown={(e) => e.key === "Escape" && setEditing(false)} />
      <Button type="submit" size="icon" variant="ghost" className="size-8" disabled={pending} aria-label="Save description">
        {pending ? <Loader2 className="animate-spin" /> : <Check />}
      </Button>
      <Button type="button" size="icon" variant="ghost" className="size-8" onClick={() => setEditing(false)} aria-label="Cancel">
        <X />
      </Button>
    </form>
  )
}

function DetailsTab({ k, managed, actions, onSaved }: { k: KmsKey; managed: boolean; actions: ReturnType<typeof useKeyActions>; onSaved: () => void }) {
  const pending = k.state === "PendingDeletion"
  const nextRotation = k.rotation_enabled ? new Date(new Date(k.last_rotated || k.created_at).getTime() + 365 * 86_400_000) : null
  return (
    <div className="flex flex-col gap-4">
      <Section title="General configuration">
        <KeyValueGrid
          columns={3}
          items={[
            { label: "Key ID", value: <CopyableText value={k.id} /> },
            { label: "Status", value: <KeyStateBadge state={k.state} /> },
            { label: "Managed by", value: managed ? <ManagedBadge /> : "Customer" },
            {
              label: "Aliases",
              value: k.aliases?.length ? (
                <span className="flex flex-col gap-0.5">
                  {k.aliases.map((a) => (
                    <span key={a} className="font-mono text-[13px] break-all">
                      {a}
                    </span>
                  ))}
                </span>
              ) : (
                ""
              ),
            },
            { label: "Description", value: <DescriptionField k={k} editable={!managed} onSaved={onSaved} /> },
            { label: "Created", value: <span>{formatDate(k.created_at)} (<TimeAgo value={k.created_at} />)</span> },
            { label: "Key spec", value: <span className="font-mono text-[13px]">{k.key_spec}</span> },
            { label: "Key usage", value: <span className="font-mono text-[13px]">{k.key_usage}</span> },
            { label: "Key material versions", value: <span className="tabular-nums">{k.key_versions}</span> },
            ...(k.deletion_date ? [{ label: "Deletion date", value: <span className="text-destructive">{formatDate(k.deletion_date)}</span> }] : []),
            { label: "ARN", value: <CopyableText value={k.arn} />, wide: true },
          ]}
        />
      </Section>

      <Section
        title="Key rotation"
        description="Rotation adds a new key material version used for new encryptions. Older versions are kept so existing ciphertexts still decrypt."
        actions={
          <Button variant="outline" size="sm" onClick={() => actions.rotate(k)} disabled={managed || pending || actions.busy} title={managed ? READ_ONLY_REASON : undefined}>
            {actions.busy ? <Loader2 className="animate-spin" /> : <RotateCcw />} Rotate now
          </Button>
        }
      >
        <div className="flex flex-col gap-4">
          <div className="flex items-start justify-between gap-4 rounded-md border p-3">
            <div>
              <Label htmlFor="kms-auto-rotation" className="font-medium">
                Automatic yearly rotation
              </Label>
              <p className="text-muted-foreground mt-0.5 text-xs">
                {managed ? "Managed by HomeCloud." : k.rotation_enabled ? "New key material is created every 365 days." : "Off. Turn it on to rotate every 365 days."}
              </p>
            </div>
            <Switch
              id="kms-auto-rotation"
              checked={k.rotation_enabled}
              disabled={managed || pending || actions.busy}
              onCheckedChange={(v) => actions.setRotation(k, v)}
            />
          </div>
          <KeyValueGrid
            columns={3}
            items={[
              { label: "Last rotated", value: k.last_rotated ? <span>{formatDate(k.last_rotated)} (<TimeAgo value={k.last_rotated} />)</span> : "Never" },
              { label: "Next automatic rotation", value: nextRotation && !pending ? formatDate(nextRotation, false) : "" },
              { label: "Key material versions", value: String(k.key_versions) },
            ]}
          />
        </div>
      </Section>

      <Section title="Tags">
        <TagList tags={k.tags} />
      </Section>
    </div>
  )
}

function AliasesTab({ k, managed, onChanged }: { k: KmsKey; managed: boolean; onChanged: () => void }) {
  const [adding, setAdding] = useState(false)
  const [deleting, setDeleting] = useState<string | null>(null)
  const aliases = k.aliases ?? []

  return (
    <Section
      title={`Aliases (${aliases.length})`}
      description="Aliases are friendly names you can use instead of the key ID in Encrypt calls and key pickers. Moving an alias means deleting it and creating it on another key."
      flush
      actions={
        <Button size="sm" onClick={() => setAdding(true)} disabled={managed} title={managed ? READ_ONLY_REASON : undefined}>
          <Plus /> Create alias
        </Button>
      }
    >
      {aliases.length === 0 ? (
        <EmptyState
          title="No aliases"
          description="This key can only be referenced by its ID or ARN."
          action={
            !managed && (
              <Button size="sm" variant="outline" onClick={() => setAdding(true)}>
                <Plus /> Create alias
              </Button>
            )
          }
        />
      ) : (
        <div className="overflow-x-auto">
          <table className="w-full text-sm">
            <thead>
              <tr className="bg-muted/40 text-muted-foreground border-b text-left text-xs">
                <th className="px-4 py-2 font-semibold">Alias name</th>
                <th className="px-4 py-2" />
              </tr>
            </thead>
            <tbody>
              {aliases.map((a) => {
                const reserved = a.startsWith("alias/hc/")
                return (
                  <tr key={a} className="border-b last:border-0">
                    <td className="px-4 py-2">
                      <CopyableText value={a} />
                    </td>
                    <td className="px-4 py-2 text-right">
                      <Button
                        variant="outline"
                        size="sm"
                        className="text-destructive hover:text-destructive"
                        disabled={reserved}
                        title={reserved ? "Service-managed aliases cannot be deleted" : undefined}
                        onClick={() => setDeleting(a)}
                      >
                        <Trash2 /> Delete
                      </Button>
                    </td>
                  </tr>
                )
              })}
            </tbody>
          </table>
        </div>
      )}

      <CreateAliasDialog keyId={adding ? k.id : null} onClose={() => setAdding(false)} onCreated={onChanged} />
      <ConfirmDialog
        open={!!deleting}
        onOpenChange={(o) => !o && setDeleting(null)}
        title="Delete alias"
        description={
          <p>
            Delete <span className="text-foreground font-mono">{deleting}</span>? The key itself is not affected, but anything that refers to it by this alias will fail
            until the alias is recreated.
          </p>
        }
        onConfirm={async () => {
          if (!deleting) return
          await api.del(`${KMS_PATH}/aliases/${seg(deleting)}`)
          toast.success(`Alias ${deleting} deleted`)
          await revalidate(KMS_PATH)
          onChanged()
        }}
      />
    </Section>
  )
}

function CreateAliasDialog({ keyId, onClose, onCreated }: { keyId: string | null; onClose: () => void; onCreated: () => void }) {
  const [name, setName] = useState("")
  const [pending, setPending] = useState(false)
  const [touched, setTouched] = useState(false)

  useEffect(() => {
    if (keyId) {
      setName("")
      setTouched(false)
    }
  }, [keyId])

  const full = normalizeAlias(name)
  const err = full ? aliasError(full) : "Enter an alias name."

  const submit = async (e: React.FormEvent) => {
    e.preventDefault()
    setTouched(true)
    if (err || !keyId) return
    setPending(true)
    try {
      await api.post(`${KMS_PATH}/aliases`, { name: full, key_id: keyId })
      toast.success(`Alias ${full} created`)
      await revalidate(KMS_PATH)
      onCreated()
      onClose()
    } catch (e) {
      toast.error(errorMessage(e))
    } finally {
      setPending(false)
    }
  }

  return (
    <Dialog open={!!keyId} onOpenChange={(o) => !o && !pending && onClose()}>
      <DialogContent className="sm:max-w-md">
        <form onSubmit={submit} className="flex flex-col gap-4">
          <DialogHeader>
            <DialogTitle>Create alias</DialogTitle>
            <DialogDescription>Alias names must be unique in the account.</DialogDescription>
          </DialogHeader>
          <Field label="Alias name" htmlFor="kms-new-alias" error={touched || name ? err : undefined}>
            <AliasInput id="kms-new-alias" autoFocus value={name} onChange={setName} invalid={!!err && (touched || !!name)} />
          </Field>
          <DialogFooter>
            <Button type="button" variant="outline" onClick={onClose} disabled={pending}>
              Cancel
            </Button>
            <Button type="submit" disabled={pending || (touched && !!err)}>
              {pending && <Loader2 className="animate-spin" />}
              Create alias
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}
