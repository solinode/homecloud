"use client"

import { useEffect, useState, type ReactNode } from "react"
import Link from "next/link"
import { useSearchParams } from "next/navigation"
import {
  ArrowRight,
  ArrowUpRight,
  BellRing,
  BookOpen,
  CheckCircle2,
  Command,
  FileCode2,
  Layers,
  Rocket,
  Scaling,
  ScrollText,
  Terminal,
  type LucideIcon,
} from "lucide-react"

import { Button } from "@/components/ui/button"
import { Skeleton } from "@/components/ui/skeleton"
import { useSession } from "@/components/console/auth"
import { CodeBlock } from "@/components/console/code-block"
import { useCommandPalette, Kbd } from "@/components/console/command-palette"
import { CopyableText } from "@/components/console/copy-button"
import { PageHeader } from "@/components/console/page-header"
import { ServiceIcon } from "@/components/console/service-icon"
import { StatTile } from "@/components/console/stat-tile"
import { StatusDot } from "@/components/console/status-badge"
import { TimeAgo } from "@/components/console/time-ago"
import { DOCS_URL, QUICK_ACTIONS, REPO_URL } from "@/lib/actions"
import { API_BASE, DEMO } from "@/lib/api"
import { formatDuration, pluralize } from "@/lib/format"
import { useApi } from "@/lib/hooks"
import { SERVICES } from "@/lib/services"
import type {
  Alarm,
  AutoScalingGroup,
  Bucket,
  Certificate,
  DbInstance,
  DynamoTable,
  EcrRepository,
  EcsService,
  EventRule,
  FileSystem,
  Health,
  HostedZoneSummary,
  IamSummary,
  Instance,
  LambdaFunction,
  LoadBalancer,
  Queue,
  Secret,
  StateMachineSummary,
  Topic,
  TrailEvent,
  UserPool,
  Vpc,
} from "@/lib/types"
import { cn } from "@/lib/utils"

const svc = (id: string) => SERVICES.find((s) => s.id === id)!

interface ServiceCount {
  key: string
  name: string
  icon: LucideIcon
  href: string
  value: number
  sub?: ReactNode
  warn?: boolean
  loading: boolean
  error: boolean
  /** counted towards "resources" (IAM users and VPCs exist on a fresh install) */
  counts?: boolean
}

function endpoint(): string {
  if (DEMO) return "http://127.0.0.1:8080"
  if (API_BASE) return API_BASE
  return typeof window === "undefined" ? "" : window.location.origin
}

function greeting(): string {
  const h = new Date().getHours()
  return h < 5 ? "Good evening" : h < 12 ? "Good morning" : h < 18 ? "Good afternoon" : "Good evening"
}

