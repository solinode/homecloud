"use client"

import { useMemo, useState } from "react"
import Link from "next/link"
import { useRouter } from "next/navigation"
import { Database, DatabaseZap, Plus } from "lucide-react"

import { Button } from "@/components/ui/button"
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select"
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip"
import { ActionsMenu, type ActionItem } from "@/components/console/actions-menu"
import { DataTable, type Column } from "@/components/console/data-table"
import { EmptyState } from "@/components/console/empty-state"
import { PageHeader } from "@/components/console/page-header"
import { StatusBadge } from "@/components/console/status-badge"
import { TimeAgo } from "@/components/console/time-ago"
import { formatDate, formatMemoryMB } from "@/lib/format"
import { useApi } from "@/lib/hooks"
import type { DbEngine, DbInstance } from "@/lib/types"
import { useDbActions } from "./db-actions"
import {
  DB_INSTANCES_PATH,
  EngineCell,
  FAMILIES,
  dbHref,
  endpointText,
  formatVcpu,
  inFamily,
  pollInterval,
  publicText,
  snapTab,
  useEngines,
  type Family,
  type FamilyConfig,
} from "./shared"

const STATUSES = ["available", "creating", "starting", "stopping", "stopped", "rebooting", "backing-up", "modifying", "deleting", "failed"]

/** DbStatusBadge adds the failure reason as a tooltip. */
export function DbStatusBadge({ inst }: { inst: Pick<DbInstance, "status" | "status_reason"> }) {
  if (!inst.status_reason) return <StatusBadge status={inst.status} />
  return (
    <Tooltip>
      <TooltipTrigger asChild>
        <span className="cursor-help">
          <StatusBadge status={inst.status} />
        </span>
      </TooltipTrigger>
      <TooltipContent className="max-w-sm">{inst.status_reason}</TooltipContent>
    </Tooltip>
  )
}

function columns(cfg: FamilyConfig, engines: DbEngine[] | undefined): Column<DbInstance>[] {
  return [
    {
      id: "id",
      header: cfg.idLabel,
      cell: (i) => (
        <Link href={dbHref(i.kind, i.id)} onClick={(e) => e.stopPropagation()} className="text-primary font-mono text-[13px] font-medium hover:underline">
          {i.id}
        </Link>
      ),
      value: (i) => i.id,
    },
    {
      id: "engine",
      header: "Engine",
      cell: (i) => <EngineCell engine={i.engine} version={i.engine_version} engines={engines} />,
      value: (i) => `${i.engine} ${i.engine_version}`,
    },
    { id: "status", header: "Status", cell: (i) => <DbStatusBadge inst={i} />, value: (i) => i.status },
    {
      id: "class",
      header: cfg.family === "rds" ? "Class" : "Node type",
      cell: (i) => (
        <span className="flex flex-col">
          <span className="font-mono text-[13px]">{i.class}</span>
          <span className="text-muted-foreground text-xs whitespace-nowrap">
            {formatVcpu(i.vcpus)}, {formatMemoryMB(i.memory_mb)}
          </span>
        </span>
      ),
      value: (i) => i.class,
      hideBelow: "sm",
    },
    {
      id: "endpoint",
      header: "Endpoint",
      cell: (i) => <span className="font-mono text-[13px] whitespace-nowrap">{endpointText(i) || "-"}</span>,
      value: (i) => `${endpointText(i)} ${i.endpoint.private_ip}`,
      hideBelow: "md",
    },
    {
      id: "public",
      header: "Public access",
      cell: (i) =>
        publicText(i) ? (
          <span className="font-mono text-[13px] whitespace-nowrap">{publicText(i)}</span>
        ) : (
          <span className="text-muted-foreground">{i.publicly_accessible ? "Yes" : "No"}</span>
        ),
      value: (i) => publicText(i),
      hideBelow: "lg",
    },
    {
      id: "created",
      header: "Created",
      cell: (i) => (
        <span title={formatDate(i.created_at)}>
          <TimeAgo value={i.created_at} />
        </span>
      ),
      value: (i) => i.created_at,
      hideBelow: "lg",
    },
  ]
}

