"use client"

import { useMemo, useState } from "react"
import Link from "next/link"
import { useRouter } from "next/navigation"
import { KeyRound, LockKeyhole, Plus } from "lucide-react"

import { Button } from "@/components/ui/button"
import { Tabs, TabsList, TabsTrigger } from "@/components/ui/tabs"
import { ActionsMenu, type ActionItem } from "@/components/console/actions-menu"
import { CopyButton } from "@/components/console/copy-button"
import { CellLink, CellText, DataTable, type Column } from "@/components/console/data-table"
import { EmptyState } from "@/components/console/empty-state"
import { PageHeader } from "@/components/console/page-header"
import { TimeAgo } from "@/components/console/time-ago"
import { useQueryParam, useSetQueryParam } from "@/lib/hooks"
import type { KmsKey } from "@/lib/types"

import { CreateKeyDialog } from "./create-key-dialog"
import { AliasesView } from "./aliases-view"
import { KIND_LABEL, KeySpecTag, KeyStateBadge, ManagedBadge, isServiceManaged, isSymmetric, keyHref, keyKind, useKeyActions, useKmsKeys } from "./shared"

function Aliases({ k }: { k: KmsKey }) {
  if (!k.aliases?.length) return <span className="text-muted-foreground">-</span>
  const [first, ...rest] = k.aliases
  return (
    <span className="flex min-w-0 items-center gap-x-1.5">
      <CellLink href={keyHref(k.id)} mono max="18rem">
        {first}
      </CellLink>
      {rest.length > 0 && (
        <span className="text-muted-foreground shrink-0 text-xs whitespace-nowrap" title={rest.join(", ")}>
          +{rest.length} more
        </span>
      )}
    </span>
  )
}

const columns: Column<KmsKey>[] = [
  { id: "alias", header: "Aliases", cell: (k) => <Aliases k={k} />, value: (k) => (k.aliases ?? []).join(" ") },
  {
    id: "id",
    header: "Key ID",
    cell: (k) => (
      <span className="inline-flex items-center gap-1 whitespace-nowrap" title={k.id}>
        {k.aliases?.length ? (
          <span className="font-mono text-[13px]">{k.id.slice(0, 8)}...</span>
        ) : (
          <CellLink href={keyHref(k.id)} mono title={k.id}>
            {k.id.slice(0, 8)}...
          </CellLink>
        )}
        <CopyButton value={k.id} label="Copy key ID" />
      </span>
    ),
    value: (k) => k.id,
  },
  { id: "state", header: "Status", cell: (k) => <KeyStateBadge state={k.state} />, value: (k) => k.state },
  {
    id: "desc",
    header: "Description",
    cell: (k) => <CellText max="18rem">{k.description}</CellText>,
    value: (k) => k.description,
    hideBelow: "md",
  },
  {
    id: "type",
    header: "Key type",
    cell: (k) => (
      <span className="inline-flex items-center gap-1.5 whitespace-nowrap">
        {KIND_LABEL[keyKind(k.key_spec)]}
        {!isSymmetric(k) && <KeySpecTag spec={k.key_spec} />}
      </span>
    ),
    value: (k) => `${keyKind(k.key_spec)} ${k.key_spec}`,
    hideBelow: "md",
  },
  {
    id: "rotation",
    header: "Rotation",
    cell: (k) =>
      !isSymmetric(k) ? (
        <span className="text-muted-foreground">-</span>
      ) : k.rotation_enabled ? (
        <span className="whitespace-nowrap">Every {k.rotation_period_days ?? 365} days</span>
      ) : (
        <span className="text-muted-foreground">Disabled</span>
      ),
    value: (k) => (isSymmetric(k) && k.rotation_enabled ? (k.rotation_period_days ?? 365) : 0),
    hideBelow: "lg",
  },
  { id: "versions", header: "Versions", cell: (k) => <span className="tabular-nums">{k.key_versions}</span>, value: (k) => k.key_versions, hideBelow: "lg" },
  {
    id: "managed",
    header: "Managed by",
    cell: (k) => (isServiceManaged(k) ? <ManagedBadge /> : <span className="text-muted-foreground">Customer</span>),
    value: (k) => (isServiceManaged(k) ? "HomeCloud" : "Customer"),
    hideBelow: "lg",
  },
  { id: "created", header: "Created", cell: (k) => <TimeAgo value={k.created_at} />, value: (k) => k.created_at, hideBelow: "sm" },
]

type View = "customer" | "managed" | "aliases"

