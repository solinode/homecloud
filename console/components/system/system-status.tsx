"use client"

import { useEffect, useMemo, useState } from "react"
import Link from "next/link"
import { RefreshCw } from "lucide-react"

import { Button } from "@/components/ui/button"
import { Skeleton } from "@/components/ui/skeleton"
import { CodeBlock } from "@/components/console/code-block"
import { CopyButton } from "@/components/console/copy-button"
import { ErrorState } from "@/components/console/error-state"
import { KeyValueGrid } from "@/components/console/key-value"
import { PageHeader } from "@/components/console/page-header"
import { Section } from "@/components/console/section"
import { StatTile } from "@/components/console/stat-tile"
import { StatusBadge, StatusDot, TONES, type Tone } from "@/components/console/status-badge"
import { TimeAgo } from "@/components/console/time-ago"
import { API_BASE, DEMO } from "@/lib/api"
import { formatBytes, formatDate, formatDuration, formatNumber, pluralize } from "@/lib/format"
import { useApi, useNow } from "@/lib/hooks"
import type { Subsystem, SubsystemStatus, SystemStatus } from "@/lib/types"
import { cn } from "@/lib/utils"

export const SYSTEM_STATUS_PATH = "/api/v1/system/status"

const SUB_TONE: Record<SubsystemStatus, Tone> = { ok: "success", warning: "warning", error: "danger", unknown: "neutral" }
const SUB_LABEL: Record<SubsystemStatus, string> = { ok: "OK", warning: "Warning", error: "Error", unknown: "Unknown" }

const OVERALL: Record<string, { tone: Tone; label: string }> = {
  ok: { tone: "success", label: "Operational" },
  degraded: { tone: "warning", label: "Degraded" },
  down: { tone: "danger", label: "Down" },
}

const KIND_TITLE: Record<string, string> = { core: "Core", helper: "Helper containers", disk: "Storage", loop: "Background loops" }
const KIND_ORDER = ["core", "helper", "disk", "loop"]

/** SystemStatus is the operator page: health of this HomeCloud server, its disks, Docker and metrics. */
export function SystemStatusPage() {
  const { data: s, error, isLoading, isValidating, mutate } = useApi<SystemStatus>(SYSTEM_STATUS_PATH, { refreshInterval: 15_000 })
  const now = useNow(1000)
  // Uptime ticks between polls: started_at is fixed, so derive it locally.
  const uptime = s ? Math.max(s.uptime_seconds, (now - new Date(s.started_at).getTime()) / 1000) : 0
  const overall = OVERALL[s?.status ?? ""] ?? { tone: "neutral" as Tone, label: "Unknown" }
  const counts = useMemo(() => {
    const c = { ok: 0, warning: 0, error: 0, unknown: 0 }
    for (const x of s?.subsystems ?? []) c[x.status] = (c[x.status] ?? 0) + 1
    return c
  }, [s])
  const problems = counts.warning + counts.error

  return (
    <div className="flex flex-col gap-6">
      <PageHeader
        title="System status"
        badge={s && <StatusBadge status={s.status} tone={overall.tone} label={overall.label} />}
        description="Health of this HomeCloud server: subsystems, disk usage, Docker, Lambda environments and recent errors. Refreshes every 15 seconds."
        actions={
          <Button variant="outline" size="sm" onClick={() => mutate()} aria-label="Refresh">
            <RefreshCw className={cn(isValidating && "animate-spin")} /> Refresh
          </Button>
        }
      />

      {error && !s && <ErrorState error={error} onRetry={() => mutate()} />}

      <div className="grid grid-cols-2 gap-3 lg:grid-cols-4 lg:gap-4">
        <StatTile
          label="Version"
          tone={overall.tone}
          value={<span className="font-mono text-[24px] tracking-[-0.02em]">{s?.version ?? "-"}</span>}
          caption={s ? `${s.commit} · ${s.go_version}` : " "}
          loading={isLoading}
        />
        <StatTile
          label="Uptime"
          value={<span className="text-[26px]">{s ? formatDuration(uptime) : "-"}</span>}
          caption={s ? `since ${formatDate(s.started_at, false)}` : " "}
          loading={isLoading}
        />
        <StatTile
          label="Region"
          value={<span className="font-mono text-[22px] tracking-[-0.02em]">{s?.region ?? "-"}</span>}
          caption={s ? `account ${s.account_id}` : " "}
          loading={isLoading}
        />
        <StatTile
          label="Subsystems"
          tone={counts.error ? "danger" : counts.warning ? "warning" : s ? "success" : undefined}
          value={counts.ok}
          unit={`/ ${s?.subsystems.length ?? 0}`}
          caption={problems ? `${pluralize(problems, "needs", "need")} attention` : "all healthy"}
          loading={isLoading}
        />
      </div>

      <div className="grid grid-cols-1 gap-6 xl:grid-cols-3">
        <div className="flex min-w-0 flex-col gap-6 xl:col-span-2">
          <SubsystemsSection subsystems={s?.subsystems} loading={isLoading} />
          <DiskSection s={s} loading={isLoading} />
          <ErrorsSection s={s} loading={isLoading} />
        </div>
        <div className="flex min-w-0 flex-col gap-6">
          <DockerSection s={s} loading={isLoading} />
          {(isLoading || s?.lambda) && <LambdaSection s={s} loading={isLoading} />}
          <ContainersSection s={s} loading={isLoading} />
          <HostSection s={s} loading={isLoading} />
        </div>
      </div>

      <PrometheusSection s={s} />
    </div>
  )
}

