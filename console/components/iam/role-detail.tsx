"use client"

import Link from "next/link"
import { useRouter } from "next/navigation"
import { useEffect, useMemo, useState } from "react"
import { AlertTriangle, Eye, EyeOff, KeyRound, Loader2, Pencil, Plus, ShieldCheck, ShieldOff, Trash2 } from "lucide-react"
import { toast } from "sonner"

import { Alert, AlertDescription } from "@/components/ui/alert"
import { Button } from "@/components/ui/button"
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog"
import { Input } from "@/components/ui/input"
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select"
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs"
import { Textarea } from "@/components/ui/textarea"
import { ActionsMenu } from "@/components/console/actions-menu"
import { useSession } from "@/components/console/auth"
import { ConfirmDialog } from "@/components/console/confirm-dialog"
import { CopyButton, CopyableText } from "@/components/console/copy-button"
import { CodeBlock } from "@/components/console/code-block"
import { CellLink, CellText, DataTable, type Column } from "@/components/console/data-table"
import { EmptyState } from "@/components/console/empty-state"
import { ErrorState } from "@/components/console/error-state"
import { Field } from "@/components/console/form-field"
import { JsonEditor, jsonError } from "@/components/console/json-editor"
import { KeyValueGrid } from "@/components/console/key-value"
import { DetailSkeleton } from "@/components/console/loading"
import { PageHeader } from "@/components/console/page-header"
import { Section } from "@/components/console/section"
import { StatusBadge } from "@/components/console/status-badge"
import { TimeAgo } from "@/components/console/time-ago"
import { API_BASE, api, errorMessage, seg } from "@/lib/api"
import { formatDate, pluralize } from "@/lib/format"
import { revalidate, useApi, useQueryParam, useSetQueryParam } from "@/lib/hooks"
import type { Health, IamRole, PolicySummary, TempCredentials } from "@/lib/types"

import { AttachPoliciesDialog, runEach } from "./dialogs"
import { IAM, LINK, PolicyTypeBadge, SecretValue, instanceProfileHref, nameError, policyHref, policyJson, policyNameFromArn } from "./common"
import { PermissionsBoundarySection } from "./boundary"
import { InlinePoliciesSection } from "./inline-policies-section"
import { SESSION_DURATIONS, formatSessionDuration, trustedEntities, validateTrustPolicy } from "./role-common"
import { DeleteRolesDialog } from "./roles-list"
import { TagsSection } from "./tags-section"

const TABS = ["permissions", "trust", "sessions", "tags"] as const

