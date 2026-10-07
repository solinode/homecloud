"use client"

import Link from "next/link"
import { useRouter } from "next/navigation"
import { useEffect, useMemo, useState } from "react"
import { AlertTriangle, Building2, Cloud, FileJson, Loader2 } from "lucide-react"
import { toast } from "sonner"

import { Button } from "@/components/ui/button"
import { Checkbox } from "@/components/ui/checkbox"
import { Input } from "@/components/ui/input"
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select"
import { Textarea } from "@/components/ui/textarea"
import { useSession } from "@/components/console/auth"
import { CopyButton } from "@/components/console/copy-button"
import { Field } from "@/components/console/form-field"
import { JsonEditor, jsonError } from "@/components/console/json-editor"
import { KeyValueGrid } from "@/components/console/key-value"
import { OptionCard, OptionGroup } from "@/components/console/option-card"
import { PageHeader } from "@/components/console/page-header"
import { Section } from "@/components/console/section"
import { Stepper } from "@/components/console/stepper"
import { TagsEditor, rowsToTags, type TagRow } from "@/components/console/tags-editor"
import { api, errorMessage } from "@/lib/api"
import { revalidate, useApi, useQueryParam } from "@/lib/hooks"
import type { IamRole, IamUser, PolicySummary, TrustPolicyDocument } from "@/lib/types"

import { BoundaryHelp, BoundarySelect } from "./boundary"
import { CheckList, IAM, PolicyPicker, PolicyTypeBadge, nameError, policyHref, policyJson, policyNameFromArn } from "./common"
import { SERVICE_PRINCIPALS, SESSION_DURATIONS, TrustedEntitiesList, accountTrust, roleHref, serviceTrust, validateTrustPolicy } from "./role-common"

const STEPS = ["Select trusted entity", "Add permissions", "Name, review, and create"]
const STEP_DEFS = STEPS.map((label, i) => ({ label, description: `Step ${i + 1}` }))

type EntityType = "service" | "account" | "custom"

const ENTITY_TYPES: { id: EntityType; label: string; description: string; icon: typeof Cloud }[] = [
  { id: "service", label: "AWS service", description: "Allow HomeCloud services like Lambda, EC2 or ECS to perform actions in this account.", icon: Cloud },
  { id: "account", label: "This account", description: "Allow users in this account to assume the role and act with its permissions.", icon: Building2 },
  { id: "custom", label: "Custom trust policy", description: "Write a trust policy in JSON to allow any principal to assume this role.", icon: FileJson },
]

function trustTextError(text: string): string | null {
  return jsonError(text) ?? validateTrustPolicy(JSON.parse(text))
}

