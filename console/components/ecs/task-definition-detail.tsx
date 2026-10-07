"use client"

import { useMemo, useState } from "react"
import Link from "next/link"
import { useRouter } from "next/navigation"
import { AlertCircle, ArrowLeft, Copy, KeyRound, Play, Plus, Trash2, Variable } from "lucide-react"

import { Button } from "@/components/ui/button"
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select"
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs"
import { ActionsMenu } from "@/components/console/actions-menu"
import { CopyButton, CopyableText } from "@/components/console/copy-button"
import { CellLink, CellText, DataTable } from "@/components/console/data-table"
import { EmptyState } from "@/components/console/empty-state"
import { ErrorState } from "@/components/console/error-state"
import { JsonEditor } from "@/components/console/json-editor"
import { KeyValueGrid } from "@/components/console/key-value"
import { DetailSkeleton } from "@/components/console/loading"
import { PageHeader } from "@/components/console/page-header"
import { Section } from "@/components/console/section"
import { StatusBadge } from "@/components/console/status-badge"
import { ApiError, seg } from "@/lib/api"
import { formatDate, formatMemoryMB } from "@/lib/format"
import { useApi, useQueryParam, useSetQueryParam } from "@/lib/hooks"
import type { EcsService, EcsTaskDefinition } from "@/lib/types"
import { SERVICES_PATH, TASK_DEFS_PATH, formatCpu, joinCommand, registerHref, secretHref, serviceHref, splitValueFrom, taskDefHref, tdKey } from "./common"
import { DeregisterDialog } from "./dialogs"
import { RunTaskDialog } from "./run-task-dialog"

const TABS = ["details", "json"] as const
type Tab = (typeof TABS)[number]