function Rows({ n = 4 }: { n?: number }) {
  return (
    <div className="flex flex-col gap-3">
      {Array.from({ length: n }, (_, i) => (
        <Skeleton key={i} className="h-5 w-full" />
      ))}
    </div>
  )
}

function SubsystemRow({ x }: { x: Subsystem }) {
  const tone = SUB_TONE[x.status] ?? "neutral"
  const name = x.kind === "loop" ? x.name.replace(/^loop:/, "") : x.name
  return (
    <li className="flex min-w-0 items-center gap-3 px-5 py-2.5">
      <StatusDot tone={tone} className="size-2 [&>span]:size-2" pulse={x.status === "warning"} />
      <span className="min-w-0 flex-1">
        <span className="block truncate font-mono text-[13px]">{name}</span>
        <span className={cn("block truncate text-xs", x.status === "ok" ? "text-faint" : tone === "danger" ? "text-danger" : tone === "warning" ? "text-warning" : "text-faint")}>
          {x.detail || "-"}
          {x.last_run_age_seconds !== undefined && x.last_run && <> · {x.last_run_age_seconds < 1 ? "just now" : `${formatDuration(x.last_run_age_seconds)} ago`}</>}
        </span>
      </span>
      {x.status !== "ok" && (
        <span className={cn("inline-flex h-[20px] shrink-0 items-center rounded-full border px-2 text-[11px] font-medium", TONES[tone])}>{SUB_LABEL[x.status] ?? x.status}</span>
      )}
    </li>
  )
}

function SubsystemsSection({ subsystems, loading }: { subsystems?: Subsystem[]; loading: boolean }) {
  const groups = KIND_ORDER.map((k) => ({ kind: k, items: (subsystems ?? []).filter((x) => x.kind === k) }))
    .concat([{ kind: "other", items: (subsystems ?? []).filter((x) => !KIND_ORDER.includes(x.kind)) }])
    .filter((g) => g.items.length > 0)
  return (
    <Section
      title="Subsystems"
      description="The API, Docker, helper containers and the background loops that drive each service."
      flush
      actions={<Legend />}
    >
      {loading ? (
        <div className="p-5">
          <Rows n={6} />
        </div>
      ) : (
        groups.map((g) => (
          <div key={g.kind} className="border-b last:border-b-0">
            <p className="hc-eyebrow px-5 pt-3.5 pb-1">{KIND_TITLE[g.kind] ?? "Other"}</p>
            <ul className={cn("grid grid-cols-1 pb-1.5", g.items.length > 3 && (g.kind === "loop" ? "grid-cols-2" : "sm:grid-cols-2"))}>
              {g.items.map((x) => (
                <SubsystemRow key={x.name} x={x} />
              ))}
            </ul>
          </div>
        ))
      )}
    </Section>
  )
}

