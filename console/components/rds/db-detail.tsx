"use client"

import { useState } from "react"
import Link from "next/link"
import { useRouter } from "next/navigation"
import { AlertCircle, ArrowLeft, Eye, EyeOff, Info, KeyRound, Loader2, Play, Settings2, ShieldCheck } from "lucide-react"

import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert"
import { Button } from "@/components/ui/button"
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs"
import { ActionsMenu, type ActionItem } from "@/components/console/actions-menu"
import { CodeBlock } from "@/components/console/code-block"
import { CopyButton, CopyableText } from "@/components/console/copy-button"
import { CellLink, cellLinkClass } from "@/components/console/data-table"
import { EmptyState } from "@/components/console/empty-state"
import { ErrorState } from "@/components/console/error-state"
import { KeyValueGrid } from "@/components/console/key-value"
import { DetailSkeleton } from "@/components/console/loading"
import { CONTAINER_CHARTS, MetricsPanel } from "@/components/console/metrics-panel"
import { PageHeader } from "@/components/console/page-header"
import { Section } from "@/components/console/section"
import { StatTile } from "@/components/console/stat-tile"
import { StatusBadge, statusLabel } from "@/components/console/status-badge"
import { TagList } from "@/components/console/tags-editor"
import { TimeAgo } from "@/components/console/time-ago"
import { ApiError, api, errorMessage, seg } from "@/lib/api"
import { formatDate, formatMemoryMB } from "@/lib/format"
import { useApi, useQueryParam, useSetQueryParam } from "@/lib/hooks"
import type { DbCredentials, DbInstance, SecretValue } from "@/lib/types"
import { cn } from "@/lib/utils"
import { CommandConsole } from "./command-console"
import { useDbActions } from "./db-actions"
import { DbConfiguration, ResetPasswordDialog } from "./db-config"
import { DbLogs } from "./db-logs"
import { QueryEditor } from "./query-editor"
import {
  DB_INSTANCES_PATH,
  EngineCell,
  EngineLogo,
  FAMILIES,
  endpointText,
  familyOfKind,
  formatVcpu,
  isRedisLike,
  isTransitional,
  pollInterval,
  publicText,
  queryTab,
  snapTab,
  supportsPasswordReset,
  supportsQuery,
  supportsSnapshots,
  useEngines,
  type Family,
  type FamilyConfig,
} from "./shared"
import { InstanceSnapshots } from "./snapshots"

