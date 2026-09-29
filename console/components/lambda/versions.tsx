"use client"

import { useEffect, useMemo, useState } from "react"
import { GitBranch, Loader2, Plus, Tag } from "lucide-react"
import { toast } from "sonner"

import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog"
import { Input } from "@/components/ui/input"
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select"
import { Switch } from "@/components/ui/switch"
import { Textarea } from "@/components/ui/textarea"
import { ActionsMenu, type ActionItem } from "@/components/console/actions-menu"
import { ConfirmDialog } from "@/components/console/confirm-dialog"
import { CopyableText } from "@/components/console/copy-button"
import { DataTable, type Column } from "@/components/console/data-table"
import { EmptyState } from "@/components/console/empty-state"
import { Field } from "@/components/console/form-field"
import { TimeAgo } from "@/components/console/time-ago"
import { api, errorMessage, seg } from "@/lib/api"
import { formatBytes } from "@/lib/format"
import { revalidate, useApi } from "@/lib/hooks"
import type { LambdaAlias, LambdaFunction } from "@/lib/types"

import { LAMBDA_PATH, fnPath } from "./common"

const LATEST = "$LATEST"

export function useVersions(name: string, enabled = true) {
  return useApi<LambdaFunction[]>(enabled ? `${fnPath(name)}/versions` : null)
}

export function useAliases(name: string, enabled = true) {
  return useApi<LambdaAlias[]>(enabled ? `${fnPath(name)}/aliases` : null)
}

/** aliasTargets describes where an alias routes: "3" or "3 (90%), 4 (10%)". */
export function aliasTargets(a: LambdaAlias) {
  const extra = Object.entries(a.additional_version_weights ?? {})
  if (!extra.length) return a.function_version
  const rest = extra.reduce((s, [, w]) => s + w, 0)
  return [`${a.function_version} (${pct(1 - rest)})`, ...extra.map(([v, w]) => `${v} (${pct(w)})`)].join(", ")
}

const pct = (w: number) => `${Math.round(w * 1000) / 10}%`

export function VersionsTab({ fn }: { fn: LambdaFunction }) {
  const versions = useVersions(fn.name)
  const aliases = useAliases(fn.name)
  const [selected, setSelected] = useState<string[]>([])
  const [publishing, setPublishing] = useState(false)
  const [deleting, setDeleting] = useState<LambdaFunction | null>(null)
  const sel = versions.data?.find((v) => v.version === selected[0]) ?? null

  const aliasesByVersion = useMemo(() => {
    const m = new Map<string, string[]>()
    for (const a of aliases.data ?? []) {
      for (const v of [a.function_version, ...Object.keys(a.additional_version_weights ?? {})]) m.set(v, [...(m.get(v) ?? []), a.name])
    }
    return m
  }, [aliases.data])

  const columns: Column<LambdaFunction>[] = [
    {
      id: "version",
      header: "Version",
      value: (v) => (v.version === LATEST ? Number.MAX_SAFE_INTEGER : Number(v.version)),
      cell: (v) => <span className="font-mono text-[13px] font-medium">{v.version}</span>,
    },
    {
      id: "aliases",
      header: "Aliases",
      value: (v) => (aliasesByVersion.get(v.version ?? "") ?? []).join(","),
      cell: (v) => (
        <span className="flex flex-wrap gap-1">
          {(aliasesByVersion.get(v.version ?? "") ?? []).map((a) => (
            <Badge key={a} variant="secondary" className="font-normal">
              <Tag className="size-3" /> {a}
            </Badge>
          ))}
        </span>
      ),
    },
    {
      id: "description",
      header: "Description",
      value: (v) => (v.version === LATEST ? "" : (v.version_description ?? v.description)),
      cell: (v) =>
        v.version === LATEST ? <span className="text-muted-foreground">Unpublished, editable code</span> : v.version_description || v.description || "-",
    },
    { id: "runtime", header: "Runtime", value: (v) => v.runtime || v.image_uri || "", cell: (v) => <span className="font-mono text-xs">{v.runtime || "Image"}</span>, hideBelow: "md" },
    { id: "size", header: "Code size", value: (v) => v.code_size, cell: (v) => formatBytes(v.code_size), hideBelow: "lg" },
    { id: "modified", header: "Last modified", value: (v) => v.last_modified, cell: (v) => <TimeAgo value={v.last_modified} />, hideBelow: "sm" },
    {
      id: "arn",
      header: "ARN",
      value: (v) => `${fn.arn}:${v.version}`,
      cell: (v) => <CopyableText value={`${fn.arn}:${v.version}`} display={`…:${v.version}`} />,
      hideBelow: "lg",
    },
  ]

  const items: ActionItem[] = [
    {
      label: "Delete version",
      destructive: true,
      onSelect: () => setDeleting(sel),
      disabled: !sel || sel.version === LATEST,
    },
  ]

  return (
    <>
      <DataTable
        title="Versions"
        description="A published version is an immutable snapshot of the function's code and configuration. $LATEST is the version you edit."
        data={versions.data}
        columns={columns}
        rowId={(v) => v.version ?? LATEST}
        loading={versions.isLoading}
        error={versions.error}
        onRefresh={() => {
          versions.mutate()
          aliases.mutate()
        }}
        refreshing={versions.isValidating}
        selection="single"
        selected={selected}
        onSelectedChange={setSelected}
        noSearch
        defaultSort={{ id: "version", desc: true }}
        actions={
          <>
            <ActionsMenu items={items} disabled={!sel} />
            <Button size="sm" onClick={() => setPublishing(true)}>
              <GitBranch /> Publish new version
            </Button>
          </>
        }
      />
      <PublishDialog open={publishing} onOpenChange={setPublishing} fn={fn} />
      <ConfirmDialog
        open={!!deleting}
        onOpenChange={(o) => !o && setDeleting(null)}
        title={`Delete version ${deleting?.version ?? ""}?`}
        description="The version's code and configuration are deleted permanently. Versions that an alias routes to cannot be deleted."
        onConfirm={async () => {
          if (!deleting) return
          await api.del(`${fnPath(fn.name)}?qualifier=${encodeURIComponent(deleting.version ?? "")}`)
          toast.success(`Deleted version ${deleting.version}`)
          setSelected([])
          await revalidate(LAMBDA_PATH)
        }}
      />
    </>
  )
}