function Legend() {
  return (
    <span className="text-faint flex flex-wrap items-center gap-x-3 gap-y-1 text-xs">
      {(["ok", "warning", "error", "unknown"] as SubsystemStatus[]).map((k) => (
        <span key={k} className="flex items-center gap-1.5">
          <StatusDot tone={SUB_TONE[k]} />
          {SUB_LABEL[k]}
        </span>
      ))}
    </span>
  )
}

function DiskSection({ s, loading }: { s?: SystemStatus; loading: boolean }) {
  const d = s?.disk
  const total = d?.data_dir.fs_total_bytes ?? 0
  const used = total ? total - (d?.data_dir.fs_free_bytes ?? 0) : 0
  const measured = typeof d?.data_dir.used_bytes === "number"
  const ours = d?.data_dir.used_bytes ?? 0
  const pct = total ? (used / total) * 100 : 0
  const usedTone = pct >= 90 ? "bg-danger" : pct >= 80 ? "bg-warning" : "bg-faint/50"
  return (
    <Section
      title="Disk usage"
      description={d?.data_dir.path ? <span className="font-mono text-xs break-all">{d.data_dir.path}</span> : "Data directory and Docker volumes."}
      actions={
        d && (
          <span className="text-faint text-xs">
            {d.measured_at ? (
              <>
                measured <TimeAgo value={d.measured_at} />
              </>
            ) : (
              "first measurement pending"
            )}
          </span>
        )
      }
    >
      {loading || !d ? (
        <Rows n={3} />
      ) : (
        <div className="flex flex-col gap-6">
          <div>
            <div className="mb-2 flex flex-wrap items-baseline justify-between gap-x-4 gap-y-1">
              <span className="text-sm font-medium">Filesystem</span>
              <span className="text-muted-foreground text-[13px] tabular-nums">
                <span className="text-foreground font-medium">{formatBytes(used)}</span> of {formatBytes(total)} used · {formatBytes(d.data_dir.fs_free_bytes)} free
              </span>
            </div>
            <div
              className="bg-muted flex h-2.5 w-full overflow-hidden rounded-full"
              role="meter"
              aria-label="Filesystem usage"
              aria-valuemin={0}
              aria-valuemax={100}
              aria-valuenow={Math.round(pct)}
            >
              {total > 0 && (
                <>
                  <span className="bg-brand h-full" style={{ width: `${Math.min(100, (ours / total) * 100)}%`, minWidth: ours ? 3 : 0 }} />
                  <span className={cn("h-full", usedTone)} style={{ width: `${Math.max(0, ((used - ours) / total) * 100)}%` }} />
                </>
              )}
            </div>
            <div className="text-faint mt-2 flex flex-wrap items-center gap-x-4 gap-y-1 text-xs">
              <span className="flex items-center gap-1.5">
                <span className="bg-brand size-2 rounded-sm" /> HomeCloud data
              </span>
              <span className="flex items-center gap-1.5">
                <span className={cn("size-2 rounded-sm", usedTone)} /> Other
              </span>
              <span className="flex items-center gap-1.5">
                <span className="bg-muted size-2 rounded-sm border" /> Free
              </span>
              <span className="ml-auto tabular-nums">{pct.toFixed(1)}% used</span>
            </div>
          </div>
          <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
            <UsageTile
              label="Data directory"
              value={measured ? formatBytes(ours) : "-"}
              share={total ? ours / total : 0}
              caption={measured ? `${total ? ((ours / total) * 100).toFixed(2) : "0"}% of the filesystem` : "Not measured yet"}
            />
            <UsageTile
              label="Docker volumes"
              value={d.volumes.error ? "-" : formatBytes(d.volumes.size_bytes)}
              share={total && !d.volumes.error ? d.volumes.size_bytes / total : 0}
              caption={d.volumes.error ? d.volumes.error : `${pluralize(d.volumes.count, "volume")} (Docker's own disk)`}
              danger={!!d.volumes.error}
            />
          </div>
        </div>
      )}
    </Section>
  )
}