export function DbDetail({ family }: { family: Family }) {
  const page = FAMILIES[family]
  const router = useRouter()
  const id = useQueryParam("id")
  const tabParam = useQueryParam("tab")
  const setParam = useSetQueryParam()
  const engines = useEngines()

  const { data: inst, error, isLoading, isValidating, mutate } = useApi<DbInstance>(id ? `${DB_INSTANCES_PATH}/${seg(id)}` : null, {
    refreshInterval: (d) => pollInterval(d ? [d.status] : []),
  })
  // Wording follows the resource itself (a cache opened under /rds/ still reads "cluster").
  const cfg = inst ? FAMILIES[familyOfKind(inst.kind)] : page
  const actions = useDbActions(cfg, { onDeleted: () => router.push(page.base) })
  const [resetting, setResetting] = useState(false)

  const qTab = queryTab(inst?.kind ?? (family === "elasticache" ? "cache" : ""))
  const sTab = snapTab(inst?.kind ?? (family === "elasticache" ? "cache" : ""))
  const tabs = ["connectivity", "monitoring", qTab, "logs", sTab, "configuration"]
  const tab = tabs.includes(tabParam) ? tabParam : "connectivity"

  const crumbs = [{ label: page.service, href: page.base }, { label: page.Nouns, href: page.base }, { label: id || page.Noun }]
  const back = (
    <Button variant="outline" size="sm" asChild>
      <Link href={page.base}>
        <ArrowLeft /> Back to {page.nouns}
      </Link>
    </Button>
  )

  if (!id) {
    return (
      <>
        <PageHeader title={page.Noun} breadcrumbs={crumbs} />
        <EmptyState title={`No ${page.noun} selected`} description={`Open a ${page.noun} from the ${page.nouns} list.`} action={back} />
      </>
    )
  }
  if (error && !inst) {
    const notFound = error instanceof ApiError && error.status === 404
    return (
      <div className="flex flex-col gap-4">
        <PageHeader title={id} breadcrumbs={crumbs} />
        {notFound ? (
          <Section>
            <EmptyState icon={AlertCircle} title={`${page.Noun} not found`} description={`${page.Noun} ${id} does not exist or has been deleted.`} action={back} />
          </Section>
        ) : (
          <ErrorState error={error} onRetry={() => mutate()} />
        )}
      </div>
    )
  }
  if (isLoading || !inst) return <DetailSkeleton />

  const s = inst.status
  const stateItems: ActionItem[] = [
    { label: "Start", onSelect: () => actions.start([inst.id]), disabled: actions.busy || s !== "stopped" },
    { label: "Stop", onSelect: () => actions.stop([inst.id]), disabled: actions.busy || s !== "available" },
    { label: "Reboot", onSelect: () => actions.reboot([inst.id]), disabled: actions.busy || s !== "available" },
    { separator: true },
    { label: "Modify", onSelect: () => setParam("tab", "configuration") },
    ...(supportsSnapshots(inst.engine) ? [{ label: `Take ${cfg.snap}`, onSelect: () => setParam("tab", sTab), disabled: s !== "available" }] : []),
    ...(supportsPasswordReset(inst.engine) && inst.secret_name
      ? [{ label: "Reset master password", onSelect: () => setResetting(true), disabled: s !== "available" }]
      : []),
    { separator: true },
    { label: "Delete", destructive: true, onSelect: () => actions.remove([inst]), disabled: actions.busy || s === "deleting" },
  ]

  return (
    <div className="flex flex-col gap-4">
      <PageHeader
        title={
          <span className="flex items-center gap-2.5">
            <EngineLogo engine={inst.engine} />
            {inst.id}
          </span>
        }
        badge={<StatusBadge status={s} />}
        description={
          <span className="flex flex-wrap items-center gap-x-2">
            <EngineCell engine={inst.engine} version={inst.engine_version} engines={engines.data?.engines} />
            <span>·</span>
            <span className="font-mono text-[13px]">{inst.class}</span>
            <span>·</span>
            <span>{inst.availability_zone}</span>
          </span>
        }
        breadcrumbs={crumbs}
        actions={
          <>
            <Button variant="outline" size="sm" onClick={() => mutate()} disabled={isValidating} aria-label="Refresh">
              {isValidating ? <Loader2 className="animate-spin" /> : null}
              Refresh
            </Button>
            <ActionsMenu items={stateItems} />
            {s === "stopped" ? (
              <Button size="sm" onClick={() => actions.start([inst.id])} disabled={actions.busy}>
                <Play /> Start
              </Button>
            ) : (
              <Button size="sm" onClick={() => setParam("tab", "configuration")} disabled={s === "deleting"}>
                <Settings2 /> Modify
              </Button>
            )}
          </>
        }
      />

      {s === "failed" ? (
        <Alert variant="destructive">
          <AlertCircle />
          <AlertTitle>The {cfg.noun} failed</AlertTitle>
          <AlertDescription>
            <span className="font-mono text-[13px] break-words">{inst.status_reason || "No reason was recorded."}</span>
            <span>Check the Logs tab for details. You can delete the {cfg.noun} and create it again, or restore it from a {cfg.snap}.</span>
          </AlertDescription>
        </Alert>
      ) : inst.status_reason ? (
        <Alert variant="info">
          <Info />
          <AlertTitle>Status reason</AlertTitle>
          <AlertDescription>
            <span className="font-mono text-[13px]">{inst.status_reason}</span>
          </AlertDescription>
        </Alert>
      ) : null}
      {isTransitional(s) && (
        <Alert variant="info">
          <Loader2 className="animate-spin" />
          <AlertDescription>
            The {cfg.noun} is {statusLabel(s).toLowerCase()}. This page refreshes automatically.
          </AlertDescription>
        </Alert>
      )}

      <div className="grid grid-cols-2 gap-3 lg:grid-cols-4">
        <StatTile label="vCPUs" value={inst.vcpus} caption={inst.class} />
        <StatTile
          label="Memory"
          value={formatMemoryMB(inst.memory_mb).split(" ")[0]}
          unit={formatMemoryMB(inst.memory_mb).split(" ")[1]}
          caption={cfg.family === "rds" ? "Instance memory" : "Node memory"}
        />
        {inst.engine !== "memcached" ? (
          <StatTile label="Storage" value={inst.storage_gb} unit="GiB" caption="Advisory size" />
        ) : (
          <StatTile label="Port" value={inst.endpoint.port || "-"} caption="Memcached text protocol" />
        )}
        {supportsSnapshots(inst.engine) ? (
          <StatTile
            label="Backup retention"
            value={inst.backup_retention_days}
            unit={inst.backup_retention_days === 1 ? "day" : "days"}
            tone={inst.backup_retention_days > 0 ? "success" : "neutral"}
            caption={inst.latest_backup ? <>Latest <TimeAgo value={inst.latest_backup} /></> : "No automated backup yet"}
          />
        ) : (
          <StatTile label="Backups" value="-" caption="Not supported by Memcached" />
        )}
      </div>

      <Tabs value={tab} onValueChange={(v) => setParam("tab", v === "connectivity" ? null : v)}>
        <TabsList>
          <TabsTrigger value="connectivity">Connectivity &amp; security</TabsTrigger>
          <TabsTrigger value="monitoring">Monitoring</TabsTrigger>
          <TabsTrigger value={qTab}>{inst.engine === "mongodb" ? "Shell" : cfg.family === "rds" ? "Query editor" : "Command console"}</TabsTrigger>
          <TabsTrigger value="logs">Logs</TabsTrigger>
          <TabsTrigger value={sTab}>{cfg.Snaps}</TabsTrigger>
          <TabsTrigger value="configuration">Configuration</TabsTrigger>
        </TabsList>
        <TabsContent value="connectivity">
          <ConnectivityTab cfg={cfg} inst={inst} />
        </TabsContent>
        <TabsContent value="monitoring">
          <MetricsPanel
            namespace={cfg.namespace}
            dimensions={{ [cfg.dimension]: inst.id }}
            charts={CONTAINER_CHARTS}
            note={
              s !== "available" ? (
                <p className="text-muted-foreground text-sm">Metrics are collected every minute while the {cfg.noun} is running.</p>
              ) : undefined
            }
          />
        </TabsContent>
        <TabsContent value={qTab}>
          {!supportsQuery(inst.engine) ? (
            <Section>
              <EmptyState
                icon={Info}
                title="Memcached has no command console"
                description={
                  <>
                    Memcached speaks a simple text protocol with no query language. Connect from inside the VPC (or from this host when public) with a
                    memcached client, or with <span className="font-mono">telnet {inst.endpoint.address} {inst.endpoint.port}</span>.
                  </>
                }
              />
            </Section>
          ) : isRedisLike(inst.engine) ? (
            <CommandConsole inst={inst} />
          ) : (
            <QueryEditor inst={inst} />
          )}
        </TabsContent>
        <TabsContent value="logs">
          <DbLogs inst={inst} noun={cfg.noun} />
        </TabsContent>
        <TabsContent value={sTab}>
          {supportsSnapshots(inst.engine) ? (
            <InstanceSnapshots cfg={cfg} inst={inst} />
          ) : (
            <Section>
              <EmptyState
                icon={Info}
                title={`Memcached does not support ${cfg.snaps}`}
                description="Memcached keeps data only in memory, with no persistence, so there is nothing to back up. Use Redis or Valkey when you need backups."
              />
            </Section>
          )}
        </TabsContent>
        <TabsContent value="configuration">
          <DbConfiguration cfg={cfg} inst={inst} onResetPassword={() => setResetting(true)} />
        </TabsContent>
      </Tabs>

      {actions.dialogs}
      <ResetPasswordDialog inst={resetting ? inst : null} onClose={() => setResetting(false)} />
    </div>
  )
}

