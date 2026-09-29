"use client"

import { useState } from "react"
import Link from "next/link"
import { AlertCircle, ArrowLeft, Download, ExternalLink, FlaskConical, Loader2, Trash2 } from "lucide-react"

import { Button } from "@/components/ui/button"
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs"
import { CopyButton, CopyableText } from "@/components/console/copy-button"
import { EmptyState } from "@/components/console/empty-state"
import { ErrorState } from "@/components/console/error-state"
import { KeyValueGrid } from "@/components/console/key-value"
import { DetailSkeleton } from "@/components/console/loading"
import { MetricsPanel } from "@/components/console/metrics-panel"
import { PageHeader } from "@/components/console/page-header"
import { Section } from "@/components/console/section"
import { TimeAgo } from "@/components/console/time-ago"
import { logGroupHref } from "@/components/cloudwatch/common"
import { ApiError } from "@/lib/api"
import { formatBytes, formatDate, formatMemoryMB } from "@/lib/format"
import { useApi, useQueryParam, useSetQueryParam } from "@/lib/hooks"
import type { FunctionDetail as FunctionDetailData, LambdaFunction } from "@/lib/types"

import { CodeTab, downloadZipUrl } from "./code-tab"
import { DeleteFunctionDialog, EnvironmentStateBadge, RuntimeBadge, fnPath, formatTimeout, useRuntimeLabels } from "./common"
import { ConfigurationTab, FunctionUrlTab } from "./config-tab"
import { LogsTab } from "./logs-tab"
import { TestTab } from "./test-tab"
import { TriggersTab } from "./triggers"

const TABS = ["code", "test", "configuration", "url", "triggers", "monitoring", "logs"] as const
type Tab = (typeof TABS)[number]

export function FunctionDetail() {
  const name = useQueryParam("name")
  const tabParam = useQueryParam("tab")
  const setParam = useSetQueryParam()
  const tab: Tab = (TABS as readonly string[]).includes(tabParam) ? (tabParam as Tab) : "code"
  const labels = useRuntimeLabels()

  const { data, error, isLoading, isValidating, mutate } = useApi<FunctionDetailData>(name ? fnPath(name) : null, { refreshInterval: 15_000 })
  const [deleting, setDeleting] = useState(false)

  const crumbs = [{ label: "Lambda", href: "/lambda/" }, { label: "Functions", href: "/lambda/" }, { label: name || "Function" }]

  if (!name) {
    return (
      <>
        <PageHeader title="Function" breadcrumbs={crumbs} />
        <EmptyState title="No function selected" description="Open a function from the functions list." action={<BackButton />} />
      </>
    )
  }
  if (error && !data) {
    const notFound = error instanceof ApiError && error.status === 404
    return (
      <div className="flex flex-col gap-4">
        <PageHeader title={name} breadcrumbs={crumbs} />
        {notFound ? (
          <Section>
            <EmptyState icon={AlertCircle} title="Function not found" description={`Function ${name} does not exist. It may have been deleted.`} action={<BackButton />} />
          </Section>
        ) : (
          <ErrorState error={error} onRetry={() => mutate()} />
        )}
      </div>
    )
  }
  if (isLoading || !data) return <DetailSkeleton />

  const fn = data.configuration

  return (
    <div className="flex flex-col gap-4">
      <PageHeader
        title={fn.name}
        badge={
          <>
            <RuntimeBadge runtime={fn.runtime} label={labels.get(fn.runtime)} />
            <EnvironmentStateBadge state={data.environment_state} />
          </>
        }
        description={fn.description || undefined}
        breadcrumbs={crumbs}
        actions={
          <>
            <Button variant="outline" size="sm" onClick={() => mutate()} disabled={isValidating} aria-label="Refresh">
              {isValidating ? <Loader2 className="animate-spin" /> : null}
              Refresh
            </Button>
            <Button variant="outline" size="sm" asChild>
              <a href={downloadZipUrl(fn.name)} download={`${fn.name}.zip`}>
                <Download /> Download .zip
              </a>
            </Button>
            <Button variant="outline" size="sm" className="text-destructive hover:text-destructive" onClick={() => setDeleting(true)}>
              <Trash2 /> Delete
            </Button>
            <Button size="sm" onClick={() => setParam("tab", "test")}>
              <FlaskConical /> Test
            </Button>
          </>
        }
      />

      <Overview fn={fn} />

      <Tabs value={tab} onValueChange={(v) => setParam("tab", v === "code" ? null : v)}>
        <TabsList>
          <TabsTrigger value="code">Code</TabsTrigger>
          <TabsTrigger value="test">Test</TabsTrigger>
          <TabsTrigger value="configuration">Configuration</TabsTrigger>
          <TabsTrigger value="url">Function URL</TabsTrigger>
          <TabsTrigger value="triggers">Triggers</TabsTrigger>
          <TabsTrigger value="monitoring">Monitoring</TabsTrigger>
          <TabsTrigger value="logs">Logs</TabsTrigger>
        </TabsList>
        {/* The code tab stays mounted so unsaved edits survive switching tabs. */}
        <TabsContent value="code" forceMount className="data-[state=inactive]:hidden">
          <CodeTab fn={fn} active={tab === "code"} />
        </TabsContent>
        <TabsContent value="test">
          <TestTab fn={fn} />
        </TabsContent>
        <TabsContent value="configuration">
          <ConfigurationTab fn={fn} />
        </TabsContent>
        <TabsContent value="url">
          <FunctionUrlTab fn={fn} />
        </TabsContent>
        <TabsContent value="triggers">
          <TriggersTab fn={fn} />
        </TabsContent>
        <TabsContent value="monitoring">
          <MetricsPanel
            namespace="HC/Lambda"
            dimensions={{ FunctionName: fn.name }}
            charts={[
              { title: "Invocations", description: "Count per period", metrics: [{ name: "Invocations" }], stat: "Sum" },
              { title: "Errors", description: "Count per period", metrics: [{ name: "Errors" }], stat: "Sum" },
              { title: "Duration (average)", description: "Milliseconds", metrics: [{ name: "Duration", label: "Average" }], stat: "Average" },
              { title: "Duration (maximum)", description: "Milliseconds", metrics: [{ name: "Duration", label: "Maximum" }], stat: "Maximum" },
            ]}
          />
        </TabsContent>
        <TabsContent value="logs">
          <LogsTab fn={fn} />
        </TabsContent>
      </Tabs>

      <DeleteFunctionDialog fn={deleting ? fn : null} onOpenChange={setDeleting} redirect />
    </div>
  )
}