function UsageTile({ label, value, share, caption, danger }: { label: string; value: string; share: number; caption: string; danger?: boolean }) {
  return (
    <div className="bg-muted/40 rounded-lg border p-3.5">
      <div className="flex items-baseline justify-between gap-2">
        <span className="text-faint text-xs font-medium">{label}</span>
        <span className="text-[15px] font-semibold tracking-[-0.02em] tabular-nums">{value}</span>
      </div>
      <div className="bg-muted mt-2.5 h-1.5 w-full overflow-hidden rounded-full border-0">
        <span className="bg-brand block h-full rounded-full" style={{ width: `${Math.min(100, share * 100)}%`, minWidth: share > 0 ? 3 : 0 }} />
      </div>
      <p className={cn("mt-2 truncate text-xs", danger ? "text-danger" : "text-faint")} title={caption}>
        {caption}
      </p>
    </div>
  )
}

function DockerSection({ s, loading }: { s?: SystemStatus; loading: boolean }) {
  const d = s?.docker
  return (
    <Section
      title="Docker"
      actions={d && <StatusBadge status={d.reachable ? "ok" : "error"} label={d.reachable ? "Reachable" : "Unreachable"} />}
    >
      {loading || !d ? (
        <Rows />
      ) : !d.reachable ? (
        <p className="text-danger text-sm break-words">{d.error || "The Docker daemon is not reachable."}</p>
      ) : (
        <KeyValueGrid
          columns={2}
          items={[
            { label: "Version", value: <span className="font-mono text-[13px]">{d.server_version}</span> },
            { label: "Storage driver", value: <span className="font-mono text-[13px]">{d.storage_driver}</span> },
            { label: "Operating system", value: d.os, wide: true },
            { label: "Kernel", value: <span className="font-mono text-[13px] break-all">{d.kernel_version}</span>, wide: true },
            { label: "CPUs / memory", value: `${d.cpus} · ${formatBytes(d.memory_bytes)}` },
            { label: "Architecture", value: <span className="font-mono text-[13px]">{d.architecture}</span> },
            { label: "Containers", value: <>{d.containers_running} running <span className="text-faint">/ {d.containers}</span></> },
            { label: "Images", value: d.images },
          ]}
        />
      )}
    </Section>
  )
}

function LambdaSection({ s, loading }: { s?: SystemStatus; loading: boolean }) {
  const l = s?.lambda
  const starts = (l?.cold_starts ?? 0) + (l?.warm_starts ?? 0)
  const warmPct = starts ? ((l?.warm_starts ?? 0) / starts) * 100 : 0
  const envs: [string, number, Tone][] = l
    ? [
        ["Idle", l.environments_idle, "success"],
        ["Busy", l.environments_busy, "info"],
        ["Starting", l.environments_starting, "warning"],
      ]
    : []
  return (
    <Section title="Lambda environments" description="Warm execution environments kept between invocations.">
      {loading || !l ? (
        <Rows n={3} />
      ) : (
        <div className="flex flex-col gap-5">
          <div className="grid grid-cols-3 gap-px overflow-hidden rounded-lg border bg-[var(--border)]">
            {envs.map(([label, n, tone]) => (
              <div key={label} className="bg-card px-3 py-2.5">
                <span className="text-faint flex items-center gap-1.5 text-xs">
                  <StatusDot tone={n ? tone : "neutral"} /> {label}
                </span>
                <span className="mt-1 block text-xl font-semibold tracking-[-0.04em] tabular-nums">{n}</span>
              </div>
            ))}
          </div>
          <div>
            <div className="mb-2 flex items-baseline justify-between gap-2 text-[13px]">
              <span className="font-medium">Starts</span>
              <span className="text-muted-foreground tabular-nums">
                {formatNumber(l.warm_starts)} warm · {formatNumber(l.cold_starts)} cold
              </span>
            </div>
            <div className="bg-muted flex h-2 w-full overflow-hidden rounded-full">
              <span className="bg-success h-full" style={{ width: `${warmPct}%` }} />
              <span className="bg-info h-full" style={{ width: `${starts ? 100 - warmPct : 0}%` }} />
            </div>
            <p className="text-faint mt-2 text-xs">{starts ? `${warmPct.toFixed(1)}% of invocations reused a warm environment` : "No invocations since start"}</p>
          </div>
        </div>
      )}
    </Section>
  )
}

const STATE_ORDER = ["running", "restarting", "created", "paused", "exited", "dead"]