export function ConsoleHome() {
  const session = useSession()
  const sp = useSearchParams()
  const palette = useCommandPalette()
  const [hello, setHello] = useState("Welcome")
  useEffect(() => setHello(greeting()), [])

  const health = useApi<Health>("/api/v1/health", { refreshInterval: 30_000 })
  const instances = useApi<Instance[]>("/api/v1/ec2/instances", { refreshInterval: 15_000 })
  const buckets = useApi<Bucket[]>("/api/v1/s3/buckets")
  const iam = useApi<IamSummary>("/api/v1/iam/summary")
  const secrets = useApi<Secret[]>("/api/v1/secrets")
  const alarms = useApi<Alarm[]>("/api/v1/cloudwatch/alarms", { refreshInterval: 30_000 })
  const vpcs = useApi<Vpc[]>("/api/v1/vpc/vpcs")
  const functions = useApi<LambdaFunction[]>("/api/v1/lambda/functions")
  const databases = useApi<DbInstance[]>("/api/v1/rds/instances", { refreshInterval: 30_000 })
  const queues = useApi<Queue[]>("/api/v1/sqs/queues", { refreshInterval: 30_000 })
  const tables = useApi<DynamoTable[]>("/api/v1/dynamodb/tables")
  const topics = useApi<Topic[]>("/api/v1/sns/topics")
  const rules = useApi<EventRule[]>("/api/v1/events/rules", { refreshInterval: 60_000 })
  const ecsServices = useApi<EcsService[]>("/api/v1/ecs/services", { refreshInterval: 30_000 })
  const loadBalancers = useApi<LoadBalancer[]>("/api/v1/elb/load-balancers")
  const repositories = useApi<EcrRepository[]>("/api/v1/ecr/repositories")
  const fileSystems = useApi<FileSystem[]>("/api/v1/efs/file-systems")
  const stateMachines = useApi<StateMachineSummary[]>("/api/v1/sfn/state-machines", { refreshInterval: 30_000 })
  const stacks = useApi<{ name: string; status: string }[]>("/api/v1/cloudformation/stacks", { refreshInterval: 30_000 })
  const zones = useApi<HostedZoneSummary[]>("/api/v1/route53/zones")
  const certs = useApi<Certificate[]>("/api/v1/acm/certificates")
  const pools = useApi<UserPool[]>("/api/v1/cognito/user-pools")
  const asgs = useApi<AutoScalingGroup[]>("/api/v1/autoscaling/groups", { refreshInterval: 30_000 })
  const events = useApi<TrailEvent[]>("/api/v1/cloudtrail/events", { query: { limit: 8 }, refreshInterval: 30_000 })

  const live = (instances.data ?? []).filter((i) => i.state !== "terminated")
  const running = live.filter((i) => i.state === "running").length
  const firing = (alarms.data ?? []).filter((a) => a.state === "ALARM")
  const dbs = (databases.data ?? []).filter((d) => d.kind !== "cache")
  const caches = (databases.data ?? []).filter((d) => d.kind === "cache")
  const dbAvailable = dbs.filter((d) => d.status === "available").length
  const queuedMessages = (queues.data ?? []).reduce((n, q) => n + (q.approximate_number_of_messages ?? 0), 0)
  const items = (tables.data ?? []).reduce((n, t) => n + (t.item_count ?? 0), 0)
  const enabledRules = (rules.data ?? []).filter((r) => r.state === "ENABLED").length
  const ecsRunning = (ecsServices.data ?? []).reduce((n, x) => n + (x.running_count ?? 0), 0)
  const ecsDesired = (ecsServices.data ?? []).reduce((n, x) => n + (x.desired_count ?? 0), 0)
  const runningExecutions = (stateMachines.data ?? []).reduce((n, m) => n + (m.executions?.RUNNING ?? 0), 0)
  const stacksInProgress = (stacks.data ?? []).filter((x) => x.status.endsWith("_IN_PROGRESS")).length
  const stacksFailed = (stacks.data ?? []).filter((x) => x.status.endsWith("_FAILED") || x.status.includes("ROLLBACK")).length
  const certsExpiring = (certs.data ?? []).filter((c) => new Date(c.not_after).getTime() - Date.now() < 30 * 86_400_000).length
  const asgInstances = (asgs.data ?? []).reduce((n, g) => n + (g.instances ?? []).length, 0)
  const cognitoUsers = (pools.data ?? []).reduce((n, p) => n + (p.users ?? 0), 0)

  const S = (id: string, value: number, q: { isLoading: boolean; error?: unknown }, sub?: ReactNode, extra: Partial<ServiceCount> = {}): ServiceCount => {
    const s = svc(id)
    return { key: id, name: s.name, icon: s.icon, href: s.href, value, sub, loading: q.isLoading, error: !!q.error, counts: true, ...extra }
  }
  const counts: ServiceCount[] = [
    S("ec2", live.length, instances, `${running} running`),
    S("lambda", functions.data?.length ?? 0, functions, "functions"),
    S("s3", buckets.data?.length ?? 0, buckets, "buckets"),
    S("rds", dbs.length, databases, `${dbAvailable} available`),
    S("elasticache", caches.length, databases, "cache clusters"),
    S("dynamodb", tables.data?.length ?? 0, tables, `${items.toLocaleString()} items`),
    S("ecs", ecsServices.data?.length ?? 0, ecsServices, `${ecsRunning}/${ecsDesired} tasks running`),
    S("ecr", repositories.data?.length ?? 0, repositories, "repositories"),
    S("sqs", queues.data?.length ?? 0, queues, `${queuedMessages.toLocaleString()} messages`),
    S("sns", topics.data?.length ?? 0, topics, pluralize((topics.data ?? []).reduce((n, t) => n + (t.subscriptions ?? 0), 0), "subscription")),
    S("eventbridge", rules.data?.length ?? 0, rules, `${enabledRules} enabled`),
    S("sfn", stateMachines.data?.length ?? 0, stateMachines, runningExecutions ? `${runningExecutions} running` : "state machines"),
    S("cloudformation", stacks.data?.length ?? 0, stacks, stacksFailed ? `${stacksFailed} failed` : stacksInProgress ? `${stacksInProgress} in progress` : "stacks", {
      warn: stacksFailed > 0,
    }),
    S("elb", loadBalancers.data?.length ?? 0, loadBalancers, "load balancers"),
    S("efs", fileSystems.data?.length ?? 0, fileSystems, "file systems"),
    S("route53", zones.data?.length ?? 0, zones, `${(zones.data ?? []).filter((z) => z.private).length} private`),
    S("acm", certs.data?.length ?? 0, certs, certsExpiring ? `${certsExpiring} expiring` : "certificates", { warn: certsExpiring > 0 }),
    S("cognito", pools.data?.length ?? 0, pools, pluralize(cognitoUsers, "user")),
    S("secrets", secrets.data?.length ?? 0, secrets, "secrets"),
    {
      key: "autoscaling",
      name: "Auto Scaling",
      icon: Scaling,
      href: "/ec2/autoscaling/",
      value: asgs.data?.length ?? 0,
      sub: pluralize(asgInstances, "instance"),
      loading: asgs.isLoading,
      error: !!asgs.error,
      counts: true,
    },
    S("iam", iam.data?.users ?? 0, iam, iam.data ? `${iam.data.groups} groups, ${iam.data.policies} policies` : "users", { counts: false }),
    S("vpc", vpcs.data?.length ?? 0, vpcs, "VPCs", { counts: false }),
  ]

  const loaded = counts.every((c) => !c.loading)
  const total = counts.filter((c) => c.counts && !c.error).reduce((n, c) => n + c.value, 0)
  const inUse = counts.filter((c) => c.value > 0 || c.loading)
  const unused = counts.filter((c) => !c.loading && c.value === 0 && !c.error)
  const firstRun = sp.get("welcome") === "1" || (loaded && total === 0)

  const quick = QUICK_ACTIONS.filter((a) => a.featured)

  return (
    <div className="flex flex-col gap-6">
      <PageHeader
        title={
          <>
            {hello}, <span className="text-muted-foreground">{session.user.name}</span>
          </>
        }
        description={
          firstRun ? (
            "Your HomeCloud is up. Point your tools at it and create your first resources."
          ) : (
            <>
              {loaded ? pluralize(total, "resource") : "Counting resources"} across {inUse.filter((c) => c.counts).length} services in{" "}
              <span className="text-foreground font-mono text-[13px]">{health.data?.region ?? "us-east-1"}</span>.
            </>
          )
        }
        actions={
          <>
            <Button variant="outline" size="sm" onClick={() => palette.setOpen(true)} className="hidden sm:inline-flex">
              <Command /> Search
              <Kbd className="ml-1">K</Kbd>
            </Button>
            <Button asChild size="sm">
              <Link href="/ec2/launch/">
                <Rocket /> Launch instance
              </Link>
            </Button>
          </>
        }
      />

      {firstRun ? (
        <GetStarted />
      ) : (
        <>
          <div className="grid grid-cols-2 gap-3 lg:grid-cols-4 lg:gap-4">
            <StatTile
              label="Instances"
              href="/ec2/"
              tone={running > 0 ? "success" : "neutral"}
              value={running}
              unit={`/ ${live.length}`}
              caption="running / total"
              loading={instances.isLoading}
            />
            <StatTile
              label="Alarms"
              href="/cloudwatch/alarms/"
              tone={firing.length ? "danger" : "success"}
              value={<span className={cn(firing.length > 0 && "text-danger")}>{firing.length}</span>}
              unit={`/ ${alarms.data?.length ?? 0}`}
              caption={firing.length ? "in ALARM" : "all clear"}
              loading={alarms.isLoading}
            />
            <StatTile
              label="Resources"
              value={total}
              caption={`across ${inUse.filter((c) => c.counts).length} services`}
              loading={!loaded}
            />
            <StatTile
              label="HomeCloud"
              tone={health.error ? "danger" : "success"}
              value={<span className="font-mono text-[24px] tracking-[-0.02em]">{health.data?.version ?? "-"}</span>}
              caption={health.error ? "API unreachable" : health.data ? `up ${formatDuration(health.data.uptime_seconds)}` : " "}
              loading={health.isLoading}
            />
          </div>

          <div className="grid grid-cols-1 gap-6 xl:grid-cols-3">
            <div className="flex min-w-0 flex-col gap-6 xl:col-span-2">
              <Panel title="Resources by service" action={<span className="text-faint text-xs">live counts</span>}>
                <ul className="grid grid-cols-2 gap-px overflow-hidden rounded-b-xl bg-[var(--border)] xl:grid-cols-3">
                  {inUse.map((c) => (
                    <li key={c.key} className="bg-card">
                      <Link href={c.href} className="hover:bg-muted/50 group flex items-center gap-3 px-3.5 py-3 transition-colors sm:px-4 sm:py-3.5">
                        <ServiceIcon service={c} size="md" className="hidden sm:inline-flex" />
                        <span className="min-w-0 flex-1">
                          <span className="block truncate text-[13px] font-medium">{c.name}</span>
                          <span className={cn("block truncate text-xs", c.warn ? "text-warning" : "text-faint")}>{c.error ? "unavailable" : c.sub}</span>
                        </span>
                        {c.loading ? (
                          <Skeleton className="h-6 w-8" />
                        ) : (
                          <span className="text-xl font-semibold tracking-[-0.04em] tabular-nums">{c.error ? "–" : c.value}</span>
                        )}
                      </Link>
                    </li>
                  ))}
                  {/* fill the last row so the hairline grid stays even */}
                  {Array.from({ length: (3 - (inUse.length % 3)) % 3 }, (_, i) => (
                    <li key={`pad-${i}`} className="bg-card hidden xl:block" aria-hidden />
                  ))}
                  {inUse.length % 2 === 1 && <li className="bg-card xl:hidden" aria-hidden />}
                </ul>
                {unused.length > 0 && (
                  <div className="flex flex-wrap items-center gap-1.5 border-t px-4 py-3">
                    <span className="text-faint mr-1 text-xs">Not in use yet</span>
                    {unused.map((c) => (
                      <Link
                        key={c.key}
                        href={c.href}
                        className="text-muted-foreground hover:text-foreground hover:border-border-strong rounded-full border px-2 py-0.5 text-xs transition-colors"
                      >
                        {c.name}
                      </Link>
                    ))}
                  </div>
                )}
              </Panel>

              <Panel
                title="Recent activity"
                action={
                  <Link href="/cloudtrail/" className="text-muted-foreground hover:text-foreground inline-flex items-center gap-1 text-xs">
                    Event history <ArrowRight className="size-3" />
                  </Link>
                }
              >
                {events.isLoading ? (
                  <div className="flex flex-col gap-3 p-4">
                    {Array.from({ length: 5 }, (_, i) => (
                      <Skeleton key={i} className="h-5 w-full" />
                    ))}
                  </div>
                ) : events.error ? (
                  <p className="text-muted-foreground p-4 text-sm">{events.error.message}</p>
                ) : !events.data?.length ? (
                  <p className="text-muted-foreground p-8 text-center text-sm">No API calls recorded yet.</p>
                ) : (
                  <ul className="divide-y">
                    {events.data.slice(0, 8).map((e) => (
                      <li key={e.id} className="flex items-center gap-3 px-4 py-2.5 text-[13px]">
                        <StatusDot tone={e.status < 400 ? "success" : "danger"} />
                        <span className="min-w-0 flex-1">
                          <span className="flex min-w-0 items-baseline gap-2">
                            <span className="truncate font-mono text-[12.5px]">{e.action}</span>
                            {e.status >= 400 && <span className="text-danger font-mono text-[11px]">{e.status}</span>}
                          </span>
                          <span className="text-faint block truncate font-mono text-[11.5px]" title={e.resource}>
                            {e.resource || e.path}
                          </span>
                        </span>
                        <span className="text-muted-foreground hidden max-w-36 truncate sm:block">{e.user}</span>
                        <span className="text-faint w-20 shrink-0 text-right text-xs">
                          <TimeAgo value={e.time} />
                        </span>
                      </li>
                    ))}
                  </ul>
                )}
              </Panel>
            </div>

            <div className="flex min-w-0 flex-col gap-6">
              <Panel
                title="Alarms"
                action={
                  <Link href="/cloudwatch/alarms/" className="text-muted-foreground hover:text-foreground inline-flex items-center gap-1 text-xs">
                    All alarms <ArrowRight className="size-3" />
                  </Link>
                }
              >
                {alarms.isLoading ? (
                  <div className="p-4">
                    <Skeleton className="h-12 w-full" />
                  </div>
                ) : firing.length === 0 ? (
                  <div className="flex items-center gap-3 px-4 py-4">
                    <span className="bg-success-soft text-success border-success/20 flex size-8 items-center justify-center rounded-lg border">
                      <CheckCircle2 className="size-4" />
                    </span>
                    <div>
                      <p className="text-[13px] font-medium">All clear</p>
                      <p className="text-faint text-xs">
                        {alarms.data?.length ? `${pluralize(alarms.data.length, "alarm")}, none firing` : "No alarms configured"}
                      </p>
                    </div>
                  </div>
                ) : (
                  <ul className="divide-y">
                    {firing.slice(0, 5).map((a) => (
                      <li key={a.arn}>
                        <Link href="/cloudwatch/alarms/" className="hover:bg-muted/50 flex items-start gap-3 px-4 py-3 transition-colors">
                          <BellRing className="text-danger mt-0.5 size-4 shrink-0" />
                          <span className="min-w-0">
                            <span className="block truncate text-[13px] font-medium">{a.name}</span>
                            <span className="text-faint block truncate font-mono text-[11.5px]">
                              {a.namespace} · {a.metric}
                            </span>
                          </span>
                        </Link>
                      </li>
                    ))}
                  </ul>
                )}
              </Panel>

              <Panel title="Quick actions">
                <div className="grid grid-cols-2 gap-2 p-3">
                  {quick.map((q) => (
                    <Link
                      key={q.href}
                      href={q.href}
                      className="hover:border-border-strong hover:bg-muted/50 group flex flex-col gap-2 rounded-lg border p-3 transition-colors"
                    >
                      <q.icon className="text-muted-foreground group-hover:text-primary size-4 transition-colors" />
                      <span className="text-[13px] leading-tight font-medium">{q.label}</span>
                    </Link>
                  ))}
                </div>
                <button
                  type="button"
                  onClick={() => palette.setOpen(true)}
                  className="text-muted-foreground hover:text-foreground flex w-full items-center justify-between border-t px-4 py-2.5 text-xs"
                >
                  More actions in the command palette
                  <span className="flex gap-0.5">
                    <Kbd>⌘</Kbd>
                    <Kbd>K</Kbd>
                  </span>
                </button>
              </Panel>

              <ConnectCli compact />
            </div>
          </div>

          <DocLinks />
        </>
      )}
    </div>
  )
}

