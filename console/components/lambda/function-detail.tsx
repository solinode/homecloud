"use client"

import { useState } from "react"
import Link from "next/link"
import { AlertCircle, ArrowLeft, Copy, Download, ExternalLink, FlaskConical, Loader2, Trash2 } from "lucide-react"
import { toast } from "sonner"

import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert"
import { Button } from "@/components/ui/button"
import { roleHref } from "@/components/iam/role-common"
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs"
import { ActionsMenu } from "@/components/console/actions-menu"
import { CopyButton, CopyableText, copyText } from "@/components/console/copy-button"
import { EmptyState } from "@/components/console/empty-state"
import { ErrorState } from "@/components/console/error-state"
import { KeyValueGrid } from "@/components/console/key-value"
import { DetailSkeleton } from "@/components/console/loading"
import { MetricsPanel } from "@/components/console/metrics-panel"
import { PageHeader } from "@/components/console/page-header"
import { Section } from "@/components/console/section"
import { StatTile } from "@/components/console/stat-tile"
import { TimeAgo } from "@/components/console/time-ago"
import { logGroupHref } from "@/components/cloudwatch/common"
import { ApiError } from "@/lib/api"
import { formatBytes, formatDate, formatMemoryMB } from "@/lib/format"
import { useApi, useQueryParam, useSetQueryParam } from "@/lib/hooks"
import type { FunctionDetail as FunctionDetailData, LambdaFunction } from "@/lib/types"

import { CodeTab, downloadZipUrl } from "./code-tab"
import { DeleteFunctionDialog, EnvironmentStateBadge, FunctionStateBadge, RuntimeBadge, fnPath, formatTimeout, layerHref, splitLayerArn, useRuntimeLabels } from "./common"
import { ConfigurationTab, FunctionUrlTab } from "./config-tab"
import { ImageCodeTab } from "./image-picker"
import { AliasesTab, VersionsTab } from "./versions"
import { LogsTab } from "./logs-tab"
import { TestTab } from "./test-tab"
import { TriggersTab } from "./triggers"

