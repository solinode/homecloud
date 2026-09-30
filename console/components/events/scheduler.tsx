"use client"

import { useState } from "react"
import { CalendarClock, Plus } from "lucide-react"
import { toast } from "sonner"

import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select"
import { Switch } from "@/components/ui/switch"
import { Textarea } from "@/components/ui/textarea"
import { ActionsMenu } from "@/components/console/actions-menu"
import { ConfirmDialog } from "@/components/console/confirm-dialog"
import { DataTable, type Column } from "@/components/console/data-table"
import { EmptyState } from "@/components/console/empty-state"
import { Field } from "@/components/console/form-field"
import { FormDialog } from "@/components/console/form-dialog"
import { PageHeader } from "@/components/console/page-header"
import { StatusBadge } from "@/components/console/status-badge"
import { api, seg } from "@/lib/api"
import { revalidate, useApi } from "@/lib/hooks"
import type { IamRole, Schedule, ScheduleGroup } from "@/lib/types"

import { TARGET_KINDS, targetKind, targetKindLabel, targetName, type TargetKind } from "./common"
import { useTargetOptions } from "./rule-wizard"

const SCHED = "/api/v1/scheduler"
const schedPath = (s: { GroupName: string; Name: string }) => `${SCHED}/schedule-groups/${seg(s.GroupName)}/schedules/${seg(s.Name)}`