export function RoleDetail() {
  const name = useQueryParam("name")
  const tabParam = useQueryParam("tab")
  const setParam = useSetQueryParam()
  const router = useRouter()
  const tab = (TABS as readonly string[]).includes(tabParam) ? tabParam : "permissions"

  const path = name ? `${IAM}/roles/${seg(name)}` : null
  const { data: role, error, isLoading, mutate } = useApi<IamRole>(path)
  const policiesQ = useApi<PolicySummary[]>(name ? `${IAM}/policies` : null)
  const [confirmDelete, setConfirmDelete] = useState(false)
  const [editOpen, setEditOpen] = useState(false)

  const refresh = () => {
    mutate()
    revalidate(IAM)
  }

  const crumbs = [{ label: "IAM", href: "/iam/" }, { label: "Roles", href: "/iam/roles/" }, { label: name || "Role" }]

  if (!name) return <ErrorState error={new Error("No role name given. Open a role from the Roles list.")} />
  if (isLoading && !role)
    return (
      <div className="flex flex-col gap-4">
        <PageHeader title={name} breadcrumbs={crumbs} />
        <DetailSkeleton />
      </div>
    )
  if (error || !role)
    return (
      <div className="flex flex-col gap-4">
        <PageHeader title={name} breadcrumbs={crumbs} />
        <ErrorState error={error ?? new Error("Role not found")} onRetry={() => mutate()} />
      </div>
    )

  const inlineCount = Object.keys(role.inline_policies ?? {}).length

  return (
    <div className="flex flex-col gap-4">
      <PageHeader
        title={role.name}
        breadcrumbs={crumbs}
        badge={role.service_linked ? <StatusBadge status="service-linked" tone="info" label="Service-linked role" /> : undefined}
        actions={
          <>
            <ActionsMenu
              items={[
                { label: "Assume role", icon: <KeyRound />, onSelect: () => setParam("tab", "sessions") },
                { separator: true },
                { label: "Delete role", icon: <Trash2 />, destructive: true, onSelect: () => setConfirmDelete(true) },
              ]}
            />
            <Button size="sm" onClick={() => setEditOpen(true)}>
              <Pencil /> Edit
            </Button>
          </>
        }
      />

      <Section title="Summary">
        <KeyValueGrid
          columns={3}
          items={[
            { label: "ARN", value: <CopyableText value={role.arn} className="text-[13px]" />, wide: true },
            { label: "Description", value: role.description, wide: true },
            { label: "Created", value: formatDate(role.created_at) },
            { label: "Last activity", value: role.last_used ? <TimeAgo value={role.last_used} /> : "Never" },
            { label: "Maximum session duration", value: formatSessionDuration(role.max_session_duration) },
            { label: "Role ID", value: <CopyableText value={role.id} className="text-[13px]" /> },
            { label: "Path", value: <span className="font-mono text-[13px]">{role.path}</span> },
            { label: "Permissions", value: `${pluralize(role.attached_policies.length, "managed policy", "managed policies")}, ${inlineCount} inline` },
            {
              label: "Permissions boundary",
              value: role.permissions_boundary ? (
                <Link href={policyHref(policyNameFromArn(role.permissions_boundary))} className={LINK}>
                  {policyNameFromArn(role.permissions_boundary)}
                </Link>
              ) : (
                "Not set"
              ),
            },
            {
              label: "Instance profiles",
              value: role.instance_profiles?.length ? (
                <span className="flex flex-wrap gap-x-2">
                  {role.instance_profiles.map((p) => (
                    <Link key={p} href={instanceProfileHref(p)} className={LINK}>
                      {p}
                    </Link>
                  ))}
                </span>
              ) : role.trusted_services?.includes("ec2.amazonaws.com") ? (
                <span>
                  None{" "}
                  <Link href={`/iam/instance-profiles/?create=1&role=${encodeURIComponent(role.name)}`} className="text-primary hover:underline">
                    (create one)
                  </Link>
                </span>
              ) : (
                "None"
              ),
            },
          ]}
        />
      </Section>

      <Tabs value={tab} onValueChange={(v) => setParam("tab", v === "permissions" ? null : v)}>
        <TabsList className="max-w-full overflow-x-auto">
          <TabsTrigger value="permissions">Permissions</TabsTrigger>
          <TabsTrigger value="trust">Trust relationships</TabsTrigger>
          <TabsTrigger value="sessions">Sessions</TabsTrigger>
          <TabsTrigger value="tags">Tags</TabsTrigger>
        </TabsList>
        <TabsContent value="permissions">
          <PermissionsTab role={role} policies={policiesQ.data} loading={policiesQ.isLoading} onChanged={refresh} />
        </TabsContent>
        <TabsContent value="trust">
          <TrustTab role={role} onChanged={refresh} />
        </TabsContent>
        <TabsContent value="sessions">
          <SessionsTab role={role} onChanged={refresh} />
        </TabsContent>
        <TabsContent value="tags">
          <TagsSection
            tags={role.tags}
            noun="roles"
            onSave={async (tags) => {
              // PATCH with tags replaces the whole set (TagRole + UntagRole).
              await api.patch(`${IAM}/roles/${seg(role.name)}`, { tags })
              refresh()
            }}
          />
        </TabsContent>
      </Tabs>

      <DeleteRolesDialog
        open={confirmDelete}
        onOpenChange={setConfirmDelete}
        roles={[role]}
        onDeleted={() => {
          revalidate(IAM)
          router.push("/iam/roles/")
        }}
      />
      <EditRoleDialog open={editOpen} onOpenChange={setEditOpen} role={role} onSaved={refresh} />
    </div>
  )
}