const TABS = ["code", "test", "configuration", "versions", "aliases", "url", "triggers", "monitoring", "logs"] as const
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
  const isImage = fn.package_type === "Image"

  return (
    <div className="flex flex-col gap-4">
      <PageHeader
        title={fn.name}
        badge={
          <>
            {isImage ? <RuntimeBadge runtime="Container image" /> : <RuntimeBadge runtime={fn.runtime} label={labels.get(fn.runtime)} />}
            {fn.state && fn.state !== "Active" && <FunctionStateBadge fn={fn} />}
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
            {!isImage && (
              <Button variant="outline" size="sm" asChild>
                <a href={downloadZipUrl(fn.name)} download={`${fn.name}.zip`}>
                  <Download /> Download .zip
                </a>
              </Button>
            )}
            <ActionsMenu
              items={[
                {
                  label: "Copy ARN",
                  icon: <Copy />,
                  onSelect: async () => {
                    if (await copyText(fn.arn)) toast.success("ARN copied")
                  },
                },
                { label: "View versions", onSelect: () => setParam("tab", "versions") },
                { label: "Function URL", onSelect: () => setParam("tab", "url") },
                { separator: true },
                { label: "Delete function", icon: <Trash2 />, destructive: true, onSelect: () => setDeleting(true) },
              ]}
            />
            <Button size="sm" onClick={() => setParam("tab", "test")}>
              <FlaskConical /> Test
            </Button>
          </>
        }
      />

      {(fn.state === "Failed" || fn.last_update_status === "Failed") && (
        <Alert variant="destructive">
          <AlertCircle />
          <AlertTitle>{fn.state === "Failed" ? "The function failed to become active" : "The last update failed"}</AlertTitle>
          <AlertDescription>
            {fn.state === "Failed" ? fn.state_reason : fn.last_update_status_reason}
            {fn.state === "Failed" && fn.state_reason_code && <span className="font-mono"> ({fn.state_reason_code})</span>}
          </AlertDescription>
        </Alert>
      )}
      {(fn.state === "Pending" || fn.last_update_status === "InProgress") && (
        <Alert>
          <Loader2 className="animate-spin" />
          <AlertTitle>{fn.state === "Pending" ? "Creating the function" : "Updating the function"}</AlertTitle>
          <AlertDescription>{fn.state_reason || fn.last_update_status_reason || "Pulling the image. Invocations wait until it is ready."}</AlertDescription>
        </Alert>
      )}

      <Overview fn={fn} detail={data} />

      <Tabs value={tab} onValueChange={(v) => setParam("tab", v === "code" ? null : v)}>
        <TabsList>
          <TabsTrigger value="code">Code</TabsTrigger>
          <TabsTrigger value="test">Test</TabsTrigger>
          <TabsTrigger value="configuration">Configuration</TabsTrigger>
          <TabsTrigger value="versions">Versions</TabsTrigger>
          <TabsTrigger value="aliases">Aliases</TabsTrigger>
          <TabsTrigger value="url">Function URL</TabsTrigger>
          <TabsTrigger value="triggers">Triggers</TabsTrigger>
          <TabsTrigger value="monitoring">Monitoring</TabsTrigger>
          <TabsTrigger value="logs">Logs</TabsTrigger>
        </TabsList>
        {/* The code tab stays mounted so unsaved edits survive switching tabs. */}
        <TabsContent value="code" forceMount className="data-[state=inactive]:hidden">
          {isImage ? <ImageCodeTab fn={fn} /> : <CodeTab fn={fn} active={tab === "code"} />}
        </TabsContent>
        <TabsContent value="test">
          <TestTab fn={fn} />
        </TabsContent>
        <TabsContent value="configuration">
          <ConfigurationTab fn={fn} detail={data} />
        </TabsContent>
        <TabsContent value="versions">
          <VersionsTab fn={fn} />
        </TabsContent>
        <TabsContent value="aliases">
          <AliasesTab fn={fn} />
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

function Overview({ fn, detail }: { fn: LambdaFunction; detail: FunctionDetailData }) {
  const url = fn.function_url?.enabled ? fn.function_url.url : ""
  const roleName = fn.role?.split("/").pop()
  const layers = fn.layers ?? []
  return (
    <div className="flex flex-col gap-4">
      <div className="grid grid-cols-1 gap-4 sm:grid-cols-2 lg:grid-cols-4">
      <StatTile label="Memory" value={formatMemoryMB(fn.memory_mb)} />
      <StatTile label="Timeout" value={formatTimeout(fn.timeout_seconds)} />
      <StatTile
        label="Concurrency"
        tone={(detail.concurrent_executions ?? 0) > 0 ? "success" : undefined}
        value={detail.concurrent_executions ?? 0}
        unit={`/ ${detail.concurrency_limit ?? "-"}`}
        caption={fn.reserved_concurrency != null ? "Running / reserved limit" : "Running / limit"}
      />
      <StatTile label="Code size" value={formatBytes(fn.code_size)} />
    </div>
    <Section title="Function overview">
      <KeyValueGrid
        columns={3}
        items={[
          fn.package_type === "Image"
            ? { label: "Image URI", value: <span className="font-mono text-[13px] break-all">{fn.image_uri}</span> }
            : { label: "Handler", value: <span className="font-mono text-[13px] break-all">{fn.handler}</span> },
          { label: "State", value: <FunctionStateBadge fn={fn} /> },
          { label: "Architecture", value: <span className="font-mono text-[13px]">{(fn.architectures ?? []).join(", ") || "x86_64"}</span> },
          {
            label: "Execution role",
            value: roleName ? (
              <Link href={roleHref(roleName)} className="text-primary hover:underline">
                {roleName}
              </Link>
            ) : (
              <span className="text-muted-foreground">None</span>
            ),
          },
          {
            label: "Layers",
            value: layers.length ? (
              <span className="flex flex-wrap gap-x-2">
                {layers.map((l) => {
                  const p = splitLayerArn(l)
                  return (
                    <Link key={l} href={layerHref(p.name)} className="text-primary hover:underline">
                      {p.name}:{p.version}
                    </Link>
                  )
                })}
              </span>
            ) : (
              <span className="text-muted-foreground">None</span>
            ),
          },
          { label: "Last modified", value: <span>{formatDate(fn.last_modified)} (<TimeAgo value={fn.last_modified} />)</span> },
          { label: "Code SHA-256", value: <CopyableText value={fn.code_sha256} display={`${fn.code_sha256.slice(0, 16)}…`} /> },
          { label: "Latest version", value: fn.last_version ? String(fn.last_version) : <span className="text-muted-foreground">Not published</span> },
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
    </div>
  )
}
