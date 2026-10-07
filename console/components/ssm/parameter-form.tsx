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
import { OptionCard, OptionGroup } from "@/components/console/option-card"
import { PageHeader } from "@/components/console/page-header"
import { Section } from "@/components/console/section"
import { Tag } from "@/components/console/tag"
import { TagsEditor, rowsToTags, type TagRow } from "@/components/console/tags-editor"
import { KeyPicker, keyHref, keyIdFromArn, keyLabel, useKmsKeys } from "@/components/kms/shared"
import { errorMessage, api } from "@/lib/api"
import { revalidate, useApi, useQueryParam } from "@/lib/hooks"
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select"
import type { PutSsmParameterInput, PutSsmParameterResult, SsmDataType, SsmParameterTier, SsmParameterType, SsmParameterValue } from "@/lib/types"
import { cn } from "@/lib/utils"

import {
  DATA_TYPES,
  DEFAULT_KEY_ALIAS,
  DEFAULT_KEY_ALIASES,
  PARAMETER_PATH,
  SSM_PATH,
  TIERS,
  TierBadge,
  TypeBadge,
  listItems,
  maxForTier,
  nameError,
  parameterHref,
  useParameter,
} from "./shared"

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
              tier: (existing.param.tier as SsmParameterTier) || "Standard",
              dataType: (existing.param.data_type as SsmDataType) || "text",
              allowedPattern: existing.param.allowed_pattern ?? "",
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
  tier: SsmParameterTier
  dataType: SsmDataType
  allowedPattern: string
}