export function RoleCreate() {
  const router = useRouter()
  const session = useSession()
  const serviceParam = useQueryParam("service")
  const summary = useApi<{ account_id: string }>(`${IAM}/summary`)
  const accountId = summary.data?.account_id || session.account_id
  const roles = useApi<IamRole[]>(`${IAM}/roles`)
  const users = useApi<IamUser[]>(`${IAM}/users`)
  const policies = useApi<PolicySummary[]>(`${IAM}/policies`)

  const [step, setStep] = useState(0)
  const [type, setType] = useState<EntityType>("service")
  const [service, setService] = useState(SERVICE_PRINCIPALS[0].principal)
  const [specificUsers, setSpecificUsers] = useState(false)
  const [trustUsers, setTrustUsers] = useState<string[]>([])
  const [customText, setCustomText] = useState("")
  const [selectedPolicies, setSelectedPolicies] = useState<string[]>([])
  const [name, setName] = useState("")
  const [description, setDescription] = useState("")
  const [maxSession, setMaxSession] = useState(3600)
  const [tags, setTags] = useState<TagRow[]>([])
  const [boundary, setBoundary] = useState("")
  const [touched, setTouched] = useState(false)
  const [pending, setPending] = useState(false)

  useEffect(() => {
    if (!serviceParam) return
    setType("service")
    setService(serviceParam)
  }, [serviceParam])

  const userArns = useMemo(() => {
    const byName = new Map((users.data ?? []).map((u) => [u.name, u.arn]))
    return trustUsers.map((n) => byName.get(n)).filter((a): a is string => !!a)
  }, [users.data, trustUsers])

  const generated: TrustPolicyDocument | null =
    type === "service"
      ? serviceTrust([service])
      : type === "account" && accountId && (!specificUsers || userArns.length)
        ? accountTrust(accountId, specificUsers ? userArns : [])
        : null

  // Seed the custom editor with the policy generated so far.
  const chooseType = (t: EntityType) => {
    if (t === "custom" && !customText) setCustomText(policyJson(generated ?? serviceTrust([service])))
    setType(t)
    setTouched(false)
  }

  const trustDoc: TrustPolicyDocument | null = type === "custom" ? (trustTextError(customText) ? null : (JSON.parse(customText) as TrustPolicyDocument)) : generated
  const trustErr =
    type === "custom"
      ? trustTextError(customText)
      : type === "account" && specificUsers && !userArns.length
        ? "Select at least one user, or allow the whole account."
        : type === "service" && !service
          ? "Choose a service."
          : !trustDoc
            ? "Loading the account ID..."
            : null
  const nErr = nameError("role", name) ?? (roles.data?.some((r) => r.name === name) ? `A role named ${name} already exists.` : null)
  const tagErr = tags.some((t) => !t.key.trim() && t.value.trim()) ? "Every tag needs a key." : null
  const tagKeys = tags.map((t) => t.key.trim()).filter(Boolean)
  const dupTag = new Set(tagKeys).size !== tagKeys.length ? "Tag keys must be unique." : null

  const go = (to: number) => {
    if (to > step) {
      setTouched(true)
      if (step === 0 && trustErr) return
    }
    setTouched(false)
    setStep(to)
    window.scrollTo({ top: 0 })
  }

  const create = async () => {
    setTouched(true)
    if (nErr || tagErr || dupTag || trustErr || !trustDoc) return
    setPending(true)
    try {
      const r = await api.post<IamRole>(`${IAM}/roles`, {
        name,
        description,
        assume_role_policy: trustDoc,
        max_session_duration: maxSession,
        policies: selectedPolicies,
        tags: rowsToTags(tags),
        permissions_boundary: boundary || undefined,
      })
      toast.success(`Role ${r.name} created`)
      revalidate(IAM)
      router.push(roleHref(r.name))
    } catch (e) {
      toast.error(errorMessage(e))
      setPending(false)
    }
  }

  const byName = new Map((policies.data ?? []).map((p) => [p.name, p]))
  const trustJson = trustDoc ? policyJson(trustDoc) : ""

  return (
    <div className="flex flex-col gap-4 pb-20">
      <PageHeader
        title="Create role"
        description="A role grants temporary credentials with its permissions to the entities it trusts."
        breadcrumbs={[{ label: "IAM", href: "/iam/" }, { label: "Roles", href: "/iam/roles/" }, { label: "Create role" }]}
      />

      <div className="grid grid-cols-1 gap-6 lg:grid-cols-[13rem_minmax(0,1fr)]">
        <div className="min-w-0">
          <Stepper steps={STEP_DEFS} current={step} onStepClick={(i) => i < step && go(i)} className="lg:hidden" />
          <Stepper steps={STEP_DEFS} current={step} onStepClick={(i) => i < step && go(i)} orientation="vertical" className="hidden lg:flex" />
        </div>

        <div className="flex min-w-0 flex-col gap-4">
          {step === 0 && (
            <>
              <Section title="Trusted entity type">
                <OptionGroup label="Trusted entity type" columns={3}>
                  {ENTITY_TYPES.map((t) => (
                    <OptionCard key={t.id} selected={type === t.id} onSelect={() => chooseType(t.id)} icon={t.icon} title={t.label} description={t.description} />
                  ))}
                </OptionGroup>
              </Section>

              {type === "service" && (
                <Section title="Use case" description="Choose the service that will assume this role. The trust policy allows its service principal to call sts:AssumeRole.">
                  <OptionGroup label="Service" columns={3}>
                    {SERVICE_PRINCIPALS.map((s) => (
                      <OptionCard
                        key={s.principal}
                        selected={service === s.principal}
                        onSelect={() => setService(s.principal)}
                        title={s.name}
                        description={
                          <>
                            <span className="text-faint mb-0.5 block font-mono text-[11px] break-all">{s.principal}</span>
                            {s.description}
                          </>
                        }
                      />
                    ))}
                    {!SERVICE_PRINCIPALS.some((s) => s.principal === service) && service && (
                      <OptionCard selected onSelect={() => undefined} title={<span className="font-mono break-all">{service}</span>} description="Service principal from the link that opened this page." />
                    )}
                  </OptionGroup>
                  <p className="text-muted-foreground mt-3 text-xs">
                    For another service principal, choose <span className="text-foreground font-medium">Custom trust policy</span>.
                  </p>
                </Section>
              )}

              {type === "account" && (
                <Section title="This account" description="Principals in this account can assume the role if their own permissions allow sts:AssumeRole on it.">
                  <div className="flex flex-col gap-4">
                    <KeyValueGrid items={[{ label: "Account ID", value: <span className="font-mono">{accountId || "..."}</span> }]} />
                    <label className="hover:bg-muted/50 flex cursor-pointer items-start gap-3 rounded-lg border p-3.5 transition-colors">
                      <Checkbox checked={specificUsers} onCheckedChange={(v) => setSpecificUsers(v === true)} className="mt-0.5" />
                      <span className="flex flex-col gap-0.5">
                        <span className="text-sm font-medium">Allow only specific users</span>
                        <span className="text-muted-foreground text-xs">
                          Name the users in the trust policy instead of the whole account. Named users can assume the role without an sts:AssumeRole permission of their own.
                        </span>
                      </span>
                    </label>
                    {specificUsers && (
                      <div className="flex flex-col gap-1.5">
                        <CheckList
                          loading={users.isLoading}
                          items={(users.data ?? []).map((u) => ({ id: u.name, label: u.name, sub: u.root ? "Root user" : undefined }))}
                          selected={trustUsers}
                          onChange={setTrustUsers}
                          empty="No users in this account."
                        />
                        {touched && trustErr && <p className="text-destructive text-xs">{trustErr}</p>}
                      </div>
                    )}
                  </div>
                </Section>
              )}

              {type === "custom" && (
                <Section
                  title="Custom trust policy"
                  description='Each statement needs an Effect, a Principal ("*" or an object with AWS, Service or Federated) and sts: actions. Resource is not allowed.'
                >
                  <JsonEditor value={customText} onChange={setCustomText} validate={validateTrustPolicy} rows={14} />
                </Section>
              )}

              {type !== "custom" && trustDoc && (
                <Section title="Trust policy preview" actions={<CopyButton value={trustJson} size="sm" label="Copy JSON" toastMessage="Trust policy copied" />}>
                  <JsonEditor value={trustJson} readOnly rows={Math.min(trustJson.split("\n").length, 20)} />
                </Section>
              )}
            </>
          )}

          {step === 1 && (
            <Section title="Permissions policies" description="Choose the policies that define what the role can do. You can change them after creating the role.">
              <PolicyPicker policies={policies.data} loading={policies.isLoading} selected={selectedPolicies} onChange={setSelectedPolicies} maxHeight="max-h-[28rem]" />
            </Section>
          )}
          {step === 1 && (
            <Section title="Set permissions boundary" description={<BoundaryHelp kind="role" />}>
              <Field label="Permissions boundary" optional htmlFor="role-boundary">
                <BoundarySelect id="role-boundary" value={boundary} onChange={setBoundary} />
              </Field>
            </Section>
          )}

          {step === 2 && (
            <>
              <Section title="Role details">
                <div className="grid max-w-3xl gap-4">
                  <Field label="Role name" htmlFor="role-name" error={touched ? nErr : null} help="Up to 64 characters: letters, digits and + = , . @ _ -.">
                    <Input id="role-name" autoFocus autoComplete="off" value={name} onChange={(e) => setName(e.target.value.trim())} placeholder="e.g. orders-lambda-role" aria-invalid={touched && !!nErr} />
                  </Field>
                  <Field label="Description" htmlFor="role-desc" optional>
                    <Textarea id="role-desc" rows={2} value={description} onChange={(e) => setDescription(e.target.value)} maxLength={1000} placeholder="What the role is used for" />
                  </Field>
                  <Field label="Maximum session duration" help="How long credentials from sts:AssumeRole last at most (API callers choose up to this).">
                    <Select value={String(maxSession)} onValueChange={(v) => setMaxSession(Number(v))}>
                      <SelectTrigger className="w-full sm:w-56" aria-label="Maximum session duration">
                        <SelectValue />
                      </SelectTrigger>
                      <SelectContent>
                        {SESSION_DURATIONS.map((d) => (
                          <SelectItem key={d.value} value={String(d.value)}>
                            {d.label}
                          </SelectItem>
                        ))}
                      </SelectContent>
                    </Select>
                  </Field>
                </div>
              </Section>

              <Section title="Step 1: Select trusted entities" actions={<Button size="sm" variant="outline" onClick={() => go(0)}>Edit</Button>}>
                <div className="flex flex-col gap-3">
                  {trustDoc && <TrustedEntitiesList role={{ assume_role_policy: trustDoc }} max={20} />}
                  <JsonEditor value={trustJson} readOnly rows={Math.min(trustJson.split("\n").length, 20)} />
                </div>
              </Section>

              <Section title="Step 2: Add permissions" flush actions={<Button size="sm" variant="outline" onClick={() => go(1)}>Edit</Button>}>
                {selectedPolicies.length ? (
                  <ul>
                    {selectedPolicies.map((p) => (
                      <li key={p} className="flex flex-wrap items-center gap-2 border-b px-5 py-2.5 text-sm last:border-0">
                        <Link href={policyHref(p)} className="text-primary min-w-0 truncate font-medium hover:underline" target="_blank" title={p}>
                          {p}
                        </Link>
                        {byName.get(p) && <PolicyTypeBadge managed={byName.get(p)!.managed} />}
                        {byName.get(p)?.description && <span className="text-muted-foreground w-full text-xs sm:w-auto">{byName.get(p)!.description}</span>}
                      </li>
                    ))}
                  </ul>
                ) : (
                  <p className="text-muted-foreground flex items-center gap-2 px-5 py-4 text-sm">
                    <AlertTriangle className="text-warning size-4 shrink-0" /> No policies selected. The role cannot do anything until you add permissions.
                  </p>
                )}
                <p className="border-t px-5 py-2.5 text-sm">
                  <span className="text-muted-foreground">Permissions boundary: </span>
                  {boundary ? policyNameFromArn(boundary) : "Not set"}
                </p>
              </Section>

              <Section title="Step 3: Add tags" description="Optional key-value pairs to organize and find roles.">
                <Field label="Tags" optional error={tagErr ?? dupTag}>
                  <TagsEditor rows={tags} onChange={setTags} addLabel="Add new tag" />
                </Field>
              </Section>
            </>
          )}

          <div className="bg-background/95 supports-[backdrop-filter]:bg-background/80 sticky bottom-0 z-10 -mx-1 flex flex-wrap items-center justify-end gap-2 border-t px-1 py-3 backdrop-blur">
            {touched && step === 0 && trustErr && !(type === "account" && specificUsers) && <span className="text-destructive mr-auto text-sm">{trustErr}</span>}
            <Button variant="outline" asChild>
              <Link href="/iam/roles/">Cancel</Link>
            </Button>
            {step > 0 && (
              <Button variant="outline" onClick={() => go(step - 1)} disabled={pending}>
                Previous
              </Button>
            )}
            {step < 2 ? (
              <Button onClick={() => go(step + 1)}>Next</Button>
            ) : (
              <Button onClick={create} disabled={pending}>
                {pending && <Loader2 className="animate-spin" />} Create role
              </Button>
            )}
          </div>
        </div>
      </div>
    </div>
  )
}