function PublishDialog({ open, onOpenChange, fn }: { open: boolean; onOpenChange: (o: boolean) => void; fn: LambdaFunction }) {
  const [description, setDescription] = useState("")
  const [pending, setPending] = useState(false)
  useEffect(() => {
    if (open) setDescription("")
  }, [open])

  const submit = async (e: React.FormEvent) => {
    e.preventDefault()
    setPending(true)
    try {
      const v = await api.post<LambdaFunction>(`${fnPath(fn.name)}/versions`, { description })
      toast.success(`Published version ${v.version}`)
      await revalidate(LAMBDA_PATH)
      onOpenChange(false)
    } catch (e) {
      toast.error(errorMessage(e))
    } finally {
      setPending(false)
    }
  }

  return (
    <Dialog open={open} onOpenChange={(o) => !pending && onOpenChange(o)}>
      <DialogContent className="sm:max-w-md">
        <form onSubmit={submit} className="flex flex-col gap-4">
          <DialogHeader>
            <DialogTitle>Publish new version</DialogTitle>
            <DialogDescription>
              Snapshots the current code and configuration of $LATEST as version {(fn.last_version ?? 0) + 1}. If nothing changed since the last version, that
              version is returned instead.
            </DialogDescription>
          </DialogHeader>
          <Field label="Version description" htmlFor="pub-desc" optional>
            <Textarea id="pub-desc" rows={2} maxLength={256} value={description} onChange={(e) => setDescription(e.target.value)} autoFocus />
          </Field>
          <DialogFooter>
            <Button type="button" variant="outline" onClick={() => onOpenChange(false)} disabled={pending}>
              Cancel
            </Button>
            <Button type="submit" disabled={pending}>
              {pending && <Loader2 className="animate-spin" />}
              Publish
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}

// ---- aliases ----

export function AliasesTab({ fn }: { fn: LambdaFunction }) {
  const aliases = useAliases(fn.name)
  const [selected, setSelected] = useState<string[]>([])
  const [dialog, setDialog] = useState<{ alias: LambdaAlias | null } | null>(null)
  const [deleting, setDeleting] = useState<LambdaAlias | null>(null)
  const sel = aliases.data?.find((a) => a.name === selected[0]) ?? null

  const columns: Column<LambdaAlias>[] = [
    { id: "name", header: "Name", value: (a) => a.name, cell: (a) => <span className="font-medium">{a.name}</span> },
    { id: "version", header: "Versions", value: (a) => a.function_version, cell: (a) => <span className="font-mono text-[13px]">{aliasTargets(a)}</span> },
    { id: "description", header: "Description", value: (a) => a.description, cell: (a) => a.description || "-", hideBelow: "md" },
    { id: "arn", header: "ARN", value: (a) => a.arn, cell: (a) => <CopyableText value={a.arn} display={`…:${a.name}`} />, hideBelow: "sm" },
  ]

  const items: ActionItem[] = [
    { label: "Edit", onSelect: () => setDialog({ alias: sel }), disabled: !sel },
    { separator: true },
    { label: "Delete", destructive: true, onSelect: () => setDeleting(sel), disabled: !sel },
  ]

  return (
    <>
      <DataTable
        title="Aliases"
        description="An alias is a named pointer to a version, optionally splitting traffic between two versions. Invoke function:alias to use it."
        data={aliases.data}
        columns={columns}
        rowId={(a) => a.name}
        loading={aliases.isLoading}
        error={aliases.error}
        onRefresh={() => aliases.mutate()}
        refreshing={aliases.isValidating}
        selection="single"
        selected={selected}
        onSelectedChange={setSelected}
        noSearch
        defaultSort={{ id: "name" }}
        actions={
          <>
            <ActionsMenu items={items} disabled={!sel} />
            <Button size="sm" onClick={() => setDialog({ alias: null })}>
              <Plus /> Create alias
            </Button>
          </>
        }
        empty={
          <EmptyState
            icon={Tag}
            title="No aliases"
            description="Create an alias such as prod or live that points to a published version."
            action={
              <Button size="sm" onClick={() => setDialog({ alias: null })}>
                <Plus /> Create alias
              </Button>
            }
          />
        }
      />
      <AliasDialog state={dialog} onClose={() => setDialog(null)} fn={fn} />
      <ConfirmDialog
        open={!!deleting}
        onOpenChange={(o) => !o && setDeleting(null)}
        title={`Delete alias ${deleting?.name ?? ""}?`}
        description="Callers that invoke the alias start getting errors. Its asynchronous invocation settings and permissions are removed; the versions stay."
        onConfirm={async () => {
          if (!deleting) return
          await api.del(`${fnPath(fn.name)}/aliases/${seg(deleting.name)}`)
          toast.success(`Deleted alias ${deleting.name}`)
          setSelected([])
          await revalidate(LAMBDA_PATH)
        }}
      />
    </>
  )
}

const ALIAS_RE = /^(?!^[0-9]+$)[a-zA-Z0-9_-]{1,128}$/

function AliasDialog({ state, onClose, fn }: { state: { alias: LambdaAlias | null } | null; onClose: () => void; fn: LambdaFunction }) {
  const open = !!state
  const editing = state?.alias ?? null
  const versions = useVersions(fn.name, open)
  const [name, setName] = useState("")
  const [description, setDescription] = useState("")
  const [version, setVersion] = useState("")
  const [weighted, setWeighted] = useState(false)
  const [extra, setExtra] = useState("")
  const [weight, setWeight] = useState("10")
  const [touched, setTouched] = useState(false)
  const [pending, setPending] = useState(false)

  useEffect(() => {
    if (!state) return
    const a = state.alias
    const w = Object.entries(a?.additional_version_weights ?? {})[0]
    setName(a?.name ?? "")
    setDescription(a?.description ?? "")
    setVersion(a?.function_version ?? "")
    setWeighted(!!w)
    setExtra(w?.[0] ?? "")
    setWeight(w ? String(Math.round(w[1] * 1000) / 10) : "10")
    setTouched(false)
  }, [state])

  const published = (versions.data ?? []).filter((v) => v.version !== LATEST)
  const w = Number(weight)
  const errors: Record<string, string> = {}
  if (!editing && (!ALIAS_RE.test(name) || name === LATEST)) errors.name = "1-128 letters, digits, - or _, not only digits"
  if (!version) errors.version = "Choose a version"
  if (weighted) {
    if (version === LATEST) errors.version = "Weighted aliases must point to a published version"
    if (!extra) errors.extra = "Choose the additional version"
    else if (extra === version) errors.extra = "Choose a different version"
    if (!Number.isFinite(w) || w < 0 || w > 100) errors.weight = "0-100%"
  }
  const valid = Object.keys(errors).length === 0
  const e = (k: string) => (touched ? errors[k] : undefined)

  const submit = async (ev: React.FormEvent) => {
    ev.preventDefault()
    setTouched(true)
    if (!valid) return
    setPending(true)
    const body = {
      function_version: version,
      description,
      additional_version_weights: weighted ? { [extra]: Math.round(w * 10) / 1000 } : {},
    }
    try {
      if (editing) {
        await api.patch(`${fnPath(fn.name)}/aliases/${seg(editing.name)}`, { ...body, revision_id: editing.revision_id })
        toast.success(`Updated alias ${editing.name}`)
      } else {
        await api.post(`${fnPath(fn.name)}/aliases`, { ...body, name })
        toast.success(`Created alias ${name}`)
      }
      await revalidate(LAMBDA_PATH)
      onClose()
    } catch (err) {
      toast.error(errorMessage(err))
    } finally {
      setPending(false)
    }
  }

  return (
    <Dialog open={open} onOpenChange={(o) => !o && !pending && onClose()}>
      <DialogContent className="sm:max-w-lg">
        <form onSubmit={submit} className="flex flex-col gap-5">
          <DialogHeader>
            <DialogTitle>{editing ? `Edit alias ${editing.name}` : "Create alias"}</DialogTitle>
            <DialogDescription>Invoke {fn.name}:alias, or use the alias ARN in triggers, to run the version it points to.</DialogDescription>
          </DialogHeader>
          {!editing && (
            <Field label="Name" htmlFor="alias-name" error={e("name")}>
              <Input id="alias-name" value={name} onChange={(ev) => setName(ev.target.value)} placeholder="prod" className="font-mono" autoFocus spellCheck={false} />
            </Field>
          )}
          <Field label="Description" htmlFor="alias-desc" optional>
            <Input id="alias-desc" value={description} onChange={(ev) => setDescription(ev.target.value)} maxLength={256} />
          </Field>
          <Field label="Version" htmlFor="alias-version" error={e("version")}>
            <Select value={version} onValueChange={setVersion} disabled={!versions.data}>
              <SelectTrigger id="alias-version" className="w-full">
                <SelectValue placeholder={versions.data ? "Choose a version" : "Loading versions..."} />
              </SelectTrigger>
              <SelectContent>
                {(versions.data ?? []).map((v) => (
                  <SelectItem key={v.version} value={v.version ?? LATEST}>
                    <span className="font-mono">{v.version}</span>
                    {v.version_description && <span className="text-muted-foreground text-xs">{v.version_description}</span>}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </Field>
          <div className="flex flex-col gap-3 rounded-md border p-3">
            <label className="flex items-start justify-between gap-4">
              <span>
                <span className="text-sm font-medium">Weighted alias</span>
                <span className="text-muted-foreground block text-xs">Send a share of invocations to a second published version, e.g. for a canary.</span>
              </span>
              <Switch checked={weighted} onCheckedChange={setWeighted} disabled={published.length < 2} aria-label="Weighted alias" />
            </label>
            {published.length < 2 && <p className="text-muted-foreground text-xs">Publish at least two versions to split traffic.</p>}
            {weighted && (
              <div className="grid grid-cols-1 gap-3 sm:grid-cols-[minmax(0,1fr)_8rem]">
                <Field label="Additional version" htmlFor="alias-extra" error={e("extra")}>
                  <Select value={extra} onValueChange={setExtra}>
                    <SelectTrigger id="alias-extra" className="w-full">
                      <SelectValue placeholder="Version" />
                    </SelectTrigger>
                    <SelectContent>
                      {published
                        .filter((v) => v.version !== version)
                        .map((v) => (
                          <SelectItem key={v.version} value={v.version!}>
                            <span className="font-mono">{v.version}</span>
                          </SelectItem>
                        ))}
                    </SelectContent>
                  </Select>
                </Field>
                <Field label="Weight (%)" htmlFor="alias-weight" error={e("weight")}>
                  <Input id="alias-weight" type="number" min={0} max={100} step={0.1} value={weight} onChange={(ev) => setWeight(ev.target.value)} />
                </Field>
                {Number.isFinite(w) && w >= 0 && w <= 100 && extra && version && (
                  <p className="text-muted-foreground text-xs sm:col-span-2">
                    {Math.round((100 - w) * 10) / 10}% to version {version}, {w}% to version {extra}.
                  </p>
                )}
              </div>
            )}
          </div>
          <DialogFooter>
            <Button type="button" variant="outline" onClick={onClose} disabled={pending}>
              Cancel
            </Button>
            <Button type="submit" disabled={pending}>
              {pending && <Loader2 className="animate-spin" />}
              {editing ? "Save" : "Create alias"}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}