/** patternError checks an AllowedPattern (JavaScript syntax; the server uses Go RE2, which is close for common patterns). */
function patternError(pattern: string, value: string): string | null {
  if (!pattern) return null
  if (pattern.length > 1024) return "Patterns are at most 1024 characters."
  let re: RegExp
  try {
    re = new RegExp(pattern)
  } catch (e) {
    return `Invalid regular expression: ${e instanceof Error ? e.message : String(e)}`
  }
  if (value && !re.test(value)) return "The value doesn't match the allowed pattern."
  return null
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
  const [tier, setTier] = useState<SsmParameterTier>(initial?.tier ?? "Standard")
  const [dataType, setDataType] = useState<SsmDataType>(initial?.dataType ?? "text")
  const [pattern, setPattern] = useState(initial?.allowedPattern ?? "")
  const [submitted, setSubmitted] = useState(false)
  const [pending, setPending] = useState(false)
  const [apiError, setApiError] = useState<string | null>(null)

  // Resolve the current KMS key (an ARN) to a picker value once the keys are loaded.
  const keyInit = useRef(false)
  const defaultKey = keys.data?.find((k) => k.aliases?.some((a) => DEFAULT_KEY_ALIASES.includes(a)))
  useEffect(() => {
    if (keyInit.current || !keys.data) return
    keyInit.current = true
    if (!initial?.keyArn) return
    const id = keyIdFromArn(initial.keyArn)
    if (id !== defaultKey?.id && keys.data.some((k) => k.id === id)) setKeySel(id)
  }, [keys.data, initial, defaultKey])

  const nameErr = editing ? null : nameError(name)
  const wasAdvanced = initial?.tier === "Advanced"
  const maxValue = maxForTier(tier)
  const valueErr = !value
    ? "Enter a value."
    : value.length > maxValue
      ? tier === "Standard"
        ? `Standard parameters are at most ${maxValue} characters. Choose the Advanced or Intelligent-Tiering tier for larger values.`
        : `Values are at most ${maxValue} characters.`
      : dataType === "aws:ec2:image" && !/^ami-[0-9a-f]{8,17}$/.test(value)
        ? "The aws:ec2:image data type needs an AMI ID such as ami-0123456789abcdef0."
        : null
  const patternErr = patternError(pattern, value)
  const dataTypeErr = dataType === "aws:ssm:integration" && type !== "SecureString" ? "aws:ssm:integration parameters must be SecureString." : null
  const effectiveTier = tier === "Intelligent-Tiering" ? (wasAdvanced || value.length > 4096 ? "Advanced" : "Standard") : tier
  const chosenKey = keySel === DEFAULT_KEY ? defaultKey : keys.data?.find((k) => k.id === keySel)
  const keyErr = type === "SecureString" && chosenKey && chosenKey.state !== "Enabled" ? "This key can't encrypt because it isn't enabled." : null
  const overwriteErr = editing && !overwrite ? "Updating an existing parameter requires overwrite." : null
  const valid = !nameErr && !valueErr && !keyErr && !overwriteErr && !patternErr && !dataTypeErr
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
      tier,
      data_type: dataType,
      allowed_pattern: pattern || undefined,
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
            <OptionGroup label="Parameter type" columns={3}>
              {TYPES.map((t) => (
                <OptionCard key={t.type} selected={type === t.type} onSelect={() => setType(t.type)} icon={t.icon} title={t.title} description={t.blurb} />
              ))}
            </OptionGroup>

            {secure && (
              <div className="mt-4 flex max-w-2xl flex-col gap-2">
                <Field
                  label="KMS key"
                  htmlFor="ssm-key"
                  error={keyErr ?? (keys.error ? errorMessage(keys.error) : undefined)}
                  help={
                    keySel === DEFAULT_KEY ? (
                      <>
                        The managed key <span className="font-mono">{DEFAULT_KEY_ALIAS}</span>
                        {defaultKey ? "" : " (created automatically on first use)"}.
                      </>
                    ) : (
                      "The value is encrypted with this key; decrypting it requires kms:Decrypt on the key. Only symmetric keys can be used."
                    )
                  }
                >
                  <KeyPicker
                    id="ssm-key"
                    keys={keys.data?.filter((k) => k.id !== defaultKey?.id)}
                    value={keySel}
                    onChange={setKeySel}
                    onlyEnabled
                    symmetricOnly
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
                error={submitted ? valueErr : value.length > maxValue ? valueErr : undefined}
                help={`${value.length} / ${maxValue} characters${type === "StringList" ? ". Separate items with commas." : ""}`}
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
                    <Tag key={i} accent={it ? "neutral" : "danger"} className={cn(it && "text-foreground")}>
                      {it || "(empty)"}
                    </Tag>
                  ))}
                </div>
              )}
            </div>
          </Section>

          <Section title="Tier and validation">
            <div className="flex max-w-2xl flex-col gap-4">
              <Field label="Tier" help={wasAdvanced ? "Advanced parameters can't be moved back to the Standard tier." : undefined}>
                <OptionGroup label="Parameter tier" columns={3}>
                  {TIERS.map((t) => (
                    <OptionCard
                      key={t.tier}
                      selected={tier === t.tier}
                      disabled={wasAdvanced && t.tier === "Standard"}
                      onSelect={() => setTier(t.tier)}
                      title={t.title}
                      description={t.blurb}
                    />
                  ))}
                </OptionGroup>
              </Field>
              <Field label="Data type" htmlFor="ssm-data-type" error={dataTypeErr} help={DATA_TYPES.find((d) => d.type === dataType)?.blurb}>
                <Select value={dataType} onValueChange={(v) => setDataType(v as SsmDataType)}>
                  <SelectTrigger id="ssm-data-type" className="w-full font-mono text-[13px] sm:w-72">
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    {DATA_TYPES.map((d) => (
                      <SelectItem key={d.type} value={d.type} className="font-mono text-[13px]">
                        {d.type}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              </Field>
              <Field
                label="Allowed pattern"
                htmlFor="ssm-pattern"
                optional
                error={submitted || pattern ? patternErr : undefined}
                help={
                  editing && initial?.allowedPattern
                    ? "A regular expression every new value must match. Leaving it empty keeps the current pattern."
                    : "A regular expression every value must match, e.g. ^\\d+$ for digits only."
                }
              >
                <Input
                  id="ssm-pattern"
                  value={pattern}
                  onChange={(e) => setPattern(e.target.value)}
                  autoComplete="off"
                  spellCheck={false}
                  placeholder="^[a-z0-9-]+$"
                  className="font-mono text-[13px]"
                  aria-invalid={(submitted || !!pattern) && !!patternErr}
                />
              </Field>
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
                <SummaryItem label="Tier">
                  <span className="inline-flex items-center gap-1.5">
                    <TierBadge tier={effectiveTier} />
                    {tier === "Intelligent-Tiering" && <span className="text-muted-foreground text-xs">(Intelligent-Tiering)</span>}
                  </span>
                </SummaryItem>
                <SummaryItem label="Data type">
                  <span className="font-mono text-[13px]">{dataType}</span>
                </SummaryItem>
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
              <div className="flex flex-wrap items-center justify-end gap-2 border-t pt-4">
                <Button type="button" variant="outline" asChild>
                  <Link href={editing ? parameterHref(editName) : "/ssm/"}>Cancel</Link>
                </Button>
                <Button type="submit" disabled={pending || (editing && !overwrite)}>
                  {pending ? <Loader2 className="animate-spin" /> : <Save />}
                  {editing ? "Save changes" : "Create parameter"}
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
      <dt className="text-faint mb-0.5 text-xs font-medium">{label}</dt>
      <dd className="min-w-0 break-words">{children}</dd>
    </div>
  )
}
