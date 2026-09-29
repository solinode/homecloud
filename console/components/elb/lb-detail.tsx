"use client"

import { useState } from "react"
import Link from "next/link"
import { useRouter } from "next/navigation"
import { AlertCircle, ArrowLeft, ArrowRight, ExternalLink, FileCode, Loader2, Plus, Trash2 } from "lucide-react"
import { toast } from "sonner"

import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert"
import { Button } from "@/components/ui/button"
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs"
import { ConfirmDialog } from "@/components/console/confirm-dialog"
import { CopyButton, CopyableText } from "@/components/console/copy-button"
import { EmptyState } from "@/components/console/empty-state"
import { ErrorState } from "@/components/console/error-state"
import { KeyValueGrid } from "@/components/console/key-value"
import { DetailSkeleton } from "@/components/console/loading"
import { CONTAINER_CHARTS, MetricsPanel } from "@/components/console/metrics-panel"
import { PageHeader } from "@/components/console/page-header"
import { Section } from "@/components/console/section"
import { TagList } from "@/components/console/tags-editor"
import { TimeAgo } from "@/components/console/time-ago"
import { ApiError, api, seg } from "@/lib/api"
import { formatDate, pluralize } from "@/lib/format"
import { revalidate, useApi, useQueryParam, useSetQueryParam } from "@/lib/hooks"
import type { ElbListener, ElbRule, LoadBalancer, LoadBalancerConfig } from "@/lib/types"
import { DeleteLoadBalancerDialog } from "./lb-list"
import { AddListenerDialog, AddRuleDialog, ReprovisionWarning } from "./listener-dialogs"
import {
  ELB_PREFIX,
  LBS_PATH,
  LbPublicPorts,
  LbStateBadge,
  TgLink,
  isLbTransitional,
  lbPollInterval,
  listenerPublicPort,
  publicUrl,
  subnetsHref,
  vpcHref,
} from "./shared"

const TABS = ["details", "listeners", "config", "monitoring"] as const
type Tab = (typeof TABS)[number]

export function LoadBalancerDetail() {
  const router = useRouter()
  const name = useQueryParam("name")
  const tabParam = useQueryParam("tab")
  const setParam = useSetQueryParam()
  const tab: Tab = (TABS as readonly string[]).includes(tabParam) ? (tabParam as Tab) : "details"

  const { data: lb, error, isLoading, isValidating, mutate } = useApi<LoadBalancer>(name ? `${LBS_PATH}/${seg(name)}` : null, {
    refreshInterval: (d) => lbPollInterval(d ? [d.state] : []),
  })
  const [deleting, setDeleting] = useState(false)

  const crumbs = [{ label: "ELB", href: "/elb/" }, { label: "Load balancers", href: "/elb/" }, { label: name || "Load balancer" }]

  if (!name) {
    return (
      <>
        <PageHeader title="Load balancer" breadcrumbs={crumbs} />
        <EmptyState title="No load balancer selected" description="Open a load balancer from the list." action={<BackButton />} />
      </>
    )
  }
  if (error && !lb) {
    const notFound = error instanceof ApiError && error.status === 404
    return (
      <div className="flex flex-col gap-4">
        <PageHeader title={name} breadcrumbs={crumbs} />
        {notFound ? (
          <Section>
            <EmptyState icon={AlertCircle} title="Load balancer not found" description={`Load balancer ${name} does not exist.`} action={<BackButton />} />
          </Section>
        ) : (
          <ErrorState error={error} onRetry={() => mutate()} />
        )}
      </div>
    )
  }
  if (isLoading || !lb) return <DetailSkeleton />

  return (
    <div className="flex flex-col gap-4">
      <PageHeader
        title={lb.name}
        badge={<LbStateBadge state={lb.state} />}
        description={
          <span>
            Application load balancer · {lb.scheme === "internal" ? "Internal" : "Internet-facing"} · <span className="font-mono text-[13px]">{lb.dns_name}</span>
          </span>
        }
        breadcrumbs={crumbs}
        actions={
          <>
            <Button variant="outline" size="sm" onClick={() => mutate()} disabled={isValidating} aria-label="Refresh">
              {isValidating ? <Loader2 className="animate-spin" /> : null}
              Refresh
            </Button>
            <Button variant="outline" size="sm" onClick={() => setParam("tab", "config")}>
              <FileCode /> View generated config
            </Button>
            <Button variant="outline" size="sm" className="text-destructive" onClick={() => setDeleting(true)}>
              <Trash2 /> Delete
            </Button>
          </>
        }
      />

      {lb.state === "failed" && lb.state_reason && (
        <Alert variant="destructive">
          <AlertCircle />
          <AlertTitle>Load balancer failed</AlertTitle>
          <AlertDescription>
            <span className="font-mono text-[13px] break-all">{lb.state_reason}</span>
          </AlertDescription>
        </Alert>
      )}
      {isLbTransitional(lb.state) && (
        <p className="text-muted-foreground flex items-center gap-2 text-sm">
          <Loader2 className="size-4 animate-spin" /> The load balancer is being provisioned. This page refreshes automatically.
        </p>
      )}

      <Tabs value={tab} onValueChange={(v) => setParam("tab", v === "details" ? null : v)}>
        <TabsList>
          <TabsTrigger value="details">Details</TabsTrigger>
          <TabsTrigger value="listeners">Listeners and rules ({lb.listeners.length})</TabsTrigger>
          <TabsTrigger value="config">Generated config</TabsTrigger>
          <TabsTrigger value="monitoring">Monitoring</TabsTrigger>
        </TabsList>
        <TabsContent value="details">
          <DetailsTab lb={lb} />
        </TabsContent>
        <TabsContent value="listeners">
          <ListenersTab lb={lb} />
        </TabsContent>
        <TabsContent value="config">
          <ConfigTab lb={lb} />
        </TabsContent>
        <TabsContent value="monitoring">
          <MetricsPanel
            namespace="HC/ELB"
            dimensions={{ LoadBalancer: lb.name }}
            charts={CONTAINER_CHARTS}
            note={<p className="text-muted-foreground text-xs">Resource usage of the load balancer&apos;s nginx container, sampled every minute.</p>}
          />
        </TabsContent>
      </Tabs>

      <DeleteLoadBalancerDialog lb={deleting ? lb : null} onClose={() => setDeleting(false)} onDeleted={() => router.push("/elb/")} />
    </div>
  )
}

