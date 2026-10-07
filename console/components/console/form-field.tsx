import type { ReactNode } from "react"

import { Label } from "@/components/ui/label"
import { cn } from "@/lib/utils"

/** Field is a labelled form control with optional help text and error. */
export function Field({
  label,
  htmlFor,
  help,
  error,
  optional,
  children,
  className,
}: {
  label: ReactNode
  htmlFor?: string
  help?: ReactNode
  error?: ReactNode
  optional?: boolean
  children: ReactNode
  className?: string
}) {
  return (
    <div className={cn("flex flex-col gap-1.5", className)}>
      <Label htmlFor={htmlFor} className="text-[13px] font-medium">
        {label}
        {optional && <span className="text-faint ml-1.5 text-xs font-normal">optional</span>}
      </Label>
      {children}
      {error ? <p className="text-destructive text-xs">{error}</p> : help ? <p className="text-muted-foreground text-xs">{help}</p> : null}
    </div>
  )
}
