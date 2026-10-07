"use client"

import { useState } from "react"
import Link from "next/link"
import { useRouter } from "next/navigation"
import { Eye, KeyRound, Plus, Trash2 } from "lucide-react"

import { Button } from "@/components/ui/button"
import { ActionsMenu } from "@/components/console/actions-menu"
import { CellLink, CellText, DataTable, type Column } from "@/components/console/data-table"
import { EmptyState } from "@/components/console/empty-state"
import { PageHeader } from "@/components/console/page-header"
import { StatusBadge } from "@/components/console/status-badge"
import { Tag } from "@/components/console/tag"
import { TimeAgo } from "@/components/console/time-ago"
import { formatDate } from "@/lib/format"
import { useApi } from "@/lib/hooks"
import type { Secret } from "@/lib/types"

import { DeleteSecretDialog } from "./delete-secret-dialog"

export const secretHref = (name: string) => `/secrets/secret/?name=${encodeURIComponent(name)}`

/** Display names for the services that create and own secrets. */
const MANAGED_BY_LABEL: Record<string, string> = {
  rds: "RDS",
  s3: "S3",
  ssm: "Systems Manager",
  kms: "KMS",
  ecr: "ECR",
  elasticache: "ElastiCache",
  docdb: "DocumentDB",
  ecs: "ECS",
  lambda: "Lambda",
  cloudformation: "CloudFormation",
}

export function ManagedBadge({ by }: { by?: string }) {
  if (!by) return <span className="text-muted-foreground">-</span>
  return (
    <Tag accent="info" mono={false} title={`Created and managed by ${MANAGED_BY_LABEL[by.toLowerCase()] ?? by}`}>
      {MANAGED_BY_LABEL[by.toLowerCase()] ?? by}
    </Tag>
  )
}

export function SecretList() {
  const router = useRouter()
  const { data, error, isLoading, isValidating, mutate } = useApi<Secret[]>("/api/v1/secrets")
  const [selected, setSelected] = useState<string[]>([])
  const [deleteTarget, setDeleteTarget] = useState<Secret | null>(null)
  const sel = data?.find((s) => s.name === selected[0])

  const columns: Column<Secret>[] = [
    {
      id: "name",
      header: "Secret name",
      value: (s) => s.name,
      cell: (s) => (
        <CellLink href={secretHref(s.name)} max="24rem">
          {s.name}
        </CellLink>
      ),
    },
    {
      id: "description",
      header: "Description",
      value: (s) => s.description,
      cell: (s) =>
<CellText max="20rem">{s.description}</CellText>,
      hideBelow: "md",
    },
    { id: "managed", header: "Managed by", value: (s) => s.managed_by ?? "", cell: (s) => <ManagedBadge by={s.managed_by} />, hideBelow: "sm" },
    {
      id: "rotation",
      header: "Rotation",
      value: (s) => (s.rotation_error ? "failed" : s.rotation_enabled ? "enabled" : "disabled"),
      cell: (s) =>
        s.rotation_error ? (
          <StatusBadge status="failed" label="Failed" tone="danger" />
        ) : s.rotation_enabled ? (
          <StatusBadge status="enabled" />
        ) : (
          <StatusBadge status="disabled" />
        ),
      hideBelow: "lg",
    },
    { id: "accessed", header: "Last retrieved", value: (s) => s.last_accessed ?? "", cell: (s) => <TimeAgo value={s.last_accessed} />, hideBelow: "lg" },
    { id: "updated", header: "Last changed", value: (s) => s.updated_at, cell: (s) => <TimeAgo value={s.updated_at} />, hideBelow: "sm" },
    { id: "versions", header: "Versions", value: (s) => s.versions.length, cell: (s) => <span className="tabular-nums">{s.versions.length}</span>, hideBelow: "lg" },
    {
      id: "status",
      header: "Status",
      value: (s) => (s.deletion_date ? `deleting ${s.deletion_date}` : "active"),
      cell: (s) =>
        s.deletion_date ? (
          <StatusBadge status="scheduled for deletion" label={`Scheduled for deletion on ${formatDate(s.deletion_date, false)}`} tone="danger" />
        ) : (
          <StatusBadge status="active" label="Active" />
        ),
    },
  ]

  return (
    <div className="flex flex-col gap-6">
      <PageHeader
        title="Secrets"
        description="Store, retrieve, rotate and version credentials, API keys and other secrets. Values are encrypted at rest with AES-256-GCM or a KMS key you choose."
        breadcrumbs={[{ label: "Secrets Manager", href: "/secrets/" }, { label: "Secrets" }]}
      />
      <DataTable
        title="Secrets"
        data={data}
        columns={columns}
        rowId={(s) => s.name}
        loading={isLoading}
        error={error}
        onRefresh={() => mutate()}
        refreshing={isValidating}
        selection="single"
        selected={selected}
        onSelectedChange={setSelected}
        searchPlaceholder="Filter secrets by name, description or tag"
        filter={(s, q) =>
          s.name.toLowerCase().includes(q) ||
          s.description.toLowerCase().includes(q) ||
          Object.entries(s.tags ?? {}).some(([k, v]) => k.toLowerCase().includes(q) || v.toLowerCase().includes(q))
        }
        defaultSort={{ id: "name" }}
        actions={
          <>
            <ActionsMenu
              disabled={!sel}
              items={[
                { label: "View details", icon: <Eye />, onSelect: () => sel && router.push(secretHref(sel.name)) },
                { separator: true },
                { label: "Delete secret", icon: <Trash2 />, destructive: true, disabled: !!sel?.deletion_date, onSelect: () => sel && setDeleteTarget(sel) },
              ]}
            />
            <Button size="sm" asChild>
              <Link href="/secrets/create/">
                <Plus /> Store a new secret
              </Link>
            </Button>
          </>
        }
        empty={
          <EmptyState
            icon={KeyRound}
            title="No secrets"
            description="Store database credentials, API keys and other sensitive values securely."
            action={
              <Button size="sm" asChild>
                <Link href="/secrets/create/">
                  <Plus /> Store a new secret
                </Link>
              </Button>
            }
          />
        }
      />
      <DeleteSecretDialog secret={deleteTarget} onOpenChange={(o) => !o && setDeleteTarget(null)} />
    </div>
  )
}
