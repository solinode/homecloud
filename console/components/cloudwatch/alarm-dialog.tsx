"use client"

import { useEffect, useMemo, useState } from "react"
import { Loader2, Plus, X } from "lucide-react"
import { toast } from "sonner"

import { Button } from "@/components/ui/button"
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog"
import { Input } from "@/components/ui/input"
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select"
import { Textarea } from "@/components/ui/textarea"
import { Field } from "@/components/console/form-field"
import { MetricChart } from "@/components/console/metric-chart"
import { TagsEditor, rowsToTags, tagsToRows, type TagRow } from "@/components/console/tags-editor"
import { api, errorMessage, seg } from "@/lib/api"
import { revalidate, useApi } from "@/lib/hooks"
import type { Alarm, ComparisonOperator, MetricSeries, Statistic, TreatMissingData } from "@/lib/types"
import { cn } from "@/lib/utils"

import { alarmCondition, dimsText, friendlyDims, OPERATORS, PERIODS, STATISTICS, TREAT_MISSING, useInstanceNames } from "./common"

const ACTION_RE = /^(https?:\/\/\S+|arn:\S+)$/

function actionError(v: string): string | null {
  if (!v.trim()) return "Enter a URL or ARN, or remove this row"
  return ACTION_RE.test(v.trim()) ? null : "Must be an http(s):// URL or an SNS topic ARN (arn:...)"
}

function ActionsEditor({ id, values, onChange, showErrors }: { id: string; values: string[]; onChange: (v: string[]) => void; showErrors: boolean }) {
  return (
    <div className="flex flex-col gap-2">
      {values.map((v, i) => {
        const err = showErrors ? actionError(v) : null
        return (
          <div key={i} className="flex flex-col gap-1">
            <div className="flex gap-2">
              <Input
                id={i === 0 ? id : undefined}
                className="h-8 font-mono text-[13px]"
                value={v}
                placeholder="https://hooks.example.com/alert or arn:aws:sns:us-east-1:...:topic"
                aria-invalid={!!err}
                onChange={(e) => onChange(values.map((x, j) => (j === i ? e.target.value : x)))}
              />
              <Button type="button" variant="ghost" size="icon" className="size-8 shrink-0" aria-label="Remove" onClick={() => onChange(values.filter((_, j) => j !== i))}>
                <X />
              </Button>
            </div>
            {err && <span className="text-destructive text-xs">{err}</span>}
          </div>
        )
      })}
      <Button type="button" variant="outline" size="sm" className="self-start" onClick={() => onChange([...values, ""])}>
        <Plus /> Add notification target
      </Button>
    </div>
  )
}

const selectKey = (s: { namespace: string; name: string; dimensions: Record<string, string> | null }) => `${s.namespace}|${s.name}|${dimsText(s.dimensions)}`

/**
 * AlarmDialog creates an alarm, or edits one (PUT with the same name replaces it).
 */