function BackButton() {
  return (
    <Button variant="outline" size="sm" asChild>
      <Link href="/elb/">
        <ArrowLeft /> Back to load balancers
      </Link>
    </Button>
  )
}

function DetailsTab({ lb }: { lb: LoadBalancer }) {
  return (
    <div className="flex flex-col gap-4">
      <Section title="Load balancer summary">
        <KeyValueGrid
          columns={3}
          items={[
            { label: "Name", value: lb.name },
            { label: "State", value: <LbStateBadge state={lb.state} /> },
            { label: "Scheme", value: lb.scheme === "internal" ? "Internal" : "Internet-facing" },
            { label: "DNS name", value: <CopyableText value={lb.dns_name} /> },
            { label: "Private IPv4 address", value: lb.private_ip ? <CopyableText value={lb.private_ip} /> : "" },
            { label: "Type", value: "Application (HTTP)" },
            {
              label: "VPC",
              value: (
                <Link href={vpcHref(lb.vpc_id)} className="text-primary font-mono text-[13px] hover:underline">
                  {lb.vpc_id}
                </Link>
              ),
            },
            {
              label: "Subnet",
              value: (
                <Link href={subnetsHref(lb.vpc_id)} className="text-primary font-mono text-[13px] hover:underline">
                  {lb.subnet_id}
                </Link>
              ),
            },
            { label: "Created", value: <span>{formatDate(lb.created_at)} (<TimeAgo value={lb.created_at} />)</span> },
            { label: "Public host", value: lb.scheme === "internet-facing" && lb.public_host ? <CopyableText value={lb.public_host} /> : "" },
            {
              label: "Public endpoints",
              value: (
                <LbPublicPorts
                  lb={lb}
                  empty={lb.scheme === "internal" ? "None (internal load balancer)" : lb.state === "active" ? "None" : "Published once the load balancer is active"}
                />
              ),
            },
            { label: "Container ID", value: lb.container_id ? <CopyableText value={lb.container_id} display={lb.container_id.slice(0, 12)} /> : "" },
            { label: "ARN", value: <CopyableText value={lb.arn} />, wide: true },
          ]}
        />
      </Section>
      <Section title="In-VPC endpoints" description="Instances, tasks and functions in the same VPC reach the load balancer by its DNS name.">
        <ul className="flex flex-col gap-1">
          {lb.listeners.map((l) => (
            <li key={l.id}>
              <CopyableText value={`http://${lb.dns_name}${l.port === 80 ? "" : `:${l.port}`}`} />
            </li>
          ))}
        </ul>
      </Section>
      <Section title="Tags">
        <TagList tags={lb.tags ?? undefined} />
      </Section>
    </div>
  )
}