/** Panel is a card with a compact header row, used by the dashboard. */
function Panel({ title, action, children, className }: { title: ReactNode; action?: ReactNode; children: ReactNode; className?: string }) {
  return (
    <section className={cn("bg-card rounded-xl border shadow-xs", className)}>
      <header className="flex h-11 items-center justify-between gap-3 border-b px-4">
        <h2 className="text-[13px] font-semibold tracking-[-0.01em]">{title}</h2>
        {action}
      </header>
      {children}
    </section>
  )
}

function ConnectCli({ compact }: { compact?: boolean }) {
  const ep = endpoint()
  return (
    <Panel
      title="Connect the AWS CLI"
      action={
        <a href={`${REPO_URL}#quick-start`} target="_blank" rel="noreferrer" className="text-muted-foreground hover:text-foreground inline-flex items-center gap-1 text-xs">
          Guide <ArrowUpRight className="size-3" />
        </a>
      }
    >
      <div className="flex flex-col gap-3 p-4">
        {!compact && <p className="text-muted-foreground text-[13px]">Every AWS tool reads these variables. Run this on the machine where HomeCloud is installed:</p>}
        <CodeBlock prompt code={`eval "$(homecloud aws-env)"\naws sts get-caller-identity`} />
        <div className="flex min-w-0 items-center justify-between gap-2 text-xs">
          <span className="text-faint shrink-0 font-mono">AWS_ENDPOINT_URL</span>
          <CopyableText value={ep} className="text-muted-foreground min-w-0" />
        </div>
      </div>
    </Panel>
  )
}

