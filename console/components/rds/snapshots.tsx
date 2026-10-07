"use client"

import { useEffect, useMemo, useState } from "react"
import Link from "next/link"
import { useRouter } from "next/navigation"
import { Archive, Camera, History, Loader2 } from "lucide-react"
import { toast } from "sonner"

import { Button } from "@/components/ui/button"
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select"
import { Switch } from "@/components/ui/switch"
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip"
import { ActionsMenu, type ActionItem } from "@/components/console/actions-menu"
import { ConfirmDialog } from "@/components/console/confirm-dialog"
import { CellLink, CellText, DataTable, type Column } from "@/components/console/data-table"
import { EmptyState } from "@/components/console/empty-state"
import { Field } from "@/components/console/form-field"
import { PageHeader } from "@/components/console/page-header"
import { StatusBadge } from "@/components/console/status-badge"
import { Tag } from "@/components/console/tag"
import { TimeAgo } from "@/components/console/time-ago"
import { api, errorMessage, seg } from "@/lib/api"
import { formatBytes, formatDate } from "@/lib/format"
import { revalidate, useApi } from "@/lib/hooks"
import type { DbInstance, DbSnapshot } from "@/lib/types"
import {
  ClassSelect,
  DB_INSTANCES_PATH,
  DB_SNAPSHOTS_PATH,
  EngineCell,
  FAMILIES,
  RDS_PATH,
  dbHref,
  familyOfKind,
  idError,
  inFamily,
  useEngines,
  type Family,
  type FamilyConfig,
} from "./shared"

function snapColumns(cfg: FamilyConfig, withSource: boolean, engines: ReturnType<typeof useEngines>["data"]): Column<DbSnapshot>[] {
  const cols: Column<DbSnapshot>[] = [
    {
      id: "id",
      header: `${cfg.Snap} ID`,
      cell: (s) => (
        <CellText mono className="font-medium">
          {s.id}
        </CellText>
      ),
      value: (s) => s.id,
    },
  ]
  if (withSource) {
    cols.push(
      {
        id: "source",
        header: `Source ${cfg.noun}`,
        cell: (s) => (
          <CellLink href={dbHref(s.kind, s.source_instance)} mono>
            {s.source_instance}
          </CellLink>
        ),
        value: (s) => s.source_instance,
      },
      {
        id: "engine",
        header: "Engine",
        cell: (s) => <EngineCell engine={s.engine} version={s.engine_version} engines={engines?.engines} />,
        value: (s) => `${s.engine} ${s.engine_version}`,
        hideBelow: "md",
      },
    )
  }
  cols.push(
    {
      id: "type",
      header: "Type",
      cell: (s) => (
        <Tag mono={false} accent={s.type === "manual" ? "brand" : "neutral"}>
          {s.type === "manual" ? "Manual" : "Automated"}
        </Tag>
      ),
      value: (s) => s.type,
      hideBelow: "sm",
    },
    {
      id: "status",
      header: "Status",
      cell: (s) =>
        s.status_reason ? (
          <Tooltip>
            <TooltipTrigger asChild>
              <span>
                <StatusBadge status={s.status} />
              </span>
            </TooltipTrigger>
            <TooltipContent className="max-w-sm">{s.status_reason}</TooltipContent>
          </Tooltip>
        ) : (
          <StatusBadge status={s.status} />
        ),
      value: (s) => s.status,
    },
    { id: "size", header: "Size", cell: (s) => <span className="tabular-nums whitespace-nowrap">{formatBytes(s.size_bytes)}</span>, value: (s) => s.size_bytes, hideBelow: "sm" },
    {
      id: "created",
      header: "Created",
      cell: (s) => (
        <span title={formatDate(s.created_at)}>
          <TimeAgo value={s.created_at} />
        </span>
      ),
      value: (s) => s.created_at,
      hideBelow: "md",
    },
  )
  return cols
}

/** useSnapshotActions holds the restore and delete dialogs for snapshots. */
function useSnapshotActions(cfg: FamilyConfig) {
  const [restoring, setRestoring] = useState<DbSnapshot | null>(null)
  const [deleting, setDeleting] = useState<DbSnapshot | null>(null)
  const dialogs = (
    <>
      <RestoreDialog snapshot={restoring} onClose={() => setRestoring(null)} />
      <ConfirmDialog
        open={!!deleting}
        onOpenChange={(o) => !o && setDeleting(null)}
        title={`Delete ${cfg.snap} ${deleting?.id ?? ""}?`}
        description={`The ${cfg.snap} file is removed from disk. ${cfg.Nouns} already restored from it are not affected.`}
        confirmText={deleting?.id}
        onConfirm={async () => {
          if (!deleting) return
          await api.del(`${DB_SNAPSHOTS_PATH}/${seg(deleting.id)}`)
          toast.success(`Deleted ${cfg.snap} ${deleting.id}`)
          await revalidate(DB_SNAPSHOTS_PATH)
        }}
      />
    </>
  )
  return { restore: setRestoring, remove: setDeleting, dialogs }
}