export function AlarmDialog({
  open,
  onOpenChange,
  alarm,
  existing,
}: {
  open: boolean
  onOpenChange: (o: boolean) => void
  alarm?: Alarm | null
  existing?: Alarm[]
}) {
  const metrics = useApi<MetricSeries[]>(open ? "/api/v1/cloudwatch/metrics" : null)
  const names = useInstanceNames()
  const editing = !!alarm

  const [mode, setMode] = useState<"pick" | "manual">("pick")
  const [namespace, setNamespace] = useState("")
  const [metric, setMetric] = useState("")
  const [dimKey, setDimKey] = useState("")
  const [dimRows, setDimRows] = useState<TagRow[]>([])
  const [statistic, setStatistic] = useState<Statistic>("Average")
  const [period, setPeriod] = useState(60)
  const [operator, setOperator] = useState<ComparisonOperator>("GreaterThanThreshold")
  const [threshold, setThreshold] = useState("80")
  const [evalPeriods, setEvalPeriods] = useState("3")
  const [dpToAlarm, setDpToAlarm] = useState("3")
  const [missing, setMissing] = useState<TreatMissingData>("missing")
  const [alarmActions, setAlarmActions] = useState<string[]>([])
  const [okActions, setOkActions] = useState<string[]>([])
  const [name, setName] = useState("")
  const [description, setDescription] = useState("")
  const [touched, setTouched] = useState(false)
  const [pending, setPending] = useState(false)
  const [initialized, setInitialized] = useState(false)

  useEffect(() => {
    if (!open) {
      setInitialized(false)
      return
    }
    setTouched(false)
    if (alarm) {
      setNamespace(alarm.namespace)
      setMetric(alarm.metric)
      setDimKey(dimsText(alarm.dimensions))
      setDimRows(tagsToRows(alarm.dimensions))
      setStatistic(alarm.statistic)
      setPeriod(alarm.period)
      setOperator(alarm.comparison_operator)
      setThreshold(String(alarm.threshold))
      setEvalPeriods(String(alarm.evaluation_periods))
      setDpToAlarm(String(alarm.datapoints_to_alarm || alarm.evaluation_periods))
      setMissing(alarm.treat_missing_data || "missing")
      setAlarmActions(alarm.alarm_actions ?? [])
      setOkActions(alarm.ok_actions ?? [])
      setName(alarm.name)
      setDescription(alarm.description ?? "")
    } else {
      setNamespace("")
      setMetric("")
      setDimKey("")
      setDimRows([{ key: "InstanceId", value: "" }])
      setStatistic("Average")
      setPeriod(60)
      setOperator("GreaterThanThreshold")
      setThreshold("80")
      setEvalPeriods("3")
      setDpToAlarm("3")
      setMissing("missing")
      setAlarmActions([])
      setOkActions([])
      setName("")
      setDescription("")
    }
    setMode("pick")
  }, [open, alarm])

  // Once metrics load: pick sensible defaults, or switch to manual entry when the alarm's metric has no data.
  useEffect(() => {
    if (!open || initialized || !metrics.data) return
    setInitialized(true)
    if (alarm) {
      const found = metrics.data.some((s) => selectKey(s) === selectKey({ namespace: alarm.namespace, name: alarm.metric, dimensions: alarm.dimensions }))
      if (!found) setMode("manual")
      return
    }
    if (metrics.data.length === 0) {
      setMode("manual")
      setNamespace("")
      return
    }
    const cpu = metrics.data.find((s) => s.namespace === "HC/EC2" && s.name === "CPUUtilization") ?? metrics.data[0]
    setNamespace(cpu.namespace)
    setMetric(cpu.name)
    setDimKey(dimsText(cpu.dimensions))
  }, [open, initialized, metrics.data, alarm])

  const namespaces = useMemo(() => [...new Set((metrics.data ?? []).map((s) => s.namespace))].sort(), [metrics.data])
  const metricNames = useMemo(
    () => [...new Set((metrics.data ?? []).filter((s) => s.namespace === namespace).map((s) => s.name))].sort(),
    [metrics.data, namespace],
  )
  const dimSets = useMemo(() => (metrics.data ?? []).filter((s) => s.namespace === namespace && s.name === metric), [metrics.data, namespace, metric])
  const picked = dimSets.find((s) => dimsText(s.dimensions) === dimKey)

  const dimensions: Record<string, string> | undefined =
    mode === "pick" ? (picked?.dimensions ?? undefined) : rowsToTags(dimRows.filter((r) => r.key.trim() && r.value.trim()))

  const thresholdNum = Number(threshold)
  const evalNum = Number(evalPeriods)
  const dpNum = Number(dpToAlarm)
  const errors = {
    namespace: !namespace.trim() ? "Namespace is required" : null,
    metric: !metric.trim() ? "Metric name is required" : null,
    dims: mode === "pick" && dimSets.length > 0 && !picked ? "Select a dimension set" : null,
    threshold: threshold.trim() === "" || !Number.isFinite(thresholdNum) ? "Enter a number" : null,
    eval: !Number.isInteger(evalNum) || evalNum < 1 || evalNum > 100 ? "Between 1 and 100" : null,
    dp: !Number.isInteger(dpNum) || dpNum < 1 ? "At least 1" : Number.isInteger(evalNum) && dpNum > evalNum ? "Cannot exceed the evaluation periods" : null,
    name: !name.trim() ? "Name is required" : name.includes("/") ? "Name cannot contain /" : name.length > 255 ? "At most 255 characters" : !editing && existing?.some((a) => a.name === name.trim()) ? "An alarm with this name already exists" : null,
    actions: [...alarmActions, ...okActions].some((a) => actionError(a)),
  }
  const valid = !errors.namespace && !errors.metric && !errors.dims && !errors.threshold && !errors.eval && !errors.dp && !errors.name && !errors.actions

  const previewRange = Math.min(1440, Math.max(60, (period * Math.max(1, evalNum || 1) * 4) / 60))

  const submit = async (e: React.FormEvent) => {
    e.preventDefault()
    setTouched(true)
    if (!valid) return
    setPending(true)
    const body = {
      // keep settings this form does not edit (set through the CloudWatch API)
      ...(alarm
        ? {
            unit: alarm.unit,
            extended_statistic: alarm.extended_statistic,
            metrics: alarm.metrics,
            actions_enabled: alarm.actions_enabled,
            insufficient_data_actions: alarm.insufficient_data_actions,
          }
        : {}),
      description: description.trim(),
      namespace: namespace.trim(),
      metric: metric.trim(),
      dimensions: dimensions ?? {},
      statistic,
      period,
      evaluation_periods: evalNum,
      datapoints_to_alarm: dpNum,
      treat_missing_data: missing,
      threshold: thresholdNum,
      comparison_operator: operator,
      alarm_actions: alarmActions.map((a) => a.trim()),
      ok_actions: okActions.map((a) => a.trim()),
    }
    try {
      await api.put<Alarm>(`/api/v1/cloudwatch/alarms/${seg(name.trim())}`, body)
      toast.success(editing ? `Alarm ${name} updated` : `Alarm ${name} created`)
      revalidate("/api/v1/cloudwatch/alarms")
      onOpenChange(false)
    } catch (err) {
      toast.error(errorMessage(err))
    } finally {
      setPending(false)
    }
  }

  const t = touched
  return (
    <Dialog open={open} onOpenChange={(o) => !pending && onOpenChange(o)}>
      <DialogContent className="max-h-[92vh] overflow-y-auto sm:max-w-5xl">
        <form onSubmit={submit} className="flex flex-col gap-5">
          <DialogHeader>
            <DialogTitle>{editing ? `Edit alarm ${alarm?.name}` : "Create alarm"}</DialogTitle>
            <DialogDescription>
              An alarm watches one metric and changes state when it crosses a threshold. Notifications go to webhooks or SNS topics on every state change.
            </DialogDescription>
          </DialogHeader>

          <div className="grid grid-cols-1 gap-6 lg:grid-cols-2">
            <div className="flex min-w-0 flex-col gap-4">
              <div className="flex items-center justify-between gap-2">
                <h3 className="text-sm font-semibold">Metric</h3>
                <div className="bg-muted inline-flex rounded-md p-0.5 text-xs">
                  {(["pick", "manual"] as const).map((m) => (
                    <button
                      key={m}
                      type="button"
                      onClick={() => setMode(m)}
                      className={cn("rounded px-2.5 py-1 font-medium", mode === m ? "bg-background shadow-xs" : "text-muted-foreground hover:text-foreground")}
                    >
                      {m === "pick" ? "Select metric" : "Enter manually"}
                    </button>
                  ))}
                </div>
              </div>

              {mode === "pick" ? (
                <>
                  {metrics.data && metrics.data.length === 0 && (
                    <p className="text-muted-foreground text-sm">No metrics have data yet. Use &quot;Enter manually&quot; to alarm on a metric that will be published later.</p>
                  )}
                  <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
                    <Field label="Namespace" htmlFor="alarm-ns" error={t ? errors.namespace : undefined}>
                      <Select
                        value={namespace}
                        onValueChange={(v) => {
                          setNamespace(v)
                          const first = (metrics.data ?? []).find((s) => s.namespace === v)
                          setMetric(first?.name ?? "")
                          setDimKey(first ? dimsText(first.dimensions) : "")
                        }}
                      >
                        <SelectTrigger id="alarm-ns" className="w-full">
                          <SelectValue placeholder={metrics.isLoading ? "Loading..." : "Select"} />
                        </SelectTrigger>
                        <SelectContent>
                          {namespaces.map((n) => (
                            <SelectItem key={n} value={n}>
                              {n}
                            </SelectItem>
                          ))}
                        </SelectContent>
                      </Select>
                    </Field>
                    <Field label="Metric name" htmlFor="alarm-metric" error={t ? errors.metric : undefined}>
                      <Select
                        value={metric}
                        onValueChange={(v) => {
                          setMetric(v)
                          const first = (metrics.data ?? []).find((s) => s.namespace === namespace && s.name === v)
                          setDimKey(first ? dimsText(first.dimensions) : "")
                        }}
                        disabled={!namespace}
                      >
                        <SelectTrigger id="alarm-metric" className="w-full">
                          <SelectValue placeholder="Select" />
                        </SelectTrigger>
                        <SelectContent>
                          {metricNames.map((n) => (
                            <SelectItem key={n} value={n}>
                              {n}
                            </SelectItem>
                          ))}
                        </SelectContent>
                      </Select>
                    </Field>
                  </div>
                  <Field label="Dimensions" htmlFor="alarm-dims" error={t ? errors.dims : undefined}>
                    <Select value={dimKey || "__none"} onValueChange={(v) => setDimKey(v === "__none" ? "" : v)} disabled={!metric}>
                      <SelectTrigger id="alarm-dims" className="w-full">
                        <SelectValue placeholder="Select" />
                      </SelectTrigger>
                      <SelectContent>
                        {dimSets.map((s) => {
                          const k = dimsText(s.dimensions)
                          return (
                            <SelectItem key={k || "__none"} value={k || "__none"}>
                              <span className="font-mono text-xs">{k || "(no dimensions)"}</span>
                              {friendlyDims(s.dimensions, names) !== Object.values(s.dimensions ?? {}).join(", ") && (
                                <span className="text-muted-foreground text-xs">{friendlyDims(s.dimensions, names)}</span>
                              )}
                            </SelectItem>
                          )
                        })}
                      </SelectContent>
                    </Select>
                  </Field>
                </>
              ) : (
                <>
                  <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
                    <Field label="Namespace" htmlFor="alarm-ns-m" error={t ? errors.namespace : undefined}>
                      <Input id="alarm-ns-m" value={namespace} onChange={(e) => setNamespace(e.target.value)} placeholder="HC/EC2 or MyApp" />
                    </Field>
                    <Field label="Metric name" htmlFor="alarm-metric-m" error={t ? errors.metric : undefined}>
                      <Input id="alarm-metric-m" value={metric} onChange={(e) => setMetric(e.target.value)} placeholder="CPUUtilization" />
                    </Field>
                  </div>
                  <Field label="Dimensions" optional help="Must match the dimensions the metric is published with exactly.">
                    <TagsEditor rows={dimRows} onChange={setDimRows} keyPlaceholder="InstanceId" valuePlaceholder="i-0123..." />
                  </Field>
                </>
              )}

              <div className="grid grid-cols-2 gap-3">
                <Field label="Statistic" htmlFor="alarm-stat">
                  <Select value={statistic} onValueChange={(v) => setStatistic(v as Statistic)}>
                    <SelectTrigger id="alarm-stat" className="w-full">
                      <SelectValue />
                    </SelectTrigger>
                    <SelectContent>
                      {STATISTICS.map((s) => (
                        <SelectItem key={s} value={s}>
                          {s}
                        </SelectItem>
                      ))}
                    </SelectContent>
                  </Select>
                </Field>
                <Field label="Period" htmlFor="alarm-period">
                  <Select value={String(period)} onValueChange={(v) => setPeriod(Number(v))}>
                    <SelectTrigger id="alarm-period" className="w-full">
                      <SelectValue />
                    </SelectTrigger>
                    <SelectContent>
                      {!PERIODS.some((p) => p.value === period) && <SelectItem value={String(period)}>{period} seconds</SelectItem>}
                      {PERIODS.map((p) => (
                        <SelectItem key={p.value} value={String(p.value)}>
                          {p.label}
                        </SelectItem>
                      ))}
                    </SelectContent>
                  </Select>
                </Field>
              </div>

              <h3 className="mt-1 text-sm font-semibold">Conditions</h3>
              <div className="grid grid-cols-1 gap-3 sm:grid-cols-[1fr_120px]">
                <Field label={`Whenever ${metric || "the metric"} is...`} htmlFor="alarm-op">
                  <Select value={operator} onValueChange={(v) => setOperator(v as ComparisonOperator)}>
                    <SelectTrigger id="alarm-op" className="w-full">
                      <SelectValue />
                    </SelectTrigger>
                    <SelectContent>
                      {OPERATORS.map((o) => (
                        <SelectItem key={o.value} value={o.value}>
                          <span className="w-6 font-mono">{o.symbol}</span> {o.label}
                        </SelectItem>
                      ))}
                    </SelectContent>
                  </Select>
                </Field>
                <Field label="Threshold" htmlFor="alarm-threshold" error={t ? errors.threshold : undefined}>
                  <Input id="alarm-threshold" inputMode="decimal" value={threshold} onChange={(e) => setThreshold(e.target.value)} aria-invalid={!!(t && errors.threshold)} />
                </Field>
              </div>
              <Field
                label="Datapoints to alarm"
                htmlFor="alarm-dp"
                error={t ? errors.dp || errors.eval : undefined}
                help="The alarm goes to ALARM when M of the last N evaluation periods breach the threshold."
              >
                <div className="flex items-center gap-2 text-sm">
                  <Input id="alarm-dp" inputMode="numeric" className="w-20" aria-label="Datapoints to alarm (M)" value={dpToAlarm} onChange={(e) => setDpToAlarm(e.target.value.replace(/[^\d]/g, ""))} aria-invalid={!!(t && errors.dp)} />
                  <span className="text-muted-foreground">out of</span>
                  <Input id="alarm-eval" inputMode="numeric" className="w-20" aria-label="Evaluation periods (N)" value={evalPeriods} onChange={(e) => setEvalPeriods(e.target.value.replace(/[^\d]/g, ""))} aria-invalid={!!(t && errors.eval)} />
                </div>
              </Field>
              <Field label="Missing data treatment" htmlFor="alarm-missing" help="How to evaluate periods with no datapoints.">
                <Select value={missing} onValueChange={(v) => setMissing(v as TreatMissingData)}>
                  <SelectTrigger id="alarm-missing" className="w-full">
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    {TREAT_MISSING.map((o) => (
                      <SelectItem key={o.value} value={o.value}>
                        {o.label}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              </Field>
            </div>

            <div className="flex min-w-0 flex-col gap-4">
              <div className="flex flex-col gap-2">
                <h3 className="text-sm font-semibold">Preview</h3>
                {namespace && metric ? (
                  <MetricChart
                    queries={[{ namespace: namespace.trim(), name: metric.trim(), dimensions, label: metric }]}
                    rangeMinutes={previewRange}
                    period={period}
                    stat={statistic}
                    threshold={Number.isFinite(thresholdNum) && threshold.trim() !== "" ? thresholdNum : undefined}
                    height={200}
                  />
                ) : (
                  <div className="text-muted-foreground flex h-[200px] items-center justify-center rounded-md border border-dashed text-sm">Select a metric to preview it</div>
                )}
                {!errors.threshold && !errors.eval && !errors.dp && metric && (
                  <p className="text-muted-foreground text-xs">
                    <span className="text-foreground font-medium">Condition:</span>{" "}
                    {alarmCondition({ metric, comparison_operator: operator, threshold: thresholdNum, evaluation_periods: evalNum, datapoints_to_alarm: dpNum, period, statistic })}
                    <span className="text-destructive"> (dashed line)</span>
                  </p>
                )}
              </div>

              <h3 className="text-sm font-semibold">Notifications</h3>
              <Field label="When in ALARM, notify" htmlFor="alarm-actions" optional>
                <ActionsEditor id="alarm-actions" values={alarmActions} onChange={setAlarmActions} showErrors={t} />
              </Field>
              <Field label="When back to OK, notify" htmlFor="ok-actions" optional>
                <ActionsEditor id="ok-actions" values={okActions} onChange={setOkActions} showErrors={t} />
              </Field>

              <h3 className="text-sm font-semibold">Name and description</h3>
              <Field label="Alarm name" htmlFor="alarm-name" error={t ? errors.name : undefined} help={editing ? "The name cannot be changed." : undefined}>
                <Input id="alarm-name" value={name} disabled={editing} onChange={(e) => setName(e.target.value)} placeholder="web-high-cpu" aria-invalid={!!(t && errors.name)} />
              </Field>
              <Field label="Description" htmlFor="alarm-desc" optional>
                <Textarea id="alarm-desc" rows={2} value={description} onChange={(e) => setDescription(e.target.value)} maxLength={1024} />
              </Field>
            </div>
          </div>

          <DialogFooter>
            <Button type="button" variant="outline" onClick={() => onOpenChange(false)} disabled={pending}>
              Cancel
            </Button>
            <Button type="submit" disabled={pending}>
              {pending && <Loader2 className="animate-spin" />}
              {editing ? "Save changes" : "Create alarm"}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}