function ConnectivityTab({ cfg, inst }: { cfg: FamilyConfig; inst: DbInstance }) {
  const pub = publicText(inst)
  return (
    <div className="flex flex-col gap-4">
      <Section title="Connectivity">
        <div className="flex flex-col gap-5">
          <KeyValueGrid
            columns={3}
            items={[
              { label: "Endpoint", value: <CopyableText value={inst.endpoint.address} /> },
              { label: "Port", value: String(inst.endpoint.port) },
              { label: "Private IP address", value: <CopyableText value={inst.endpoint.private_ip} /> },
              {
                label: "Public endpoint",
                value: pub ? (
                  <CopyableText value={pub} />
                ) : inst.publicly_accessible ? (
                  <span className="text-muted-foreground">Assigned when the {cfg.noun} is available</span>
                ) : (
                  <span className="text-muted-foreground">Not publicly accessible</span>
                ),
              },
              {
                label: "VPC",
                value: (
                  <CellLink href="/vpc/" mono className="w-fit">
                    {inst.vpc_id}
                  </CellLink>
                ),
              },
              {
                label: "Subnet",
                value: (
                  <CellLink href="/vpc/subnets/" mono className="w-fit">
                    {inst.subnet_id}
                  </CellLink>
                ),
              },
              { label: "Availability zone", value: inst.availability_zone },
            ]}
          />
          {inst.endpoint.connect_hint && (
            <div className="flex flex-col gap-1.5">
              <CodeBlock
                title={`Connect ${pub ? "from this host or your network" : "from inside the VPC"}`}
                code={inst.endpoint.connect_hint}
                copyLabel="Copy connection string"
              />
              {inst.endpoint.connect_hint.includes("<password>") || inst.engine === "mysql" || inst.engine === "mariadb" ? (
                <p className="text-muted-foreground text-xs">Use the master {inst.master_username ? "password" : "auth token"} below. Other resources in the VPC can use the endpoint name.</p>
              ) : null}
            </div>
          )}
        </div>
      </Section>

      <CredentialsSection cfg={cfg} inst={inst} />

      <Section title="Details">
        <KeyValueGrid
          columns={3}
          items={[
            { label: cfg.idLabel, value: <CopyableText value={inst.id} /> },
            { label: "Status", value: <StatusBadge status={inst.status} /> },
            { label: "Engine", value: <EngineCell engine={inst.engine} version={inst.engine_version} /> },
            { label: cfg.family === "rds" ? "Class" : "Node type", value: <span className="font-mono text-[13px]">{inst.class}</span> },
            { label: "vCPU", value: formatVcpu(inst.vcpus) },
            { label: "Memory", value: formatMemoryMB(inst.memory_mb) },
            ...(inst.engine !== "memcached" ? [{ label: "Storage", value: `${inst.storage_gb} GiB (advisory)` }] : []),
            ...(inst.master_username ? [{ label: "Master username", value: <CopyableText value={inst.master_username} /> }] : []),
            ...(inst.db_name ? [{ label: "Database name", value: <CopyableText value={inst.db_name} /> }] : []),
            {
              label: "Created",
              value: (
                <span>
                  {formatDate(inst.created_at)} (<TimeAgo value={inst.created_at} />)
                </span>
              ),
            },
            {
              label: "Restored from",
              value: inst.restored_from ? (
                <CellLink href={cfg.snapshotsPath} mono className="w-fit">
                  {inst.restored_from}
                </CellLink>
              ) : (
                ""
              ),
            },
            ...(supportsSnapshots(inst.engine)
              ? [
                  {
                    label: `Latest automated ${cfg.snap}`,
                    value: inst.latest_backup ? (
                      <span>
                        {formatDate(inst.latest_backup)} (<TimeAgo value={inst.latest_backup} />)
                      </span>
                    ) : (
                      <span className="text-muted-foreground">None yet</span>
                    ),
                  },
                  {
                    label: "Backup retention",
                    value: inst.backup_retention_days > 0 ? `${inst.backup_retention_days} day${inst.backup_retention_days === 1 ? "" : "s"}` : "Disabled",
                  },
                ]
              : []),
            {
              label: "Deletion protection",
              value: inst.deletion_protection ? (
                <span className="inline-flex items-center gap-1">
                  <ShieldCheck className="text-success size-4" /> Enabled
                </span>
              ) : (
                "Disabled"
              ),
            },
            {
              label: "Container ID",
              value: inst.container_id ? <CopyableText value={inst.container_id} display={inst.container_id.slice(0, 12)} /> : "",
            },
            { label: "ARN", value: <CopyableText value={inst.arn} />, wide: true },
          ]}
        />
      </Section>

      <Section title="Tags">
        <TagList tags={inst.tags} />
      </Section>
    </div>
  )
}