export function DbList({ family }: { family: Family }) {
  const cfg = FAMILIES[family]
  const router = useRouter()
  const engines = useEngines()
  const { data, error, isLoading, isValidating, mutate } = useApi<DbInstance[]>(DB_INSTANCES_PATH, {
    refreshInterval: (d) => pollInterval((d ?? []).filter((i) => inFamily(cfg, i.kind)).map((i) => i.status)),
  })
  const [selected, setSelected] = useState<string[]>([])
  const [engine, setEngine] = useState("all")
  const [status, setStatus] = useState("all")
  const actions = useDbActions(cfg)

  const mine = useMemo(() => (data ?? []).filter((i) => inFamily(cfg, i.kind)), [data, cfg])
  const rows = useMemo(
    () => mine.filter((i) => (engine === "all" || i.engine === engine) && (status === "all" || i.status === status)),
    [mine, engine, status],
  )
  const cols = useMemo(() => columns(cfg, engines.data?.engines), [cfg, engines.data])
  const familyEngines = (engines.data?.engines ?? []).filter((e) => inFamily(cfg, e.kind))

  const sel = rows.filter((i) => selected.includes(i.id))
  const single = sel.length === 1 ? sel[0] : null
  const ids = sel.map((i) => i.id)
  const all = (s: string) => sel.length > 0 && sel.every((i) => i.status === s)
  const off = actions.busy
  const Icon = family === "rds" ? Database : DatabaseZap

  const items: ActionItem[] = [
    { label: "Start", onSelect: () => actions.start(ids), disabled: off || !all("stopped") },
    { label: "Stop", onSelect: () => actions.stop(ids), disabled: off || !all("available") },
    { label: "Reboot", onSelect: () => actions.reboot(ids), disabled: off || !all("available") },
    { separator: true },
    { label: "Modify", onSelect: () => single && router.push(dbHref(single.kind, single.id, "configuration")), disabled: !single },
    { label: `Take ${cfg.snap}`, onSelect: () => single && router.push(dbHref(single.kind, single.id, snapTab(single.kind))), disabled: !single || single.engine === "memcached" },
    { separator: true },
    { label: "Delete", destructive: true, onSelect: () => actions.remove(sel), disabled: off || !sel.length || sel.some((i) => i.status === "deleting") },
  ]

  const counts = useMemo(() => {
    const c: Record<string, number> = {}
    for (const i of mine) {
      c[i.status] = (c[i.status] ?? 0) + 1
      c[`e:${i.engine}`] = (c[`e:${i.engine}`] ?? 0) + 1
    }
    return c
  }, [mine])

  const createBtn = (
    <Button size="sm" asChild>
      <Link href={cfg.createPath}>
        <Plus /> Create {cfg.noun}
      </Link>
    </Button>
  )

  return (
    <div className="flex flex-col gap-4">
      <PageHeader
        title={cfg.Nouns}
        description={
          family === "rds"
            ? "Managed PostgreSQL, MySQL, MariaDB and MongoDB (DocumentDB compatible) databases running in your VPC, with snapshots and a built-in query editor."
            : "In-memory Redis, Valkey and Memcached clusters running in your VPC, with backups and a built-in command console."
        }
        breadcrumbs={[{ label: cfg.service, href: cfg.base }, { label: cfg.Nouns }]}
      />
      <DataTable
        title={cfg.Nouns}
        data={data ? rows : undefined}
        columns={cols}
        rowId={(i) => i.id}
        loading={isLoading}
        error={error}
        onRefresh={() => mutate()}
        refreshing={isValidating}
        selection="multi"
        selected={selected}
        onSelectedChange={setSelected}
        searchPlaceholder={`Find ${cfg.nouns} by identifier, engine or endpoint`}
        defaultSort={{ id: "created", desc: true }}
        actions={
          <>
            <ActionsMenu items={items} disabled={!sel.length} />
            {createBtn}
          </>
        }
        filters={
          <>
            <Select value={engine} onValueChange={setEngine}>
              <SelectTrigger size="sm" className="h-8 w-44" aria-label="Engine filter">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="all">All engines</SelectItem>
                {familyEngines.map((e) => (
                  <SelectItem key={e.name} value={e.name}>
                    {e.label.replace(" (DocumentDB compatible)", "")}
                    {counts[`e:${e.name}`] ? <span className="text-muted-foreground text-xs">({counts[`e:${e.name}`]})</span> : null}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
            <Select value={status} onValueChange={setStatus}>
              <SelectTrigger size="sm" className="h-8 w-40" aria-label="Status filter">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="all">All statuses</SelectItem>
                {STATUSES.map((s) => (
                  <SelectItem key={s} value={s}>
                    <span>{s === "backing-up" ? "Backing up" : s.charAt(0).toUpperCase() + s.slice(1)}</span>
                    {counts[s] ? <span className="text-muted-foreground text-xs">({counts[s]})</span> : null}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </>
        }
        empty={
          mine.length > 0 ? (
            <EmptyState
              icon={Icon}
              title={`No ${cfg.nouns} match these filters`}
              action={
                <Button variant="outline" size="sm" onClick={() => (setEngine("all"), setStatus("all"))}>
                  Clear filters
                </Button>
              }
            />
          ) : (
            <EmptyState
              icon={Icon}
              title={`No ${cfg.nouns}`}
              description={
                family === "rds"
                  ? "Create a PostgreSQL, MySQL, MariaDB or MongoDB database. It gets a private endpoint in your VPC, generated master credentials in Secrets Manager and daily backups."
                  : "Create a Redis, Valkey or Memcached cluster. It gets a private endpoint in your VPC and, for Redis and Valkey, an auth token in Secrets Manager."
              }
              action={createBtn}
            />
          )
        }
      />
      {actions.dialogs}
    </div>
  )
}