function ContainersSection({ s, loading }: { s?: SystemStatus; loading: boolean }) {
  const rows = useMemo(() => {
    const m = new Map<string, { state: string; count: number }[]>()
    for (const c of s?.containers ?? []) m.set(c.service, [...(m.get(c.service) ?? []), { state: c.state, count: c.count }])
    return [...m.entries()]
      .map(([service, states]) => ({
        service,
        total: states.reduce((n, x) => n + x.count, 0),
        states: states.sort((a, b) => STATE_ORDER.indexOf(a.state) - STATE_ORDER.indexOf(b.state)),
      }))
      .sort((a, b) => b.total - a.total || a.service.localeCompare(b.service))
  }, [s])
  const tone = (st: string): Tone => (st === "running" ? "success" : st === "restarting" || st === "created" || st === "paused" ? "warning" : st === "dead" ? "danger" : "neutral")
  return (
    <Section title="Containers" description="HomeCloud-managed containers by service and state." flush>
      {loading ? (
        <div className="p-5">
          <Rows />
        </div>
      ) : rows.length === 0 ? (
        <p className="text-muted-foreground px-5 py-6 text-sm">No HomeCloud containers.</p>
      ) : (
        <ul className="divide-y">
          {rows.map((r) => (
            <li key={r.service} className="flex items-center justify-between gap-3 px-5 py-2.5">
              <span className="min-w-0 truncate font-mono text-[13px]">{r.service}</span>
              <span className="flex flex-wrap justify-end gap-1.5">
                {r.states.map((x) => (
                  <span key={x.state} className={cn("inline-flex h-[20px] items-center gap-1.5 rounded-full border px-2 text-[11px] font-medium tabular-nums", TONES[tone(x.state)])}>
                    {x.count} {x.state}
                  </span>
                ))}
              </span>
            </li>
          ))}
        </ul>
      )}
    </Section>
  )
}

function HostSection({ s, loading }: { s?: SystemStatus; loading: boolean }) {
  return (
    <Section title="Server process">
      {loading || !s ? (
        <Rows n={3} />
      ) : (
        <KeyValueGrid
          columns={2}
          className="grid-cols-2"
          items={[
            { label: "Platform", value: <span className="font-mono text-[13px]">{`${s.host.os}/${s.host.arch}`}</span> },
            { label: "CPUs", value: s.host.cpus },
            { label: "PID", value: <span className="font-mono text-[13px]">{s.process.pid}</span> },
            { label: "Goroutines", value: formatNumber(s.process.goroutines) },
            { label: "Heap in use", value: formatBytes(s.process.heap_bytes) },
            { label: "Memory from OS", value: formatBytes(s.process.sys_bytes) },
          ]}
        />
      )}
    </Section>
  )
}

function ErrorsSection({ s, loading }: { s?: SystemStatus; loading: boolean }) {
  const errs = s?.recent_errors ?? []
  return (
    <Section
      title="Recent errors"
      description="Errors logged by background work since the server started, newest first."
      flush
      actions={s && <span className="text-faint text-xs">{pluralize(errs.length, "error")}</span>}
    >
      {loading ? (
        <div className="p-5">
          <Rows n={3} />
        </div>
      ) : errs.length === 0 ? (
        <p className="text-muted-foreground flex items-center gap-2 px-5 py-6 text-sm">
          <StatusDot tone="success" /> No errors since the server started.
        </p>
      ) : (
        <ul className="max-h-[26rem] divide-y overflow-y-auto">
          {errs.map((e, i) => (
            <li key={`${e.time}-${i}`} className="flex flex-col gap-1.5 px-5 py-3 sm:flex-row sm:items-start sm:gap-4">
              <span className="flex shrink-0 items-center gap-2 sm:w-44">
                <span className="text-danger bg-danger-soft border-danger/25 inline-flex h-[20px] items-center rounded-full border px-2 font-mono text-[11px]">{e.source}</span>
                <TimeAgo value={e.time} className="text-faint text-xs whitespace-nowrap" />
              </span>
              <span className="text-muted-foreground min-w-0 font-mono text-[12.5px] leading-relaxed break-words">{e.message}</span>
            </li>
          ))}
        </ul>
      )}
    </Section>
  )
}

