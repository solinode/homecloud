"use client"

import { useEffect, useRef, useState } from "react"
import Link from "next/link"
import { useRouter } from "next/navigation"
import { AlertCircle, Eye, EyeOff, List, Loader2, Lock, Save, Type } from "lucide-react"
import { toast } from "sonner"

import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert"
import { Button } from "@/components/ui/button"
import { Checkbox } from "@/components/ui/checkbox"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { Textarea } from "@/components/ui/textarea"
import { EmptyState } from "@/components/console/empty-state"
import { ErrorState } from "@/components/console/error-state"
import { Field } from "@/components/console/form-field"
import { DetailSkeleton } from "@/components/console/loading"
import { PageHeader } from "@/components/console/page-header"
import { Section } from "@/components/console/section"
import { TagsEditor, rowsToTags, type TagRow } from "@/components/console/tags-editor"
import { KeyPicker, keyHref, keyIdFromArn, keyLabel, useKmsKeys } from "@/components/kms/shared"
import { errorMessage, api } from "@/lib/api"
import { revalidate, useApi, useQueryParam } from "@/lib/hooks"
import type { PutSsmParameterInput, PutSsmParameterResult, SsmParameterType, SsmParameterValue } from "@/lib/types"
import { cn } from "@/lib/utils"

import { DEFAULT_KEY_ALIAS, MAX_VALUE, PARAMETER_PATH, SSM_PATH, TypeBadge, listItems, nameError, parameterHref, useParameter } from "./shared"

const DEFAULT_KEY = "default"

const TYPES: { type: SsmParameterType; icon: typeof Type; title: string; blurb: string }[] = [
  { type: "String", icon: Type, title: "String", blurb: "Any text value, such as a URL or a JSON document." },
  { type: "StringList", icon: List, title: "StringList", blurb: "Comma-separated values, such as a list of hosts." },
  { type: "SecureString", icon: Lock, title: "SecureString", blurb: "Encrypted with a KMS key. Use for passwords and API keys." },
]

export function ParameterForm() {
  const editName = useQueryParam("name")
  const editing = !!editName
  const existing = useParameter(editName)
  const current = useApi<SsmParameterValue>(editing ? PARAMETER_PATH : null, {
    query: { name: editName, with_decryption: true },
    revalidateOnFocus: false,
  })

  const crumbs = [
    { label: "Systems Manager", href: "/ssm/" },
    { label: "Parameter Store", href: "/ssm/" },
    ...(editing ? [{ label: editName, href: parameterHref(editName) }, { label: "Edit" }] : [{ label: "Create parameter" }]),
  ]

  if (editing) {
    if (existing.error && !existing.data) {
      return (
        <div className="flex flex-col gap-4">
          <PageHeader title="Edit parameter" breadcrumbs={crumbs} />
          <ErrorState error={existing.error} onRetry={() => existing.mutate()} />
        </div>
      )
    }
    if (existing.notFound) {
      return (
        <div className="flex flex-col gap-4">
          <PageHeader title="Edit parameter" breadcrumbs={crumbs} />
          <Section>
            <EmptyState
              icon={AlertCircle}
              title="Parameter not found"
              description={`Parameter ${editName} does not exist.`}
              action={
                <Button size="sm" asChild>
                  <Link href="/ssm/create/">Create a parameter</Link>
                </Button>
              }
            />
          </Section>
        </div>
      )
    }
    // Wait for the value too (or its error: an undecryptable SecureString can still be overwritten).
    if (!existing.param || (!current.data && !current.error)) return <DetailSkeleton />
  }

  return (
    <FormBody
      key={editName}
      crumbs={crumbs}
      editName={editName}
      initial={
        editing && existing.param
          ? {
              type: existing.param.type,
              description: existing.param.description,
              keyArn: existing.param.key_id,
              value: current.data?.value ?? "",
              version: existing.param.version,
              valueError: current.error ? errorMessage(current.error) : null,
            }
          : null
      }
    />
  )
}

