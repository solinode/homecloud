"use client"

import type { ReactNode } from "react"

import { CopyButton } from "@/components/console/copy-button"
import { cn } from "@/lib/utils"

/**
 * CodeBlock is a copyable monospace block. Like the landing page, code stays
 * dark in both themes. `prompt` prefixes each line with an orange "$".
 */
export function CodeBlock({
  code,
  title,
  prompt,
  className,
  copyLabel = "Copy",
  children,
}: {
  code: string
  title?: ReactNode
  prompt?: boolean
  className?: string
  copyLabel?: string
  /** custom rendering of `code` (e.g. masked secrets); `code` is still what gets copied */
  children?: ReactNode
}) {
  const lines = code.split("\n")
  return (
    <div
      className={cn(
        "bg-code text-code-foreground border-code-line relative overflow-hidden rounded-lg border shadow-[inset_0_1px_0_rgb(255_255_255/0.04)] dark:border-border-strong",
        className,
      )}
    >
      {title && (
        <div className="border-code-line flex h-9 items-center justify-between gap-3 border-b pr-1.5 pl-4 font-mono text-[11.5px] text-[#9a9aa4]">
          <span className="truncate">{title}</span>
          <CopyButton value={code} label={copyLabel} className="text-[#9a9aa4] hover:bg-white/10 hover:text-white" />
        </div>
      )}
      <div className="relative">
        <pre className="overflow-x-auto px-4 py-3.5 font-mono text-[12.5px] leading-[1.7]">
          {children ??
            lines.map((l, i) => (
              <div key={i} className="whitespace-pre">
                {prompt && l.trim() && !l.startsWith("#") && <span className="mr-2.5 text-[#ff9a5c] select-none">$</span>}
                <span className={cn(l.startsWith("#") && "text-[#7d8590]")}>{l || " "}</span>
              </div>
            ))}
        </pre>
        {!title && (
          <CopyButton value={code} label={copyLabel} className="absolute top-2 right-2 bg-white/5 text-[#9a9aa4] hover:bg-white/10 hover:text-white" />
        )}
      </div>
    </div>
  )
}
