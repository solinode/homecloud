"use client"

import Link from "next/link"
import { useMemo, useState } from "react"
import { FileText, Plus, Trash2 } from "lucide-react"
import { toast } from "sonner"

import { Button } from "@/components/ui/button"
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select"
import { ActionsMenu } from "@/components/console/actions-menu"
import { ConfirmDialog } from "@/components/console/confirm-dialog"
import { DataTable, type Column } from "@/components/console/data-table"
import { EmptyState } from "@/components/console/empty-state"
import { PageHeader } from "@/components/console/page-header"
import { TimeAgo } from "@/components/console/time-ago"
import { api, seg } from "@/lib/api"
import { revalidate, useApi, useQueryParam, useSetQueryParam } from "@/lib/hooks"
import type { PolicySummary } from "@/lib/types"

import { IAM, LINK, PolicyTypeBadge, policyHref } from "./common"

export function PoliciesList() {
  const { data, error, isLoading, isValidating, mutate } = useApi<PolicySummary[]>(`${IAM}/policies`)
  const scopeParam = useQueryParam("scope")
  const setParam = useSetQueryParam()
  const scope = scopeParam === "managed" || scopeParam === "local" ? scopeParam : "all"
  const [selected, setSelected] = useState<string[]>([])
  const [confirm, setConfirm] = useState(false)

  const rows = useMemo(() => (data ?? []).filter((p) => (scope === "all" ? true : scope === "managed" ? p.managed : !p.managed)), [data, scope])
  const sel = (data ?? []).find((p) => p.name === selected[0])

  const columns: Column<PolicySummary>[] = [
    {
      id: "name",
      header: "Policy name",
      value: (p) => p.name,
      cell: (p) => (
        <Link href={policyHref(p.name)} className={LINK}>
          {p.name}
        </Link>
      ),
    },
    { id: "type", header: "Type", value: (p) => (p.managed ? "HomeCloud managed" : "Customer managed"), cell: (p) => <PolicyTypeBadge managed={p.managed} /> },
    {
      id: "used",
      header: "Used as",
      value: (p) => p.attachment_count,
      cell: (p) => (p.attachment_count ? `Permissions policy (${p.attachment_count})` : <span className="text-muted-foreground">None</span>),
      hideBelow: "sm",
    },
    { id: "desc", header: "Description", value: (p) => p.description, cell: (p) => <span className="text-muted-foreground line-clamp-2">{p.description || "-"}</span>, hideBelow: "md" },
    { id: "updated", header: "Last edited", value: (p) => p.updated_at, cell: (p) => <TimeAgo value={p.updated_at} />, hideBelow: "lg" },
  ]

  return (
    <div className="flex flex-col gap-4">
      <PageHeader
        title="Policies"
        description="A policy is an object that defines permissions. Attach policies to users and groups to control what they can do."
        breadcrumbs={[{ label: "IAM", href: "/iam/" }, { label: "Policies" }]}
      />
      <DataTable
        title="Policies"
        data={data ? rows : undefined}
        columns={columns}
        rowId={(p) => p.name}
        loading={isLoading}
        error={error}
        onRetry={() => mutate()}
        onRefresh={() => mutate()}
        refreshing={isValidating}
        selection="single"
        selected={selected}
        onSelectedChange={setSelected}
        searchPlaceholder="Filter policies by name or description"
        defaultSort={{ id: "name" }}
        filters={
          <Select value={scope} onValueChange={(v) => setParam("scope", v === "all" ? null : v)}>
            <SelectTrigger size="sm" className="w-48">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="all">All types</SelectItem>
              <SelectItem value="managed">HomeCloud managed</SelectItem>
              <SelectItem value="local">Customer managed</SelectItem>
            </SelectContent>
          </Select>
        }
        actions={
          <>
            <ActionsMenu
              disabled={!sel}
              items={[
                {
                  label: "Delete",
                  icon: <Trash2 />,
                  destructive: true,
                  disabled: !sel || sel.managed,
                  hint: sel?.managed ? "HomeCloud managed policies cannot be deleted" : undefined,
                  onSelect: () => setConfirm(true),
                },
              ]}
            />
            <Button size="sm" asChild>
              <Link href="/iam/policies/create/">
                <Plus /> Create policy
              </Link>
            </Button>
          </>
        }
        empty={
          <EmptyState
            icon={FileText}
            title={scope === "local" ? "No customer managed policies" : "No policies"}
            description="Create your own policy to grant exactly the permissions a user or group needs."
            action={
              <Button size="sm" asChild>
                <Link href="/iam/policies/create/">
                  <Plus /> Create policy
                </Link>
              </Button>
            }
          />
        }
      />
      <ConfirmDialog
        open={confirm}
        onOpenChange={setConfirm}
        title={`Delete ${sel?.name}?`}
        description={
          sel?.attachment_count
            ? `This policy is attached to ${sel.attachment_count} user(s) or group(s). Detach it from every entity before deleting it.`
            : "The policy is permanently deleted. This cannot be undone."
        }
        confirmText={sel?.name}
        onConfirm={async () => {
          if (!sel) return
          await api.del(`${IAM}/policies/${seg(sel.name)}`)
          toast.success(`Policy ${sel.name} deleted`)
          setSelected([])
          revalidate(IAM)
        }}
      />
    </div>
  )
}
