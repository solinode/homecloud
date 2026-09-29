"use client"

import { Plus, Trash2 } from "lucide-react"

import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select"
import { cn } from "@/lib/utils"

import { MAX_PORT_RANGE, RULE_PRESETS, newRuleDraft, validateRule, type RuleDraft } from "./common"

/** applyPreset fills protocol and ports from a well-known preset. */
export function applyPreset(d: RuleDraft, label: string): RuleDraft {
  const p = RULE_PRESETS.find((x) => x.label === label)
  if (!p) return d
  return {
    ...d,
    preset: label,
    protocol: p.protocol,
    from: p.port ? String(p.port) : d.preset === label ? d.from : "",
    to: p.port ? String(p.port) : d.preset === label ? d.to : "",
  }
}

/** RuleFields is the form row of one inbound rule. */
export function RuleFields({
  rule,
  onChange,
  showErrors,
  onRemove,
  compact,
}: {
  rule: RuleDraft
  onChange: (r: RuleDraft) => void
  showErrors: boolean
  onRemove?: () => void
  compact?: boolean
}) {
  const errs = showErrors ? validateRule(rule) : {}
  const preset = RULE_PRESETS.find((p) => p.label === rule.preset)
  const fixed = !!preset?.port
  const id = rule.key
  return (
    <div className={cn("grid grid-cols-2 gap-3 sm:grid-cols-12", !compact && "bg-muted/30 rounded-md border p-3")}>
      <div className="col-span-2 flex flex-col gap-1 sm:col-span-3">
        <label className="text-muted-foreground text-xs font-medium" htmlFor={`${id}-type`}>
          Type
        </label>
        <Select value={rule.preset} onValueChange={(v) => onChange(applyPreset(rule, v))}>
          <SelectTrigger id={`${id}-type`} size="sm" className="w-full">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            {RULE_PRESETS.map((p) => (
              <SelectItem key={p.label} value={p.label}>
                {p.label}
                {p.port && <span className="text-muted-foreground font-mono text-xs">{p.port}</span>}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
      </div>
      <div className="flex flex-col gap-1 sm:col-span-2">
        <label className="text-muted-foreground text-xs font-medium" htmlFor={`${id}-proto`}>
          Protocol
        </label>
        <Select
          value={rule.protocol}
          disabled={fixed}
          onValueChange={(v) => onChange({ ...rule, protocol: v as "tcp" | "udp", preset: v === "udp" ? "Custom UDP" : "Custom TCP" })}
        >
          <SelectTrigger id={`${id}-proto`} size="sm" className="w-full">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="tcp">TCP</SelectItem>
            <SelectItem value="udp">UDP</SelectItem>
          </SelectContent>
        </Select>
      </div>
      <div className="flex flex-col gap-1 sm:col-span-2">
        <label className="text-muted-foreground text-xs font-medium" htmlFor={`${id}-from`}>
          From port
        </label>
        <Input
          id={`${id}-from`}
          inputMode="numeric"
          className="h-8 font-mono text-[13px]"
          value={rule.from}
          disabled={fixed}
          placeholder="8080"
          aria-invalid={!!errs.from}
          onChange={(e) => {
            const v = e.target.value.replace(/[^\d]/g, "")
            onChange({ ...rule, from: v, to: rule.to === rule.from ? v : rule.to })
          }}
        />
        {errs.from && <span className="text-destructive text-xs">{errs.from}</span>}
      </div>
      <div className="flex flex-col gap-1 sm:col-span-2">
        <label className="text-muted-foreground text-xs font-medium" htmlFor={`${id}-to`}>
          To port
        </label>
        <Input
          id={`${id}-to`}
          inputMode="numeric"
          className="h-8 font-mono text-[13px]"
          value={rule.to}
          disabled={fixed}
          placeholder={rule.from || "8080"}
          aria-invalid={!!errs.to}
          onChange={(e) => onChange({ ...rule, to: e.target.value.replace(/[^\d]/g, "") })}
        />
        {errs.to && <span className="text-destructive text-xs">{errs.to}</span>}
      </div>
      <div className="col-span-2 flex flex-col gap-1 sm:col-span-3">
        <label className="text-muted-foreground text-xs font-medium" htmlFor={`${id}-cidr`}>
          Source CIDR
        </label>
        <div className="flex gap-2">
          <Input
            id={`${id}-cidr`}
            className="h-8 font-mono text-[13px]"
            value={rule.cidr}
            placeholder="0.0.0.0/0"
            aria-invalid={!!errs.cidr}
            onChange={(e) => onChange({ ...rule, cidr: e.target.value.trim() })}
          />
          {onRemove && (
            <Button type="button" variant="ghost" size="icon" className="size-8 shrink-0" onClick={onRemove} aria-label="Remove rule">
              <Trash2 />
            </Button>
          )}
        </div>
        {errs.cidr && <span className="text-destructive text-xs">{errs.cidr}</span>}
      </div>
      <div className="col-span-2 flex flex-col gap-1 sm:col-span-12">
        <label className="text-muted-foreground text-xs font-medium" htmlFor={`${id}-desc`}>
          Description <span className="font-normal">- optional</span>
        </label>
        <Input
          id={`${id}-desc`}
          className="h-8"
          value={rule.description}
          maxLength={255}
          placeholder="e.g. Web traffic"
          onChange={(e) => onChange({ ...rule, description: e.target.value })}
        />
      </div>
    </div>
  )
}

/** RulesEditor edits a list of inbound rules (used when creating a group). */
export function RulesEditor({ rules, onChange, showErrors }: { rules: RuleDraft[]; onChange: (r: RuleDraft[]) => void; showErrors: boolean }) {
  return (
    <div className="flex flex-col gap-3">
      {rules.length === 0 && <p className="text-muted-foreground text-sm">No inbound rules. Nothing will be published on the host.</p>}
      {rules.map((r, i) => (
        <RuleFields
          key={r.key}
          rule={r}
          showErrors={showErrors}
          onChange={(nr) => onChange(rules.map((x, j) => (j === i ? nr : x)))}
          onRemove={() => onChange(rules.filter((_, j) => j !== i))}
        />
      ))}
      <div className="flex flex-wrap items-center gap-3">
        <Button type="button" variant="outline" size="sm" onClick={() => onChange([...rules, newRuleDraft("HTTP")])}>
          <Plus /> Add rule
        </Button>
        <span className="text-muted-foreground text-xs">Each rule may cover at most {MAX_PORT_RANGE} ports.</span>
      </div>
    </div>
  )
}
