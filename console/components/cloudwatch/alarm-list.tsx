"use client"

import { useEffect, useMemo, useState } from "react"
import Link from "next/link"
import { Bell, Pencil, Plus, Trash2 } from "lucide-react"
import { toast } from "sonner"

import { Button } from "@/components/ui/button"
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select"
import { ActionsMenu } from "@/components/console/actions-menu"
import { ConfirmDialog } from "@/components/console/confirm-dialog"
import { CopyableText } from "@/components/console/copy-button"
import { DataTable, type Column } from "@/components/console/data-table"
import { EmptyState } from "@/components/console/empty-state"
import { KeyValueGrid } from "@/components/console/key-value"
import { PageHeader } from "@/components/console/page-header"
import { TimeAgo } from "@/components/console/time-ago"
import { api, seg } from "@/lib/api"
import { formatDate } from "@/lib/format"
import { revalidate, useApi, useQueryParam, useSetQueryParam } from "@/lib/hooks"
import type { Alarm, AlarmState } from "@/lib/types"

import { AlarmDialog } from "./alarm-dialog"
import { ALARM_STATE_LABEL, alarmCondition, AlarmStateBadge, dimsText, friendlyDims, periodLabel, useInstanceNames } from "./common"

const STATE_ORDER: Record<AlarmState, number> = { ALARM: 0, INSUFFICIENT_DATA: 1, OK: 2 }

function ActionList({ values }: { values: string[] }) {
  if (!values?.length) return <span className="text-muted-foreground">None</span>
  return (
    <ul className="flex flex-col gap-0.5">
      {values.map((v) => (
        <li key={v} className="font-mono text-[13px] break-all">
          {v}
        </li>
      ))}
    </ul>
  )
}