interface Initial {
  type: SsmParameterType
  description: string
  keyArn: string
  value: string
  version: number
  valueError: string | null
}

function FormBody({ crumbs, editName, initial }: { crumbs: { label: string; href?: string }[]; editName: string; initial: Initial | null }) {
  const router = useRouter()
  const keys = useKmsKeys()
  const editing = !!initial

  const [name, setName] = useState(editName)
  const [description, setDescription] = useState(initial?.description ?? "")
  const [type, setType] = useState<SsmParameterType>(initial?.type ?? "String")
  const [keySel, setKeySel] = useState(DEFAULT_KEY)
  const [value, setValue] = useState(initial?.value ?? "")
  const [masked, setMasked] = useState(true)
  const [overwrite, setOverwrite] = useState(true)
  const [tags, setTags] = useState<TagRow[]>([])
  const [submitted, setSubmitted] = useState(false)
  const [pending, setPending] = useState(false)
  const [apiError, setApiError] = useState<string | null>(null)

  // Resolve the current KMS key (an ARN) to a picker value once the keys are loaded.
  const keyInit = useRef(false)
  const defaultKey = keys.data?.find((k) => k.aliases?.includes(DEFAULT_KEY_ALIAS))
  useEffect(() => {
    if (keyInit.current || !keys.data) return
    keyInit.current = true
    if (!initial?.keyArn) return
    const id = keyIdFromArn(initial.keyArn)
    if (id !== defaultKey?.id && keys.data.some((k) => k.id === id)) setKeySel(id)
  }, [keys.data, initial, defaultKey])

  const nameErr = editing ? null : nameError(name)
  const valueErr = !value ? "Enter a value." : value.length > MAX_VALUE ? `Values are at most ${MAX_VALUE} characters.` : null
  const chosenKey = keySel === DEFAULT_KEY ? defaultKey : keys.data?.find((k) => k.id === keySel)
  const keyErr = type === "SecureString" && chosenKey && chosenKey.state !== "Enabled" ? "This key can't encrypt because it isn't enabled." : null
  const overwriteErr = editing && !overwrite ? "Updating an existing parameter requires overwrite." : null
  const valid = !nameErr && !valueErr && !keyErr && !overwriteErr
  const secure = type === "SecureString"

  const submit = async (e: React.FormEvent) => {
    e.preventDefault()
    setSubmitted(true)
    setApiError(null)
    if (!valid) return
    const finalName = editing ? editName : name.trim()
    let keyId: string | undefined
    if (secure) {
      // "" makes the API keep the parameter's current key (or use alias/hc/ssm for new ones);
      // name the default alias explicitly when switching back to it.
      keyId = keySel === DEFAULT_KEY ? (defaultKey ? DEFAULT_KEY_ALIAS : "") : keySel
    }
    const body: PutSsmParameterInput = {
      name: finalName,
      value,
      type,
      key_id: keyId,
      description: description.trim(),
      overwrite: editing ? overwrite : false,
      tags: editing ? undefined : rowsToTags(tags),
    }
    setPending(true)
    try {
      const r = await api.put<PutSsmParameterResult>(PARAMETER_PATH, body)
      toast.success(editing ? `Parameter updated to version ${r.version}` : `Parameter ${finalName} created`)
      await revalidate(SSM_PATH)
      router.push(parameterHref(finalName))
    } catch (err) {
      const msg = errorMessage(err)
      setApiError(msg)
      toast.error(msg)
    } finally {
      setPending(false)
    }
  }

  const items = type === "StringList" && value ? listItems(value) : []

  return (
    <div className="flex flex-col gap-4">
      <PageHeader
        title={editing ? <span className="break-all">Edit {editName}</span> : "Create parameter"}
        description={
          editing
            ? `Saving stores version ${(initial?.version ?? 0) + 1}. Earlier versions stay in the parameter history.`
            : "Parameters store configuration data and secrets. Use a hierarchy like /app/db/url to organise them."
        }
        breadcrumbs={crumbs}
      />
      <form onSubmit={submit} className="grid grid-cols-1 items-start gap-4 xl:grid-cols-[minmax(0,1fr)_320px]" noValidate>
        <div className="flex min-w-0 flex-col gap-4">
          <Section title="Parameter details">
            <div className="flex max-w-2xl flex-col gap-4">
              <Field
                label="Name"
                htmlFor="ssm-name"
                error={submitted || name ? nameErr : undefined}
                help={editing ? "Parameter names can't be changed." : "Letters, digits and _ . - /. Hierarchical names start with /, e.g. /app/db/url."}
              >
                <Input
                  id="ssm-name"
                  value={name}
                  onChange={(e) => setName(e.target.value)}
                  disabled={editing}
                  autoFocus={!editing}
                  autoComplete="off"
                  spellCheck={false}
                  placeholder="/app/db/url"
                  className="font-mono text-[13px]"
                  aria-invalid={(submitted || !!name) && !!nameErr}
                />
              </Field>
              <Field label="Description" htmlFor="ssm-desc" optional>
                <Input id="ssm-desc" value={description} onChange={(e) => setDescription(e.target.value)} placeholder="Connection string for the orders database" />
              </Field>
            </div>
          </Section>

          <Section title="Type">
            <div className="grid grid-cols-1 gap-3 md:grid-cols-3" role="radiogroup" aria-label="Parameter type">
              {TYPES.map((t) => {
                const active = type === t.type
                return (
                  <button
                    key={t.type}
                    type="button"
                    role="radio"
                    aria-checked={active}
                    onClick={() => setType(t.type)}
                    className={cn(
                      "flex flex-col gap-1 rounded-lg border p-3 text-left transition-colors",
                      active ? "border-primary bg-primary/5 ring-primary/30 ring-1 dark:bg-primary/10" : "hover:bg-muted/40",
                    )}
                  >
                    <span className="flex items-center gap-2 text-sm font-medium">
                      <t.icon className={cn("size-4", active ? "text-primary" : "text-muted-foreground")} />
                      {t.title}
                    </span>
                    <span className="text-muted-foreground text-xs">{t.blurb}</span>
                  </button>
                )
              })}
            </div>

            {secure && (
              <div className="mt-4 flex max-w-2xl flex-col gap-2">
                <Field
                  label="KMS key"
                  htmlFor="ssm-key"
                  error={keyErr ?? (keys.error ? errorMessage(keys.error) : undefined)}
                  help={
                    keySel === DEFAULT_KEY ? (
                      <>
                        The HomeCloud managed key <span className="font-mono">{DEFAULT_KEY_ALIAS}</span>
                        {defaultKey ? "" : " (created automatically on first use)"}.
                      </>
                    ) : (
                      "The value is encrypted with this key; decrypting it requires kms:Decrypt on the key."
                    )
                  }
                >
                  <KeyPicker
                    id="ssm-key"
                    keys={keys.data}
                    value={keySel}
                    onChange={setKeySel}
                    onlyEnabled
                    extra={[{ value: DEFAULT_KEY, label: DEFAULT_KEY_ALIAS, hint: "default" }]}
                  />
                </Field>
              </div>
            )}
          </Section>

          <Section
            title="Value"
            actions={
              secure && (
                <Button type="button" variant="outline" size="sm" onClick={() => setMasked(!masked)}>
                  {masked ? <Eye /> : <EyeOff />} {masked ? "Show value" : "Hide value"}
                </Button>
              )
            }
          >
            <div className="flex flex-col gap-3">
              {initial?.valueError && (
                <Alert variant="destructive">
                  <AlertCircle />
                  <AlertTitle>The current value could not be loaded</AlertTitle>
                  <AlertDescription>{initial.valueError} Enter a new value to overwrite it.</AlertDescription>
                </Alert>
              )}
              <Field
                label="Value"
                htmlFor="ssm-value"
                error={submitted ? valueErr : value.length > MAX_VALUE ? valueErr : undefined}
                help={`${value.length} / ${MAX_VALUE} characters${type === "StringList" ? ". Separate items with commas." : ""}`}
              >
                <Textarea
                  id="ssm-value"
                  value={value}
                  onChange={(e) => setValue(e.target.value)}
                  rows={6}
                  spellCheck={false}
                  autoComplete="off"
                  placeholder={type === "StringList" ? "a.example.com,b.example.com" : secure ? "s3cr3t" : "postgres://db:5432/app"}
                  className={cn("font-mono text-[13px]", secure && masked && "[-webkit-text-security:disc]")}
                  aria-invalid={submitted && !!valueErr}
                />
              </Field>
              {items.length > 0 && (
                <div className="flex flex-wrap items-center gap-1.5">
                  <span className="text-muted-foreground text-xs">{items.length} items:</span>
                  {items.map((it, i) => (
                    <span key={i} className={cn("bg-muted rounded-md border px-2 py-0.5 font-mono text-xs", !it && "border-destructive text-destructive")}>
                      {it || "(empty)"}
                    </span>
                  ))}
                </div>
              )}
            </div>
          </Section>

          {!editing && (
            <Section title="Tags" description="Tags can only be set when the parameter is created.">
              <TagsEditor rows={tags} onChange={setTags} />
            </Section>
          )}
        </div>

        <aside className="xl:sticky xl:top-4">
          <Section title="Summary">
            <div className="flex flex-col gap-4">
              <dl className="flex flex-col gap-3 text-sm">
                <SummaryItem label="Name">
                  <span className={cn("font-mono text-[13px] break-all", !name && "text-muted-foreground")}>{name || "not set"}</span>
                </SummaryItem>
                <SummaryItem label="Type">
                  <TypeBadge type={type} />
                </SummaryItem>
                {secure && (
                  <SummaryItem label="KMS key">
                    {chosenKey ? (
                      <Link href={keyHref(chosenKey.id)} className="text-primary font-mono text-[13px] hover:underline">
                        {keyLabel(chosenKey)}
                      </Link>
                    ) : (
                      <span className="font-mono text-[13px]">{DEFAULT_KEY_ALIAS}</span>
                    )}
                  </SummaryItem>
                )}
                <SummaryItem label="Value">{value ? `${value.length} characters` : <span className="text-muted-foreground">empty</span>}</SummaryItem>
                <SummaryItem label="Tier">Standard</SummaryItem>
              </dl>
              {editing && (
                <div className="flex items-start gap-2">
                  <Checkbox id="ssm-overwrite" checked={overwrite} onCheckedChange={(v) => setOverwrite(v === true)} className="mt-0.5" />
                  <div>
                    <Label htmlFor="ssm-overwrite" className="font-medium">
                      Overwrite existing value
                    </Label>
                    <p className={cn("mt-0.5 text-xs", overwriteErr ? "text-destructive" : "text-muted-foreground")}>
                      {overwriteErr ?? `Creates version ${(initial?.version ?? 0) + 1}.`}
                    </p>
                  </div>
                </div>
              )}
              {apiError && (
                <Alert variant="destructive">
                  <AlertCircle />
                  <AlertDescription className="break-words">{apiError}</AlertDescription>
                </Alert>
              )}
              {submitted && !valid && <p className="text-destructive text-xs">Some settings need attention. Check the highlighted fields.</p>}
              <div className="flex flex-col gap-2 border-t pt-4">
                <Button type="submit" disabled={pending || (editing && !overwrite)}>
                  {pending ? <Loader2 className="animate-spin" /> : <Save />}
                  {editing ? "Save changes" : "Create parameter"}
                </Button>
                <Button type="button" variant="outline" asChild>
                  <Link href={editing ? parameterHref(editName) : "/ssm/"}>Cancel</Link>
                </Button>
              </div>
            </div>
          </Section>
        </aside>
      </form>
    </div>
  )
}

function SummaryItem({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div>
      <dt className="text-muted-foreground text-xs font-medium">{label}</dt>
      <dd className="min-w-0 break-words">{children}</dd>
    </div>
  )
}