function snapPoll(d: DbSnapshot[] | undefined, extra = false) {
  return extra || (d ?? []).some((s) => s.status === "creating") ? 2000 : 15_000
}

/** SnapshotsPage lists every snapshot (backup) of the family. */
export function SnapshotsPage({ family }: { family: Family }) {
  const cfg = FAMILIES[family]
  const engines = useEngines()
  const { data, error, isLoading, isValidating, mutate } = useApi<DbSnapshot[]>(DB_SNAPSHOTS_PATH, { refreshInterval: (d) => snapPoll(d) })
  const [selected, setSelected] = useState<string[]>([])
  const [type, setType] = useState("all")
  const actions = useSnapshotActions(cfg)

  const mine = useMemo(() => (data ?? []).filter((s) => inFamily(cfg, s.kind)), [data, cfg])
  const rows = useMemo(() => (type === "all" ? mine : mine.filter((s) => s.type === type)), [mine, type])
  const columns = useMemo(() => snapColumns(cfg, true, engines.data), [cfg, engines.data])
  const sel = rows.find((s) => selected.includes(s.id)) ?? null

  const items: ActionItem[] = [
    { label: `Restore ${cfg.snap}`, onSelect: () => sel && actions.restore(sel), disabled: !sel || sel.status !== "available" },
    { separator: true },
    { label: `Delete ${cfg.snap}`, destructive: true, onSelect: () => sel && actions.remove(sel), disabled: !sel || sel.status === "creating" },
  ]

  return (
    <div className="flex flex-col gap-4">
      <PageHeader
        title={cfg.Snaps}
        description={
          family === "rds"
            ? "Point-in-time dumps of your databases. Restore a snapshot to create a new database with the same engine, data and master credentials."
            : "Point-in-time RDB dumps of your Redis and Valkey clusters. Restore a backup to create a new cluster with the same data and auth token."
        }
        breadcrumbs={[{ label: cfg.service, href: cfg.base }, { label: cfg.Snaps }]}
      />
      <DataTable
        title={cfg.Snaps}
        data={data ? rows : undefined}
        columns={columns}
        rowId={(s) => s.id}
        loading={isLoading}
        error={error}
        onRefresh={() => mutate()}
        refreshing={isValidating}
        selection="single"
        selected={selected}
        onSelectedChange={setSelected}
        searchPlaceholder={`Find ${cfg.snaps} by ID, source or engine`}
        defaultSort={{ id: "created", desc: true }}
        actions={
          <>
            <ActionsMenu items={items} disabled={!sel} />
            <Button size="sm" disabled={!sel || sel.status !== "available"} onClick={() => sel && actions.restore(sel)}>
              <History /> Restore
            </Button>
          </>
        }
        filters={
          <Select value={type} onValueChange={setType}>
            <SelectTrigger size="sm" className="h-8 w-40" aria-label="Type filter">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="all">All types</SelectItem>
              <SelectItem value="manual">Manual</SelectItem>
              <SelectItem value="automated">Automated</SelectItem>
            </SelectContent>
          </Select>
        }
        empty={
          mine.length > 0 ? (
            <EmptyState icon={Archive} title={`No ${type} ${cfg.snaps}`} action={<Button variant="outline" size="sm" onClick={() => setType("all")}>Show all</Button>} />
          ) : (
            <EmptyState
              icon={Archive}
              title={`No ${cfg.snaps}`}
              description={`Take a manual ${cfg.snap} from a ${cfg.noun}'s ${cfg.Snaps} tab. ${cfg.Nouns} with a backup retention period also get a daily automated ${cfg.snap}.`}
              action={
                <Button size="sm" variant="outline" asChild>
                  <Link href={cfg.base}>Go to {cfg.nouns}</Link>
                </Button>
              }
            />
          )
        }
      />
      {actions.dialogs}
    </div>
  )
}

