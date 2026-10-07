"use client"

import { useState, type ReactNode } from "react"
import { Loader2, ShieldAlert } from "lucide-react"
import { toast } from "sonner"

import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert"
import { Button } from "@/components/ui/button"
import { Checkbox } from "@/components/ui/checkbox"
import { ConfirmDialog } from "@/components/console/confirm-dialog"
import { StatusBadge } from "@/components/console/status-badge"
import { api, errorMessage, seg } from "@/lib/api"
import { pluralize } from "@/lib/format"
import { revalidate } from "@/lib/hooks"
import type { DbInstance } from "@/lib/types"
import { DB_INSTANCES_PATH, RDS_PATH, supportsSnapshots, type FamilyConfig } from "./shared"

/** Runs a state change over several instances in parallel and reports the outcome. */
async function bulk(cfg: FamilyConfig, ids: string[], call: (id: string) => Promise<unknown>, done: string) {
  const p = Promise.allSettled(ids.map(call))
  // stop blocks until the container exits; refresh early to show "stopping".
  setTimeout(() => revalidate(DB_INSTANCES_PATH), 400)
  const res = await p
  const ok = res.filter((r) => r.status === "fulfilled").length
  const failed = res.filter((r): r is PromiseRejectedResult => r.status === "rejected")
  if (ok) toast.success(ids.length === 1 ? `${done} ${ids[0]}` : `${done} ${pluralize(ok, cfg.noun)}`)
  for (const f of failed.slice(0, 3)) toast.error(errorMessage(f.reason))
  await revalidate(DB_INSTANCES_PATH)
}

export interface DbActions {
  start: (ids: string[]) => Promise<void>
  stop: (ids: string[]) => Promise<void>
  reboot: (ids: string[]) => Promise<void>
  remove: (list: DbInstance[]) => void
  busy: boolean
  dialogs: ReactNode
}

/**
 * useDbActions provides the start/stop/reboot/delete operations shared by the
 * list and detail pages, plus the delete dialog.
 */
export function useDbActions(cfg: FamilyConfig, opts: { onDeleted?: () => void } = {}): DbActions {
  const [busy, setBusy] = useState(false)
  const [deleting, setDeleting] = useState<DbInstance[] | null>(null)
  const [finalSnap, setFinalSnap] = useState(true)
  const [unprotecting, setUnprotecting] = useState(false)

  const wrap = (call: (id: string) => Promise<unknown>, done: string) => async (ids: string[]) => {
    if (!ids.length) return
    setBusy(true)
    try {
      await bulk(cfg, ids, call, done)
    } finally {
      setBusy(false)
    }
  }

  const start = wrap((id) => api.post(`${DB_INSTANCES_PATH}/${seg(id)}/start`), "Starting")
  const stop = wrap((id) => api.post(`${DB_INSTANCES_PATH}/${seg(id)}/stop`), "Stopped")
  const reboot = wrap((id) => api.post(`${DB_INSTANCES_PATH}/${seg(id)}/reboot`), "Rebooting")

  const list = deleting ?? []
  const one = list.length === 1 ? list[0] : null
  const protectedOnes = list.filter((i) => i.deletion_protection)
  const snapEligible = list.filter((i) => supportsSnapshots(i.engine))
  const canFinalSnap = snapEligible.some((i) => i.status === "available")

  const unprotect = async () => {
    setUnprotecting(true)
    try {
      await Promise.all(protectedOnes.map((i) => api.patch(`${DB_INSTANCES_PATH}/${seg(i.id)}`, { deletion_protection: false })))
      toast.success(`Turned off deletion protection for ${protectedOnes.map((i) => i.id).join(", ")}`)
      setDeleting(list.map((i) => ({ ...i, deletion_protection: false })))
      await revalidate(DB_INSTANCES_PATH)
    } catch (e) {
      toast.error(errorMessage(e))
    } finally {
      setUnprotecting(false)
    }
  }

  const dialogs = (
    <ConfirmDialog
      open={!!deleting}
      onOpenChange={(o) => !o && setDeleting(null)}
      title={one ? `Delete ${one.id}?` : `Delete ${pluralize(list.length, cfg.noun)}?`}
      description={
        <>
          Deleting removes the {cfg.noun} container, its data volume and {cfg.family === "rds" ? "master credentials secret" : "auth token secret"}. Automated{" "}
          {cfg.snaps} are deleted too; manual {cfg.snaps} are kept. This cannot be undone.
        </>
      }
      confirmText={one ? one.id : "delete"}
      actionLabel="Delete"
      onConfirm={async () => {
        if (protectedOnes.length) throw new Error("Turn off deletion protection first")
        const res = await Promise.allSettled(
          list.map((i) =>
            api.del(`${DB_INSTANCES_PATH}/${seg(i.id)}`, finalSnap && supportsSnapshots(i.engine) && i.status === "available" ? { final_snapshot: true } : undefined),
          ),
        )
        await revalidate(RDS_PATH)
        const failed = res.filter((r): r is PromiseRejectedResult => r.status === "rejected")
        const ok = list.length - failed.length
        if (ok) toast.success(list.length === 1 ? `Deleted ${list[0].id}` : `Deleted ${pluralize(ok, cfg.noun)}`)
        if (failed.length) throw failed[0].reason
        opts.onDeleted?.()
      }}
    >
      {list.length > 1 && (
        <ul className="bg-muted/40 max-h-40 overflow-y-auto rounded-md border p-2 text-sm">
          {list.map((i) => (
            <li key={i.id} className="flex items-center justify-between gap-2 py-0.5">
              <span className="truncate font-mono text-[13px]">{i.id}</span>
              <StatusBadge status={i.status} />
            </li>
          ))}
        </ul>
      )}
      {protectedOnes.length > 0 && (
        <Alert variant="warning">
          <ShieldAlert />
          <AlertTitle>Deletion protection is on</AlertTitle>
          <AlertDescription>
            <p>
              {one ? `${one.id} has` : `${protectedOnes.map((i) => i.id).join(", ")} have`} deletion protection enabled, so the {cfg.noun} cannot be
              deleted. Turn it off to continue.
            </p>
            <Button type="button" variant="outline" size="sm" className="mt-2" onClick={unprotect} disabled={unprotecting}>
              {unprotecting && <Loader2 className="animate-spin" />}
              Turn off deletion protection
            </Button>
          </AlertDescription>
        </Alert>
      )}
      {snapEligible.length > 0 && (
        <label className="flex items-start gap-3 rounded-md border p-3 text-sm">
          <Checkbox className="mt-0.5" checked={finalSnap && canFinalSnap} disabled={!canFinalSnap} onCheckedChange={(v) => setFinalSnap(!!v)} />
          <span className="flex flex-col gap-0.5">
            <span className="font-medium">Create final {cfg.snap}</span>
            <span className="text-muted-foreground text-xs">
              {canFinalSnap
                ? `Takes a manual ${cfg.snap} named <id>-final-<timestamp> before deleting, so you can restore the data later. This can take a while.`
                : `A final ${cfg.snap} needs the ${cfg.noun} to be available.`}
            </span>
          </span>
        </label>
      )}
    </ConfirmDialog>
  )

  return {
    start,
    stop,
    reboot,
    remove: (l) => {
      if (!l.length) return
      setFinalSnap(true)
      setDeleting(l)
    },
    busy,
    dialogs,
  }
}
