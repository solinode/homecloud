"use client"

import Link from "next/link"
import type { ReactNode } from "react"
import {
  ArrowRight,
  BadgeCheck,
  Bell,
  Cable,
  Container,
  Cpu,
  Database,
  FolderOpen,
  FunctionSquare,
  Globe,
  HardDrive,
  KeyRound,
  Layers,
  ListOrdered,
  Megaphone,
  Network,
  Package,
  Scaling,
  ScrollText,
  ShieldCheck,
  Split,
  Table2,
  Terminal,
  Upload,
  UserPlus,
  UsersRound,
  Workflow,
} from "lucide-react"

import { Skeleton } from "@/components/ui/skeleton"
import { useSession } from "@/components/console/auth"
import { KeyValueGrid } from "@/components/console/key-value"
import { PageHeader } from "@/components/console/page-header"
import { Section } from "@/components/console/section"
import { StatusBadge } from "@/components/console/status-badge"
import { TimeAgo } from "@/components/console/time-ago"
import { CopyableText } from "@/components/console/copy-button"
import { formatDuration, pluralize } from "@/lib/format"
import { useApi } from "@/lib/hooks"
import { SERVICES, servicesByCategory } from "@/lib/services"
import type {
  Alarm,
  AutoScalingGroup,
  Certificate,
  HostedZoneSummary,
  UserPool,
  Bucket,
  DbInstance,
  DynamoTable,
  EcrRepository,
  EcsService,
  EventRule,
  FileSystem,
  Health,
  IamSummary,
  Instance,
  LambdaFunction,
  LoadBalancer,
  Queue,
  Secret,
  StateMachineSummary,
  Topic,
  TrailEvent,
  Vpc,
} from "@/lib/types"
import { cn } from "@/lib/utils"

function Tile({
  href,
  icon: Icon,
  color,
  service,
  value,
  sub,
  loading,
  error,
}: {
  href: string
  icon: typeof Cpu
  color: string
  service: string
  value: ReactNode
  sub: ReactNode
  loading: boolean
  error?: boolean
}) {
  return (
    <Link href={href} className="bg-card group flex flex-col gap-3 rounded-lg border p-4 shadow-xs transition-shadow hover:shadow-md">
      <div className="flex items-center justify-between">
        <span className="flex items-center gap-2 text-sm font-medium">
          <span className={cn("flex size-7 items-center justify-center rounded-md", color)}>
            <Icon className="size-4" />
          </span>
          {service}
        </span>
        <ArrowRight className="text-muted-foreground size-4 opacity-0 transition-opacity group-hover:opacity-100" />
      </div>
      {loading ? (
        <Skeleton className="h-8 w-20" />
      ) : error ? (
        <span className="text-muted-foreground text-sm">Unavailable</span>
      ) : (
        <span className="text-3xl font-semibold tracking-tight tabular-nums">{value}</span>
      )}
      <span className="text-muted-foreground text-xs">{sub}</span>
    </Link>
  )
}

const QUICK_LINKS = [
  { href: "/ec2/launch/", label: "Launch an instance", icon: Cpu },
  { href: "/ec2/autoscaling/create/", label: "Create an Auto Scaling group", icon: Scaling },
  { href: "/lambda/create/", label: "Create a function", icon: FunctionSquare },
  { href: "/rds/create/", label: "Create a database", icon: Database },
  { href: "/sqs/create/", label: "Create a queue", icon: ListOrdered },
  { href: "/cloudformation/create/", label: "Create a stack", icon: Layers },
  { href: "/s3/?create=1", label: "Create a bucket", icon: Upload },
  { href: "/iam/users/?create=1", label: "Add an IAM user", icon: UserPlus },
  { href: "/secrets/create/", label: "Store a secret", icon: KeyRound },
  { href: "/route53/?create=1", label: "Create a hosted zone", icon: Globe },
  { href: "/acm/?request=1", label: "Request a certificate", icon: BadgeCheck },
  { href: "/cognito/?create=1", label: "Create a user pool", icon: UsersRound },
  { href: "/cloudwatch/logs/", label: "View logs", icon: Terminal },
  { href: "/cloudwatch/alarms/?create=1", label: "Create an alarm", icon: Bell },
]

