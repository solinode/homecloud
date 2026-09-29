"use client"

import { useEffect, useState } from "react"
import { Loader2, Pencil } from "lucide-react"
import { toast } from "sonner"

import { Button } from "@/components/ui/button"
import { Section } from "@/components/console/section"
import { TagList, TagsEditor, rowsToTags, tagsToRows, type TagRow } from "@/components/console/tags-editor"
import { errorMessage } from "@/lib/api"
import type { Tags } from "@/lib/types"

/**
 * TagsSection shows an IAM entity's tags with a "Manage tags" editor. onSave
 * receives the complete new tag set (the caller replaces the entity's tags).
 */
export function TagsSection({
  tags,
  noun,
  onSave,
  readOnlyReason,
}: {
  tags: Tags | null | undefined
  /** "users", "roles", ... for the description. */
  noun: string
  onSave: (tags: Tags) => Promise<unknown>
  /** When set, tags cannot be edited and this is shown instead of the button. */
  readOnlyReason?: string
}) {
  const [editing, setEditing] = useState(false)
  const [rows, setRows] = useState<TagRow[]>([])
  const [pending, setPending] = useState(false)
  const [err, setErr] = useState<string | null>(null)

  useEffect(() => {
    if (editing) {
      setRows(tagsToRows(tags))
      setErr(null)
    }
  }, [editing, tags])

  const save = async () => {
    const keys = rows.map((r) => r.key.trim()).filter(Boolean)
    if (rows.some((r) => !r.key.trim() && r.value.trim())) return setErr("Every tag needs a key.")
    if (new Set(keys).size !== keys.length) return setErr("Tag keys must be unique.")
    if (keys.some((k) => k.length > 128)) return setErr("Tag keys can be at most 128 characters.")
    if (rows.some((r) => r.value.length > 256)) return setErr("Tag values can be at most 256 characters.")
    setPending(true)
    try {
      await onSave(rowsToTags(rows) ?? {})
      toast.success("Tags saved")
      setEditing(false)
    } catch (e) {
      toast.error(errorMessage(e))
    } finally {
      setPending(false)
    }
  }

  return (
    <Section
      title="Tags"
      description={`Key-value pairs to organize and find ${noun}.`}
      actions={
        readOnlyReason ? undefined : editing ? (
          <>
            <Button variant="outline" size="sm" onClick={() => setEditing(false)} disabled={pending}>
              Cancel
            </Button>
            <Button size="sm" onClick={save} disabled={pending}>
              {pending && <Loader2 className="animate-spin" />}
              Save tags
            </Button>
          </>
        ) : (
          <Button variant="outline" size="sm" onClick={() => setEditing(true)}>
            <Pencil /> Manage tags
          </Button>
        )
      }
    >
      {editing ? (
        <div className="flex flex-col gap-2">
          <TagsEditor rows={rows} onChange={(r) => (setRows(r), setErr(null))} />
          {err && <p className="text-destructive text-xs">{err}</p>}
        </div>
      ) : (
        <div className="flex flex-col gap-2">
          <TagList tags={tags} />
          {readOnlyReason && <p className="text-muted-foreground text-xs">{readOnlyReason}</p>}
        </div>
      )}
    </Section>
  )
}
