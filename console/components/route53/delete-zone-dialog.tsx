"use client"

import { useEffect, useState } from "react"
import { useRouter } from "next/navigation"
import { toast } from "sonner"

import { Checkbox } from "@/components/ui/checkbox"
import { ConfirmDialog } from "@/components/console/confirm-dialog"
import { api, seg } from "@/lib/api"
import { pluralize } from "@/lib/format"
import { revalidate } from "@/lib/hooks"

import { R53_PATH, ZONES_PATH, zoneLabel } from "./shared"

/**
 * DeleteZoneDialog deletes a hosted zone after the user types its name. A zone
 * with records needs force=true, which the user opts into explicitly.
 */
export function DeleteZoneDialog({
  zone,
  onClose,
  redirect,
}: {
  /** records: user-created records (excluding SOA/NS) */
  zone: { id: string; name: string; records: number } | null
  onClose: () => void
  redirect?: boolean
}) {
  const router = useRouter()
  const [force, setForce] = useState(false)
  useEffect(() => setForce(false), [zone])
  const label = zone ? zoneLabel(zone.name) : ""
  const hasRecords = !!zone && zone.records > 0

  return (
    <ConfirmDialog
      open={!!zone}
      onOpenChange={(o) => !o && onClose()}
      title={`Delete hosted zone ${label}`}
      confirmText={label}
      description={
        <p>
          The zone stops answering immediately. Clients that cached its records keep them until their TTL expires.
          {!hasRecords && " The zone has no records besides the generated SOA and NS."}
        </p>
      }
      onConfirm={async () => {
        if (!zone) return
        if (hasRecords && !force) throw new Error(`Confirm that the ${pluralize(zone.records, "record")} in the zone should be deleted too`)
        await api.del(`${ZONES_PATH}/${seg(zone.id)}`, hasRecords ? { force: true } : undefined)
        toast.success(`Deleted hosted zone ${label}`)
        if (redirect) router.push("/route53/")
        await revalidate(R53_PATH)
      }}
    >
      {hasRecords && (
        <label className="bg-warning-soft border-warning/25 text-foreground flex cursor-pointer items-start gap-2 rounded-lg border p-3 text-sm">
          <Checkbox className="mt-0.5" checked={force} onCheckedChange={(c) => setForce(!!c)} />
          <span>
            Also delete the {pluralize(zone!.records, "record")} in this zone (force delete).
          </span>
        </label>
      )}
    </ConfirmDialog>
  )
}
