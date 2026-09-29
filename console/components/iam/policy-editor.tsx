"use client"

import { useState } from "react"
import { Plus } from "lucide-react"

import { JsonEditor, jsonError } from "@/components/console/json-editor"
import type { PolicyDocument, PolicyStatement } from "@/lib/types"
import { cn } from "@/lib/utils"

import { COMMON_ACTIONS, asList, policyJson, statementsOf, validatePolicy } from "./common"

/** policyTextError returns the first JSON or schema error of a policy text. */
export function policyTextError(text: string): string | null {
  const e = jsonError(text)
  if (e) return e
  return validatePolicy(JSON.parse(text))
}

/**
 * addAction appends an action to the document: into the first
 * Allow statement on Resource "*" if there is one, else a new statement.
 */
function addAction(text: string, action: string): string {
  let doc: PolicyDocument
  try {
    doc = JSON.parse(text) as PolicyDocument
  } catch {
    return text
  }
  if (!doc || typeof doc !== "object") return text
  // A single statement object becomes an array so another can be added.
  const list: PolicyStatement[] = statementsOf<PolicyStatement>(doc).filter((s) => s && typeof s === "object")
  const st = list.find((s) => s.Effect === "Allow" && s.Action !== undefined && !s.Condition && asList(s.Resource).length === 1 && asList(s.Resource)[0] === "*")
  if (st) {
    const acts = asList(st.Action)
    if (!acts.includes(action)) st.Action = [...acts, action]
  } else {
    list.push({ Effect: "Allow", Action: [action], Resource: ["*"] })
  }
  const wasObject = !!doc.Statement && typeof doc.Statement === "object" && !Array.isArray(doc.Statement)
  doc.Statement = list.length === 1 && wasObject ? list[0] : list
  if (!doc.Version) doc.Version = "2012-10-17"
  return policyJson(doc)
}

/** PolicyEditor is a JSON policy editor with a picker of common actions. */
export function PolicyEditor({ value, onChange, rows = 18 }: { value: string; onChange: (v: string) => void; rows?: number }) {
  const [service, setService] = useState(COMMON_ACTIONS[0].service)
  const actions = COMMON_ACTIONS.find((s) => s.service === service)?.actions ?? []
  const invalid = !!jsonError(value)
  let present: string[] = []
  if (!invalid) {
    try {
      present = statementsOf<PolicyStatement>(JSON.parse(value) as PolicyDocument).flatMap((s) => asList(s?.Action))
    } catch {
      present = []
    }
  }

  return (
    <div className="grid grid-cols-1 gap-4 lg:grid-cols-[minmax(0,1fr)_16rem]">
      <JsonEditor value={value} onChange={onChange} rows={rows} validate={validatePolicy} />
      <div className="flex flex-col gap-2 rounded-md border p-3">
        <p className="text-sm font-medium">Add actions</p>
        <p className="text-muted-foreground text-xs">Click an action to allow it on all resources. Edit the JSON to narrow Resource.</p>
        <div className="flex flex-wrap gap-1">
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
        </div>
        <div className="flex flex-wrap gap-1.5">
          {actions.map((a) => {
            const has = present.includes(a)
            return (
              <button
                key={a}
                type="button"
                disabled={invalid || has}
                onClick={() => onChange(addAction(value, a))}
                title={has ? "Already in the policy" : invalid ? "Fix the JSON first" : `Allow ${a}`}
                className={cn(
                  "inline-flex items-center gap-1 rounded-full border px-2 py-0.5 font-mono text-[11px] transition-colors",
                  has ? "border-emerald-500/40 bg-emerald-50 text-emerald-700 dark:bg-emerald-500/10 dark:text-emerald-300" : "hover:bg-accent disabled:opacity-50",
                )}
              >
                {!has && <Plus className="size-3" />}
                {a}
              </button>
            )
          })}
        </div>
      </div>
    </div>
  )
}
