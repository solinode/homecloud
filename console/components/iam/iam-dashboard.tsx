"use client"

import Link from "next/link"
import { useEffect, useMemo, useState, type ReactNode } from "react"
import { AlertTriangle, ArrowRight, CheckCircle2, FileText, FlaskConical, Info, KeyRound, ShieldCheck, UserCog, UserPlus, Users, UsersRound } from "lucide-react"

import { Skeleton } from "@/components/ui/skeleton"
import { CopyableText } from "@/components/console/copy-button"
import { KeyValueGrid } from "@/components/console/key-value"
import { PageHeader } from "@/components/console/page-header"
import { Section } from "@/components/console/section"
import { useSession } from "@/components/console/auth"
import { useApi } from "@/lib/hooks"
import { pluralize } from "@/lib/format"
import type { IamGroup, IamRole, IamSummary, IamUser, PolicySummary } from "@/lib/types"
import { cn } from "@/lib/utils"

import { DAY, IAM, keyIdleDays, policyHref, signInUrl, useAccessKeysByUser, userHref } from "./common"
import { createRoleHref, roleHref } from "./role-common"

function StatCard({ href, icon: Icon, label, value, sub, loading }: { href: string; icon: typeof Users; label: string; value: ReactNode; sub?: ReactNode; loading: boolean }) {
  return (
    <Link href={href} className="bg-card group flex flex-col gap-2 rounded-lg border p-4 shadow-xs transition-shadow hover:shadow-md">
      <span className="text-muted-foreground flex items-center justify-between text-sm">
        <span className="flex items-center gap-2">
          <Icon className="size-4" /> {label}
        </span>
        <ArrowRight className="size-4 opacity-0 transition-opacity group-hover:opacity-100" />
      </span>
      {loading ? <Skeleton className="h-8 w-16" /> : <span className="text-primary text-3xl font-semibold tracking-tight tabular-nums">{value}</span>}
      {sub && <span className="text-muted-foreground text-xs">{sub}</span>}
    </Link>
  )
}

type Severity = "warning" | "info" | "ok"
interface Rec {
  id: string
  severity: Severity
  title: ReactNode
  detail?: ReactNode
}

const SEV_ICON = {
  warning: <AlertTriangle className="size-4 shrink-0 text-amber-600 dark:text-amber-400" />,
  info: <Info className="size-4 shrink-0 text-blue-600 dark:text-blue-400" />,
  ok: <CheckCircle2 className="size-4 shrink-0 text-emerald-600 dark:text-emerald-400" />,
}

function names(list: string[], href: (n: string) => string, max = 5) {
  const shown = list.slice(0, max)
  return (
    <>
      {shown.map((n, i) => (
        <span key={n}>
          {i > 0 && ", "}
          <Link href={href(n)} className="text-primary hover:underline">
            {n}
          </Link>
        </span>
      ))}
      {list.length > max && ` and ${list.length - max} more`}
    </>
  )
}

