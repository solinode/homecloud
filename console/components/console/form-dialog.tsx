"use client"

import { useEffect, useRef, useState, type ReactNode } from "react"
import { Loader2 } from "lucide-react"
import { toast } from "sonner"

import { Button } from "@/components/ui/button"
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog"
import { errorMessage } from "@/lib/api"

/** Return KEEP_OPEN from onSubmit to leave the dialog open after success. */
export const KEEP_OPEN = Symbol("keep-open")

/**
 * FormDialog is a modal form. onSubmit may throw (the error is toasted and the
 * dialog stays open); on success the dialog closes. Mount the form contents
 * only while open (children are unmounted on close, so state resets).
 */
export function FormDialog({
  open,
  onOpenChange,
  title,
  description,
  submitLabel,
  onSubmit,
  disabled,
  wide,
  children,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
  title: ReactNode
  description?: ReactNode
  submitLabel: string
  onSubmit: () => Promise<unknown>
  disabled?: boolean
  wide?: boolean
  children: ReactNode
}) {
  const [pending, setPending] = useState(false)
  // A ref, not state: two quick Enter presses arrive before a re-render.
  const inFlight = useRef(false)
  const mounted = useRef(true)
  useEffect(() => {
    mounted.current = true
    return () => {
      mounted.current = false
    }
  }, [])
  const submit = async (e: React.FormEvent) => {
    e.preventDefault()
    e.stopPropagation() // dialogs portal out of, but React-bubble into, an enclosing form
    if (disabled || inFlight.current) return
    inFlight.current = true
    setPending(true)
    try {
      const keepOpen = (await onSubmit()) === KEEP_OPEN
      if (!keepOpen) onOpenChange(false)
    } catch (err) {
      toast.error(errorMessage(err))
    } finally {
      inFlight.current = false
      if (mounted.current) setPending(false)
    }
  }
  return (
    <Dialog open={open} onOpenChange={(o) => !pending && onOpenChange(o)}>
      <DialogContent className={wide ? "sm:max-w-2xl" : undefined}>
        <form onSubmit={submit} className="flex flex-col gap-4">
          <DialogHeader>
            <DialogTitle>{title}</DialogTitle>
            {description && <DialogDescription>{description}</DialogDescription>}
          </DialogHeader>
          {open && children}
          <DialogFooter>
            <Button type="button" variant="outline" onClick={() => onOpenChange(false)} disabled={pending}>
              Cancel
            </Button>
            <Button type="submit" disabled={pending || disabled}>
              {pending && <Loader2 className="animate-spin" />}
              {submitLabel}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}
