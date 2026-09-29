"use client"

import { useRef } from "react"

import { cn } from "@/lib/utils"

/** insertText replaces the selection, keeping the browser's undo history when possible. */
function insertText(t: HTMLTextAreaElement, text: string, onChange: (v: string) => void) {
  t.focus()
  let ok = false
  try {
    ok = document.execCommand("insertText", false, text)
  } catch {
    ok = false
  }
  if (!ok) {
    const { selectionStart: s, selectionEnd: e, value } = t
    const next = value.slice(0, s) + text + value.slice(e)
    onChange(next)
    requestAnimationFrame(() => {
      t.selectionStart = t.selectionEnd = s + text.length
    })
  }
}

/**
 * CodeEditor is a monospace textarea with line numbers. Tab indents by two
 * spaces (a multi-line selection indents every line), Shift+Tab outdents.
 */
export function CodeEditor({
  value,
  onChange,
  readOnly,
  onSave,
  className,
  minRows = 20,
  ariaLabel,
}: {
  value: string
  onChange: (v: string) => void
  readOnly?: boolean
  onSave?: () => void
  className?: string
  minRows?: number
  ariaLabel?: string
}) {
  const ref = useRef<HTMLTextAreaElement>(null)
  const lines = Math.max(value.split("\n").length, minRows)

  const onKeyDown = (e: React.KeyboardEvent<HTMLTextAreaElement>) => {
    if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === "s") {
      e.preventDefault()
      onSave?.()
      return
    }
    if (e.key !== "Tab" || readOnly || e.altKey || e.metaKey || e.ctrlKey) return
    e.preventDefault()
    const t = e.currentTarget
    const { selectionStart: s, selectionEnd: en } = t
    const multi = value.slice(s, en).includes("\n")
    if (!e.shiftKey && !multi) {
      insertText(t, "  ", onChange)
      return
    }
    // Indent or outdent every line touched by the selection.
    const lineStart = value.lastIndexOf("\n", s - 1) + 1
    const block = value.slice(lineStart, en)
    const out = block
      .split("\n")
      .map((l) => (e.shiftKey ? l.replace(/^ {1,2}/, "") : `  ${l}`))
      .join("\n")
    if (out === block) return
    t.selectionStart = lineStart
    t.selectionEnd = en
    insertText(t, out, onChange)
    requestAnimationFrame(() => {
      t.selectionStart = lineStart
      t.selectionEnd = lineStart + out.length
    })
  }

  return (
    <div className={cn("bg-muted/30 flex max-h-[65vh] min-h-64 overflow-auto rounded-md border font-mono text-[13px] leading-5", className)}>
      <div aria-hidden className="text-muted-foreground/60 bg-muted/50 h-fit min-h-full border-r px-2 py-2 text-right select-none">
        {Array.from({ length: lines }, (_, i) => (
          <div key={i}>{i + 1}</div>
        ))}
      </div>
      <textarea
        ref={ref}
        aria-label={ariaLabel}
        value={value}
        readOnly={readOnly}
        onChange={(e) => onChange(e.target.value)}
        onKeyDown={onKeyDown}
        spellCheck={false}
        autoCapitalize="off"
        autoCorrect="off"
        wrap="off"
        rows={lines}
        className="min-w-0 flex-1 resize-none overflow-x-auto overflow-y-hidden bg-transparent px-3 py-2 whitespace-pre outline-none"
      />
    </div>
  )
}