export function ConsoleHome() {
  const session = useSession()
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
  const events = useApi<TrailEvent[]>("/api/v1/cloudtrail/events", { query: { limit: 10 }, refreshInterval: 30_000 })

  const live = (instances.data ?? []).filter((i) => i.state !== "terminated")
  const running = live.filter((i) => i.state === "running").length
  const inAlarm = (alarms.data ?? []).filter((a) => a.state === "ALARM").length
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
  const certsExpiring = (certs.data ?? []).filter((c) => new Date(c.not_after).getTime() - Date.now() < 30 * 86_400_000).length
  const asgInstances = (asgs.data ?? []).reduce((n, g) => n + (g.instances ?? []).length, 0)
  const cognitoUsers = (pools.data ?? []).reduce((n, p) => n + (p.users ?? 0), 0)
  const stacksFailed = (stacks.data ?? []).filter((x) => x.status.endsWith("_FAILED") || x.status.includes("ROLLBACK")).length

  return (
    <div className="flex flex-col gap-6">
      <PageHeader
        title="Console Home"
        description={
          <>
            Welcome back, <span className="text-foreground font-medium">{session.user.name}</span>. Here is what is running in your HomeCloud.
          </>
        }
      />

      <div className="grid grid-cols-2 gap-3 sm:gap-4 lg:grid-cols-3 xl:grid-cols-6">
        <Tile
          href="/ec2/"
          icon={Cpu}
          color="bg-orange-500/10 text-orange-600 dark:text-orange-400"
          service="EC2"
          value={
            <>
              {running}
              <span className="text-muted-foreground text-lg font-normal"> / {live.length}</span>
            </>
          }
          sub="Instances running / total"
          loading={instances.isLoading}
          error={!!instances.error}
        />
        <Tile
          href="/lambda/"
          icon={FunctionSquare}
          color="bg-orange-500/10 text-orange-600 dark:text-orange-400"
          service="Lambda"
          value={functions.data?.length ?? 0}
          sub="Functions"
          loading={functions.isLoading}
          error={!!functions.error}
        />
        <Tile
          href="/s3/"
          icon={HardDrive}
          color="bg-emerald-500/10 text-emerald-600 dark:text-emerald-400"
          service="S3"
          value={buckets.data?.length ?? 0}
          sub="Buckets"
          loading={buckets.isLoading}
          error={!!buckets.error}
        />
        <Tile
          href="/rds/"
          icon={Database}
          color="bg-blue-500/10 text-blue-600 dark:text-blue-400"
          service="RDS"
          value={
            <>
              {dbAvailable}
              <span className="text-muted-foreground text-lg font-normal"> / {dbs.length}</span>
            </>
          }
          sub={`Databases available / total${caches.length ? `, ${caches.length} cache clusters` : ""}`}
          loading={databases.isLoading}
          error={!!databases.error}
        />
        <Tile
          href="/dynamodb/"
          icon={Table2}
          color="bg-blue-500/10 text-blue-600 dark:text-blue-400"
          service="DynamoDB"
          value={tables.data?.length ?? 0}
          sub={`Tables, ${items.toLocaleString()} items`}
          loading={tables.isLoading}
          error={!!tables.error}
        />
        <Tile
          href="/sqs/"
          icon={ListOrdered}
          color="bg-fuchsia-500/10 text-fuchsia-600 dark:text-fuchsia-400"
          service="SQS"
          value={queues.data?.length ?? 0}
          sub={`Queues, ${queuedMessages.toLocaleString()} messages available`}
          loading={queues.isLoading}
          error={!!queues.error}
        />
        <Tile
          href="/sns/"
          icon={Megaphone}
          color="bg-fuchsia-500/10 text-fuchsia-600 dark:text-fuchsia-400"
          service="SNS"
          value={topics.data?.length ?? 0}
          sub={`Topics, ${(topics.data ?? []).reduce((n, t) => n + (t.subscriptions ?? 0), 0)} subscriptions`}
          loading={topics.isLoading}
          error={!!topics.error}
        />
        <Tile
          href="/events/"
          icon={Cable}
          color="bg-fuchsia-500/10 text-fuchsia-600 dark:text-fuchsia-400"
          service="EventBridge"
          value={
            <>
              {enabledRules}
              <span className="text-muted-foreground text-lg font-normal"> / {rules.data?.length ?? 0}</span>
            </>
          }
          sub="Rules enabled / total"
          loading={rules.isLoading}
          error={!!rules.error}
        />
        <Tile
          href="/ecs/"
          icon={Container}
          color="bg-orange-500/10 text-orange-600 dark:text-orange-400"
          service="ECS"
          value={ecsServices.data?.length ?? 0}
          sub={`Services, ${ecsRunning} / ${ecsDesired} tasks running`}
          loading={ecsServices.isLoading}
          error={!!ecsServices.error}
        />
        <Tile
          href="/ecr/"
          icon={Package}
          color="bg-orange-500/10 text-orange-600 dark:text-orange-400"
          service="ECR"
          value={repositories.data?.length ?? 0}
          sub="Repositories"
          loading={repositories.isLoading}
          error={!!repositories.error}
        />
        <Tile
          href="/elb/"
          icon={Split}
          color="bg-violet-500/10 text-violet-600 dark:text-violet-400"
          service="ELB"
          value={loadBalancers.data?.length ?? 0}
          sub="Load balancers"
          loading={loadBalancers.isLoading}
          error={!!loadBalancers.error}
        />
        <Tile
          href="/efs/"
          icon={FolderOpen}
          color="bg-emerald-500/10 text-emerald-600 dark:text-emerald-400"
          service="EFS"
          value={fileSystems.data?.length ?? 0}
          sub="File systems"
          loading={fileSystems.isLoading}
          error={!!fileSystems.error}
        />
        <Tile
          href="/sfn/"
          icon={Workflow}
          color="bg-fuchsia-500/10 text-fuchsia-600 dark:text-fuchsia-400"
          service="Step Functions"
          value={stateMachines.data?.length ?? 0}
          sub={`State machines${runningExecutions ? `, ${runningExecutions} running` : ""}`}
          loading={stateMachines.isLoading}
          error={!!stateMachines.error}
        />
        <Tile
          href="/cloudformation/"
          icon={Layers}
          color="bg-pink-500/10 text-pink-600 dark:text-pink-400"
          service="CloudFormation"
          value={stacks.data?.length ?? 0}
          sub={`Stacks${stacksInProgress ? `, ${stacksInProgress} in progress` : ""}${stacksFailed ? `, ${stacksFailed} failed/rolled back` : ""}`}
          loading={stacks.isLoading}
          error={!!stacks.error}
        />
        <Tile
          href="/iam/users/"
          icon={ShieldCheck}
          color="bg-red-500/10 text-red-600 dark:text-red-400"
          service="IAM"
          value={iam.data?.users ?? 0}
          sub={iam.data ? `Users, ${iam.data.groups} groups, ${iam.data.policies} policies` : "Users"}
          loading={iam.isLoading}
          error={!!iam.error}
        />
        <Tile
          href="/secrets/"
          icon={KeyRound}
          color="bg-red-500/10 text-red-600 dark:text-red-400"
          service="Secrets Manager"
          value={secrets.data?.length ?? 0}
          sub="Secrets"
          loading={secrets.isLoading}
          error={!!secrets.error}
        />
        <Tile
          href="/cloudwatch/alarms/"
          icon={Bell}
          color="bg-pink-500/10 text-pink-600 dark:text-pink-400"
          service="CloudWatch"
          value={<span className={cn(inAlarm > 0 && "text-red-600 dark:text-red-400")}>{inAlarm}</span>}
          sub={`Alarms in ALARM of ${alarms.data?.length ?? 0}`}
          loading={alarms.isLoading}
          error={!!alarms.error}
        />
        <Tile
          href="/ec2/autoscaling/"
          icon={Scaling}
          color="bg-orange-500/10 text-orange-600 dark:text-orange-400"
          service="Auto Scaling"
          value={asgs.data?.length ?? 0}
          sub={`Groups, ${pluralize(asgInstances, "instance")}`}
          loading={asgs.isLoading}
          error={!!asgs.error}
        />
        <Tile
          href="/route53/"
          icon={Globe}
          color="bg-violet-500/10 text-violet-600 dark:text-violet-400"
          service="Route 53"
          value={zones.data?.length ?? 0}
          sub={`Hosted zones, ${(zones.data ?? []).filter((z) => z.private).length} private`}
          loading={zones.isLoading}
          error={!!zones.error}
        />
        <Tile
          href="/acm/"
          icon={BadgeCheck}
          color="bg-red-500/10 text-red-600 dark:text-red-400"
          service="Certificate Manager"
          value={<span className={cn(certsExpiring > 0 && "text-amber-600 dark:text-amber-400")}>{certs.data?.length ?? 0}</span>}
          sub={`Certificates${certsExpiring ? `, ${certsExpiring} expiring or expired` : ""}`}
          loading={certs.isLoading}
          error={!!certs.error}
        />
        <Tile
          href="/cognito/"
          icon={UsersRound}
          color="bg-red-500/10 text-red-600 dark:text-red-400"
          service="Cognito"
          value={pools.data?.length ?? 0}
          sub={`User pools, ${pluralize(cognitoUsers, "user")}`}
          loading={pools.isLoading}
          error={!!pools.error}
        />
        <Tile
          href="/vpc/"
          icon={Network}
          color="bg-violet-500/10 text-violet-600 dark:text-violet-400"
          service="VPC"
          value={vpcs.data?.length ?? 0}
          sub="VPCs"
          loading={vpcs.isLoading}
          error={!!vpcs.error}
        />
      </div>

      <div className="grid grid-cols-1 gap-6 xl:grid-cols-3">
        <Section
          className="xl:col-span-2"
          flush
          title="Recent activity"
          description="The last 10 events recorded by CloudTrail"
          actions={
            <Link href="/cloudtrail/" className="text-primary text-sm hover:underline">
              View event history
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
            <p className="text-muted-foreground p-8 text-center text-sm">No activity yet</p>
          ) : (
            <div className="overflow-x-auto">
              <table className="w-full text-sm">
                <thead>
                  <tr className="bg-muted/40 text-muted-foreground border-b text-left text-xs">
                    <th className="px-4 py-2 font-semibold">Event</th>
                    <th className="px-3 py-2 font-semibold">User</th>
                    <th className="hidden px-3 py-2 font-semibold md:table-cell">Resource</th>
                    <th className="px-3 py-2 font-semibold">Result</th>
                    <th className="px-4 py-2 text-right font-semibold">Time</th>
                  </tr>
                </thead>
                <tbody>
                  {events.data.map((e) => (
                    <tr key={e.id} className="border-b last:border-0">
                      <td className="px-4 py-2 font-mono text-[13px]">{e.action}</td>
                      <td className="px-3 py-2">{e.user}</td>
                      <td className="text-muted-foreground hidden max-w-72 truncate px-3 py-2 font-mono text-xs md:table-cell" title={e.resource}>
                        {e.resource}
                      </td>
                      <td className="px-3 py-2">
                        <StatusBadge status={e.status < 400 ? "success" : "failed"} label={String(e.status)} />
                      </td>
                      <td className="text-muted-foreground px-4 py-2 text-right">
                        <TimeAgo value={e.time} />
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}
        </Section>

        <div className="flex flex-col gap-6">
          <Section title="HomeCloud">
            {health.isLoading ? (
              <Skeleton className="h-24 w-full" />
            ) : (
              <KeyValueGrid
                columns={2}
                items={[
                  {
                    label: "API status",
                    value: health.error ? <StatusBadge status="error" label="Unreachable" /> : <StatusBadge status={health.data?.status ?? "ok"} tone="success" label="Healthy" />,
                  },
                  { label: "Version", value: health.data?.version },
                  { label: "Region", value: <span className="font-mono">{health.data?.region ?? "us-east-1"}</span> },
                  { label: "Uptime", value: health.data ? formatDuration(health.data.uptime_seconds) : "-" },
                  { label: "Account ID", value: <CopyableText value={session.account_id} /> },
                  { label: "Signed in as", value: session.user.name },
                ]}
              />
            )}
          </Section>
          <Section title="Quick links">
            <ul className="grid grid-cols-1 gap-1 sm:grid-cols-2 xl:grid-cols-1">
              {QUICK_LINKS.map((q) => (
                <li key={q.href}>
                  <Link href={q.href} className="hover:bg-accent flex items-center gap-2 rounded-md px-2 py-1.5 text-sm">
                    <q.icon className="text-muted-foreground size-4" />
                    <span className="text-primary">{q.label}</span>
                  </Link>
                </li>
              ))}
            </ul>
          </Section>
        </div>
      </div>

      <Section title="All services">
        <div className="grid grid-cols-1 gap-x-6 gap-y-5 sm:grid-cols-2 lg:grid-cols-4">
          {servicesByCategory(SERVICES).map((g) => (
            <div key={g.category}>
              <p className="text-muted-foreground mb-2 text-xs font-semibold tracking-wide uppercase">{g.category}</p>
              <ul className="flex flex-col gap-1">
                {g.services.map((s) => (
                  <li key={s.id}>
                    {s.comingSoon ? (
                      <span className="text-muted-foreground flex items-center gap-2 text-sm">
                        <s.icon className="size-4 opacity-60" /> {s.name}
                        <span className="bg-muted rounded px-1.5 text-[10px] font-medium uppercase">Soon</span>
                      </span>
                    ) : (
                      <Link href={s.href} className="text-primary flex items-center gap-2 text-sm hover:underline">
                        <s.icon className="size-4" /> {s.name}
                      </Link>
                    )}
                  </li>
                ))}
              </ul>
            </div>
          ))}
        </div>
      </Section>
      <p className="text-muted-foreground flex items-center gap-1.5 text-xs">
        <ScrollText className="size-3.5" /> Every change you make in the console is recorded in CloudTrail.
      </p>
    </div>
  )
}