/** InstanceSnapshots is the Snapshots / Backups tab of a database or cluster. */
export function InstanceSnapshots({ cfg, inst }: { cfg: FamilyConfig; inst: DbInstance }) {
  const [taking, setTaking] = useState(false)
  const [busy, setBusy] = useState(false)
  const { data, error, isLoading, isValidating, mutate } = useApi<DbSnapshot[]>(DB_SNAPSHOTS_PATH, {
    query: { instance: inst.id },
    refreshInterval: (d) => snapPoll(d, busy),
  })
  const [selected, setSelected] = useState<string[]>([])
  const actions = useSnapshotActions(cfg)
  const columns = useMemo(() => snapColumns(cfg, false, undefined), [cfg])
  const sel = (data ?? []).find((s) => selected.includes(s.id)) ?? null

  const items: ActionItem[] = [
    { label: `Restore ${cfg.snap}`, onSelect: () => sel && actions.restore(sel), disabled: !sel || sel.status !== "available" },
    { separator: true },
    { label: `Delete ${cfg.snap}`, destructive: true, onSelect: () => sel && actions.remove(sel), disabled: !sel || sel.status === "creating" },
  ]

  return (
    <div className="flex flex-col gap-4">
      <DataTable
        title={cfg.Snaps}
        description={
          inst.backup_retention_days > 0
            ? `Automated ${cfg.snaps} run daily and are kept for ${inst.backup_retention_days} day${inst.backup_retention_days === 1 ? "" : "s"}. Manual ${cfg.snaps} are kept until you delete them.`
            : `Automated ${cfg.snaps} are off (retention 0 days). Manual ${cfg.snaps} are kept until you delete them.`
        }
        data={data}
        columns={columns}
        rowId={(s) => s.id}
        loading={isLoading}
        error={error}
        onRefresh={() => mutate()}
        refreshing={isValidating}
        selection="single"
        selected={selected}
        onSelectedChange={setSelected}
        noSearch={(data?.length ?? 0) < 6}
        defaultSort={{ id: "created", desc: true }}
        actions={
          <>
            <ActionsMenu items={items} disabled={!sel} />
            <Button size="sm" onClick={() => setTaking(true)} disabled={inst.status !== "available"} title={inst.status !== "available" ? `The ${cfg.noun} must be available` : undefined}>
              <Camera /> Take {cfg.snap}
            </Button>
          </>
        }
        empty={
          <EmptyState
            icon={Archive}
            title={`No ${cfg.snaps} yet`}
            description={`A ${cfg.snap} captures the ${cfg.noun}'s data so you can restore it into a new ${cfg.noun}.`}
            action={
              <Button size="sm" variant="outline" onClick={() => setTaking(true)} disabled={inst.status !== "available"}>
                <Camera /> Take {cfg.snap}
              </Button>
            }
          />
        }
      />
      <TakeSnapshotDialog cfg={cfg} inst={taking ? inst : null} onClose={() => setTaking(false)} onBusy={setBusy} />
      {actions.dialogs}
    </div>
  )
}