export function TaskDefinitionDetail() {
  const router = useRouter()
  const family = useQueryParam("family")
  const revision = useQueryParam("revision")
  const tabParam = useQueryParam("tab")
  const setParam = useSetQueryParam()
  const tab: Tab = (TABS as readonly string[]).includes(tabParam) ? (tabParam as Tab) : "details"

  const key = family ? (revision ? `${family}:${revision}` : family) : ""
  const { data: td, error, isLoading, mutate } = useApi<EcsTaskDefinition>(key ? `${TASK_DEFS_PATH}/${seg(key)}` : null)
  const revs = useApi<EcsTaskDefinition[]>(family ? TASK_DEFS_PATH : null, { query: { family, status: "all" } })
  const services = useApi<Pick<EcsService, "name" | "task_definition" | "status">[]>(SERVICES_PATH)
  const [deregistering, setDeregistering] = useState(false)
  const [running, setRunning] = useState(false)

  const crumbs = [
    { label: "ECS", href: "/ecs/" },
    { label: "Task definitions", href: "/ecs/task-definitions/" },
    { label: td ? tdKey(td) : key || "Task definition" },
  ]
  const usedBy = useMemo(() => (td ? (services.data ?? []).filter((s) => s.task_definition === tdKey(td)) : []), [services.data, td])

  if (!key) {
    return (
      <>
        <PageHeader title="Task definition" breadcrumbs={crumbs} />
        <EmptyState title="No task definition selected" description="Open a revision from the task definitions list." action={<BackButton />} />
      </>
    )
  }
  if (error && !td) {
    const notFound = error instanceof ApiError && error.status === 404
    return (
      <div className="flex flex-col gap-4">
        <PageHeader title={key} breadcrumbs={crumbs} />
        {notFound ? (
          <Section>
            <EmptyState icon={AlertCircle} title="Task definition not found" description={`Task definition ${key} does not exist.`} action={<BackButton />} />
          </Section>
        ) : (
          <ErrorState error={error} onRetry={() => mutate()} />
        )}
      </div>
    )
  }
  if (isLoading || !td) return <DetailSkeleton />

  const active = td.status === "ACTIVE"
  const env = Object.entries(td.environment ?? {}).sort((a, b) => a[0].localeCompare(b[0]))
  const secrets = td.secrets ?? []

  return (
    <div className="flex flex-col gap-4">
      <PageHeader
        title={tdKey(td)}
        badge={<StatusBadge status={td.status.toLowerCase()} />}
        description={<span className="font-mono text-[13px] break-all">{td.image}</span>}
        breadcrumbs={crumbs}
        actions={
          <>
            {(revs.data?.length ?? 0) > 1 && (
              <Select value={String(td.revision)} onValueChange={(r) => r && router.push(taskDefHref(td.family, r))}>
                <SelectTrigger size="sm" className="h-8 w-40" aria-label="Revision">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {revs.data!.map((r) => (
                    <SelectItem key={r.revision} value={String(r.revision)}>
                      Revision {r.revision}
                      {r.status !== "ACTIVE" && <span className="text-muted-foreground text-xs">(inactive)</span>}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            )}
            <Button variant="outline" size="sm" onClick={() => setRunning(true)} disabled={!active}>
              <Play /> Run task
            </Button>
            <ActionsMenu
              items={[
                { label: "Create service", icon: <Plus />, onSelect: () => router.push(`/ecs/create/?family=${encodeURIComponent(td.family)}`) },
                { separator: true },
                { label: "Deregister", icon: <Trash2 />, destructive: true, disabled: !active, onSelect: () => setDeregistering(true) },
              ]}
            />
            <Button size="sm" asChild>
              <Link href={registerHref(tdKey(td))}>
                <Copy /> Create new revision
              </Link>
            </Button>
          </>
        }
      />

      <Tabs value={tab} onValueChange={(v) => setParam("tab", v === "details" ? null : v)}>
        <TabsList>
          <TabsTrigger value="details">Details</TabsTrigger>
          <TabsTrigger value="json">JSON</TabsTrigger>
        </TabsList>
        <TabsContent value="details">
          <div className="flex flex-col gap-4">
            <Section title="Overview">
              <KeyValueGrid
                columns={3}
                items={[
                  { label: "Family", value: <span className="font-mono text-[13px]">{td.family}</span> },
                  { label: "Revision", value: String(td.revision) },
                  { label: "Status", value: <StatusBadge status={td.status.toLowerCase()} /> },
                  { label: "Registered", value: formatDate(td.created_at) },
                  {
                    label: "Used by services",
                    value: usedBy.length ? (
                      <span className="flex flex-wrap gap-x-3">
                        {usedBy.map((s) => (
                          <Link key={s.name} href={serviceHref(s.name)} className="text-primary hover:underline">
                            {s.name}
                          </Link>
                        ))}
                      </span>
                    ) : services.data ? (
                      "None"
                    ) : (
                      ""
                    ),
                  },
                  { label: "ARN", value: <CopyableText value={td.arn} />, wide: true },
                ]}
              />
            </Section>
            <Section title="Container">
              <KeyValueGrid
                columns={3}
                items={[
                  { label: "Image", value: <CopyableText value={td.image} />, wide: true },
                  { label: "CPU", value: formatCpu(td.cpu) },
                  { label: "Memory", value: formatMemoryMB(td.memory_mb) },
                  { label: "Container port", value: td.container_port ? `${td.container_port}/tcp` : "None" },
                  {
                    label: "Command",
                    value: td.command?.length ? <CopyableText value={joinCommand(td.command)} /> : "Image default",
                    wide: true,
                  },
                  {
                    label: "Entrypoint",
                    value: td.entrypoint?.length ? <CopyableText value={joinCommand(td.entrypoint)} /> : "Image default",
                    wide: true,
                  },
                ]}
              />
            </Section>
            <DataTable
              title="Environment variables"
              data={env}
              rowId={([k]) => k}
              noSearch={env.length < 10}
              searchPlaceholder="Filter variables"
              columns={[
                { id: "name", header: "Name", value: ([k]) => k, cell: ([k]) => <CellText mono>{k}</CellText>, className: "w-1/3" },
                { id: "value", header: "Value", value: ([, v]) => v, cell: ([, v]) => <span className="font-mono text-[13px] break-all">{v}</span> },
              ]}
              empty={<EmptyState icon={Variable} title="No environment variables" className="py-8" />}
            />
            <DataTable
              title="Secrets"
              description="Resolved from Secrets Manager when each task starts and injected as environment variables."
              data={secrets}
              rowId={(s) => s.name}
              noSearch
              columns={[
                { id: "name", header: "Environment variable", value: (s) => s.name, cell: (s) => <CellText mono>{s.name}</CellText>, className: "w-1/3" },
                {
                  id: "from",
                  header: "Value from",
                  value: (s) => s.value_from,
                  cell: (s) => {
                    const [name, jsonKey] = splitValueFrom(s.value_from)
                    return (
                      <span className="flex items-center whitespace-nowrap">
                        <CellLink href={secretHref(name)} mono>
                          {name}
                        </CellLink>
                        {jsonKey && <span className="text-muted-foreground font-mono text-[13px]">:{jsonKey}</span>}
                      </span>
                    )
                  },
                },
              ]}
              empty={<EmptyState icon={KeyRound} title="No secrets" className="py-8" />}
            />
          </div>
        </TabsContent>
        <TabsContent value="json">
          <Section title="Task definition JSON" actions={<CopyButton value={JSON.stringify(td, null, 2)} label="Copy JSON" size="sm" />}>
            <JsonEditor value={JSON.stringify(td, null, 2)} readOnly rows={24} />
          </Section>
        </TabsContent>
      </Tabs>

      <DeregisterDialog td={deregistering ? td : null} onClose={() => setDeregistering(false)} />
      <RunTaskDialog open={running} onClose={() => setRunning(false)} initial={tdKey(td)} />
    </div>
  )
}

function BackButton() {
  return (
    <Button variant="outline" size="sm" asChild>
      <Link href="/ecs/task-definitions/">
        <ArrowLeft /> Back to task definitions
      </Link>
    </Button>
  )
}
