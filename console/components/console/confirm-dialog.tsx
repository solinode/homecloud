"use client"

import { useEffect, useState, type ReactNode } from "react"
import { Loader2 } from "lucide-react"

import { Button } from "@/components/ui/button"
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog"
import { Input } from "@/components/ui/input"
import { errorMessage } from "@/lib/api"
import { toast } from "sonner"

/**
 * ConfirmDialog asks before a destructive action. With confirmText set, the
 * user must type it (e.g. the bucket name) before the action is enabled.
 * onConfirm may throw; the error is toasted and the dialog stays open.
 */
export function ConfirmDialog({
  open,
  onOpenChange,
  title,
  description,
  children,
  confirmText,
  actionLabel = "Delete",
  destructive = true,
  onConfirm,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
  title: ReactNode
  description?: ReactNode
  children?: ReactNode
  confirmText?: string
  actionLabel?: string
  destructive?: boolean
  onConfirm: () => Promise<unknown> | unknown
}) {
  const [typed, setTyped] = useState("")
  const [pending, setPending] = useState(false)

  useEffect(() => {
    if (open) setTyped("")
  }, [open])

  const enabled = !pending && (!confirmText || typed === confirmText)

  const submit = async (e?: React.FormEvent) => {
    e?.preventDefault()
    if (!enabled) return
    setPending(true)
    try {
      await onConfirm()
      onOpenChange(false)
    } catch (err) {
      toast.error(errorMessage(err))
    } finally {
      setPending(false)
    }
  }

  return (
    <Dialog open={open} onOpenChange={(o) => !pending && onOpenChange(o)}>
      <DialogContent className="sm:max-w-lg">
        <form onSubmit={submit} className="flex flex-col gap-4">
          <DialogHeader>
            <DialogTitle>{title}</DialogTitle>
            {description && <DialogDescription asChild><div>{description}</div></DialogDescription>}
          </DialogHeader>
          {children}
          {confirmText && (
            <div className="flex flex-col gap-2">
              <label className="text-sm" htmlFor="confirm-input">
                To confirm, type <span className="bg-muted rounded px-1 font-mono font-medium">{confirmText}</span> in the field.
              </label>
              <Input id="confirm-input" autoFocus autoComplete="off" value={typed} onChange={(e) => setTyped(e.target.value)} placeholder={confirmText} />
            </div>
          )}
          <DialogFooter>
            <Button type="button" variant="outline" onClick={() => onOpenChange(false)} disabled={pending}>
              Cancel
            </Button>
            <Button type="submit" variant={destructive ? "destructive" : "default"} disabled={!enabled}>
              {pending && <Loader2 className="animate-spin" />}
              {actionLabel}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}
