"use client"

import Link from "next/link"
import { useEffect, useMemo, useState } from "react"
import { Info, Loader2, ShieldAlert } from "lucide-react"
import { toast } from "sonner"

import { Button } from "@/components/ui/button"
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog"
import { Select, SelectContent, SelectGroup, SelectItem, SelectLabel, SelectTrigger, SelectValue } from "@/components/ui/select"
import { CopyableText } from "@/components/console/copy-button"
import { Field } from "@/components/console/form-field"
import { Section } from "@/components/console/section"
import { errorMessage } from "@/lib/api"
import { useApi } from "@/lib/hooks"
import type { PolicySummary } from "@/lib/types"

import { IAM, LINK, PolicyTypeBadge, policyHref, policyNameFromArn } from "./common"

const NONE = "__none__"

/**
 * BoundarySelect picks a managed policy (by ARN) to use as a permissions
 * boundary, or none.
 */
export function BoundarySelect({ value, onChange, id, className }: { value: string; onChange: (arn: string) => void; id?: string; className?: string }) {
  const { data, isLoading } = useApi<PolicySummary[]>(`${IAM}/policies`)
  const [aws, customer] = useMemo(() => {
    const all = [...(data ?? [])].sort((a, b) => a.name.localeCompare(b.name))
    return [all.filter((p) => p.managed), all.filter((p) => !p.managed)]
  }, [data])
  return (
    <Select value={value || NONE} onValueChange={(v) => onChange(v === NONE ? "" : v)} disabled={isLoading}>
      <SelectTrigger id={id} className={className ?? "w-full sm:w-96"} aria-label="Permissions boundary">
        <SelectValue placeholder={isLoading ? "Loading policies..." : "No permissions boundary"} />
      </SelectTrigger>
      <SelectContent className="max-h-80">
        <SelectItem value={NONE}>No permissions boundary</SelectItem>
        {customer.length > 0 && (
          <SelectGroup>
            <SelectLabel>Customer managed</SelectLabel>
            {customer.map((p) => (
              <SelectItem key={p.arn} value={p.arn}>
                {p.name}
              </SelectItem>
            ))}
          </SelectGroup>
        )}
        <SelectGroup>
          <SelectLabel>AWS managed</SelectLabel>
          {aws.map((p) => (
            <SelectItem key={p.arn} value={p.arn}>
              {p.name}
            </SelectItem>
          ))}
        </SelectGroup>
      </SelectContent>
    </Select>
  )
}

/** BoundaryHelp explains what a permissions boundary does. */
export function BoundaryHelp({ kind }: { kind: "user" | "role" }) {
  return (
    <>
      A permissions boundary is a managed policy that sets the maximum permissions the {kind} can have. The {kind}&apos;s effective permissions are the ones allowed by both its
      policies and the boundary.
    </>
  )
}

/**
 * PermissionsBoundarySection shows a user's or role's permissions boundary.
 * With onSet (arn "" removes) it can change it.
 */
export function PermissionsBoundarySection({
  kind,
  arn,
  onSet,
  readOnlyNote,
}: {
  kind: "user" | "role"
  arn: string | null | undefined
  onSet?: (arn: string) => Promise<unknown>
  readOnlyNote?: string
}) {
  const policies = useApi<PolicySummary[]>(arn ? `${IAM}/policies` : null)
  const policy = arn ? (policies.data ?? []).find((p) => p.arn === arn) : undefined
  const missing = !!arn && !!policies.data && !policy
  const [open, setOpen] = useState(false)
  const [removing, setRemoving] = useState(false)

  const remove = async () => {
    if (!onSet) return
    setRemoving(true)
    try {
      await onSet("")
      toast.success("Permissions boundary removed")
    } catch (e) {
      toast.error(errorMessage(e))
    } finally {
      setRemoving(false)
    }
  }

  return (
    <Section
      title="Permissions boundary"
      description={<BoundaryHelp kind={kind} />}
      actions={
        onSet ? (
          <>
            {arn && (
              <Button size="sm" variant="outline" onClick={remove} disabled={removing}>
                {removing && <Loader2 className="animate-spin" />} Remove boundary
              </Button>
            )}
            <Button size="sm" variant="outline" onClick={() => setOpen(true)}>
              {arn ? "Change boundary" : "Set boundary"}
            </Button>
          </>
        ) : undefined
      }
    >
      <div className="flex flex-col gap-2 text-sm">
        {arn ? (
          <>
            <div className="flex flex-wrap items-center gap-2">
              <ShieldAlert className="text-warning size-4 shrink-0" />
              <Link href={policyHref(policyNameFromArn(arn))} className={LINK}>
                {policyNameFromArn(arn)}
              </Link>
              {policy && <PolicyTypeBadge managed={policy.managed} />}
            </div>
            <CopyableText value={arn} className="text-[13px]" />
            {missing && <p className="text-destructive text-xs">The boundary policy no longer exists, so it allows nothing: the {kind} has no effective permissions.</p>}
          </>
        ) : (
          <p className="text-muted-foreground">Permissions boundary is not set.</p>
        )}
        {!onSet && readOnlyNote && (
          <p className="text-muted-foreground flex items-start gap-2 text-xs">
            <Info className="mt-0.5 size-3.5 shrink-0" /> {readOnlyNote}
          </p>
        )}
      </div>
      {onSet && <SetBoundaryDialog open={open} onOpenChange={setOpen} kind={kind} current={arn ?? ""} onSet={onSet} />}
    </Section>
  )
}

function SetBoundaryDialog({
  open,
  onOpenChange,
  kind,
  current,
  onSet,
}: {
  open: boolean
  onOpenChange: (o: boolean) => void
  kind: "user" | "role"
  current: string
  onSet: (arn: string) => Promise<unknown>
}) {
  const [value, setValue] = useState(current)
  const [pending, setPending] = useState(false)
  useEffect(() => {
    if (open) setValue(current)
  }, [open, current])
  const save = async () => {
    setPending(true)
    try {
      await onSet(value)
      toast.success(value ? "Permissions boundary set" : "Permissions boundary removed")
      onOpenChange(false)
    } catch (e) {
      toast.error(errorMessage(e))
    } finally {
      setPending(false)
    }
  }
  return (
    <Dialog open={open} onOpenChange={(o) => !pending && onOpenChange(o)}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>Set permissions boundary</DialogTitle>
          <DialogDescription>
            <BoundaryHelp kind={kind} />
          </DialogDescription>
        </DialogHeader>
        <Field label="Boundary policy" htmlFor="set-boundary" help="Choose “No permissions boundary” to remove it.">
          <BoundarySelect id="set-boundary" value={value} onChange={setValue} className="w-full" />
        </Field>
        <DialogFooter>
          <Button variant="outline" onClick={() => onOpenChange(false)} disabled={pending}>
            Cancel
          </Button>
          <Button onClick={save} disabled={pending || value === current}>
            {pending && <Loader2 className="animate-spin" />} Set boundary
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