function Step({ n, title, children, aside }: { n: number; title: ReactNode; children: ReactNode; aside?: ReactNode }) {
  return (
    <li className="bg-card flex flex-col gap-3 rounded-xl border p-5 shadow-xs">
      <div className="flex items-center gap-3">
        <span className="bg-brand-soft text-primary border-brand-line flex size-6 items-center justify-center rounded-md border font-mono text-[11px] font-semibold">
          {n}
        </span>
        <h3 className="text-[15px] font-semibold tracking-[-0.015em]">{title}</h3>
        {aside && <span className="ml-auto">{aside}</span>}
      </div>
      {children}
    </li>
  )
}

/** GetStarted replaces the dashboard while the account has no resources. */
function GetStarted() {
  const ep = endpoint()
  return (
    <div className="flex flex-col gap-6">
      <section className="hc-backdrop bg-card overflow-hidden rounded-2xl border px-6 py-8 shadow-xs sm:px-10 sm:py-10">
        <p className="hc-eyebrow flex items-center gap-2 !text-primary">
          <span className="bg-brand size-1.5 rounded-[2px] shadow-[0_0_10px_var(--brand)]" /> Get started
        </p>
        <h2 className="mt-4 max-w-2xl text-[28px] leading-[1.1] font-semibold tracking-[-0.04em] sm:text-[34px]">
          Your cloud is running. Point your AWS tools at it.
        </h2>
        <p className="text-muted-foreground mt-3 max-w-2xl text-[15px] leading-relaxed">
          HomeCloud speaks the AWS APIs, so the AWS CLI, SDKs, Terraform and CloudFormation work unchanged. Three steps and you have real resources here.
        </p>
        <div className="mt-6 flex flex-wrap gap-2">
          {QUICK_ACTIONS.filter((a) => a.featured).map((a) => (
            <Button key={a.href} asChild variant="outline" size="sm">
              <Link href={a.href}>
                <a.icon /> {a.label}
              </Link>
            </Button>
          ))}
        </div>
      </section>

      <ol className="grid grid-cols-1 gap-4 lg:grid-cols-3">
        <Step n={1} title="Connect the AWS CLI" aside={<Terminal className="text-faint size-4" />}>
          <p className="text-muted-foreground text-[13px]">On the HomeCloud machine, load the endpoint and credentials into your shell:</p>
          <CodeBlock prompt code={`eval "$(homecloud aws-env)"`} />
          <p className="text-faint text-xs">
            Elsewhere, set <code className="text-muted-foreground font-mono">AWS_ENDPOINT_URL</code> to{" "}
            <CopyableText value={ep} className="text-muted-foreground align-middle" /> and use an IAM access key.
          </p>
        </Step>
        <Step n={2} title="Run your first command" aside={<Command className="text-faint size-4" />}>
          <p className="text-muted-foreground text-[13px]">Create a bucket and list it, exactly as you would on AWS:</p>
          <CodeBlock prompt code={`aws s3 mb s3://hello-homecloud\naws s3 ls`} />
          <p className="text-faint text-xs">It shows up under S3 here right away, and the call is recorded in CloudTrail.</p>
        </Step>
        <Step n={3} title="Deploy a sample stack" aside={<Layers className="text-faint size-4" />}>
          <p className="text-muted-foreground text-[13px]">
            The <span className="text-foreground">shop</span> example builds a VPC, load balancer, ECS service, Postgres, queues and Lambdas with the stock AWS provider:
          </p>
          <CodeBlock prompt code={`cd examples/terraform/shop\nterraform init && terraform apply`} />
          <div className="flex flex-wrap gap-2">
            <Button asChild variant="outline" size="xs">
              <a href={`${REPO_URL}/tree/main/examples/terraform/shop`} target="_blank" rel="noreferrer">
                <FileCode2 /> View the Terraform
              </a>
            </Button>
            <Button asChild variant="ghost" size="xs">
              <Link href="/cloudformation/create/">
                <Layers /> Or a CloudFormation example
              </Link>
            </Button>
          </div>
        </Step>
      </ol>

      <DocLinks />
    </div>
  )
}

