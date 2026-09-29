"use client"

import { useEffect, useState, type ReactNode } from "react"
import { Loader2 } from "lucide-react"
import { toast } from "sonner"

import { Button } from "@/components/ui/button"
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog"
import { errorMessage } from "@/lib/api"
import { useApi } from "@/lib/hooks"
import type { PolicySummary } from "@/lib/types"

import { CheckList, IAM, PolicyPicker } from "./common"

/**
 * runEach applies fn to every item in order, stopping at the first failure.
 * Returns how many succeeded; the failure is toasted.
 */
export async function runEach(items: string[], fn: (item: string) => Promise<unknown>): Promise<number> {
  let n = 0
  for (const it of items) {
    try {
      await fn(it)
      n++
    } catch (e) {
      toast.error(`${it}: ${errorMessage(e)}`)
      break
    }
  }
  return n
}

/** AttachPoliciesDialog picks policies that are not attached yet. */
export function AttachPoliciesDialog({
  open,
  onOpenChange,
  title,
  description,
  exclude,
  onAttach,
  actionLabel = "Attach policies",
}: {
  open: boolean
  onOpenChange: (o: boolean) => void
  title: ReactNode
  description?: ReactNode
  exclude: string[]
  /** Attach the selected policies; return true to close. */
  onAttach: (names: string[]) => Promise<boolean>
  actionLabel?: string
}) {
  const policies = useApi<PolicySummary[]>(open ? `${IAM}/policies` : null)
  const [selected, setSelected] = useState<string[]>([])
  const [pending, setPending] = useState(false)
  useEffect(() => {
    if (open) setSelected([])
  }, [open])

  const submit = async () => {
    setPending(true)
    try {
      if (await onAttach(selected)) onOpenChange(false)
    } finally {
      setPending(false)
    }
  }

  return (
    <Dialog open={open} onOpenChange={(o) => !pending && onOpenChange(o)}>
      <DialogContent className="max-h-[92vh] overflow-y-auto sm:max-w-2xl">
        <DialogHeader>
          <DialogTitle>{title}</DialogTitle>
          {description && <DialogDescription>{description}</DialogDescription>}
        </DialogHeader>
        <PolicyPicker policies={policies.data} loading={policies.isLoading} selected={selected} onChange={setSelected} exclude={exclude} />
        <DialogFooter>
          <Button variant="outline" onClick={() => onOpenChange(false)} disabled={pending}>
            Cancel
          </Button>
          <Button onClick={submit} disabled={pending || !selected.length}>
            {pending && <Loader2 className="animate-spin" />} {actionLabel}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

/** PickDialog is a checkbox list dialog (add users to a group, add a user to groups). */
export function PickDialog({
  open,
  onOpenChange,
  title,
  description,
  items,
  loading,
  empty,
  actionLabel,
  onSubmit,
}: {
  open: boolean
  onOpenChange: (o: boolean) => void
  title: ReactNode
  description?: ReactNode
  items: { id: string; label: ReactNode; sub?: ReactNode }[]
  loading?: boolean
  empty: ReactNode
  actionLabel: string
  onSubmit: (ids: string[]) => Promise<boolean>
}) {
  const [selected, setSelected] = useState<string[]>([])
  const [pending, setPending] = useState(false)
  useEffect(() => {
    if (open) setSelected([])
  }, [open])

  const submit = async () => {
    setPending(true)
    try {
      if (await onSubmit(selected)) onOpenChange(false)
    } finally {
      setPending(false)
    }
  }

  return (
    <Dialog open={open} onOpenChange={(o) => !pending && onOpenChange(o)}>
      <DialogContent className="max-h-[92vh] overflow-y-auto">
        <DialogHeader>
          <DialogTitle>{title}</DialogTitle>
          {description && <DialogDescription>{description}</DialogDescription>}
        </DialogHeader>
        <CheckList items={items} selected={selected} onChange={setSelected} empty={empty} loading={loading} />
        <DialogFooter>
          <Button variant="outline" onClick={() => onOpenChange(false)} disabled={pending}>
            Cancel
          </Button>
          <Button onClick={submit} disabled={pending || !selected.length}>
            {pending && <Loader2 className="animate-spin" />} {actionLabel}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