function ListenersTab({ lb }: { lb: LoadBalancer }) {
  const [adding, setAdding] = useState(false)
  const [ruleFor, setRuleFor] = useState<ElbListener | null>(null)
  const [deletingListener, setDeletingListener] = useState<ElbListener | null>(null)
  const [deletingRule, setDeletingRule] = useState<{ listener: ElbListener; rule: ElbRule } | null>(null)
  const busy = isLbTransitional(lb.state)

  const listeners = [...lb.listeners].sort((a, b) => a.port - b.port)

  return (
    <div className="flex flex-col gap-4">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <p className="text-muted-foreground text-sm">
          {pluralize(lb.listeners.length, "listener")}. Rules are applied live; adding or deleting a listener re-provisions the load balancer.
        </p>
        <Button size="sm" onClick={() => setAdding(true)} disabled={busy}>
          <Plus /> Add listener
        </Button>
      </div>

      {listeners.map((l) => {
        const pub = listenerPublicPort(lb, l)
        const rules = [...(l.rules ?? [])].sort((a, b) => a.priority - b.priority)
        return (
          <Section
            key={l.id}
            flush
            title={
              <span className="flex flex-wrap items-center gap-x-2 gap-y-1">
                <span className="font-mono">
                  {l.protocol}:{l.port}
                </span>
                {pub ? (
                  <a
                    href={publicUrl(lb, pub)}
                    target="_blank"
                    rel="noreferrer"
                    className="text-primary inline-flex items-center gap-1 font-mono text-[13px] font-normal hover:underline"
                  >
                    <ArrowRight className="text-muted-foreground size-3.5" />
                    {publicUrl(lb, pub).replace("http://", "")}
                    <ExternalLink className="size-3" />
                  </a>
                ) : l.public_port && lb.scheme === "internet-facing" ? (
                  <span className="text-muted-foreground text-xs font-normal">host port {l.public_port} (not published yet)</span>
                ) : null}
              </span>
            }
            description={<span className="font-mono text-xs">Listener {l.id}</span>}
            actions={
              <div className="flex flex-wrap gap-2">
                <Button size="sm" variant="outline" onClick={() => setRuleFor(l)} disabled={busy}>
                  <Plus /> Add rule
                </Button>
                <Button
                  size="sm"
                  variant="outline"
                  className="text-destructive"
                  onClick={() => setDeletingListener(l)}
                  disabled={busy || lb.listeners.length <= 1}
                  title={lb.listeners.length <= 1 ? "A load balancer needs at least one listener" : undefined}
                >
                  <Trash2 /> Delete listener
                </Button>
              </div>
            }
          >
            <div className="overflow-x-auto">
              <table className="w-full text-sm">
                <thead>
                  <tr className="bg-muted/40 border-b">
                    <th className="text-muted-foreground w-20 px-4 py-2 text-left text-xs font-semibold">Priority</th>
                    <th className="text-muted-foreground px-3 py-2 text-left text-xs font-semibold">Conditions</th>
                    <th className="text-muted-foreground px-3 py-2 text-left text-xs font-semibold">Action</th>
                    <th className="w-12 px-4 py-2" />
                  </tr>
                </thead>
                <tbody>
                  {rules.map((r) => (
                    <tr key={r.id} className="border-b">
                      <td className="px-4 py-2 font-mono text-[13px]">{r.priority}</td>
                      <td className="px-3 py-2">
                        <div className="flex flex-col gap-0.5">
                          {r.host_header && (
                            <span className="whitespace-nowrap">
                              <span className="text-muted-foreground text-xs">Host is </span>
                              <span className="font-mono text-[13px]">{r.host_header}</span>
                            </span>
                          )}
                          {r.path_prefix && (
                            <span className="whitespace-nowrap">
                              <span className="text-muted-foreground text-xs">Path starts with </span>
                              <span className="font-mono text-[13px]">{r.path_prefix}</span>
                            </span>
                          )}
                        </div>
                      </td>
                      <td className="px-3 py-2 whitespace-nowrap">
                        <span className="text-muted-foreground text-xs">Forward to </span>
                        <TgLink name={r.target_group} />
                      </td>
                      <td className="px-4 py-1.5 text-right">
                        <Button
                          size="icon"
                          variant="ghost"
                          className="size-8"
                          onClick={() => setDeletingRule({ listener: l, rule: r })}
                          disabled={busy}
                          aria-label={`Delete rule ${r.priority}`}
                        >
                          <Trash2 />
                        </Button>
                      </td>
                    </tr>
                  ))}
                  <tr className="bg-muted/20">
                    <td className="text-muted-foreground px-4 py-2 text-xs">Default</td>
                    <td className="text-muted-foreground px-3 py-2 text-xs">{rules.length ? "If no other rule applies" : "All requests"}</td>
                    <td className="px-3 py-2 whitespace-nowrap">
                      <span className="text-muted-foreground text-xs">Forward to </span>
                      <TgLink name={l.default_target_group} />
                    </td>
                    <td />
                  </tr>
                </tbody>
              </table>
            </div>
          </Section>
        )
      })}

      <AddListenerDialog lb={lb} open={adding} onOpenChange={setAdding} />
      <AddRuleDialog lb={lb} listener={ruleFor} onClose={() => setRuleFor(null)} />
      <ConfirmDialog
        open={!!deletingListener}
        onOpenChange={(o) => !o && setDeletingListener(null)}
        title={`Delete listener ${deletingListener?.protocol ?? "HTTP"}:${deletingListener?.port ?? ""}?`}
        description={
          <div className="flex flex-col gap-3">
            <p>
              The listener and its {pluralize(deletingListener?.rules?.length ?? 0, "rule")} are removed. Target groups are kept.
            </p>
            <ReprovisionWarning lb={lb} />
          </div>
        }
        onConfirm={async () => {
          if (!deletingListener) return
          await api.del(`${LBS_PATH}/${seg(lb.name)}/listeners/${seg(deletingListener.id)}`)
          toast.success(`Deleted listener HTTP:${deletingListener.port}; re-provisioning ${lb.name}`)
          await revalidate(ELB_PREFIX)
        }}
      />
      <ConfirmDialog
        open={!!deletingRule}
        onOpenChange={(o) => !o && setDeletingRule(null)}
        title="Delete rule?"
        description={
          deletingRule && (
            <p>
              Requests matching{" "}
              <span className="text-foreground font-mono">
                {[deletingRule.rule.host_header && `host ${deletingRule.rule.host_header}`, deletingRule.rule.path_prefix && `path ${deletingRule.rule.path_prefix}`]
                  .filter(Boolean)
                  .join(" + ")}
              </span>{" "}
              will go to the listener&apos;s default target group <span className="text-foreground font-medium">{deletingRule.listener.default_target_group}</span>{" "}
              instead of <span className="text-foreground font-medium">{deletingRule.rule.target_group}</span>. Applied without downtime.
            </p>
          )
        }
        onConfirm={async () => {
          if (!deletingRule) return
          await api.del(`${LBS_PATH}/${seg(lb.name)}/listeners/${seg(deletingRule.listener.id)}/rules/${seg(deletingRule.rule.id)}`)
          toast.success("Deleted rule")
          await revalidate(ELB_PREFIX)
        }}
      />
    </div>
  )
}

function ConfigTab({ lb }: { lb: LoadBalancer }) {
  const { data, error, isLoading, isValidating, mutate } = useApi<LoadBalancerConfig>(`${LBS_PATH}/${seg(lb.name)}/config`, { refreshInterval: 15_000 })
  return (
    <Section
      title="Generated nginx configuration"
      description="Rendered by HomeCloud from the listeners, rules and healthy targets. Read-only: it is regenerated whenever routing or target health changes."
      actions={
        <div className="flex gap-2">
          <Button size="sm" variant="outline" onClick={() => mutate()} disabled={isValidating}>
            {isValidating && <Loader2 className="animate-spin" />}
            Refresh
          </Button>
          {data && <CopyButton value={data.nginx_conf} size="sm" label="Copy" toastMessage="Configuration copied" />}
        </div>
      }
    >
      {error ? (
        <ErrorState error={error} onRetry={() => mutate()} />
      ) : isLoading || !data ? (
        <div className="bg-muted/50 h-64 animate-pulse rounded-md border" />
      ) : (
        <pre className="bg-muted/50 max-h-[70vh] overflow-auto rounded-md border p-3 font-mono text-[12.5px] leading-relaxed">{data.nginx_conf}</pre>
      )}
    </Section>
  )
}