function BackButton() {
  return (
    <Button variant="outline" size="sm" asChild>
      <Link href="/lambda/">
        <ArrowLeft /> Back to functions
      </Link>
    </Button>
  )
}

function Overview({ fn }: { fn: LambdaFunction }) {
  const url = fn.function_url?.enabled ? fn.function_url.url : ""
  return (
    <Section title="Function overview">
      <KeyValueGrid
        columns={3}
        items={[
          { label: "Handler", value: <span className="font-mono text-[13px] break-all">{fn.handler}</span> },
          { label: "Memory", value: formatMemoryMB(fn.memory_mb) },
          { label: "Timeout", value: formatTimeout(fn.timeout_seconds) },
          { label: "Last modified", value: <span>{formatDate(fn.last_modified)} (<TimeAgo value={fn.last_modified} />)</span> },
          { label: "Code size", value: formatBytes(fn.code_size) },
          { label: "Code SHA-256", value: <CopyableText value={fn.code_sha256} display={`${fn.code_sha256.slice(0, 16)}…`} /> },
          {
            label: "Log group",
            value: (
              <Link href={logGroupHref(fn.log_group)} className="text-primary font-mono text-[13px] break-all hover:underline">
                {fn.log_group}
              </Link>
            ),
          },
          {
            label: "Function URL",
            value: url ? (
              <span className="inline-flex max-w-full items-center gap-1">
                <a href={url} target="_blank" rel="noreferrer" className="text-primary inline-flex min-w-0 items-center gap-1 font-mono text-[13px] hover:underline">
                  <span className="truncate">{url}</span>
                  <ExternalLink className="size-3.5 shrink-0" />
                </a>
                <CopyButton value={url} label="Copy URL" />
              </span>
            ) : (
              <span className="text-muted-foreground">Disabled</span>
            ),
          },
          { label: "Function ARN", value: <CopyableText value={fn.arn} />, wide: true },
        ]}
      />
    </Section>
  )
}
