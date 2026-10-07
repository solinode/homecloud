"use client"

import { useState } from "react"
import { Check, Copy } from "lucide-react"
import { toast } from "sonner"

import { Button } from "@/components/ui/button"
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip"
import { cn } from "@/lib/utils"

export async function copyText(text: string): Promise<boolean> {
  try {
    await navigator.clipboard.writeText(text)
    return true
  } catch {
    // Fallback for non-secure contexts (plain http on a LAN address).
    const ta = document.createElement("textarea")
    ta.value = text
    ta.style.position = "fixed"
    ta.style.opacity = "0"
    document.body.appendChild(ta)
    ta.select()
    const ok = document.execCommand("copy")
    document.body.removeChild(ta)
    return ok
  }
}

/** CopyButton copies a value to the clipboard with a check-mark confirmation. */
export function CopyButton({
  value,
  label,
  className,
  size = "icon",
  toastMessage,
}: {
  value: string
  label?: string
  className?: string
  size?: "icon" | "sm"
  toastMessage?: string
}) {
  const [done, setDone] = useState(false)
  const onClick = async (e: React.MouseEvent) => {
    e.stopPropagation()
    e.preventDefault()
    if (await copyText(value)) {
      setDone(true)
      if (toastMessage) toast.success(toastMessage)
      setTimeout(() => setDone(false), 1500)
    } else {
      toast.error("Could not copy to the clipboard")
    }
  }
  const Icon = done ? Check : Copy
  if (size === "sm") {
    return (
      <Button type="button" variant="outline" size="sm" onClick={onClick} className={className}>
        <Icon className={cn(done && "text-success")} />
        {label ?? "Copy"}
      </Button>
    )
  }
  return (
    <Tooltip>
      <TooltipTrigger asChild>
        <button
          type="button"
          onClick={onClick}
          aria-label={label ?? "Copy"}
          className={cn(
            "text-faint hover:text-foreground hover:bg-accent inline-flex size-6 shrink-0 items-center justify-center rounded-md transition-colors",
            className,
          )}
        >
          <Icon className={cn("size-3.5", done && "text-success")} />
        </button>
      </TooltipTrigger>
      <TooltipContent>{done ? "Copied" : (label ?? "Copy")}</TooltipContent>
    </Tooltip>
  )
}

/** CopyableText shows a value (monospace by default) followed by a copy button. */
export function CopyableText({ value, mono = true, className, display }: { value: string; mono?: boolean; className?: string; display?: React.ReactNode }) {
  if (!value) return <span className="text-muted-foreground">-</span>
  return (
    <span className={cn("inline-flex max-w-full items-center gap-1", className)}>
      <span className={cn("truncate", mono && "font-mono text-[0.8125rem]")} title={value}>
        {display ?? value}
      </span>
      <CopyButton value={value} />
    </span>
  )
}