function CredentialsSection({ cfg, inst }: { cfg: FamilyConfig; inst: DbInstance }) {
  const [creds, setCreds] = useState<DbCredentials | null>(null)
  const [loading, setLoading] = useState(false)
  const [err, setErr] = useState<string | null>(null)
  const [reveal, setReveal] = useState(false)

  if (!inst.secret_name) {
    return (
      <Section title="Security">
        <Alert variant="warning">
          <Info />
          <AlertDescription>
            This {cfg.noun} has no authentication. Anything that can reach the endpoint can use it; keep it private unless you trust your network.
          </AlertDescription>
        </Alert>
      </Section>
    )
  }

  const load = async () => {
    setLoading(true)
    setErr(null)
    try {
      const v = await api.get<SecretValue>(`/api/v1/secrets/${seg(inst.secret_name!)}/value`)
      setCreds(JSON.parse(v.value) as DbCredentials)
      setReveal(false)
    } catch (e) {
      setErr(e instanceof SyntaxError ? "The secret value is not valid JSON" : errorMessage(e))
    } finally {
      setLoading(false)
    }
  }

  const secretLink = (
    <Link href={`/secrets/secret/?name=${encodeURIComponent(inst.secret_name)}`} className={cn(cellLinkClass(), "font-mono")}>
      {inst.secret_name}
    </Link>
  )

  return (
    <Section
      title={inst.master_username ? "Master credentials" : "Auth token"}
      description={<>Stored in Secrets Manager as {secretLink}.</>}
      actions={
        creds ? (
          <Button variant="outline" size="sm" onClick={() => (setCreds(null), setReveal(false))}>
            <EyeOff /> Hide credentials
          </Button>
        ) : (
          <Button variant="outline" size="sm" onClick={load} disabled={loading}>
            {loading ? <Loader2 className="animate-spin" /> : <KeyRound />}
            Show credentials
          </Button>
        )
      }
    >
      {err ? (
        <Alert variant="destructive">
          <AlertCircle />
          <AlertDescription>{err}</AlertDescription>
        </Alert>
      ) : !creds ? (
        <p className="text-muted-foreground text-sm">
          Credentials are hidden. Reading them is recorded in CloudTrail as <span className="font-mono">secretsmanager:GetSecretValue</span>.
        </p>
      ) : (
        <KeyValueGrid
          columns={3}
          items={[
            ...(creds.username ? [{ label: "Username", value: <CopyableText value={creds.username} /> }] : []),
            {
              label: creds.username ? "Password" : "Auth token",
              value: (
                <span className="inline-flex max-w-full items-center gap-1">
                  <span className="truncate font-mono text-[13px]">{reveal ? creds.password : "•".repeat(Math.min(creds.password.length, 24))}</span>
                  <button
                    type="button"
                    onClick={() => setReveal(!reveal)}
                    aria-label={reveal ? "Hide password" : "Reveal password"}
                    className="text-muted-foreground hover:text-foreground hover:bg-accent inline-flex size-6 shrink-0 items-center justify-center rounded"
                  >
                    {reveal ? <EyeOff className="size-3.5" /> : <Eye className="size-3.5" />}
                  </button>
                  <CopyButton value={creds.password} label="Copy password" />
                </span>
              ),
            },
            ...(creds.dbname ? [{ label: "Database", value: <CopyableText value={creds.dbname} /> }] : []),
            { label: "Host (VPC)", value: <CopyableText value={`${creds.host}:${creds.port}`} /> },
            ...(publicText(inst) ? [{ label: "Host (public)", value: <CopyableText value={publicText(inst)} /> }] : []),
            ...(endpointText(inst) !== `${creds.host}:${creds.port}` ? [{ label: "Endpoint", value: <CopyableText value={endpointText(inst)} /> }] : []),
          ]}
        />
      )}
    </Section>
  )
}