function EditRoleDialog({ open, onOpenChange, role, onSaved }: { open: boolean; onOpenChange: (o: boolean) => void; role: IamRole; onSaved: () => void }) {
  const [description, setDescription] = useState("")
  const [maxSession, setMaxSession] = useState(3600)
  const [pending, setPending] = useState(false)
  useEffect(() => {
    if (open) {
      setDescription(role.description)
      setMaxSession(role.max_session_duration)
    }
  }, [open, role])
  const durations = SESSION_DURATIONS.some((d) => d.value === role.max_session_duration)
    ? SESSION_DURATIONS
    : [...SESSION_DURATIONS, { value: role.max_session_duration, label: formatSessionDuration(role.max_session_duration) }].sort((a, b) => a.value - b.value)

  const save = async () => {
    setPending(true)
    try {
      await api.patch(`${IAM}/roles/${seg(role.name)}`, { description, max_session_duration: maxSession })
      toast.success(`Role ${role.name} updated`)
      onSaved()
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
          <DialogTitle>Edit {role.name}</DialogTitle>
          <DialogDescription>Change the role&apos;s description and maximum session duration.</DialogDescription>
        </DialogHeader>
        <Field label="Description" htmlFor="edit-role-desc" optional>
          <Textarea id="edit-role-desc" rows={3} value={description} onChange={(e) => setDescription(e.target.value)} maxLength={1000} />
        </Field>
        <Field label="Maximum session duration" help="Applies to credentials issued from now on.">
          <Select value={String(maxSession)} onValueChange={(v) => setMaxSession(Number(v))}>
            <SelectTrigger className="w-full sm:w-56" aria-label="Maximum session duration">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {durations.map((d) => (
                <SelectItem key={d.value} value={String(d.value)}>
                  {d.label}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </Field>
        <DialogFooter>
          <Button variant="outline" onClick={() => onOpenChange(false)} disabled={pending}>
            Cancel
          </Button>
          <Button onClick={save} disabled={pending}>
            {pending && <Loader2 className="animate-spin" />} Save changes
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

// ---- permissions ----

interface PermRow {
  policy: string
  managed: boolean | undefined
  description: string
}

function PermissionsTab({ role, policies, loading, onChanged }: { role: IamRole; policies: PolicySummary[] | undefined; loading: boolean; onChanged: () => void }) {
  const [selected, setSelected] = useState<string[]>([])
  const [attachOpen, setAttachOpen] = useState(false)
  const [confirmDetach, setConfirmDetach] = useState(false)
  const base = `${IAM}/roles/${seg(role.name)}`

  const rows = useMemo<PermRow[]>(() => {
    const byName = new Map((policies ?? []).map((p) => [p.name, p]))
    return role.attached_policies.map((p) => ({ policy: p, managed: byName.get(p)?.managed, description: byName.get(p)?.description ?? "" }))
  }, [role.attached_policies, policies])

  const columns: Column<PermRow>[] = [
    {
      id: "policy",
      header: "Policy name",
      value: (r) => r.policy,
      cell: (r) => <CellLink href={policyHref(r.policy)}>{r.policy}</CellLink>,
    },
    { id: "type", header: "Type", value: (r) => (r.managed ? "AWS managed" : "Customer managed"), cell: (r) => (r.managed === undefined ? "-" : <PolicyTypeBadge managed={r.managed} />), hideBelow: "sm" },
    { id: "desc", header: "Description", value: (r) => r.description, cell: (r) => <CellText muted>{r.description}</CellText>, hideBelow: "lg" },
  ]

  return (
    <div className="flex flex-col gap-6">
      <DataTable
        title="Permissions policies"
        description="Managed policies attached to this role. Whoever assumes the role gets these permissions."
        data={loading ? undefined : rows}
        loading={loading}
        columns={columns}
        rowId={(r) => r.policy}
        selection="multi"
        selected={selected}
        onSelectedChange={setSelected}
        searchPlaceholder="Filter policies"
        actions={
          <>
            <ActionsMenu disabled={!selected.length} items={[{ label: "Remove", icon: <Trash2 />, destructive: true, onSelect: () => setConfirmDetach(true) }]} />
            <Button size="sm" onClick={() => setAttachOpen(true)}>
              <Plus /> Add permissions
            </Button>
          </>
        }
        empty={
          <EmptyState
            icon={ShieldCheck}
            title="No permissions policies"
            description="Sessions of this role cannot do anything until you attach a policy or add an inline policy."
            action={
              <Button size="sm" onClick={() => setAttachOpen(true)}>
                <Plus /> Add permissions
              </Button>
            }
          />
        }
      />

      <InlinePoliciesSection kind="role" owner={role.name} policies={role.inline_policies} onChanged={onChanged} />

      <PermissionsBoundarySection
        kind="role"
        arn={role.permissions_boundary}
        onSet={async (arn) => {
          const p = `${IAM}/roles/${seg(role.name)}/permissions-boundary`
          await (arn ? api.put(p, { policy: arn }) : api.del(p))
          onChanged()
        }}
      />

      <AttachPoliciesDialog
        open={attachOpen}
        onOpenChange={setAttachOpen}
        title={`Add permissions to ${role.name}`}
        description="Attach managed policies to the role. Active sessions get the new permissions immediately."
        exclude={role.attached_policies}
        onAttach={async (names) => {
          const n = await runEach(names, (p) => api.post(`${base}/policies`, { policy: p }))
          if (n) toast.success(`${pluralize(n, "policy", "policies")} attached to ${role.name}`)
          onChanged()
          return n === names.length
        }}
      />

      <ConfirmDialog
        open={confirmDetach}
        onOpenChange={setConfirmDetach}
        title={selected.length === 1 ? `Remove ${selected[0]}?` : `Remove ${selected.length} policies?`}
        description={<>The role loses the permissions granted by {selected.join(", ")}. The policies themselves are not deleted.</>}
        actionLabel="Remove"
        onConfirm={async () => {
          for (const p of selected) await api.del(`${base}/policies/${seg(p)}`)
          toast.success(`${pluralize(selected.length, "policy", "policies")} removed from ${role.name}`)
          setSelected([])
          onChanged()
        }}
      />


    </div>
  )
}

// ---- trust ----

function TrustTab({ role, onChanged }: { role: IamRole; onChanged: () => void }) {
  const [editing, setEditing] = useState(false)
  const [text, setText] = useState("")
  const [saving, setSaving] = useState(false)
  const json = policyJson(role.assume_role_policy)
  const entities = trustedEntities(role.assume_role_policy)
  const err = editing ? (jsonError(text) ?? validateTrustPolicy(JSON.parse(text))) : null

  const save = async () => {
    if (err) return
    setSaving(true)
    try {
      await api.put(`${IAM}/roles/${seg(role.name)}/trust-policy`, JSON.parse(text))
      toast.success(`Trust policy of ${role.name} updated`)
      setEditing(false)
      onChanged()
    } catch (e) {
      toast.error(errorMessage(e))
    } finally {
      setSaving(false)
    }
  }

  return (
    <div className="flex flex-col gap-4">
      <Section title="Trusted entities" description="Entities that can assume this role under the conditions of the trust policy.">
        {entities.length ? (
          <ul className="flex flex-col gap-2 text-sm">
            {entities.map((e) => (
              <li key={`${e.kind}:${e.value}`} className="flex flex-col gap-0.5 sm:flex-row sm:items-center sm:gap-3">
                <span className="font-medium">{e.label}</span>
                {e.label !== e.value && <span className="text-muted-foreground font-mono text-xs break-all">{e.value}</span>}
              </li>
            ))}
          </ul>
        ) : (
          <p className="text-muted-foreground text-sm">The trust policy does not allow anyone to assume this role.</p>
        )}
      </Section>
      <Section
        title="Trust policy"
        actions={
          editing ? (
            <>
              <Button size="sm" variant="outline" onClick={() => setEditing(false)} disabled={saving}>
                Cancel
              </Button>
              <Button size="sm" onClick={save} disabled={saving || !!err}>
                {saving && <Loader2 className="animate-spin" />} Update policy
              </Button>
            </>
          ) : (
            <>
              <CopyButton value={json} size="sm" label="Copy JSON" toastMessage="Trust policy copied" />
              <Button
                size="sm"
                variant="outline"
                onClick={() => {
                  setText(json)
                  setEditing(true)
                }}
              >
                <Pencil /> Edit trust policy
              </Button>
            </>
          )
        }
      >
        {editing ? (
          <div className="flex flex-col gap-2">
            <p className="text-muted-foreground text-xs">
              Each statement needs an Effect, a Principal (&quot;*&quot; or an object with AWS, Service or Federated) and sts: actions. Resource is not allowed. Active sessions are not affected.
            </p>
            <JsonEditor value={text} onChange={setText} validate={validateTrustPolicy} rows={16} />
          </div>
        ) : (
          <JsonEditor value={json} readOnly rows={Math.min(json.split("\n").length, 30)} />
        )}
      </Section>
    </div>
  )
}

// ---- sessions ----

function endpoint(): string {
  if (API_BASE) return API_BASE
  return typeof window === "undefined" ? "" : window.location.origin
}

function SessionsTab({ role, onChanged }: { role: IamRole; onChanged: () => void }) {
  const session = useSession()
  const health = useApi<Health>("/api/v1/health")
  const region = health.data?.region ?? "us-east-1"
  const [confirmRevoke, setConfirmRevoke] = useState(false)
  const [sessionName, setSessionName] = useState("")
  const [duration, setDuration] = useState(3600)
  const [touched, setTouched] = useState(false)
  const [pending, setPending] = useState(false)
  const [creds, setCreds] = useState<TempCredentials | null>(null)
  const [showSnippet, setShowSnippet] = useState(false)

  useEffect(() => {
    setSessionName((s) => s || `console-${session.user.name}`.replace(/[^\w+=,.@-]/g, "-").slice(0, 64))
  }, [session.user.name])

  const durations = [{ value: 900, label: "15 minutes" }, ...SESSION_DURATIONS].filter((d) => d.value <= role.max_session_duration)
  const snErr = nameError("session", sessionName) ?? (sessionName.length < 2 ? "Use at least 2 characters." : null)
  const trusted = trustedEntities(role.assume_role_policy)
  const userTrusted = trusted.some((e) => e.kind === "any" || e.kind === "account" || e.value === session.user.arn)

  const assume = async () => {
    setTouched(true)
    if (snErr) return
    setPending(true)
    try {
      const c = await api.post<TempCredentials>("/api/v1/sts/assume-role", { role: role.name, session_name: sessionName, duration_seconds: duration })
      setCreds(c)
      setShowSnippet(false)
      toast.success(`Assumed ${role.name}`)
      onChanged()
    } catch (e) {
      toast.error(errorMessage(e))
    } finally {
      setPending(false)
    }
  }

  const snippet = creds
    ? [
        `export AWS_ENDPOINT_URL=${endpoint()}`,
        `export AWS_REGION=${region}`,
        `export AWS_ACCESS_KEY_ID=${creds.access_key_id}`,
        `export AWS_SECRET_ACCESS_KEY=${creds.secret_access_key}`,
        `export AWS_SESSION_TOKEN=${creds.session_token}`,
      ].join("\n")
    : ""
  const masked = creds ? snippet.replace(creds.secret_access_key, "•".repeat(16)).replace(creds.session_token, "•".repeat(24)) : ""

  return (
    <div className="flex flex-col gap-4">
      <Section
        title="Assume role"
        description="Test the role: request temporary credentials for it as the signed-in user, the way sts:AssumeRole does for the AWS CLI and SDKs."
      >
        <div className="flex flex-col gap-4">
          {!userTrusted && (
            <Alert variant="warning">
              <AlertTriangle />
              <AlertDescription>
                The trust policy does not name this account or {session.user.name}, so the request is likely to be denied. Service roles are assumed by their service, not by users.
              </AlertDescription>
            </Alert>
          )}
          <div className="grid gap-4 sm:grid-cols-[minmax(0,1fr)_14rem]">
            <Field label="Session name" htmlFor="session-name" error={touched ? snErr : null} help="Shows in the assumed-role ARN and in CloudTrail. 2-64 characters.">
              <Input id="session-name" value={sessionName} onChange={(e) => setSessionName(e.target.value.trim())} aria-invalid={touched && !!snErr} />
            </Field>
            <Field label="Duration">
              <Select value={String(duration)} onValueChange={(v) => setDuration(Number(v))}>
                <SelectTrigger className="w-full" aria-label="Duration">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {durations.map((d) => (
                    <SelectItem key={d.value} value={String(d.value)}>
                      {d.label}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </Field>
          </div>
          <div>
            <Button onClick={assume} disabled={pending}>
              {pending ? <Loader2 className="animate-spin" /> : <KeyRound />} Assume role
            </Button>
          </div>

          {creds && (
            <div className="bg-muted/40 flex flex-col gap-4 rounded-lg border p-4">
              <Alert variant="warning">
                <AlertTriangle />
                <AlertDescription>
                  <span>
                    These credentials are shown only once. Copy them now; they expire <TimeAgo value={creds.expiration} className="font-medium" /> ({formatDate(creds.expiration)}).
                  </span>
                </AlertDescription>
              </Alert>
              <KeyValueGrid
                columns={2}
                items={[
                  ...(creds.assumed_role_arn ? [{ label: "Assumed role ARN", value: <CopyableText value={creds.assumed_role_arn} className="text-[13px]" />, wide: true }] : []),
                  { label: "Access key ID", value: <CopyableText value={creds.access_key_id} className="text-[13px]" />, wide: true },
                  { label: "Secret access key", value: <SecretValue value={creds.secret_access_key} label="Secret access key" />, wide: true },
                  { label: "Session token", value: <SecretValue value={creds.session_token} label="Session token" />, wide: true },
                ]}
              />
              <CodeBlock
                title="Shell (AWS CLI and SDKs)"
                code={snippet}
                wrap
                actions={
                  <Button
                    type="button"
                    size="icon"
                    variant="ghost"
                    className="text-code-muted size-7 hover:bg-white/10 hover:text-white"
                    onClick={() => setShowSnippet(!showSnippet)}
                    aria-label={showSnippet ? "Hide secrets" : "Show secrets"}
                  >
                    {showSnippet ? <EyeOff /> : <Eye />}
                  </Button>
                }
              >
                {(showSnippet ? snippet : masked).split("\n").map((l, i) => (
                  <div key={i} className="break-all whitespace-pre-wrap">
                    {l}
                  </div>
                ))}
              </CodeBlock>
              <div className="flex justify-end">
                <Button size="sm" variant="outline" onClick={() => setCreds(null)}>
                  Done
                </Button>
              </div>
            </div>
          )}
        </div>
      </Section>

      <Section
        title="Revoke active sessions"
        description="Immediately invalidate every temporary credential issued for this role, for users and services alike. Services assume the role again on their next call."
      >
        <div className="flex flex-col gap-3 text-sm">
          <p className="text-muted-foreground">
            Last activity: {role.last_used ? <TimeAgo value={role.last_used} className="text-foreground" /> : <span className="text-foreground">never assumed</span>}
          </p>
          <div>
            <Button variant="outline" onClick={() => setConfirmRevoke(true)}>
              <ShieldOff /> Revoke active sessions
            </Button>
          </div>
        </div>
      </Section>

      <ConfirmDialog
        open={confirmRevoke}
        onOpenChange={setConfirmRevoke}
        title={`Revoke active sessions of ${role.name}?`}
        description="Every set of temporary credentials issued for this role stops working immediately. Anyone who can still assume the role can request new credentials."
        actionLabel="Revoke active sessions"
        onConfirm={async () => {
          await api.post(`${IAM}/roles/${seg(role.name)}/revoke-sessions`)
          toast.success(`Active sessions of ${role.name} revoked`)
          setCreds(null)
          onChanged()
        }}
      />
    </div>
  )
}

