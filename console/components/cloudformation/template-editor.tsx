"use client"

import { useRef } from "react"

import { cn } from "@/lib/utils"

/**
 * TemplateEditor is a monospace YAML/JSON editor with line numbers. Tab inserts
 * two spaces (Shift+Tab removes up to two leading spaces of the current line).
 * The gutter and text scroll together inside a height-limited box.
 */
export function TemplateEditor({
  value,
  onChange,
  readOnly,
  minRows = 18,
  maxHeight = "max-h-[65vh]",
  invalid,
  id,
  className,
}: {
  value: string
  onChange?: (v: string) => void
  readOnly?: boolean
  minRows?: number
  maxHeight?: string
  invalid?: boolean
  id?: string
  className?: string
}) {
  const ref = useRef<HTMLTextAreaElement>(null)
  const lines = Math.max(value.split("\n").length, minRows)

  const setSelection = (s: number, e = s) =>
    requestAnimationFrame(() => {
      if (ref.current) {
        ref.current.selectionStart = s
        ref.current.selectionEnd = e
      }
    })

  const onKeyDown = (e: React.KeyboardEvent<HTMLTextAreaElement>) => {
    if (readOnly || e.key !== "Tab" || e.metaKey || e.ctrlKey || e.altKey) return
    e.preventDefault()
    const t = e.currentTarget
    const { selectionStart: s, selectionEnd: en } = t
    if (e.shiftKey) {
      const lineStart = value.lastIndexOf("\n", s - 1) + 1
      const n = value.slice(lineStart, lineStart + 2).match(/^ {0,2}/)![0].length
      if (!n) return
      onChange?.(value.slice(0, lineStart) + value.slice(lineStart + n))
      setSelection(Math.max(lineStart, s - n), Math.max(lineStart, en - n))
      return
    }
    onChange?.(value.slice(0, s) + "  " + value.slice(en))
    setSelection(s + 2)
  }

  return (
    <div
      className={cn(
        "bg-muted/30 overflow-auto rounded-md border font-mono text-[13px] leading-5",
        maxHeight,
        invalid && !readOnly && "border-destructive/60",
        className,
      )}
    >
      <div className="flex min-w-full">
        <div aria-hidden className="text-muted-foreground/60 bg-muted/50 sticky left-0 border-r px-2 py-2 text-right select-none">
          {Array.from({ length: lines }, (_, i) => (
            <div key={i}>{i + 1}</div>
          ))}
        </div>
        <textarea
          ref={ref}
          id={id}
          value={value}
          readOnly={readOnly}
          onChange={(e) => onChange?.(e.target.value)}
          onKeyDown={onKeyDown}
          spellCheck={false}
          autoCapitalize="off"
          autoCorrect="off"
          rows={lines}
          wrap="off"
          className="min-w-0 flex-1 resize-none overflow-x-auto overflow-y-hidden bg-transparent px-3 py-2 whitespace-pre outline-none"
        />
      </div>
    </div>
  )
}
