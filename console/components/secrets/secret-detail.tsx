"use client"

import { useEffect, useState } from "react"
import Link from "next/link"
import { useRouter } from "next/navigation"
import { AlertTriangle, Check, EyeOff, KeyRound, Loader2, Pencil, RefreshCw, RotateCcw, Trash2, X } from "lucide-react"
import { toast } from "sonner"

import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { CopyButton, CopyableText } from "@/components/console/copy-button"
import { EmptyState } from "@/components/console/empty-state"
import { ErrorState } from "@/components/console/error-state"
import { KeyValueGrid } from "@/components/console/key-value"
import { DetailSkeleton } from "@/components/console/loading"
import { PageHeader } from "@/components/console/page-header"
import { Section } from "@/components/console/section"
import { StatusBadge } from "@/components/console/status-badge"
import { TagList, TagsEditor, tagsToRows, type TagRow } from "@/components/console/tags-editor"
import { TimeAgo } from "@/components/console/time-ago"
import { CodeBlock } from "@/components/s3/common"
import { api, apiUrl, errorMessage, seg } from "@/lib/api"
import { formatDate } from "@/lib/format"
import { revalidate, useAction, useApi, useQueryParam } from "@/lib/hooks"
import type { Secret, SecretValue } from "@/lib/types"
import { cn } from "@/lib/utils"

import { DeleteSecretDialog, ManagedWarning } from "./delete-secret-dialog"
import { ManagedBadge } from "./secret-list"
import { draftError, draftFromValue, draftValue, ModeToggle, parseKeyValue, SecretValueEditor, type SecretDraft, type SecretMode } from "./value-editor"

function StageBadges({ stages }: { stages: string[] }) {
  if (!stages.length) return <span className="text-muted-foreground">-</span>
  return (
    <div className="flex flex-wrap gap-1">
      {stages.map((s) => (
        <StatusBadge key={s} status={s} label={s} tone={s === "AWSCURRENT" ? "success" : "neutral"} className="font-mono normal-case" />
      ))}
    </div>
  )
}

