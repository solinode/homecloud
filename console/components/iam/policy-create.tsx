"use client"

import Link from "next/link"
import { useRouter } from "next/navigation"
import { useState } from "react"
import { Loader2 } from "lucide-react"
import { toast } from "sonner"

import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Textarea } from "@/components/ui/textarea"
import { Field } from "@/components/console/form-field"
import { PageHeader } from "@/components/console/page-header"
import { Section } from "@/components/console/section"
import { api, errorMessage } from "@/lib/api"
import { revalidate, useApi } from "@/lib/hooks"
import type { Policy, PolicySummary } from "@/lib/types"

import { IAM, POLICY_TEMPLATE, PolicyStatementsTable, nameError, policyHref, policyJson } from "./common"
import { PolicyEditor, policyTextError } from "./policy-editor"

export function PolicyCreate() {
  const router = useRouter()
  const policies = useApi<PolicySummary[]>(`${IAM}/policies`)
  const [text, setText] = useState(() => policyJson(POLICY_TEMPLATE))
  const [name, setName] = useState("")
  const [description, setDescription] = useState("")
  const [touched, setTouched] = useState(false)
  const [pending, setPending] = useState(false)

  const docErr = policyTextError(text)
  const nErr = nameError("policy", name) ?? (policies.data?.some((p) => p.name === name) ? `A policy named ${name} already exists.` : null)

  const submit = async () => {
    setTouched(true)
    if (docErr || nErr) return
    setPending(true)
    try {
      const p = await api.post<Policy>(`${IAM}/policies`, { name, description, document: JSON.parse(text) })
      toast.success(`Policy ${p.name} created`)
      revalidate(IAM)
      router.push(policyHref(p.name))
    } catch (e) {
      toast.error(errorMessage(e))
      setPending(false)
    }
  }

  return (
    <div className="flex flex-col gap-4 pb-20">
      <PageHeader
        title="Create policy"
        description="Policies grant permissions to the users and groups they are attached to. Write the policy in JSON using the Version / Statement grammar."
        breadcrumbs={[{ label: "IAM", href: "/iam/" }, { label: "Policies", href: "/iam/policies/" }, { label: "Create policy" }]}
      />
      <Section title="Policy details">
        <div className="grid max-w-3xl gap-4">
          <Field label="Policy name" htmlFor="policy-name" error={touched ? nErr : null} help="Up to 64 characters: letters, digits and + = , . @ _ -">
            <Input id="policy-name" value={name} onChange={(e) => setName(e.target.value.trim())} placeholder="e.g. ReportsBucketReadWrite" aria-invalid={touched && !!nErr} />
          </Field>
          <Field label="Description" htmlFor="policy-desc" optional>
            <Textarea id="policy-desc" rows={2} value={description} onChange={(e) => setDescription(e.target.value)} placeholder="What this policy allows" maxLength={1000} />
          </Field>
        </div>
      </Section>
      <Section title="Policy editor" description="Each statement has an Effect (Allow or Deny), one or more Actions (service:Action, wildcards allowed) and one or more Resources (ARNs or *).">
        <PolicyEditor value={text} onChange={setText} />
      </Section>
      {!docErr && (
        <Section title="Permissions summary">
          <PolicyStatementsTable doc={JSON.parse(text)} />
        </Section>
      )}
      <div className="bg-background/95 supports-[backdrop-filter]:bg-background/80 sticky bottom-0 z-10 -mx-1 flex items-center justify-end gap-2 border-t px-1 py-3 backdrop-blur">
        {touched && docErr && <span className="text-destructive mr-auto text-sm">{docErr}</span>}
        <Button variant="outline" asChild>
          <Link href="/iam/policies/">Cancel</Link>
        </Button>
        <Button onClick={submit} disabled={pending}>
          {pending && <Loader2 className="animate-spin" />} Create policy
        </Button>
      </div>
    </div>
  )
}