function TakeSnapshotDialog({ cfg, inst, onClose, onBusy }: { cfg: FamilyConfig; inst: DbInstance | null; onClose: () => void; onBusy: (b: boolean) => void }) {
  const [id, setId] = useState("")
  const [err, setErr] = useState<string | undefined>()
  const [pending, setPending] = useState(false)
  useEffect(() => {
    if (inst) {
      setId("")
      setErr(undefined)
    }
  }, [inst])

  const submit = async (e: React.FormEvent) => {
    e.preventDefault()
    if (!inst) return
    const v = id.trim()
    const bad = v ? idError(v, `${cfg.Snap} ID`) : undefined
    if (bad) {
      setErr(bad)
      return
    }
    setPending(true)
    onBusy(true)
    // The request blocks while the dump runs; show the "backing-up" state meanwhile.
    const t = setTimeout(() => revalidate(RDS_PATH), 400)
    try {
      const sn = await api.post<DbSnapshot>(`${DB_INSTANCES_PATH}/${seg(inst.id)}/snapshots`, v ? { id: v } : {})
      toast.success(`Created ${cfg.snap} ${sn.id}`)
      onClose()
    } catch (e) {
      toast.error(errorMessage(e))
    } finally {
      clearTimeout(t)
      setPending(false)
      onBusy(false)
      await revalidate(RDS_PATH)
    }
  }

  return (
    <Dialog open={!!inst} onOpenChange={(o) => !o && !pending && onClose()}>
      <DialogContent>
        <form onSubmit={submit} className="flex flex-col gap-4">
          <DialogHeader>
            <DialogTitle>Take {cfg.snap}</DialogTitle>
            <DialogDescription>
              Dumps the {cfg.noun}&apos;s data to a file on this host. The {cfg.noun} shows <em>Backing up</em> until the dump finishes; it stays online.
            </DialogDescription>
          </DialogHeader>
          <Field
            label={`${cfg.Snap} ID`}
            htmlFor="snap-id"
            optional
            error={err}
            help={`Lowercase letters, digits and hyphens. Leave empty for ${inst?.id ?? "<id>"}-<date>-<time>.`}
          >
            <Input id="snap-id" value={id} onChange={(e) => (setId(e.target.value), setErr(undefined))} placeholder={`${inst?.id ?? ""}-manual`} autoFocus className="font-mono" />
          </Field>
          <DialogFooter>
            <Button type="button" variant="outline" onClick={onClose} disabled={pending}>
              Cancel
            </Button>
            <Button type="submit" disabled={pending}>
              {pending && <Loader2 className="animate-spin" />}
              {pending ? `Taking ${cfg.snap}...` : `Take ${cfg.snap}`}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}

function RestoreDialog({ snapshot, onClose }: { snapshot: DbSnapshot | null; onClose: () => void }) {
  const router = useRouter()
  const cfg = FAMILIES[familyOfKind(snapshot?.kind ?? "")]
  const [id, setId] = useState("")
  const [cls, setCls] = useState("")
  const [pub, setPub] = useState(false)
  const [protect, setProtect] = useState(false)
  const [err, setErr] = useState<string | undefined>()
  const [pending, setPending] = useState(false)
  const source = useApi<DbInstance>(snapshot ? `${DB_INSTANCES_PATH}/${seg(snapshot.source_instance)}` : null, { revalidateOnFocus: false })

  useEffect(() => {
    if (snapshot) {
      setId(`${snapshot.source_instance}-restored`.slice(0, 63))
      setPub(false)
      setProtect(false)
      setErr(undefined)
    }
  }, [snapshot])
  // Default to the source's class while it still exists.
  const srcClass = source.data?.class
  useEffect(() => {
    if (snapshot) setCls(srcClass ?? (cfg.classKind === "cache" ? "cache.t3.micro" : "db.t3.micro"))
  }, [snapshot, srcClass, cfg.classKind])

  const submit = async (e: React.FormEvent) => {
    e.preventDefault()
    if (!snapshot) return
    const bad = idError(id.trim(), `${cfg.Noun} identifier`)
    if (bad) {
      setErr(bad)
      return
    }
    setPending(true)
    try {
      const inst = await api.post<DbInstance>(`${DB_SNAPSHOTS_PATH}/${seg(snapshot.id)}/restore`, {
        id: id.trim(),
        class: cls || undefined,
        publicly_accessible: pub,
        deletion_protection: protect,
      })
      toast.success(`Restoring ${snapshot.id} into ${inst.id}`)
      await revalidate(RDS_PATH)
      onClose()
      router.push(dbHref(inst.kind, inst.id))
    } catch (e) {
      toast.error(errorMessage(e))
    } finally {
      setPending(false)
    }
  }

  return (
    <Dialog open={!!snapshot} onOpenChange={(o) => !o && !pending && onClose()}>
      <DialogContent>
        <form onSubmit={submit} className="flex flex-col gap-4">
          <DialogHeader>
            <DialogTitle>Restore {cfg.snap}</DialogTitle>
            <DialogDescription>
              Creates a new {cfg.noun} from <span className="font-mono">{snapshot?.id}</span> with the same engine ({snapshot?.engine} {snapshot?.engine_version})
              {cfg.family === "rds" ? ", data and master credentials" : " and data"}. The source {cfg.noun} is not changed.
            </DialogDescription>
          </DialogHeader>
          <Field label={`New ${cfg.idLabel}`} htmlFor="restore-id" error={err} help="Lowercase letters, digits and hyphens; must start with a letter.">
            <Input id="restore-id" value={id} onChange={(e) => (setId(e.target.value), setErr(undefined))} autoFocus className="font-mono" />
          </Field>
          <Field label={cfg.family === "rds" ? "DB instance class" : "Node type"} htmlFor="restore-class">
            <ClassSelect id="restore-class" kind={cfg.classKind} value={cls} onChange={setCls} />
          </Field>
          <div className="flex items-center justify-between gap-4">
            <Label htmlFor="restore-public" className="flex flex-col items-start gap-0.5">
              <span>Public access</span>
              <span className="text-muted-foreground text-xs font-normal">Publish the port on this host so clients outside the VPC can connect.</span>
            </Label>
            <Switch id="restore-public" checked={pub} onCheckedChange={setPub} />
          </div>
          <div className="flex items-center justify-between gap-4">
            <Label htmlFor="restore-protect" className="flex flex-col items-start gap-0.5">
              <span>Deletion protection</span>
              <span className="text-muted-foreground text-xs font-normal">Block deletes until it is turned off.</span>
            </Label>
            <Switch id="restore-protect" checked={protect} onCheckedChange={setProtect} />
          </div>
          <DialogFooter>
            <Button type="button" variant="outline" onClick={onClose} disabled={pending}>
              Cancel
            </Button>
            <Button type="submit" disabled={pending}>
              {pending && <Loader2 className="animate-spin" />}
              Restore
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}
