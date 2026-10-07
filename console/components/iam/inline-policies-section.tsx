"use client"

import { useState } from "react"
import { FileJson, Plus } from "lucide-react"
import { toast } from "sonner"

import { Button } from "@/components/ui/button"
import { ConfirmDialog } from "@/components/console/confirm-dialog"
import { EmptyState } from "@/components/console/empty-state"
import { Section } from "@/components/console/section"
import { api, seg } from "@/lib/api"
import { pluralize } from "@/lib/format"
import type { PolicyDocument } from "@/lib/types"

import { IAM, statementCount } from "./common"
import { InlinePolicyDialog } from "./inline-policy-dialog"

const LOSES: Record<"user" | "role" | "group", string> = {
  user: "The user loses the permissions this policy grants.",
  role: "The role loses the permissions this policy grants.",
  group: "Users in the group lose the permissions this policy grants.",
}

/**
 * InlinePoliciesSection lists the inline policies of a user, role or group
 * with view, edit, create and delete (PUT/DELETE .../inline-policies/{name}).
 */
export function InlinePoliciesSection({
  kind,
  owner,
  policies,
  onChanged,
  description,
}: {
  kind: "user" | "role" | "group"
  owner: string
  policies: Record<string, PolicyDocument> | null | undefined
  onChanged: () => void
  description?: string
}) {
  const [dialog, setDialog] = useState<{ open: boolean; initial: { name: string; doc: PolicyDocument } | null; readOnly?: boolean }>({ open: false, initial: null })
  const [deleting, setDeleting] = useState<string | null>(null)
  const names = Object.keys(policies ?? {}).sort()

  return (
    <>
      <Section
        title={`Inline policies (${names.length})`}
        description={description ?? `Policies embedded in this ${kind} only.`}
        flush
        actions={
          <Button size="sm" variant="outline" onClick={() => setDialog({ open: true, initial: null })}>
            <Plus /> Create inline policy
          </Button>
        }
      >
        {!names.length ? (
          <EmptyState
            icon={FileJson}
            title="No inline policies"
            description={`Inline policies are embedded in this ${kind} and deleted with it.`}
            action={
              <Button size="sm" variant="outline" onClick={() => setDialog({ open: true, initial: null })}>
                <Plus /> Create inline policy
              </Button>
            }
          />
        ) : (
          <ul>
            {names.map((n) => {
              const doc = policies![n]
              return (
                <li key={n} className="flex flex-wrap items-center justify-between gap-2 border-b px-5 py-2.5 last:border-0">
                  <span className="flex min-w-0 items-center gap-2 text-sm">
                    <FileJson className="text-muted-foreground size-4 shrink-0" />
                    <span className="truncate font-medium">{n}</span>
                    <span className="text-muted-foreground shrink-0 text-xs">{pluralize(statementCount(doc), "statement")}</span>
                  </span>
                  <span className="flex gap-1">
                    <Button size="sm" variant="ghost" onClick={() => setDialog({ open: true, initial: { name: n, doc }, readOnly: true })}>
                      View
                    </Button>
                    <Button size="sm" variant="ghost" onClick={() => setDialog({ open: true, initial: { name: n, doc } })}>
                      Edit
                    </Button>
                    <Button size="sm" variant="ghost" className="text-destructive" onClick={() => setDeleting(n)}>
                      Delete
                    </Button>
                  </span>
                </li>
              )
            })}
          </ul>
        )}
      </Section>

      <InlinePolicyDialog
        open={dialog.open}
        onOpenChange={(o) => setDialog((s) => ({ ...s, open: o }))}
        owner={owner}
        kind={kind}
        initial={dialog.initial}
        readOnly={dialog.readOnly}
        existing={names}
        onSaved={onChanged}
      />

      <ConfirmDialog
        open={!!deleting}
        onOpenChange={(o) => !o && setDeleting(null)}
        title={`Delete inline policy ${deleting}?`}
        description={`${LOSES[kind]} Inline policies cannot be recovered.`}
        onConfirm={async () => {
          await api.del(`${IAM}/${kind}s/${seg(owner)}/inline-policies/${seg(deleting!)}`)
          toast.success(`Inline policy ${deleting} deleted`)
          onChanged()
        }}
      />
    </>
  )
}