export function AlarmList() {
  const { data, error, isLoading, isValidating, mutate } = useApi<Alarm[]>("/api/v1/cloudwatch/alarms", { refreshInterval: 15_000 })
  const names = useInstanceNames()
  const stateParam = useQueryParam("state")
  const nameParam = useQueryParam("name")
  const createParam = useQueryParam("create")
  const setParam = useSetQueryParam()
  const [selected, setSelected] = useState<string[]>([])
  const [dialog, setDialog] = useState<{ open: boolean; alarm: Alarm | null }>({ open: false, alarm: null })
  const [deleteOpen, setDeleteOpen] = useState(false)

  useEffect(() => {
    if (createParam === "1") setDialog({ open: true, alarm: null })
  }, [createParam])
  useEffect(() => {
    if (nameParam) setSelected([nameParam])
  }, [nameParam])

  const rows = useMemo(() => (data ?? []).filter((a) => !stateParam || a.state === stateParam), [data, stateParam])
  const alarm = data?.find((a) => a.name === selected[0])

  const columns = useMemo<Column<Alarm>[]>(
    () => [
      { id: "name", header: "Name", value: (a) => a.name, cell: (a) => <span className="text-primary font-medium">{a.name}</span> },
      { id: "state", header: "State", value: (a) => STATE_ORDER[a.state], cell: (a) => <AlarmStateBadge state={a.state} /> },
      {
        id: "condition",
        header: "Conditions",
        value: (a) => alarmCondition(a),
        sortable: false,
        cell: (a) => <span className="block max-w-80 truncate" title={alarmCondition(a)}>{alarmCondition(a)}</span>,
        hideBelow: "md",
      },
      {
        id: "metric",
        header: "Metric",
        value: (a) => `${a.namespace} ${a.metric} ${dimsText(a.dimensions)}`,
        cell: (a) => (
          <div className="flex max-w-72 flex-col">
            <span className="truncate">
              <span className="text-muted-foreground">{a.namespace} /</span> {a.metric}
            </span>
            {a.dimensions && Object.keys(a.dimensions).length > 0 && (
              <span className="text-muted-foreground truncate font-mono text-xs" title={dimsText(a.dimensions)}>
                {friendlyDims(a.dimensions, names)}
              </span>
            )}
          </div>
        ),
        hideBelow: "lg",
      },
      {
        id: "actions",
        header: "Actions",
        value: (a) => (a.alarm_actions?.length ?? 0) + (a.ok_actions?.length ?? 0),
        cell: (a) => {
          const n = (a.alarm_actions?.length ?? 0) + (a.ok_actions?.length ?? 0)
          return n ? <span className="tabular-nums">{n}</span> : <span className="text-muted-foreground">No actions</span>
        },
        hideBelow: "lg",
      },
      { id: "updated", header: "State updated", value: (a) => a.state_updated_at, cell: (a) => <TimeAgo value={a.state_updated_at} />, hideBelow: "sm" },
    ],
    [names],
  )

  const onDelete = async () => {
    if (!alarm) return
    await api.del(`/api/v1/cloudwatch/alarms/${seg(alarm.name)}`)
    toast.success(`Alarm ${alarm.name} deleted`)
    setSelected([])
    revalidate("/api/v1/cloudwatch/alarms")
  }

  const openCreate = () => setDialog({ open: true, alarm: null })

  return (
    <div className="flex flex-col gap-4">
      <PageHeader
        breadcrumbs={[{ label: "CloudWatch", href: "/cloudwatch/" }, { label: "Alarms" }]}
        title="Alarms"
        description="Alarms are evaluated every 30 seconds. On every state change, HomeCloud notifies the alarm's webhooks or SNS topics."
      />
      <DataTable
        title="Alarms"
        data={rows}
        count={rows.length}
        columns={columns}
        rowId={(a) => a.name}
        loading={isLoading}
        error={error}
        onRefresh={() => mutate()}
        refreshing={isValidating}
        selection="single"
        selected={selected}
        onSelectedChange={setSelected}
        searchPlaceholder="Search alarms"
        defaultSort={{ id: "state" }}
        filters={
          <Select value={stateParam || "all"} onValueChange={(v) => setParam("state", v === "all" ? null : v)}>
            <SelectTrigger size="sm" className="w-full md:w-48" aria-label="State filter">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="all">Any state</SelectItem>
              {(Object.keys(ALARM_STATE_LABEL) as AlarmState[]).map((s) => (
                <SelectItem key={s} value={s}>
                  {ALARM_STATE_LABEL[s]}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        }
        expanded={(a) =>
          a.name === selected[0] ? (
            <KeyValueGrid
              columns={3}
              items={[
                { label: "State reason", value: a.state_reason, wide: true },
                { label: "Description", value: a.description, wide: true },
                { label: "Condition", value: alarmCondition(a), wide: true },
                {
                  label: "Metric",
                  value: (
                    <Link
                      href={`/cloudwatch/metrics/?namespace=${encodeURIComponent(a.namespace)}`}
                      className="text-primary hover:underline"
                    >
                      {a.namespace} / {a.metric}
                    </Link>
                  ),
                },
                { label: "Dimensions", value: <span className="font-mono text-[13px]">{dimsText(a.dimensions)}</span> },
                { label: "Statistic / period", value: `${a.statistic}, ${periodLabel(a.period)}` },
                { label: "Alarm actions", value: <ActionList values={a.alarm_actions} /> },
                { label: "OK actions", value: <ActionList values={a.ok_actions} /> },
                { label: "Created", value: formatDate(a.created_at) },
                { label: "ARN", value: <CopyableText value={a.arn} />, wide: true },
              ]}
            />
          ) : null
        }
        actions={
          <>
            <ActionsMenu
              disabled={!alarm}
              items={[
                { label: "Edit", icon: <Pencil />, onSelect: () => alarm && setDialog({ open: true, alarm }) },
                { separator: true },
                { label: "Delete", icon: <Trash2 />, destructive: true, onSelect: () => setDeleteOpen(true) },
              ]}
            />
            <Button size="sm" onClick={openCreate}>
              <Plus /> Create alarm
            </Button>
          </>
        }
        empty={
          <EmptyState
            icon={Bell}
            title={stateParam && data?.length ? `No alarms in state ${ALARM_STATE_LABEL[stateParam as AlarmState] ?? stateParam}` : "No alarms"}
            description="Create an alarm to get notified when a metric crosses a threshold, e.g. CPU above 80% for 3 minutes."
            action={
              <Button size="sm" onClick={openCreate}>
                <Plus /> Create alarm
              </Button>
            }
          />
        }
      />
      <AlarmDialog
        open={dialog.open}
        alarm={dialog.alarm}
        existing={data}
        onOpenChange={(o) => {
          setDialog((d) => ({ ...d, open: o }))
          if (!o && createParam) setParam("create", null)
        }}
      />
      {alarm && (
        <ConfirmDialog
          open={deleteOpen}
          onOpenChange={setDeleteOpen}
          title={`Delete alarm ${alarm.name}?`}
          description="The alarm stops being evaluated and no further notifications are sent. The metric itself is not affected."
          actionLabel="Delete alarm"
          onConfirm={onDelete}
        />
      )}
    </div>
  )
}