function PrometheusSection({ s }: { s?: SystemStatus }) {
  const path = s?.metrics.path || "/metrics"
  const action = s?.metrics.action || "homecloud:GetMetrics"
  const [origin, setOrigin] = useState("")
  useEffect(() => {
    // The console is served by the API server, so the page origin is the API origin.
    setOrigin(DEMO ? "http://homecloud.local:8080" : API_BASE || window.location.origin)
  }, [])
  const base = origin || "http://homecloud.local:8080"
  const url = new URL(path, base)
  const scheme = url.protocol.replace(":", "")
  const target = url.port ? url.host : `${url.hostname}:${scheme === "https" ? 443 : 80}`
  const scrape = [
    "scrape_configs:",
    "  - job_name: homecloud",
    `    scheme: ${scheme}`,
    `    metrics_path: ${path}`,
    "    static_configs:",
    `      - targets: ["${target}"]`,
    "    basic_auth:",
    "      username: <ACCESS_KEY_ID>",
    "      password: <SECRET_ACCESS_KEY>",
  ].join("\n")
  const policy = JSON.stringify({ Version: "2012-10-17", Statement: [{ Effect: "Allow", Action: action, Resource: "*" }] }, null, 2)
  const listen = s?.metrics.listen_addr
  return (
    <Section
      title="Prometheus"
      description="HomeCloud exposes request, auth, Docker, disk, loop and Lambda metrics in the Prometheus text format."
    >
      <div className="grid grid-cols-1 gap-6 lg:grid-cols-2">
        <div className="flex min-w-0 flex-col gap-5">
          <KeyValueGrid
            columns={2}
            className="sm:grid-cols-1 lg:grid-cols-1"
            items={[
              {
                label: "Endpoint",
                value: (
                  <span className="flex min-w-0 items-center gap-1 font-mono text-[13px]">
                    <span className="min-w-0 break-all">{url.toString()}</span>
                    <CopyButton value={url.toString()} label="Copy endpoint" className="size-6 shrink-0" />
                  </span>
                ),
              },
              {
                label: "Required IAM action",
                value: (
                  <span className="flex items-center gap-1 font-mono text-[13px]">
                    {action} <CopyButton value={action} label="Copy action" className="size-6" />
                  </span>
                ),
              },
            ]}
          />
          <ol className="text-muted-foreground flex list-decimal flex-col gap-2 pl-5 text-sm leading-relaxed marker:text-faint">
            <li>
              In <Link href="/iam/users/" className="text-primary hover:underline">IAM</Link>, create a user such as{" "}
              <span className="text-foreground font-mono text-[13px]">prometheus</span> with an inline policy that allows only{" "}
              <span className="text-foreground font-mono text-[13px]">{action}</span>.
            </li>
            <li>Create an access key for that user.</li>
            <li>
              Put the key in <span className="text-foreground font-mono text-[13px]">basic_auth</span> (or send{" "}
              <span className="text-foreground font-mono text-[13px]">Authorization: Bearer KEY_ID:SECRET</span>).
            </li>
          </ol>
          <div className="bg-muted/40 rounded-lg border p-3.5 text-[13px] leading-relaxed">
            <p className="font-medium">Unauthenticated listener</p>
            <p className="text-muted-foreground mt-1">
              {listen ? (
                <>
                  This server also serves metrics without auth on <span className="text-foreground font-mono">{listen}</span>.
                </>
              ) : (
                <>
                  Start HomeCloud with <span className="text-foreground font-mono whitespace-nowrap">--metrics-addr 127.0.0.1:9464</span> to also serve{" "}
                  <span className="font-mono">/metrics</span> without auth for a Prometheus on the same host. Non-loopback addresses need{" "}
                  <span className="text-foreground font-mono whitespace-nowrap">--metrics-addr-insecure</span>.
                </>
              )}
            </p>
          </div>
        </div>
        <div className="flex min-w-0 flex-col gap-4">
          <CodeBlock title="prometheus.yml" code={scrape} copyLabel="Copy scrape config" />
          <CodeBlock title="IAM policy" code={policy} copyLabel="Copy policy" />
        </div>
      </div>
    </Section>
  )
}
