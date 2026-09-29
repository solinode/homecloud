"use client"

import type { ReactNode } from "react"
import { AlertTriangle } from "lucide-react"

import { Button } from "@/components/ui/button"
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog"
import { CopyButton } from "@/components/console/copy-button"

export interface OnceValue {
  label: string
  value: string
  secret?: boolean
}

/**
 * ShowOnceDialog displays values the API returns only once (a generated
 * temporary password, an app client secret) with copy buttons.
 */
export function ShowOnceDialog({ open, onClose, title, description, values }: { open: boolean; onClose: () => void; title: string; description?: ReactNode; values: OnceValue[] }) {
  return (
    <Dialog open={open} onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="sm:max-w-lg" onInteractOutside={(e) => e.preventDefault()}>
        <DialogHeader>
          <DialogTitle>{title}</DialogTitle>
          {description && <DialogDescription>{description}</DialogDescription>}
        </DialogHeader>
        <div className="flex flex-col gap-3">
          {values.map((v) => (
            <div key={v.label} className="flex flex-col gap-1">
              <span className="text-muted-foreground text-xs font-medium">{v.label}</span>
              <div className="bg-muted/50 flex items-center gap-2 rounded-md border px-3 py-2">
                <code className="min-w-0 flex-1 font-mono text-[13px] break-all" data-testid={`once-${v.label}`}>
                  {v.value}
                </code>
                <CopyButton value={v.value} label={`Copy ${v.label.toLowerCase()}`} />
              </div>
            </div>
          ))}
          {values.some((v) => v.secret) && (
            <p className="flex gap-2 text-sm text-amber-700 dark:text-amber-400">
              <AlertTriangle className="mt-0.5 size-4 shrink-0" />
              This is the only time the {values.filter((v) => v.secret).map((v) => v.label.toLowerCase()).join(" and ")} is shown. Copy it now and store it
              safely.
            </p>
          )}
        </div>
        <DialogFooter>
          <Button onClick={onClose}>Done</Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
