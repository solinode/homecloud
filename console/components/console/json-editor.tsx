"use client"

import { useMemo } from "react"
import { CheckCircle2, AlertCircle, WandSparkles } from "lucide-react"

import { Button } from "@/components/ui/button"
import { cn } from "@/lib/utils"

export function jsonError(text: string): string | null {
  if (!text.trim()) return "Document is empty"
  try {
    JSON.parse(text)
    return null
  } catch (e) {
    return e instanceof Error ? e.message : String(e)
  }
}

/**
 * JsonEditor is a monospace textarea with live JSON validation, line numbers
 * and a Format button. `validate` can add schema checks on parsed JSON.
 */
export function JsonEditor({
  value,
  onChange,
  rows = 16,
  readOnly,
  validate,
  className,
  id,
}: {
  value: string
  onChange?: (v: string) => void
  rows?: number
  readOnly?: boolean
  validate?: (parsed: unknown) => string | null
  className?: string
  id?: string
}) {
  const error = useMemo(() => {
    const e = jsonError(value)
    if (e) return e
    return validate ? validate(JSON.parse(value)) : null
  }, [value, validate])
  const lines = Math.max(value.split("\n").length, rows)

  const format = () => {
    try {
      onChange?.(JSON.stringify(JSON.parse(value), null, 2))
    } catch {
      // leave invalid JSON untouched
    }
  }

  const onKeyDown = (e: React.KeyboardEvent<HTMLTextAreaElement>) => {
    if (e.key === "Tab" && !readOnly) {
      e.preventDefault()
      const t = e.currentTarget
      const { selectionStart: s, selectionEnd: en } = t
      const next = value.slice(0, s) + "  " + value.slice(en)
      onChange?.(next)
      requestAnimationFrame(() => {
        t.selectionStart = t.selectionEnd = s + 2
      })
    }
  }

  return (
    <div className={cn("flex flex-col gap-2", className)}>
      <div className={cn("bg-muted/30 flex overflow-hidden rounded-md border font-mono text-[13px] leading-5", error && !readOnly && "border-destructive/60")}>
        <div aria-hidden className="text-muted-foreground/60 bg-muted/50 border-r px-2 py-2 text-right select-none">
          {Array.from({ length: lines }, (_, i) => (
            <div key={i}>{i + 1}</div>
          ))}
        </div>
        <textarea
          id={id}
          value={value}
          readOnly={readOnly}
          onChange={(e) => onChange?.(e.target.value)}
          onKeyDown={onKeyDown}
          spellCheck={false}
          rows={lines}
          className="min-w-0 flex-1 resize-none overflow-x-auto bg-transparent px-3 py-2 whitespace-pre outline-none"
        />
      </div>
      {!readOnly && (
        <div className="flex items-center justify-between gap-2 text-xs">
          {error ? (
            <span className="text-destructive flex items-center gap-1">
              <AlertCircle className="size-3.5" /> {error}
            </span>
          ) : (
            <span className="flex items-center gap-1 text-success">
              <CheckCircle2 className="size-3.5" /> Valid JSON
            </span>
          )}
          <Button type="button" size="sm" variant="ghost" onClick={format} disabled={!!jsonError(value)}>
            <WandSparkles /> Format
          </Button>
        </div>
      )}
    </div>
  )
}