function DescriptionField({ secret, onSaved }: { secret: Secret; onSaved: () => void }) {
  const [editing, setEditing] = useState(false)
  const [value, setValue] = useState(secret.description)
  const { pending, run } = useAction()
  useEffect(() => setValue(secret.description), [secret.description, editing])

  const save = async () => {
    const ok = await run(async () => {
      await api.patch(`/api/v1/secrets/${seg(secret.name)}`, { description: value })
      return true
    }, "Description updated")
    if (ok) {
      setEditing(false)
      onSaved()
    }
  }

  if (!editing)
    return (
      <span className="group inline-flex items-start gap-1">
        <span className={cn(!secret.description && "text-muted-foreground")}>{secret.description || "No description"}</span>
        <button
          type="button"
          onClick={() => setEditing(true)}
          aria-label="Edit description"
          className="text-muted-foreground hover:text-foreground hover:bg-accent inline-flex size-6 shrink-0 items-center justify-center rounded"
        >
          <Pencil className="size-3.5" />
        </button>
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

function ValueView({ value, mode }: { value: SecretValue; mode: SecretMode }) {
  const rows = parseKeyValue(value.value)
  if (mode === "kv" && rows) {
    return (
      <div className="overflow-x-auto rounded-md border">
        <table className="w-full text-sm">
          <thead>
            <tr className="bg-muted/40 text-muted-foreground border-b text-left text-xs">
              <th className="px-3 py-2 font-semibold">Secret key</th>
              <th className="px-3 py-2 font-semibold">Secret value</th>
            </tr>
          </thead>
          <tbody>
            {rows.map((r) => (
              <tr key={r.key} className="border-b last:border-0">
                <td className="px-3 py-2 align-top font-mono text-[13px] font-medium">{r.key}</td>
                <td className="px-3 py-2">
                  <span className="flex items-start gap-1">
                    <span className="min-w-0 font-mono text-[13px] break-all whitespace-pre-wrap">{r.value}</span>
                    <CopyButton value={r.value} label={`Copy ${r.key}`} />
                  </span>
                </td>
              </tr>
            ))}
            {rows.length === 0 && (
              <tr>
                <td colSpan={2} className="text-muted-foreground px-3 py-2">
                  Empty JSON object
                </td>
              </tr>
            )}
          </tbody>
        </table>
      </div>
    )
  }
  let text = value.value
  if (rows) {
    try {
      text = JSON.stringify(JSON.parse(value.value), null, 2)
    } catch {
      // keep as-is
    }
  }
  return <CodeBlock code={text} className="max-h-96 overflow-y-auto" />
}

function SecretValueSection({ secret, onChanged, viewVersion, onViewVersionDone }: { secret: Secret; onChanged: () => void; viewVersion: string | null; onViewVersionDone: () => void }) {
  const [value, setValue] = useState<SecretValue | null>(null)
  const [mode, setMode] = useState<SecretMode>("kv")
  const [loading, setLoading] = useState(false)
  const [editing, setEditing] = useState(false)
  const [draft, setDraft] = useState<SecretDraft>({ mode: "plain", rows: [], text: "" })
  const [submitted, setSubmitted] = useState(false)
  const [saving, setSaving] = useState(false)
  const scheduled = !!secret.deletion_date

  const retrieve = async (versionId?: string) => {
    setLoading(true)
    try {
      const v = await api.get<SecretValue>(`/api/v1/secrets/${seg(secret.name)}/value`, versionId ? { version_id: versionId } : undefined)
      setValue(v)
      setMode(parseKeyValue(v.value) ? "kv" : "plain")
      onChanged() // last retrieved changed
      return v
    } catch (e) {
      toast.error(errorMessage(e))
      return null
    } finally {
      setLoading(false)
    }
  }

  // A "Retrieve" click in the versions table.
  useEffect(() => {
    if (!viewVersion) return
    setEditing(false)
    retrieve(viewVersion).then(onViewVersionDone)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [viewVersion])

  // Hide the value when leaving the page state (e.g. the secret was deleted).
  useEffect(() => {
    if (scheduled) {
      setValue(null)
      setEditing(false)
    }
  }, [scheduled])

  const startEdit = async () => {
    let v = value
    if (!v || !v.stages.includes("AWSCURRENT")) v = await retrieve()
    if (!v) return
    setDraft(draftFromValue(v.value))
    setSubmitted(false)
    setEditing(true)
  }

  const save = async () => {
    setSubmitted(true)
    if (draftError(draft)) return
    setSaving(true)
    try {
      await api.put(`/api/v1/secrets/${seg(secret.name)}/value`, { value: draftValue(draft) })
      toast.success("New secret version stored")
      setEditing(false)
      onChanged()
      await retrieve()
    } catch (e) {
      toast.error(errorMessage(e))
    } finally {
      setSaving(false)
    }
  }

  const isJson = value ? parseKeyValue(value.value) !== null : false
  const isCurrent = value?.stages.includes("AWSCURRENT")

  return (
    <Section
      title="Secret value"
      description={editing ? "Saving stores a new version labelled AWSCURRENT; the current one becomes AWSPREVIOUS." : "Retrieve and view the secret value."}
      actions={
        editing ? (
          <>
            <Button variant="outline" size="sm" onClick={() => setEditing(false)} disabled={saving}>
              Cancel
            </Button>
            <Button size="sm" onClick={save} disabled={saving}>
              {saving && <Loader2 className="animate-spin" />}
              Save
            </Button>
          </>
        ) : value ? (
          <>
            {isJson && <ModeToggle mode={mode} onChange={setMode} />}
            <Button variant="outline" size="sm" onClick={() => setValue(null)}>
              <EyeOff /> Hide
            </Button>
            <Button variant="outline" size="sm" onClick={startEdit} disabled={scheduled || loading}>
              <Pencil /> Edit
            </Button>
          </>
        ) : (
          <Button size="sm" onClick={() => retrieve()} disabled={loading || scheduled}>
            {loading ? <Loader2 className="animate-spin" /> : <KeyRound />}
            Retrieve secret value
          </Button>
        )
      }
    >
      {editing ? (
        <div className="flex flex-col gap-3">
          <ManagedWarning by={secret.managed_by} action="Changing" />
          <SecretValueEditor draft={draft} onChange={setDraft} showError={submitted} />
        </div>
      ) : value ? (
        <div className="flex flex-col gap-3">
          <div className="text-muted-foreground flex flex-wrap items-center gap-2 text-xs">
            <span>Version</span>
            <span className="text-foreground font-mono">{value.version_id}</span>
            <StageBadges stages={value.stages} />
            {!isCurrent && (
              <button type="button" className="text-primary hover:underline" onClick={() => retrieve()}>
                Show current version
              </button>
            )}
          </div>
          <ValueView value={value} mode={mode} />
        </div>
      ) : scheduled ? (
        <p className="text-muted-foreground text-sm">The value can't be retrieved while the secret is scheduled for deletion. Cancel the deletion first.</p>
      ) : (
        <p className="text-muted-foreground text-sm">
          The value is hidden. Choose <span className="text-foreground font-medium">Retrieve secret value</span> to view it; this updates the secret&apos;s last retrieved date.
        </p>
      )}
    </Section>
  )
}

function TagsSection({ secret, onSaved }: { secret: Secret; onSaved: () => void }) {
  const [editing, setEditing] = useState(false)
  const [rows, setRows] = useState<TagRow[]>([])
  const { pending, run } = useAction()

  const save = async () => {
    const tags: Record<string, string> = {}
    for (const r of rows) if (r.key.trim()) tags[r.key.trim()] = r.value
    const ok = await run(async () => {
      await api.patch(`/api/v1/secrets/${seg(secret.name)}`, { tags })
      return true
    }, "Tags saved")
    if (ok) {
      setEditing(false)
      onSaved()
    }
  }

  return (
    <Section
      title="Tags"
      actions={
        editing ? (
          <>
            <Button variant="outline" size="sm" onClick={() => setEditing(false)} disabled={pending}>
              Cancel
            </Button>
            <Button size="sm" onClick={save} disabled={pending}>
              {pending && <Loader2 className="animate-spin" />}
              Save
            </Button>
          </>
        ) : (
          <Button
            variant="outline"
            size="sm"
            onClick={() => {
              setRows(tagsToRows(secret.tags))
              setEditing(true)
            }}
          >
            <Pencil /> Edit tags
          </Button>
        )
      }
    >
      {editing ? <TagsEditor rows={rows} onChange={setRows} /> : <TagList tags={secret.tags} />}
    </Section>
  )
}

export function SecretDetail() {
  const name = useQueryParam("name")
  const router = useRouter()
  const path = name ? `/api/v1/secrets/${seg(name)}` : null
  const { data: secret, error, isLoading, isValidating, mutate } = useApi<Secret>(path)
  const [deleteOpen, setDeleteOpen] = useState(false)
  const [viewVersion, setViewVersion] = useState<string | null>(null)
  const [origin, setOrigin] = useState("")
  const { pending: restoring, run } = useAction()

  useEffect(() => {
    setOrigin(new URL(apiUrl("/"), window.location.origin).origin)
  }, [])

  const crumbs = [{ label: "Secrets Manager", href: "/secrets/" }, { label: "Secrets", href: "/secrets/" }, { label: name || "Secret" }]

  if (!name) {
    return (
      <div>
        <PageHeader title="Secret" breadcrumbs={crumbs} />
        <EmptyState
          icon={KeyRound}
          title="No secret selected"
          description="Choose a secret from the list."
          action={
            <Button size="sm" asChild>
              <Link href="/secrets/">View secrets</Link>
            </Button>
          }
        />
      </div>
    )
  }
  if (error && !secret) {
    return (
      <div>
        <PageHeader title={name} breadcrumbs={crumbs} />
        <ErrorState error={error} onRetry={() => mutate()} />
      </div>
    )
  }
  if (isLoading || !secret) return <DetailSkeleton />

  const refresh = () => {
    mutate()
    revalidate("/api/v1/secrets")
  }

  const restore = async () => {
    const ok = await run(async () => {
      await api.post(`/api/v1/secrets/${seg(secret.name)}/restore`)
      return true
    }, "Deletion cancelled")
    if (ok) refresh()
  }

  const curl = `curl -H "Authorization: Bearer $TOKEN" \\\n  ${origin}/api/v1/secrets/${seg(secret.name)}/value`

  return (
    <div className="flex flex-col gap-4">
      <PageHeader
        title={<span className="break-all">{secret.name}</span>}
        breadcrumbs={crumbs}
        className="mb-1"
        badge={
          <>
            {secret.managed_by && <ManagedBadge by={secret.managed_by} />}
            {secret.deletion_date && <StatusBadge status="scheduled for deletion" label="Scheduled for deletion" tone="danger" />}
          </>
        }
        actions={
          <>
            <Button variant="outline" size="sm" onClick={refresh} aria-label="Refresh">
              <RefreshCw className={cn(isValidating && "animate-spin")} />
            </Button>
            {secret.deletion_date ? (
              <Button size="sm" onClick={restore} disabled={restoring}>
                {restoring ? <Loader2 className="animate-spin" /> : <RotateCcw />} Cancel deletion
              </Button>
            ) : (
              <Button variant="outline" size="sm" onClick={() => setDeleteOpen(true)} className="text-destructive hover:text-destructive">
                <Trash2 /> Delete
              </Button>
            )}
          </>
        }
      />

      {secret.deletion_date && (
        <Alert variant="destructive" className="border-destructive/40 bg-destructive/5">
          <AlertTriangle />
          <AlertTitle>This secret is scheduled for deletion on {formatDate(secret.deletion_date)}</AlertTitle>
          <AlertDescription>
            <p>Until then its value can&apos;t be retrieved or changed. Cancel the deletion to use the secret again.</p>
            <Button size="sm" variant="outline" className="text-foreground mt-1" onClick={restore} disabled={restoring}>
              {restoring ? <Loader2 className="animate-spin" /> : <RotateCcw />} Cancel deletion
            </Button>
          </AlertDescription>
        </Alert>
      )}

      <Section title="Secret details">
        <KeyValueGrid
          items={[
            { label: "Secret name", value: <CopyableText value={secret.name} /> },
            { label: "Secret ARN", value: <CopyableText value={secret.arn} />, wide: false },
            { label: "Managed by", value: secret.managed_by ? <ManagedBadge by={secret.managed_by} /> : "" },
            { label: "Description", value: <DescriptionField secret={secret} onSaved={refresh} />, wide: true },
            { label: "Created", value: formatDate(secret.created_at) },
            { label: "Last changed", value: <TimeAgo value={secret.updated_at} /> },
            { label: "Last retrieved", value: secret.last_accessed ? <TimeAgo value={secret.last_accessed} /> : "Never" },
            ...(secret.deletion_date ? [{ label: "Deletion date", value: <span className="text-destructive">{formatDate(secret.deletion_date)}</span> }] : []),
          ]}
        />
      </Section>

      <SecretValueSection secret={secret} onChanged={() => mutate()} viewVersion={viewVersion} onViewVersionDone={() => setViewVersion(null)} />

      <Section title={`Versions (${secret.versions.length})`} description="The last 10 versions are kept." flush>
        <div className="overflow-x-auto">
          <table className="w-full text-sm">
            <thead>
              <tr className="bg-muted/40 text-muted-foreground border-b text-left text-xs">
                <th className="px-4 py-2 font-semibold">Version ID</th>
                <th className="px-3 py-2 font-semibold">Staging labels</th>
                <th className="hidden px-3 py-2 font-semibold sm:table-cell">Created</th>
                <th className="px-4 py-2" />
              </tr>
            </thead>
            <tbody>
              {secret.versions.map((v) => (
                <tr key={v.id} className="border-b last:border-0">
                  <td className="px-4 py-2">
                    <CopyableText value={v.id} />
                  </td>
                  <td className="px-3 py-2">
                    <StageBadges stages={v.stages} />
                  </td>
                  <td className="hidden px-3 py-2 whitespace-nowrap sm:table-cell">{formatDate(v.created_at)}</td>
                  <td className="px-4 py-2 text-right">
                    <Button variant="outline" size="sm" disabled={!!secret.deletion_date || viewVersion === v.id} onClick={() => setViewVersion(v.id)}>
                      Retrieve
                    </Button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      </Section>

      <TagsSection secret={secret} onSaved={refresh} />

      <Section title="Sample code" description="Retrieve the current value from scripts with the HomeCloud API. $TOKEN is an API or console session token.">
        <CodeBlock code={curl} />
      </Section>

      <DeleteSecretDialog
        secret={deleteOpen ? secret : null}
        onOpenChange={setDeleteOpen}
        onDeleted={(force) => {
          if (force) router.push("/secrets/")
          else mutate()
        }}
      />
    </div>
  )
}
