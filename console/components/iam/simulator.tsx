"use client"

import Link from "next/link"
import { useEffect, useMemo, useState } from "react"
import { FlaskConical, Loader2, Play, Plus, X } from "lucide-react"

import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select"
import { Skeleton } from "@/components/ui/skeleton"
import { EmptyState } from "@/components/console/empty-state"
import { Field } from "@/components/console/form-field"
import { PageHeader } from "@/components/console/page-header"
import { Section } from "@/components/console/section"
import { StatusBadge } from "@/components/console/status-badge"
import { api } from "@/lib/api"
import { pluralize } from "@/lib/format"
import { useAction, useApi, useQueryParam } from "@/lib/hooks"
import type { IamGroup, IamUser, SimulationResult } from "@/lib/types"
import { cn } from "@/lib/utils"

import { COMMON_ACTIONS, IAM, groupHref, policyHref, userHref } from "./common"

const DECISION: Record<SimulationResult["decision"], { label: string; tone: "success" | "neutral" | "danger" }> = {
  allowed: { label: "Allowed", tone: "success" },
  implicitDeny: { label: "Implicitly denied", tone: "neutral" },
  explicitDeny: { label: "Explicitly denied", tone: "danger" },
}

const ACTION_RE = /^[a-zA-Z0-9*?-]+:[a-zA-Z0-9*?]+$/