function DocLinks() {
  const links = [
    { href: DOCS_URL, label: "Documentation", sub: "Install, configure and operate HomeCloud", icon: BookOpen },
    { href: `${REPO_URL}/blob/main/docs/aws-compat.md`, label: "AWS compatibility", sub: "Which APIs and flags each service supports", icon: Layers },
    { href: `${REPO_URL}/tree/main/examples`, label: "Examples", sub: "Terraform and CloudFormation you can run", icon: FileCode2 },
    { href: "/cloudtrail/", label: "Audit log", sub: "Every change made here is in CloudTrail", icon: ScrollText, internal: true },
  ]
  return (
    <div className="grid grid-cols-1 gap-3 sm:grid-cols-2 xl:grid-cols-4">
      {links.map((l) => {
        const body = (
          <>
            <l.icon className="text-faint group-hover:text-primary size-4 shrink-0 transition-colors" />
            <span className="min-w-0 flex-1">
              <span className="flex items-center gap-1 text-[13px] font-medium">
                {l.label}
                {!l.internal && <ArrowUpRight className="text-faint size-3" />}
              </span>
              <span className="text-faint block truncate text-xs">{l.sub}</span>
            </span>
          </>
        )
        const cls = "group hover:border-border-strong flex items-center gap-3 rounded-xl border px-4 py-3 transition-colors"
        return l.internal ? (
          <Link key={l.label} href={l.href} className={cls}>
            {body}
          </Link>
        ) : (
          <a key={l.label} href={l.href} target="_blank" rel="noreferrer" className={cls}>
            {body}
          </a>
        )
      })}
    </div>
  )
}
