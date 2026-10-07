"use client"

import { forwardRef, type ReactNode } from "react"

import { CopyButton } from "@/components/console/copy-button"
import { cn } from "@/lib/utils"

/**
 * CodeBlock is a copyable monospace block. Like the landing page, code stays
 * dark in both themes. `prompt` prefixes each line with an orange "$".
 *
 *   wrap       soft-wrap long lines (payloads, PEMs, log messages)
 *   maxHeight  scroll inside the block past this height (CSS length)
 *   tone       "danger" tints the text for error output
 *   actions    extra controls in the title bar, left of Copy
 *   noCopy     hide the copy button (e.g. for masked secrets)
 */
export function CodeBlock({
  code,
  title,
  prompt,
  className,
  copyLabel = "Copy",
  wrap,
  maxHeight,
  tone,
  actions,
  noCopy,
  children,
}: {
  code: string
  title?: ReactNode
  prompt?: boolean
  className?: string
  copyLabel?: string
  wrap?: boolean
  maxHeight?: string
  tone?: "danger"
  actions?: ReactNode
  noCopy?: boolean
  /** custom rendering of `code` (e.g. masked secrets); `code` is still what gets copied */
  children?: ReactNode
}) {
  const lines = code.split("\n")
  return (
    <div
      className={cn(
        "bg-code text-code-foreground border-code-line relative min-w-0 overflow-hidden rounded-lg border shadow-[inset_0_1px_0_rgb(255_255_255/0.04)] dark:border-border-strong",
        tone === "danger" && "text-code-red",
        className,
      )}
    >
      {title && (
        <div className="border-code-line text-code-muted flex h-9 items-center justify-between gap-3 border-b pr-1.5 pl-4 font-mono text-[11.5px]">
          <span className="truncate">{title}</span>
          <span className="flex shrink-0 items-center gap-1">
            {actions}
            {!noCopy && <CopyButton value={code} label={copyLabel} className="text-code-muted hover:bg-white/10 hover:text-white" />}
          </span>
        </div>
      )}
      <div className="relative">
        <pre
          className={cn("overflow-auto px-4 py-3.5 font-mono text-[12.5px] leading-[1.7]", !title && !noCopy && "pr-12")}
          style={maxHeight ? { maxHeight } : undefined}
        >
          {children ??
            lines.map((l, i) => (
              <div key={i} className={wrap ? "break-all whitespace-pre-wrap" : "whitespace-pre"}>
                {prompt && l.trim() && !l.startsWith("#") && <span className="text-code-prompt mr-2.5 select-none">$</span>}
                <span className={cn(l.startsWith("#") && "text-code-comment")}>{l || " "}</span>
              </div>
            ))}
        </pre>
        {!title && !noCopy && (
          <CopyButton value={code} label={copyLabel} className="text-code-muted absolute top-2 right-2 bg-white/5 hover:bg-white/10 hover:text-white" />
        )}
      </div>
    </div>
  )
}

/**
 * TerminalPane is the frame for streamed console/terminal output (instance
 * console output, run-command results, DB logs, container logs). It renders
 * children (use the `term*` line classes below) on the code surface with a
 * title bar. Pass `scrollRef` to auto-scroll from the caller.
 */
export const TerminalPane = forwardRef<
  HTMLDivElement,
  {
    title?: ReactNode
    actions?: ReactNode
    copyValue?: string
    height?: string
    className?: string
    bodyClassName?: string
    children: ReactNode
  }
>(function TerminalPane({ title, actions, copyValue, height = "28rem", className, bodyClassName, children }, ref) {
  return (
    <div className={cn("bg-code text-code-foreground border-code-line flex min-w-0 flex-col overflow-hidden rounded-lg border dark:border-border-strong", className)}>
      {(title || actions || copyValue !== undefined) && (
        <div className="border-code-line text-code-muted flex h-9 shrink-0 items-center justify-between gap-3 border-b pr-1.5 pl-3 font-mono text-[11.5px]">
          <span className="flex min-w-0 items-center gap-2 truncate">
            <span aria-hidden className="flex gap-1">
              <span className="size-2 rounded-full bg-white/15" />
              <span className="size-2 rounded-full bg-white/15" />
              <span className="size-2 rounded-full bg-white/15" />
            </span>
            {title}
          </span>
          <span className="flex shrink-0 items-center gap-1">
            {actions}
            {copyValue !== undefined && <CopyButton value={copyValue} label="Copy" className="text-code-muted hover:bg-white/10 hover:text-white" />}
          </span>
        </div>
      )}
      <div ref={ref} className={cn("overflow-auto px-4 py-3 font-mono text-[12.5px] leading-[1.65]", bodyClassName)} style={{ height }}>
        {children}
      </div>
    </div>
  )
})

/** Line/segment classes for terminal output on the code surface. */
export const term = {
  muted: "text-code-muted",
  comment: "text-code-comment",
  prompt: "text-code-prompt",
  error: "text-code-red",
  success: "text-code-green",
  warning: "text-code-yellow",
  info: "text-code-blue",
  accent: "text-code-violet",
} as const