export function KeysList() {
  const router = useRouter()
  const viewParam = useQueryParam("view")
  const view: View = viewParam === "managed" || viewParam === "aliases" ? viewParam : "customer"
  const setParam = useSetQueryParam()
  const { data, error, isLoading, isValidating, mutate } = useKmsKeys()
  const [selected, setSelected] = useState<string[]>([])
  const [creating, setCreating] = useState(false)
  const actions = useKeyActions()

  const counts = useMemo(() => {
    const all = data ?? []
    const managed = all.filter(isServiceManaged).length
    return { managed, customer: all.length - managed }
  }, [data])

  const rows = useMemo(() => (data ?? []).filter((k) => (view === "managed" ? isServiceManaged(k) : !isServiceManaged(k))), [data, view])
  const sel = rows.find((k) => selected.includes(k.id)) ?? null
  const locked = !sel || actions.busy || isServiceManaged(sel)
  const pendingDeletion = sel?.state === "PendingDeletion"

  const items: ActionItem[] = [
    { label: "View details", onSelect: () => sel && router.push(keyHref(sel.id)), disabled: !sel },
    { label: sel && !isSymmetric(sel) ? "Cryptographic operations" : "Encrypt / decrypt", onSelect: () => sel && router.push(keyHref(sel.id, "crypto")), disabled: !sel },
    { separator: true },
    { label: "Enable", onSelect: () => sel && actions.enable(sel), disabled: locked || sel?.state !== "Disabled" },
    { label: "Disable", onSelect: () => sel && actions.disable(sel), disabled: locked || sel?.state !== "Enabled" },
    {
      label: "Rotate key material now",
      onSelect: () => sel && actions.rotate(sel),
      disabled: locked || pendingDeletion || !sel || !isSymmetric(sel) || sel.state !== "Enabled",
      hint: sel && !isSymmetric(sel) ? "Only symmetric encryption keys support rotation." : undefined,
    },
    { separator: true },
    pendingDeletion
      ? { label: "Cancel key deletion", onSelect: () => sel && actions.cancelDeletion(sel), disabled: locked }
      : { label: "Schedule key deletion", destructive: true, onSelect: () => sel && actions.scheduleDeletion(sel), disabled: locked },
  ]

  return (
    <div className="flex flex-col gap-4">
      <PageHeader
        title="Key Management Service"
        description="Create and control the encryption keys that protect your data. HomeCloud services such as Parameter Store use their own managed keys."
        breadcrumbs={[{ label: "KMS", href: "/kms/" }, { label: view === "managed" ? "HomeCloud managed keys" : view === "aliases" ? "Aliases" : "Customer managed keys" }]}
        actions={
          <Button variant="outline" size="sm" asChild>
            <Link href="/kms/crypto/">
              <LockKeyhole /> Encrypt / decrypt
            </Link>
          </Button>
        }
      />

      <Tabs
        value={view}
        onValueChange={(v) => {
          setSelected([])
          setParam("view", v === "customer" ? null : v)
        }}
      >
        <TabsList>
          <TabsTrigger value="customer">
            Customer managed keys{data ? <span className="text-muted-foreground text-xs">({counts.customer})</span> : null}
          </TabsTrigger>
          <TabsTrigger value="managed">
            HomeCloud managed keys{data ? <span className="text-muted-foreground text-xs">({counts.managed})</span> : null}
          </TabsTrigger>
          <TabsTrigger value="aliases">Aliases</TabsTrigger>
        </TabsList>
      </Tabs>

      {view === "aliases" ? (
        <AliasesView keys={data} />
      ) : (
      <DataTable
        title={view === "managed" ? "HomeCloud managed keys" : "Customer managed keys"}
        description={
          view === "managed"
            ? "Keys created by HomeCloud services (alias/hc/*). You can use them to encrypt and decrypt, but they are read-only."
            : "Keys you create and manage."
        }
        data={data ? rows : undefined}
        columns={columns}
        rowId={(k) => k.id}
        loading={isLoading}
        error={error}
        onRefresh={() => mutate()}
        refreshing={isValidating}
        selection="single"
        selected={selected}
        onSelectedChange={setSelected}
        searchPlaceholder="Filter by alias, key ID or description"
        defaultSort={{ id: "created", desc: true }}
        actions={
          <>
            <ActionsMenu label="Key actions" items={items} disabled={!sel} />
            {view === "customer" && (
              <Button size="sm" onClick={() => setCreating(true)}>
                <Plus /> Create key
              </Button>
            )}
          </>
        }
        empty={
          view === "managed" ? (
            <EmptyState
              icon={KeyRound}
              title="No HomeCloud managed keys yet"
              description="Services create their managed key the first time they need one, e.g. when you store a SecureString parameter with the default key."
            />
          ) : (
            <EmptyState
              icon={KeyRound}
              title="No customer managed keys"
              description="Create a key to encrypt data, SecureString parameters and data keys under your own control."
              action={
                <Button size="sm" onClick={() => setCreating(true)}>
                  <Plus /> Create key
                </Button>
              }
            />
          )
        }
      />
      )}

      {actions.dialogs}
      <CreateKeyDialog open={creating} onOpenChange={setCreating} />
    </div>
  )
}
