"use client"

import { useState } from "react"
import Link from "next/link"
import { AlertCircle, ArrowLeft, RefreshCw, Trash2 } from "lucide-react"

import { Button } from "@/components/ui/button"
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs"
import { ActionsMenu } from "@/components/console/actions-menu"
import { CopyableText } from "@/components/console/copy-button"
import { EmptyState } from "@/components/console/empty-state"
import { ErrorState } from "@/components/console/error-state"
import { KeyValueGrid } from "@/components/console/key-value"
import { DetailSkeleton } from "@/components/console/loading"
import { PageHeader } from "@/components/console/page-header"
import { Section } from "@/components/console/section"
import { StatTile } from "@/components/console/stat-tile"
import { TimeAgo } from "@/components/console/time-ago"
import { ApiError } from "@/lib/api"
import { formatDate } from "@/lib/format"
import { useApi, useQueryParam, useSetQueryParam } from "@/lib/hooks"
import type { UserPool } from "@/lib/types"
import { cn } from "@/lib/utils"

import { ClientsTab } from "./clients-tab"
import { GroupsTab } from "./groups-tab"
import { IntegrationTab } from "./integration-tab"
import { DeletePoolDialog } from "./pools-list"
import { SettingsTab } from "./settings-tab"
import { SignUpBadge, policySummary, poolPath } from "./shared"
import { UsersTab } from "./users-tab"

const TABS = ["users", "groups", "clients", "settings", "integration"] as const
type Tab = (typeof TABS)[number]

function BackButton() {
  return (
    <Button variant="outline" size="sm" asChild>
      <Link href="/cognito/">
        <ArrowLeft /> Back to user pools
      </Link>
    </Button>
  )
}

export function PoolDetail() {
  const id = useQueryParam("id")
  const tabParam = useQueryParam("tab")
  const setParam = useSetQueryParam()
  const tab: Tab = (TABS as readonly string[]).includes(tabParam) ? (tabParam as Tab) : "users"
  const { data: pool, error, isLoading, isValidating, mutate } = useApi<UserPool>(id ? poolPath(id) : null, { refreshInterval: 30_000 })
  const [deleting, setDeleting] = useState(false)

  const crumbs = [{ label: "Cognito", href: "/cognito/" }, { label: "User pools", href: "/cognito/" }, { label: pool?.name || id || "User pool" }]

  if (!id) {
    return (
      <>
        <PageHeader title="User pool" breadcrumbs={crumbs} />
        <EmptyState title="No user pool selected" description="Open a user pool from the user pools list." action={<BackButton />} />
      </>
    )
  }
  if (error && !pool) {
    const notFound = error instanceof ApiError && error.status === 404
    return (
      <div className="flex flex-col gap-4">
        <PageHeader title={id} breadcrumbs={crumbs} />
        {notFound ? (
          <Section>
            <EmptyState icon={AlertCircle} title="User pool not found" description={`User pool ${id} does not exist. It may have been deleted.`} action={<BackButton />} />
          </Section>
        ) : (
          <ErrorState error={error} onRetry={() => mutate()} />
        )}
      </div>
    )
  }
  if (isLoading || !pool) return <DetailSkeleton />

  return (
    <div className="flex flex-col gap-4">
      <PageHeader
        title={pool.name}
        description={<span className="font-mono text-[13px]">{pool.id}</span>}
        breadcrumbs={crumbs}
        actions={
          <>
            <Button variant="outline" size="sm" onClick={() => mutate()} aria-label="Refresh">
              <RefreshCw className={cn(isValidating && "animate-spin")} />
            </Button>
            <ActionsMenu
              items={[
                { label: "Edit settings", onSelect: () => setParam("tab", "settings") },
                { label: "View integration", onSelect: () => setParam("tab", "integration") },
                { separator: true },
                { label: "Delete user pool", icon: <Trash2 />, destructive: true, onSelect: () => setDeleting(true) },
              ]}
            />
          </>
        }
      />

      <div className="grid grid-cols-2 gap-3 lg:grid-cols-4">
        <StatTile label="Users" value={pool.users} caption="Estimated number of users" />
        <StatTile label="Groups" value={pool.groups.length} caption="cognito:groups claim" />
        <StatTile label="App clients" value={pool.clients} caption="Sign-up and sign-in clients" />
        <StatTile
          label="Self sign-up"
          value={pool.self_sign_up ? "On" : "Off"}
          tone={pool.self_sign_up ? (pool.auto_confirm ? "success" : "warning") : "neutral"}
          caption={pool.self_sign_up ? (pool.auto_confirm ? "Auto-confirmed" : "Requires confirmation") : "Administrators create users"}
        />
      </div>

      <Section title="User pool overview">
        <KeyValueGrid
          columns={3}
          items={[
            { label: "User pool ID", value: <CopyableText value={pool.id} /> },
            { label: "Password policy", value: policySummary(pool.password_policy) },
            { label: "Self sign-up", value: <SignUpBadge pool={pool} /> },
            { label: "Created", value: <span>{formatDate(pool.created_at)} (<TimeAgo value={pool.created_at} />)</span> },
            { label: "Token issuer", value: <CopyableText value={pool.issuer} />, wide: true },
            { label: "ARN", value: <CopyableText value={pool.arn} />, wide: true },
          ]}
        />
      </Section>

      <Tabs value={tab} onValueChange={(v) => setParam("tab", v === "users" ? null : v)}>
        <div className="-mx-1 overflow-x-auto px-1">
          <TabsList>
            <TabsTrigger value="users">
              Users <span className="text-muted-foreground text-xs">({pool.users})</span>
            </TabsTrigger>
            <TabsTrigger value="groups">
              Groups <span className="text-muted-foreground text-xs">({pool.groups.length})</span>
            </TabsTrigger>
            <TabsTrigger value="clients">
              App clients <span className="text-muted-foreground text-xs">({pool.clients})</span>
            </TabsTrigger>
            <TabsTrigger value="settings">Settings</TabsTrigger>
            <TabsTrigger value="integration">Integration</TabsTrigger>
          </TabsList>
        </div>
        <TabsContent value="users">
          <UsersTab pool={pool} />
        </TabsContent>
        <TabsContent value="groups">
          <GroupsTab pool={pool} onChanged={() => mutate()} />
        </TabsContent>
        <TabsContent value="clients">
          <ClientsTab pool={pool} />
        </TabsContent>
        <TabsContent value="settings">
          <SettingsTab pool={pool} />
        </TabsContent>
        <TabsContent value="integration">
          <IntegrationTab pool={pool} />
        </TabsContent>
      </Tabs>

      <DeletePoolDialog pool={deleting ? pool : null} onClose={() => setDeleting(false)} redirect />
    </div>
  )
}
