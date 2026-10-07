"use client"

import { useState } from "react"
import { Loader2, WandSparkles } from "lucide-react"
import { toast } from "sonner"

import { Button } from "@/components/ui/button"
import { Tabs, TabsList, TabsTrigger } from "@/components/ui/tabs"
import { Textarea } from "@/components/ui/textarea"
import { TagsEditor, type TagRow } from "@/components/console/tags-editor"
import { api, errorMessage } from "@/lib/api"

export type SecretMode = "kv" | "plain"

export interface SecretDraft {
  mode: SecretMode
  rows: TagRow[]
  text: string
}

/** parseKeyValue returns the rows of a flat JSON object value, or null for anything else. */
export function parseKeyValue(value: string): TagRow[] | null {
  try {
    const v: unknown = JSON.parse(value)
    if (!v || typeof v !== "object" || Array.isArray(v)) return null
    return Object.entries(v as Record<string, unknown>).map(([key, val]) => ({
      key,
      value: typeof val === "string" ? val : JSON.stringify(val),
    }))
  } catch {
    return null
  }
}

export function rowsToJson(rows: TagRow[], pretty = false): string {
  const obj: Record<string, string> = {}
  for (const r of rows) if (r.key.trim()) obj[r.key.trim()] = r.value
  return pretty ? JSON.stringify(obj, null, 2) : JSON.stringify(obj)
}

/** draftFromValue picks the key/value editor when the value is a JSON object. */
export function draftFromValue(value: string): SecretDraft {
  const rows = parseKeyValue(value)
  return rows ? { mode: "kv", rows, text: value } : { mode: "plain", rows: [], text: value }
}

export function draftValue(d: SecretDraft): string {
  return d.mode === "kv" ? rowsToJson(d.rows) : d.text
}

export function draftError(d: SecretDraft): string | null {
  if (d.mode === "kv") {
    const keys = d.rows.map((r) => r.key.trim()).filter(Boolean)
    if (!keys.length) return "Add at least one key/value pair"
    if (new Set(keys).size !== keys.length) return "Keys must be unique"
    return null
  }
  return d.text ? null : "The secret value can't be empty"
}

/** ModeToggle is the segmented "Key/value | Plaintext" switch. */
export function ModeToggle({ mode, onChange, labels = { kv: "Key/value", plain: "Plaintext" } }: { mode: SecretMode; onChange: (m: SecretMode) => void; labels?: Record<SecretMode, string> }) {
  return (
    <Tabs value={mode} onValueChange={(v) => onChange(v as SecretMode)}>
      <TabsList aria-label="Value format">
        {(["kv", "plain"] as const).map((m) => (
          <TabsTrigger key={m} value={m}>
            {labels[m]}
          </TabsTrigger>
        ))}
      </TabsList>
    </Tabs>
  )
}

/** SecretValueEditor edits a secret value as key/value rows or as plaintext. */
export function SecretValueEditor({ draft, onChange, showError }: { draft: SecretDraft; onChange: (d: SecretDraft) => void; showError?: boolean }) {
  const [generating, setGenerating] = useState(false)

  const switchMode = (m: SecretMode) => {
    if (m === draft.mode) return
    if (m === "plain") {
      // Carry the key/value pairs over as JSON.
      const hasRows = draft.rows.some((r) => r.key.trim())
      onChange({ ...draft, mode: "plain", text: hasRows ? rowsToJson(draft.rows, true) : draft.text })
    } else {
      const rows = parseKeyValue(draft.text)
      if (!rows && draft.text.trim()) toast.info("The plaintext isn't a JSON object, so it can't be shown as key/value pairs.")
      onChange({ ...draft, mode: "kv", rows: rows ?? (draft.rows.length ? draft.rows : [{ key: "", value: "" }]) })
    }
  }

  const generate = async () => {
    setGenerating(true)
    try {
      const r = await api.post<{ password: string }>("/api/v1/secrets/random-password", undefined, { length: 32 })
      if (draft.mode === "plain") onChange({ ...draft, text: r.password })
      else {
        const rows = [...draft.rows]
        const i = rows.findIndex((x) => !x.value)
        if (i >= 0) rows[i] = { ...rows[i], key: rows[i].key || "password", value: r.password }
        else rows.push({ key: rows.some((x) => x.key === "password") ? `password${rows.length + 1}` : "password", value: r.password })
        onChange({ ...draft, rows })
      }
      toast.success("Random password generated")
    } catch (e) {
      toast.error(errorMessage(e))
    } finally {
      setGenerating(false)
    }
  }

  const err = showError ? draftError(draft) : null

  return (
    <div className="flex flex-col gap-3">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <ModeToggle mode={draft.mode} onChange={switchMode} />
        <Button type="button" variant="outline" size="sm" onClick={generate} disabled={generating}>
          {generating ? <Loader2 className="animate-spin" /> : <WandSparkles />}
          Generate random password
        </Button>
      </div>
      {draft.mode === "kv" ? (
        <TagsEditor rows={draft.rows} onChange={(rows) => onChange({ ...draft, rows })} keyPlaceholder="Secret key" valuePlaceholder="Secret value" addLabel="Add row" max={200} />
      ) : (
        <Textarea
          value={draft.text}
          onChange={(e) => onChange({ ...draft, text: e.target.value })}
          rows={8}
          spellCheck={false}
          className="font-mono text-[13px]"
          placeholder='{"username":"admin","password":"..."} or any text'
          aria-invalid={!!err}
        />
      )}
      {err && <p className="text-destructive text-xs">{err}</p>}
    </div>
  )
}
