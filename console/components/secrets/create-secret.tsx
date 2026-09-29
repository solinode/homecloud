"use client"

import { useState } from "react"
import Link from "next/link"
import { useRouter } from "next/navigation"
import { Loader2 } from "lucide-react"
import { toast } from "sonner"

import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Textarea } from "@/components/ui/textarea"
import { Field } from "@/components/console/form-field"
import { PageHeader } from "@/components/console/page-header"
import { Section } from "@/components/console/section"
import { TagsEditor, rowsToTags, type TagRow } from "@/components/console/tags-editor"
import { api, errorMessage } from "@/lib/api"
import { revalidate } from "@/lib/hooks"
import type { Secret } from "@/lib/types"

import { secretHref } from "./secret-list"
import { draftError, draftValue, SecretValueEditor, type SecretDraft } from "./value-editor"

const NAME_RE = /^[\w/+=.@!-]{1,512}$/

function nameError(name: string): string | null {
  if (!name) return "Enter a secret name"
  if (name.length > 512) return "The name can be at most 512 characters"
  if (!NAME_RE.test(name)) return "Use only letters, digits and the characters / _ + = . @ ! -"
  return null
}

export function CreateSecret() {
  const router = useRouter()
  const [draft, setDraft] = useState<SecretDraft>({ mode: "kv", rows: [{ key: "", value: "" }], text: "" })
  const [name, setName] = useState("")
  const [description, setDescription] = useState("")
  const [tags, setTags] = useState<TagRow[]>([])
  const [submitted, setSubmitted] = useState(false)
  const [pending, setPending] = useState(false)

  const nErr = nameError(name)
  const vErr = draftError(draft)

  const submit = async (e: React.FormEvent) => {
    e.preventDefault()
    setSubmitted(true)
    if (nErr || vErr) return
    setPending(true)
    try {
      const s = await api.post<Secret>("/api/v1/secrets", { name, description, value: draftValue(draft), tags: rowsToTags(tags) })
      toast.success(`Secret ${s.name} stored`)
      revalidate("/api/v1/secrets")
      router.push(secretHref(s.name))
    } catch (err) {
      toast.error(errorMessage(err))
      setPending(false)
    }
  }

  return (
    <form onSubmit={submit} className="flex flex-col gap-4 pb-20">
      <PageHeader
        title="Store a new secret"
        description="Secret values are encrypted with AES-256-GCM and versioned: updating a value keeps the previous one as AWSPREVIOUS."
        breadcrumbs={[{ label: "Secrets Manager", href: "/secrets/" }, { label: "Secrets", href: "/secrets/" }, { label: "Store a new secret" }]}
        className="mb-1"
      />

      <Section title="Secret value" description="Store key/value pairs (saved as a JSON object) or arbitrary plaintext.">
        <SecretValueEditor draft={draft} onChange={setDraft} showError={submitted} />
      </Section>

      <Section title="Secret name and description">
        <div className="flex max-w-2xl flex-col gap-4">
          <Field
            label="Secret name"
            htmlFor="secret-name"
            error={submitted || name ? nErr : null}
            help="Use / to build a hierarchy, e.g. prod/app/db. Letters, digits and / _ + = . @ ! - are allowed."
          >
            <Input id="secret-name" value={name} onChange={(e) => setName(e.target.value)} placeholder="prod/app/database" autoComplete="off" spellCheck={false} />
          </Field>
          <Field label="Description" htmlFor="secret-description" optional>
            <Textarea id="secret-description" value={description} onChange={(e) => setDescription(e.target.value)} rows={2} placeholder="Credentials for the production database" />
          </Field>
        </div>
      </Section>

      <Section title="Tags" description="Optional key/value labels to organize and find secrets.">
        <TagsEditor rows={tags} onChange={setTags} />
      </Section>

      <div className="bg-background/95 supports-[backdrop-filter]:bg-background/80 sticky bottom-0 z-10 -mx-4 flex justify-end gap-2 border-t px-4 py-3 backdrop-blur sm:mx-0 sm:rounded-lg sm:border">
        <Button type="button" variant="outline" asChild>
          <Link href="/secrets/">Cancel</Link>
        </Button>
        <Button type="submit" disabled={pending}>
          {pending && <Loader2 className="animate-spin" />}
          Store secret
        </Button>
      </div>
    </form>
  )
}
