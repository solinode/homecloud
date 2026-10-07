"use client"

import type { ReactNode } from "react"
import { AlertTriangle } from "lucide-react"

import { Alert, AlertDescription } from "@/components/ui/alert"
import { Button } from "@/components/ui/button"
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog"
import { CodeBlock } from "@/components/console/code-block"

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
            <CodeBlock key={v.label} title={v.label} code={v.value} copyLabel={`Copy ${v.label.toLowerCase()}`} wrap>
              <code className="block break-all whitespace-pre-wrap" data-testid={`once-${v.label}`}>
                {v.value}
              </code>
            </CodeBlock>
          ))}
          {values.some((v) => v.secret) && (
            <Alert variant="warning">
              <AlertTriangle />
              <AlertDescription>
                This is the only time the {values.filter((v) => v.secret).map((v) => v.label.toLowerCase()).join(" and ")} is shown. Copy it now and store it
                safely.
              </AlertDescription>
            </Alert>
          )}
        </div>
        <DialogFooter>
          <Button onClick={onClose}>Done</Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