const toLocal = (epoch?: number) => {
  if (!epoch) return ""
  const d = new Date(epoch * 1000)
  const p = (n: number) => String(n).padStart(2, "0")
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())}T${p(d.getHours())}:${p(d.getMinutes())}`
}
const fromLocal = (v: string) => (v ? Math.floor(new Date(v).getTime() / 1000) : undefined)

function tzList(): string[] {
  try {
    return (Intl as unknown as { supportedValuesOf: (k: string) => string[] }).supportedValuesOf("timeZone")
  } catch {
    return ["UTC"]
  }
}

function ScheduleDialog({ existing, groups, defaultGroup, onClose }: { existing?: Schedule; groups: ScheduleGroup[]; defaultGroup: string; onClose: () => void }) {
  const opts = useTargetOptions()
  const roles = useApi<IamRole[]>("/api/v1/iam/roles", { revalidateOnFocus: false })
  const [name, setName] = useState(existing?.Name ?? "")
  const [group, setGroup] = useState(existing?.GroupName ?? defaultGroup)
  const [expr, setExpr] = useState(existing?.ScheduleExpression ?? "rate(5 minutes)")
  const [tz, setTz] = useState(existing?.ScheduleExpressionTimezone ?? "UTC")
  const [flex, setFlex] = useState(existing?.FlexibleTimeWindow.Mode === "FLEXIBLE")
  const [window, setWindow] = useState(String(existing?.FlexibleTimeWindow.MaximumWindowInMinutes || 15))
  const [kind, setKind] = useState<TargetKind>((existing && targetKind(existing.Target.Arn)) || "lambda")
  const [arn, setArn] = useState(existing?.Target.Arn ?? "")
  const [role, setRole] = useState(existing?.Target.RoleArn ?? "")
  const [input, setInput] = useState(existing?.Target.Input ?? "")
  const [start, setStart] = useState(toLocal(existing?.StartDate))
  const [end, setEnd] = useState(toLocal(existing?.EndDate))
  const [del, setDel] = useState(existing?.ActionAfterCompletion === "DELETE")
  const [enabled, setEnabled] = useState(existing ? existing.State === "ENABLED" : true)

  const inputErr = (() => {
    if (!input.trim()) return ""
    try {
      JSON.parse(input)
      return ""
    } catch {
      return "Input must be valid JSON"
    }
  })()
  const targets = opts[kind].data ?? []

  return (
    <FormDialog
      open
      wide
      onOpenChange={(o) => !o && onClose()}
      title={existing ? `Edit schedule ${existing.Name}` : "Create schedule"}
      description="Run a target once (at), at a fixed rate, or on a cron expression."
      submitLabel={existing ? "Save schedule" : "Create schedule"}
      disabled={!name.trim() || !expr.trim() || !arn || !role || !!inputErr}
      onSubmit={async () => {
        const body: Partial<Schedule> = {
          ScheduleExpression: expr.trim(),
          ScheduleExpressionTimezone: tz || undefined,
          State: enabled ? "ENABLED" : "DISABLED",
          FlexibleTimeWindow: flex ? { Mode: "FLEXIBLE", MaximumWindowInMinutes: Number(window) } : { Mode: "OFF" },
          Target: { Arn: arn, RoleArn: role, Input: input.trim() || undefined },
          StartDate: fromLocal(start),
          EndDate: fromLocal(end),
          ActionAfterCompletion: del ? "DELETE" : "NONE",
        }
        await api.put(schedPath({ GroupName: group, Name: name.trim() }), body)
        toast.success(`${existing ? "Saved" : "Created"} schedule ${name.trim()}`)
        await revalidate(SCHED)
      }}
    >
      <div className="grid max-h-[65vh] gap-4 overflow-y-auto pr-1 sm:grid-cols-2">
        <Field label="Name" htmlFor="sc-name">
          <Input id="sc-name" value={name} onChange={(e) => setName(e.target.value)} disabled={!!existing} autoFocus={!existing} autoComplete="off" />
        </Field>
        <Field label="Schedule group" htmlFor="sc-group">
          <Select value={group} onValueChange={setGroup} disabled={!!existing}>
            <SelectTrigger id="sc-group" className="w-full">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {groups.map((g) => (
                <SelectItem key={g.Name} value={g.Name}>
                  {g.Name}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </Field>
        <Field label="Schedule expression" htmlFor="sc-expr" help="at(2026-12-31T23:59:00), rate(5 minutes) or cron(0 12 * * ? *)" className="sm:col-span-2">
          <Input id="sc-expr" value={expr} onChange={(e) => setExpr(e.target.value)} className="font-mono" spellCheck={false} />
        </Field>
        <Field label="Time zone" htmlFor="sc-tz">
          <Input id="sc-tz" value={tz} onChange={(e) => setTz(e.target.value)} list="sc-tzs" autoComplete="off" />
          <datalist id="sc-tzs">
            {tzList().map((z) => (
              <option key={z} value={z} />
            ))}
          </datalist>
        </Field>
        <Field label="Flexible time window" htmlFor="sc-flex" help="Invoke at a random moment within the window.">
          <div className="flex items-center gap-2">
            <Switch id="sc-flex" checked={flex} onCheckedChange={setFlex} />
            {flex && (
              <>
                <Input aria-label="Window minutes" type="number" min={1} max={1440} value={window} onChange={(e) => setWindow(e.target.value)} className="w-24" />
                <span className="text-muted-foreground text-sm">minutes</span>
              </>
            )}
          </div>
        </Field>
        <Field label="Target type" htmlFor="sc-kind">
          <Select
            value={kind}
            onValueChange={(v) => {
              setKind(v as TargetKind)
              setArn("")
            }}
          >
            <SelectTrigger id="sc-kind" className="w-full">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {TARGET_KINDS.map((t) => (
                <SelectItem key={t.kind} value={t.kind}>
                  {t.label}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </Field>
        <Field label="Target" htmlFor="sc-target">
          <Select value={arn} onValueChange={setArn}>
            <SelectTrigger id="sc-target" className="w-full">
              <SelectValue placeholder={opts[kind].data ? "Select a target" : "Loading..."} />
            </SelectTrigger>
            <SelectContent>
              {targets.map((t) => (
                <SelectItem key={t.arn} value={t.arn}>
                  {t.name}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </Field>
        <Field label="Execution role" htmlFor="sc-role" className="sm:col-span-2" help="The role Scheduler assumes to invoke the target.">
          <Select value={role} onValueChange={setRole}>
            <SelectTrigger id="sc-role" className="w-full">
              <SelectValue placeholder={roles.data ? "Select a role" : "Loading roles..."} />
            </SelectTrigger>
            <SelectContent>
              {(roles.data ?? []).map((r) => (
                <SelectItem key={r.arn} value={r.arn}>
                  {r.name}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </Field>
        <Field label="Input (JSON)" htmlFor="sc-input" optional error={inputErr} className="sm:col-span-2">
          <Textarea id="sc-input" value={input} onChange={(e) => setInput(e.target.value)} rows={3} className="font-mono text-xs" spellCheck={false} placeholder='{"key": "value"}' />
        </Field>
        <Field label="Start" htmlFor="sc-start" optional>
          <Input id="sc-start" type="datetime-local" value={start} onChange={(e) => setStart(e.target.value)} />
        </Field>
        <Field label="End" htmlFor="sc-end" optional>
          <Input id="sc-end" type="datetime-local" value={end} onChange={(e) => setEnd(e.target.value)} />
        </Field>
        <Field label="Delete after completion" htmlFor="sc-del" help="Remove the schedule when it has no invocations left.">
          <Switch id="sc-del" checked={del} onCheckedChange={setDel} />
        </Field>
        <Field label="Enabled" htmlFor="sc-enabled">
          <Switch id="sc-enabled" checked={enabled} onCheckedChange={setEnabled} />
        </Field>
      </div>
    </FormDialog>
  )
}

function GroupDialog({ onClose }: { onClose: () => void }) {
  const [name, setName] = useState("")
  return (
    <FormDialog
      open
      onOpenChange={(o) => !o && onClose()}
      title="Create schedule group"
      submitLabel="Create schedule group"
      disabled={!name.trim()}
      onSubmit={async () => {
        await api.post(`${SCHED}/schedule-groups`, { name: name.trim() })
        toast.success(`Created ${name.trim()}`)
        await revalidate(SCHED)
      }}
    >
      <Field label="Name" htmlFor="sg-name">
        <Input id="sg-name" value={name} onChange={(e) => setName(e.target.value)} autoFocus autoComplete="off" />
      </Field>
    </FormDialog>
  )
}

const columns: Column<Schedule>[] = [
  { id: "name", header: "Name", cell: (s) => <span className="font-medium">{s.Name}</span>, value: (s) => s.Name },
  { id: "group", header: "Group", cell: (s) => s.GroupName, value: (s) => s.GroupName, hideBelow: "sm" },
  { id: "state", header: "State", cell: (s) => <StatusBadge status={s.State} />, value: (s) => s.State },
  {
    id: "expr",
    header: "Schedule",
    cell: (s) => (
      <span className="font-mono text-[13px] whitespace-nowrap">
        {s.ScheduleExpression}
        {s.ScheduleExpressionTimezone && s.ScheduleExpressionTimezone !== "UTC" && <span className="text-muted-foreground"> ({s.ScheduleExpressionTimezone})</span>}
      </span>
    ),
    value: (s) => s.ScheduleExpression,
  },
  {
    id: "target",
    header: "Target",
    cell: (s) => (
      <span>
        {targetName(s.Target.Arn)} <span className="text-muted-foreground text-xs">{targetKindLabel(s.Target.Arn)}</span>
      </span>
    ),
    value: (s) => s.Target.Arn,
    hideBelow: "md",
  },
  { id: "flex", header: "Flexible window", cell: (s) => (s.FlexibleTimeWindow.Mode === "FLEXIBLE" ? `${s.FlexibleTimeWindow.MaximumWindowInMinutes} min` : "Off"), hideBelow: "lg" },
]

export function SchedulerPage() {
  const [group, setGroup] = useState("")
  const groups = useApi<ScheduleGroup[]>(`${SCHED}/schedule-groups`)
  const { data, error, isLoading, isValidating, mutate } = useApi<Schedule[]>(`${SCHED}/schedules`, { query: { group: group || undefined }, refreshInterval: 15_000 })
  const [selected, setSelected] = useState<string[]>([])
  const [dialog, setDialog] = useState<"create" | "edit" | "delete" | "group" | "delgroup" | null>(null)
  const key = (s: Schedule) => `${s.GroupName}/${s.Name}`
  const sel = data?.find((s) => key(s) === selected[0])
  const groupList = groups.data ?? []

  return (
    <div className="flex flex-col gap-4">
      <PageHeader
        title="Schedules"
        description="EventBridge Scheduler runs targets once, at a rate or on a cron expression, in any time zone."
        breadcrumbs={[{ label: "EventBridge", href: "/events/" }, { label: "Schedules" }]}
      />
      <DataTable
        title="Schedules"
        data={data}
        columns={columns}
        rowId={key}
        loading={isLoading}
        error={error}
        onRefresh={() => mutate()}
        refreshing={isValidating}
        selection="single"
        selected={selected}
        onSelectedChange={setSelected}
        searchPlaceholder="Find schedule"
        defaultSort={{ id: "name" }}
        filters={
          <Select value={group || "__all"} onValueChange={(v) => setGroup(v === "__all" ? "" : v)}>
            <SelectTrigger size="sm" className="w-full md:w-56" aria-label="Schedule group">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="__all">All schedule groups</SelectItem>
              {groupList.map((g) => (
                <SelectItem key={g.Name} value={g.Name}>
                  {g.Name}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        }
        actions={
          <>
            <ActionsMenu
              items={[
                { label: "Edit schedule", onSelect: () => setDialog("edit"), disabled: !sel },
                { label: "Delete schedule", destructive: true, onSelect: () => setDialog("delete"), disabled: !sel },
                { separator: true },
                { label: "Create schedule group", onSelect: () => setDialog("group") },
                { label: `Delete group ${group}`, destructive: true, onSelect: () => setDialog("delgroup"), disabled: !group || group === "default" },
              ]}
            />
            <Button size="sm" onClick={() => setDialog("create")}>
              <Plus /> Create schedule
            </Button>
          </>
        }
        empty={<EmptyState icon={CalendarClock} title="No schedules" description="Create a schedule to invoke a Lambda function, queue, topic or state machine." />}
      />
      {dialog === "create" && <ScheduleDialog groups={groupList} defaultGroup={group || "default"} onClose={() => setDialog(null)} />}
      {dialog === "edit" && sel && <ScheduleDialog existing={sel} groups={groupList} defaultGroup={sel.GroupName} onClose={() => setDialog(null)} />}
      {dialog === "group" && <GroupDialog onClose={() => setDialog(null)} />}
      {sel && (
        <ConfirmDialog
          open={dialog === "delete"}
          onOpenChange={(o) => !o && setDialog(null)}
          title={`Delete schedule ${sel.Name}?`}
          onConfirm={async () => {
            await api.del(schedPath(sel))
            toast.success(`Deleted ${sel.Name}`)
            setSelected([])
            await revalidate(SCHED)
          }}
        />
      )}
      {group && (
        <ConfirmDialog
          open={dialog === "delgroup"}
          onOpenChange={(o) => !o && setDialog(null)}
          title={`Delete schedule group ${group}?`}
          description="All schedules in the group are deleted."
          confirmText={group}
          onConfirm={async () => {
            await api.del(`${SCHED}/schedule-groups/${seg(group)}`)
            toast.success(`Deleted ${group}`)
            setGroup("")
            await revalidate(SCHED)
          }}
        />
      )}
    </div>
  )
}