export function PolicySimulator() {
  const users = useApi<IamUser[]>(`${IAM}/users`)
  const groups = useApi<IamGroup[]>(`${IAM}/groups`)
  const userParam = useQueryParam("user")
  const [user, setUser] = useState("")
  const [actions, setActions] = useState<string[]>(["s3:ListAllMyBuckets", "ec2:RunInstances", "iam:CreateUser", "secretsmanager:GetSecretValue"])
  const [input, setInput] = useState("")
  const [resource, setResource] = useState("*")
  const [service, setService] = useState(COMMON_ACTIONS[0].service)
  const [results, setResults] = useState<SimulationResult[] | null>(null)
  const { pending, run } = useAction()

  useEffect(() => {
    if (user || !users.data?.length) return
    const pick = users.data.find((u) => u.name === userParam) ?? users.data.find((u) => !u.root) ?? users.data[0]
    setUser(pick.name)
  }, [users.data, userParam, user])

  const u = users.data?.find((x) => x.name === user)
  const viaGroups = useMemo(() => (groups.data ?? []).filter((g) => u?.groups.includes(g.name)), [groups.data, u])

  const addActions = (raw: string) => {
    const parts = raw
      .split(/[\s,]+/)
      .map((s) => s.trim())
      .filter(Boolean)
    if (!parts.length) return
    setActions((a) => [...a, ...parts.filter((p) => !a.includes(p))])
    setInput("")
  }
  const invalid = actions.filter((a) => !ACTION_RE.test(a))

  const simulate = async () => {
    if (!user || !actions.length) return
    const r = await run(() => api.post<SimulationResult[]>(`${IAM}/simulate`, { user, actions, resource: resource.trim() || "*" }))
    if (r) setResults(r)
  }

  const counts = results
    ? {
        allowed: results.filter((r) => r.decision === "allowed").length,
        denied: results.filter((r) => r.decision !== "allowed").length,
      }
    : null

  return (
    <div className="flex flex-col gap-4">
      <PageHeader
        title="Policy simulator"
        description="Test whether a user is allowed to perform actions, based on its attached, group and inline policies."
        breadcrumbs={[{ label: "IAM", href: "/iam/" }, { label: "Policy simulator" }]}
      />
      <div className="grid grid-cols-1 gap-4 lg:grid-cols-[minmax(0,1fr)_20rem]">
        <div className="flex flex-col gap-4">
          <Section title="Simulation settings">
            <div className="flex flex-col gap-4">
              <div className="grid gap-4 sm:grid-cols-2">
                <Field label="User">
                  {users.isLoading ? (
                    <Skeleton className="h-9 w-full" />
                  ) : (
                    <Select
                      value={user}
                      onValueChange={(v) => {
                        setUser(v)
                        setResults(null)
                      }}
                    >
                      <SelectTrigger className="w-full">
                        <SelectValue placeholder="Choose a user" />
                      </SelectTrigger>
                      <SelectContent>
                        {(users.data ?? []).map((x) => (
                          <SelectItem key={x.name} value={x.name}>
                            {x.name}
                            {x.root ? " (root)" : ""}
                          </SelectItem>
                        ))}
                      </SelectContent>
                    </Select>
                  )}
                </Field>
                <Field label="Resource ARN" htmlFor="sim-resource" help="Use * for all resources, or an ARN such as arn:hc:s3:::my-bucket/*">
                  <Input id="sim-resource" value={resource} onChange={(e) => setResource(e.target.value)} className="font-mono text-[13px]" />
                </Field>
              </div>

              <Field label={`Actions (${actions.length})`} error={invalid.length ? `Not in service:Action form: ${invalid.join(", ")}` : null}>
                <div className="flex flex-col gap-2">
                  <div className="flex min-h-10 flex-wrap gap-1.5 rounded-md border p-2">
                    {actions.map((a) => (
                      <span
                        key={a}
                        className={cn(
                          "bg-muted inline-flex items-center gap-1 rounded-full py-0.5 pr-1 pl-2 font-mono text-xs",
                          !ACTION_RE.test(a) && "text-destructive ring-destructive/40 ring-1",
                        )}
                      >
                        {a}
                        <button type="button" onClick={() => setActions(actions.filter((x) => x !== a))} className="hover:bg-accent rounded-full p-0.5" aria-label={`Remove ${a}`}>
                          <X className="size-3" />
                        </button>
                      </span>
                    ))}
                    <input
                      value={input}
                      onChange={(e) => setInput(e.target.value)}
                      onKeyDown={(e) => {
                        if (e.key === "Enter" || e.key === ",") {
                          e.preventDefault()
                          addActions(input)
                        } else if (e.key === "Backspace" && !input && actions.length) setActions(actions.slice(0, -1))
                      }}
                      onBlur={() => addActions(input)}
                      onPaste={(e) => {
                        const t = e.clipboardData.getData("text")
                        if (/[\s,]/.test(t)) {
                          e.preventDefault()
                          addActions(t)
                        }
                      }}
                      placeholder={actions.length ? "Add action…" : "e.g. s3:GetObject (Enter to add)"}
                      className="min-w-40 flex-1 bg-transparent px-1 font-mono text-xs outline-none"
                    />
                  </div>
                  <div className="flex flex-wrap items-center gap-1">
                    <span className="text-muted-foreground mr-1 text-xs">Common actions:</span>
                    {COMMON_ACTIONS.map((s) => (
                      <button
                        key={s.service}
                        type="button"
                        onClick={() => setService(s.service)}
                        className={cn("rounded px-2 py-0.5 text-xs", service === s.service ? "bg-primary text-primary-foreground" : "bg-muted text-muted-foreground hover:bg-accent")}
                      >
                        {s.service}
                      </button>
                    ))}
                    {actions.length > 0 && (
                      <Button type="button" size="sm" variant="ghost" className="ml-auto h-6 text-xs" onClick={() => setActions([])}>
                        Clear all
                      </Button>
                    )}
                  </div>
                  <div className="flex flex-wrap gap-1.5">
                    {(COMMON_ACTIONS.find((s) => s.service === service)?.actions ?? []).map((a) => (
                      <button
                        key={a}
                        type="button"
                        disabled={actions.includes(a)}
                        onClick={() => setActions([...actions, a])}
                        className="hover:bg-accent inline-flex items-center gap-1 rounded-full border px-2 py-0.5 font-mono text-[11px] disabled:opacity-40"
                      >
                        <Plus className="size-3" /> {a}
                      </button>
                    ))}
                  </div>
                </div>
              </Field>
              <div className="flex justify-end">
                <Button onClick={simulate} disabled={pending || !user || !actions.length}>
                  {pending ? <Loader2 className="animate-spin" /> : <Play />} Run simulation
                </Button>
              </div>
            </div>
          </Section>

          <Section
            title="Results"
            flush
            description={counts ? `${pluralize(counts.allowed, "action")} allowed, ${counts.denied} denied for ${user}` : undefined}
          >
            {!results ? (
              <EmptyState icon={FlaskConical} title="No simulation yet" description="Choose a user and actions, then run the simulation." />
            ) : !results.length ? (
              <p className="text-muted-foreground p-6 text-center text-sm">No actions were simulated.</p>
            ) : (
              <div className="overflow-x-auto">
                <table className="w-full text-sm">
                  <thead>
                    <tr className="bg-muted/40 text-muted-foreground border-b text-left text-xs">
                      <th className="px-4 py-2 font-semibold">Action</th>
                      <th className="hidden px-4 py-2 font-semibold sm:table-cell">Resource</th>
                      <th className="px-4 py-2 font-semibold">Decision</th>
                    </tr>
                  </thead>
                  <tbody>
                    {results.map((r, i) => (
                      <tr key={`${r.action}-${i}`} className="border-b last:border-0">
                        <td className="px-4 py-2 font-mono text-[13px]">{r.action}</td>
                        <td className="text-muted-foreground hidden px-4 py-2 font-mono text-xs break-all sm:table-cell">{r.resource}</td>
                        <td className="px-4 py-2">
                          <StatusBadge status={r.decision} tone={DECISION[r.decision]?.tone} label={DECISION[r.decision]?.label ?? r.decision} />
                        </td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            )}
          </Section>
        </div>

        <Section title="Effective policies" description={u ? <>Policies that apply to <Link href={userHref(u.name)} className="text-primary hover:underline">{u.name}</Link></> : undefined}>
          {!u ? (
            <Skeleton className="h-24 w-full" />
          ) : (
            <div className="flex flex-col gap-4 text-sm">
              {u.root && <p className="rounded-md bg-amber-50 px-3 py-2 text-xs text-amber-800 dark:bg-amber-500/10 dark:text-amber-200">The root user is allowed every action regardless of policies.</p>}
              <PolicyGroup title="Attached directly" names={u.attached_policies} />
              {viaGroups.map((g) => (
                <PolicyGroup
                  key={g.name}
                  title={
                    <>
                      Group:{" "}
                      <Link href={groupHref(g.name)} className="text-primary hover:underline">
                        {g.name}
                      </Link>
                    </>
                  }
                  names={g.attached_policies}
                />
              ))}
              <div>
                <p className="text-muted-foreground mb-1 text-xs font-semibold tracking-wide uppercase">Inline policies</p>
                {Object.keys(u.inline_policies ?? {}).length ? (
                  <ul className="flex flex-col gap-0.5">
                    {Object.keys(u.inline_policies ?? {}).map((n) => (
                      <li key={n}>
                        <Link href={`${userHref(u.name)}`} className="text-primary hover:underline">
                          {n}
                        </Link>
                      </li>
                    ))}
                  </ul>
                ) : (
                  <p className="text-muted-foreground">None</p>
                )}
              </div>
            </div>
          )}
        </Section>
      </div>
    </div>
  )
}

function PolicyGroup({ title, names }: { title: React.ReactNode; names: string[] }) {
  return (
    <div>
      <p className="text-muted-foreground mb-1 text-xs font-semibold tracking-wide uppercase">{title}</p>
      {names.length ? (
        <ul className="flex flex-col gap-0.5">
          {names.map((n) => (
            <li key={n}>
              <Link href={policyHref(n)} className="text-primary hover:underline">
                {n}
              </Link>
            </li>
          ))}
        </ul>
      ) : (
        <p className="text-muted-foreground">None</p>
      )}
    </div>
  )
}