export function IamDashboard() {
  const session = useSession()
  const summary = useApi<IamSummary>(`${IAM}/summary`)
  const users = useApi<IamUser[]>(`${IAM}/users`)
  const groups = useApi<IamGroup[]>(`${IAM}/groups`)
  const policies = useApi<PolicySummary[]>(`${IAM}/policies`)
  const roles = useApi<IamRole[]>(`${IAM}/roles`)
  const keys = useAccessKeysByUser(users.data?.map((u) => u.name))
  const [origin, setOrigin] = useState("")
  useEffect(() => setOrigin(signInUrl()), [])

  const customer = (policies.data ?? []).filter((p) => !p.managed).length
  const allKeys = useMemo(() => Object.values(keys.data ?? {}).flat(), [keys.data])
  const activeKeys = allKeys.filter((k) => k.status === "Active").length

  const recs = useMemo<Rec[]>(() => {
    if (!users.data || !keys.data || !policies.data) return []
    const out: Rec[] = []
    const now = Date.now()
    const root = users.data.find((u) => u.root)
    const rootKeys = root ? (keys.data[root.name] ?? []).filter((k) => k.status === "Active") : []
    if (rootKeys.length)
      out.push({
        id: "root-keys",
        severity: "warning",
        title: (
          <>
            The root user has {pluralize(rootKeys.length, "active access key")}
          </>
        ),
        detail: (
          <>
            Root credentials can do anything. Create an IAM user with only the permissions it needs and{" "}
            <Link href={`${userHref(root!.name)}&tab=credentials`} className="text-primary hover:underline">
              deactivate the root access keys
            </Link>
            .
          </>
        ),
      })
    else out.push({ id: "root-keys", severity: "ok", title: "The root user has no active access keys" })

    const nonRoot = users.data.filter((u) => !u.root)
    if (!nonRoot.length)
      out.push({
        id: "no-users",
        severity: "warning",
        title: "Only the root user exists",
        detail: (
          <>
            <Link href="/iam/users/?create=1" className="text-primary hover:underline">
              Create an IAM user
            </Link>{" "}
            for everyday work instead of signing in as root.
          </>
        ),
      })

    const noCreds = nonRoot.filter((u) => !u.console_access && !(keys.data![u.name] ?? []).length).map((u) => u.name)
    if (noCreds.length)
      out.push({
        id: "no-creds",
        severity: "info",
        title: `${pluralize(noCreds.length, "user")} without any credentials`,
        detail: <>No console password and no access keys: {names(noCreds, userHref)}. Remove users that are no longer needed.</>,
      })

    const stale: string[] = []
    const never: string[] = []
    for (const [u, ks] of Object.entries(keys.data)) {
      for (const k of ks) {
        if (k.status !== "Active") continue
        if (!k.last_used) {
          if (keyIdleDays(k, now) >= 7) never.push(u)
        } else if (keyIdleDays(k, now) > 90) stale.push(u)
      }
    }
    if (stale.length)
      out.push({
        id: "stale-keys",
        severity: "warning",
        title: "Active access keys unused for more than 90 days",
        detail: <>Deactivate or delete unused keys of {names([...new Set(stale)], (n) => `${userHref(n)}&tab=credentials`)}.</>,
      })
    if (never.length)
      out.push({
        id: "never-keys",
        severity: "info",
        title: "Active access keys that have never been used",
        detail: <>Keys older than a week that were never used: {names([...new Set(never)], (n) => `${userHref(n)}&tab=credentials`)}.</>,
      })

    const unattached = policies.data.filter((p) => !p.managed && p.attachment_count === 0).map((p) => p.name)
    if (unattached.length)
      out.push({
        id: "unattached",
        severity: "info",
        title: `${pluralize(unattached.length, "customer managed policy", "customer managed policies")} not attached to anything`,
        detail: <>{names(unattached, policyHref)}. Attach them to users, groups or roles, or delete them.</>,
      })

    const direct = nonRoot.filter((u) => u.attached_policies.length > 0 && u.groups.length === 0).map((u) => u.name)
    if (direct.length && (groups.data?.length ?? 0) === 0)
      out.push({
        id: "use-groups",
        severity: "info",
        title: "Manage permissions with user groups",
        detail: (
          <>
            {pluralize(direct.length, "user")} {direct.length === 1 ? "has" : "have"} policies attached directly.{" "}
            <Link href="/iam/groups/" className="text-primary hover:underline">
              Create groups
            </Link>{" "}
            so users with the same job share permissions.
          </>
        ),
      })

    const unusedRoles = (roles.data ?? []).filter((r) => now - new Date(r.last_used ?? r.created_at).getTime() > 90 * DAY).map((r) => r.name)
    if (unusedRoles.length)
      out.push({
        id: "unused-roles",
        severity: "info",
        title: `${pluralize(unusedRoles.length, "role")} not used for more than 90 days`,
        detail: <>{names(unusedRoles, roleHref)}. Delete roles that are no longer needed.</>,
      })
    return out
  }, [users.data, keys.data, policies.data, groups.data, roles.data])

  const recsLoading = users.isLoading || policies.isLoading || (!!users.data && !keys.data)
  const signIn = origin || "/login/"

  return (
    <div className="flex flex-col gap-6">
      <PageHeader title="IAM dashboard" description="Manage who can sign in to HomeCloud and what they are allowed to do." />

      <Section title="IAM resources" description="Resources in this account">
        <div className="grid grid-cols-1 gap-4 sm:grid-cols-2 lg:grid-cols-3 2xl:grid-cols-5">
          <StatCard href="/iam/users/" icon={Users} label="Users" value={summary.data?.users ?? 0} sub={users.data ? `${users.data.filter((u) => u.console_access).length} with console access` : undefined} loading={summary.isLoading} />
          <StatCard href="/iam/groups/" icon={UsersRound} label="User groups" value={summary.data?.groups ?? 0} loading={summary.isLoading} />
          <StatCard
            href="/iam/roles/"
            icon={UserCog}
            label="Roles"
            value={summary.data?.roles ?? roles.data?.length ?? 0}
            sub={roles.data ? `${roles.data.filter((r) => r.trusted_services.length).length} for HomeCloud services` : undefined}
            loading={summary.isLoading}
          />
          <StatCard
            href="/iam/policies/"
            icon={FileText}
            label="Customer managed policies"
            value={
              <>
                {customer}
                <span className="text-muted-foreground text-lg font-normal"> / {summary.data?.policies ?? policies.data?.length ?? 0}</span>
              </>
            }
            sub="Customer managed / total policies"
            loading={summary.isLoading || policies.isLoading}
          />
          <StatCard href="/iam/users/" icon={KeyRound} label="Access keys" value={summary.data?.access_keys ?? 0} sub={keys.data ? `${activeKeys} active` : undefined} loading={summary.isLoading} />
        </div>
      </Section>

      <div className="grid grid-cols-1 gap-6 xl:grid-cols-3">
        <Section title="Security recommendations" className="xl:col-span-2" flush>
          {recsLoading ? (
            <div className="flex flex-col gap-3 p-4">
              {Array.from({ length: 3 }, (_, i) => (
                <Skeleton key={i} className="h-10 w-full" />
              ))}
            </div>
          ) : users.error || policies.error ? (
            <p className="text-muted-foreground p-4 text-sm">{(users.error ?? policies.error)?.message}</p>
          ) : (
            <ul>
              {recs.map((r) => (
                <li key={r.id} className="flex gap-3 border-b px-4 py-3 last:border-0">
                  <span className="mt-0.5">{SEV_ICON[r.severity]}</span>
                  <div className="min-w-0 text-sm">
                    <p className={cn("font-medium", r.severity === "ok" && "text-muted-foreground font-normal")}>{r.title}</p>
                    {r.detail && <p className="text-muted-foreground mt-0.5">{r.detail}</p>}
                  </div>
                </li>
              ))}
              {recs.every((r) => r.severity === "ok") && (
                <li className="text-muted-foreground px-4 py-3 text-sm">No other recommendations. Your account follows IAM best practices.</li>
              )}
            </ul>
          )}
        </Section>

        <div className="flex flex-col gap-6">
          <Section title="AWS account">
            <KeyValueGrid
              columns={2}
              items={[
                { label: "Account ID", value: <CopyableText value={summary.data?.account_id ?? session.account_id} /> },
                { label: "Signed in as", value: session.user.name },
                { label: "Sign-in URL for IAM users in this account", value: <CopyableText value={signIn} />, wide: true },
              ]}
            />
          </Section>
          <Section title="Quick links">
            <ul className="flex flex-col gap-1">
              {[
                { href: "/iam/users/?create=1", label: "Create user", icon: UserPlus },
                { href: "/iam/groups/?create=1", label: "Create user group", icon: UsersRound },
                { href: createRoleHref(), label: "Create role", icon: UserCog },
                { href: "/iam/policies/create/", label: "Create policy", icon: FileText },
                { href: `${userHref(session.user.name)}&tab=credentials`, label: "My security credentials", icon: ShieldCheck },
                { href: "/iam/simulator/", label: "Policy simulator", icon: FlaskConical },
              ].map((q) => (
                <li key={q.label}>
                  <Link href={q.href} className="hover:bg-accent flex items-center gap-2 rounded-md px-2 py-1.5 text-sm">
                    <q.icon className="text-muted-foreground size-4" />
                    <span className="text-primary">{q.label}</span>
                  </Link>
                </li>
              ))}
            </ul>
          </Section>
        </div>
      </div>
    </div>
  )
}
