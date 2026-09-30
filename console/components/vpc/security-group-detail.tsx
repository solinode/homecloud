"use client"

import { useEffect, useState } from "react"
import Link from "next/link"
import { useRouter } from "next/navigation"
import { Info, Plus, ShieldOff, Trash2 } from "lucide-react"
import { toast } from "sonner"

import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert"
import { Button } from "@/components/ui/button"
import { ConfirmDialog } from "@/components/console/confirm-dialog"
import { CopyableText } from "@/components/console/copy-button"
import { cellLinkClass } from "@/components/console/data-table"
import { EmptyState } from "@/components/console/empty-state"
import { ErrorState } from "@/components/console/error-state"
import { KeyValueGrid } from "@/components/console/key-value"
import { DetailSkeleton } from "@/components/console/loading"
import { PageHeader } from "@/components/console/page-header"
import { Section } from "@/components/console/section"
import { StatusBadge } from "@/components/console/status-badge"
import { api, errorMessage } from "@/lib/api"
import { formatDate } from "@/lib/format"
import { revalidate, useApi, useQueryParam } from "@/lib/hooks"
import type { Instance, SecurityGroup, SecurityGroupRule } from "@/lib/types"

import { deleteErrorMessage, portRange, ruleProtocol, ruleSource, ruleType, sgHref, useVpcs, VpcLink } from "./common"
import { AddRulesDialog } from "./dialogs"

export function SecurityGroupDetail() {
  const id = useQueryParam("id")
  const wantAdd = useQueryParam("add") === "1"
  const router = useRouter()
  const { data: g, error, isLoading, mutate } = useApi<SecurityGroup>(id ? `/api/v1/vpc/security-groups/${encodeURIComponent(id)}` : null)
  const vpcs = useVpcs()
  const instances = useApi<Instance[]>("/api/v1/ec2/instances")
  const [addOpen, setAddOpen] = useState(false)
  const [removeRule, setRemoveRule] = useState<SecurityGroupRule | null>(null)
  const [deleteOpen, setDeleteOpen] = useState(false)

  useEffect(() => {
    if (wantAdd && g) setAddOpen(true)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [wantAdd, !!g])

  if (!id) return <ErrorState error={new Error("No security group ID given. Open a group from the security groups list.")} />
  if (isLoading && !g) return <DetailSkeleton />
  if (error || !g)
    return (
      <div className="flex flex-col gap-4">
        <PageHeader breadcrumbs={[{ label: "VPC", href: "/vpc/" }, { label: "Security groups", href: "/vpc/security-groups/" }, { label: id }]} title={id} />
        <ErrorState error={error ?? new Error("Security group not found")} onRetry={() => mutate()} />
      </div>
    )

  const vpc = vpcs.data?.find((v) => v.id === g.vpc_id)
  const users = (instances.data ?? []).filter((i) => i.state !== "terminated" && i.security_groups?.includes(g.id))

  const onRemoveRule = async () => {
    if (!removeRule) return
    await api.del(`/api/v1/vpc/security-groups/${encodeURIComponent(g.id)}/ingress/${encodeURIComponent(removeRule.id)}`)
    toast.success("Inbound rule removed")
    mutate()
    revalidate("/api/v1/vpc/security-groups")
  }

  const onDelete = async () => {
    try {
      await api.del(`/api/v1/vpc/security-groups/${encodeURIComponent(g.id)}`)
    } catch (err) {
      throw new Error(deleteErrorMessage(err, "security group"))
    }
    toast.success(`Security group ${g.name} deleted`)
    revalidate("/api/v1/vpc")
    router.push("/vpc/security-groups/")
  }

  return (
    <div className="flex flex-col gap-5">
      <PageHeader
        breadcrumbs={[{ label: "VPC", href: "/vpc/" }, { label: "Security groups", href: "/vpc/security-groups/" }, { label: g.id }]}
        title={
          <>
            {g.name} <span className="text-muted-foreground font-mono text-base font-normal">{g.id}</span>
          </>
        }
        actions={
          <>
            <Button
              variant="outline"
              size="sm"
              disabled={g.name === "default"}
              title={g.name === "default" ? "The default security group cannot be deleted" : undefined}
              onClick={() => setDeleteOpen(true)}
            >
              <Trash2 /> Delete
            </Button>
            <Button size="sm" onClick={() => setAddOpen(true)}>
              <Plus /> Add inbound rule
            </Button>
          </>
        }
      />

      <Section title="Details">
        <KeyValueGrid
          items={[
            { label: "Security group name", value: g.name },
            { label: "Security group ID", value: <CopyableText value={g.id} /> },
            { label: "VPC", value: <VpcLink id={g.vpc_id} vpcs={vpcs.data} /> },
            { label: "Description", value: g.description },
            { label: "Inbound rules", value: String(g.ingress.length) },
            { label: "Created", value: formatDate(g.created_at) },
          ]}
        />
      </Section>

      <Alert className="border-blue-600/30 bg-blue-50 text-blue-900 dark:border-blue-400/30 dark:bg-blue-500/10 dark:text-blue-100">
        <Info />
        <AlertTitle>How inbound rules work in HomeCloud</AlertTitle>
        <AlertDescription className="text-blue-900/80 dark:text-blue-100/80">
          <p>
            Inbound rules are enforced between the resources of a VPC: a rule allows traffic from an IPv4 CIDR or from every resource that has another security
            group. Changes apply to running resources within seconds.
          </p>
          <p>
            Rules from 0.0.0.0/0 (or 127.0.0.1/32) also publish their TCP/UDP ports on the host; Docker assigns each a host port, shown on the instance&apos;s
            details page.
          </p>
        </AlertDescription>
      </Alert>

      <Section
        flush
        title={`Inbound rules (${g.ingress.length})`}
        actions={
          <Button size="sm" variant="outline" onClick={() => setAddOpen(true)}>
            <Plus /> Edit inbound rules
          </Button>
        }
      >
        {g.ingress.length === 0 ? (
          <EmptyState
            icon={ShieldOff}
            title="No inbound rules"
            description="No ports are published for instances using this group."
            action={
              <Button size="sm" onClick={() => setAddOpen(true)}>
                <Plus /> Add rule
              </Button>
            }
          />
        ) : (
          <div className="overflow-x-auto">
            <table className="w-full text-sm">
              <thead>
                <tr className="bg-muted/40 text-muted-foreground border-b text-left text-xs">
                  <th className="px-4 py-2 font-semibold">Rule ID</th>
                  <th className="px-3 py-2 font-semibold">Type</th>
                  <th className="px-3 py-2 font-semibold">Protocol</th>
                  <th className="px-3 py-2 font-semibold">Port range</th>
                  <th className="px-3 py-2 font-semibold">Source</th>
                  <th className="hidden px-3 py-2 font-semibold md:table-cell">Description</th>
                  <th className="px-4 py-2" />
                </tr>
              </thead>
              <tbody>
                {g.ingress.map((r) => (
                  <tr key={r.id} className="hover:bg-muted/40 border-b last:border-0">
                    <td className="px-4 py-2 font-mono text-[13px]">{r.id}</td>
                    <td className="px-3 py-2">{ruleType(r)}</td>
                    <td className="px-3 py-2">{ruleProtocol(r)}</td>
                    <td className="px-3 py-2 font-mono text-[13px]">{portRange(r)}</td>
                    <td className="px-3 py-2 font-mono text-[13px]">
                      {r.source_group ? (
                        <Link href={sgHref(r.source_group)} className={cellLinkClass()}>
                          {r.source_group === g.id ? `${r.source_group} (this group)` : r.source_group}
                        </Link>
                      ) : (
                        r.cidr
                      )}
                    </td>
                    <td className="text-muted-foreground hidden px-3 py-2 md:table-cell">{r.description || "-"}</td>
                    <td className="px-4 py-2 text-right">
                      <Button variant="ghost" size="sm" className="text-destructive hover:text-destructive" onClick={() => setRemoveRule(r)}>
                        <Trash2 /> Remove
                      </Button>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </Section>

      <Section title="Outbound rules">
        {vpc && !vpc.internet_access ? (
          <p className="text-sm">
            <StatusBadge status="isolated" tone="warning" label="Internet blocked" />{" "}
            <span className="text-muted-foreground">
              VPC {vpc.name || vpc.id} is isolated: outbound traffic to the internet is blocked. Traffic inside the VPC is allowed.
            </span>
          </p>
        ) : (
          <p className="text-sm">
            <StatusBadge status="allowed" label="All traffic allowed" />{" "}
            <span className="text-muted-foreground">All outbound traffic is allowed (destination 0.0.0.0/0, all protocols and ports).</span>
          </p>
        )}
      </Section>

      <Section flush title={`Instances using this group (${users.length})`}>
        {instances.error ? (
          <p className="text-muted-foreground p-4 text-sm">{errorMessage(instances.error)}</p>
        ) : users.length === 0 ? (
          <p className="text-muted-foreground p-4 text-sm">{instances.isLoading ? "Loading..." : "No instances use this security group."}</p>
        ) : (
          <div className="overflow-x-auto">
            <table className="w-full text-sm">
              <thead>
                <tr className="bg-muted/40 text-muted-foreground border-b text-left text-xs">
                  <th className="px-4 py-2 font-semibold">Instance</th>
                  <th className="px-3 py-2 font-semibold">Name</th>
                  <th className="px-3 py-2 font-semibold">State</th>
                  <th className="px-3 py-2 font-semibold">Private IP</th>
                  <th className="hidden px-4 py-2 font-semibold md:table-cell">Published ports</th>
                </tr>
              </thead>
              <tbody>
                {users.map((i) => (
                  <tr key={i.id} className="border-b last:border-0">
                    <td className="px-4 py-2">
                      <Link href={`/ec2/instance/?id=${encodeURIComponent(i.id)}`} className={`${cellLinkClass()} font-mono text-[13px]`}>
                        {i.id}
                      </Link>
                    </td>
                    <td className="px-3 py-2">{i.name || "-"}</td>
                    <td className="px-3 py-2">
                      <StatusBadge status={i.state} />
                    </td>
                    <td className="px-3 py-2 font-mono text-[13px]">{i.private_ip || "-"}</td>
                    <td className="hidden px-4 py-2 font-mono text-[13px] md:table-cell">
                      {Object.entries(i.public_ports ?? {})
                        .map(([k, v]) => `${k} -> ${v}`)
                        .join(", ") || "-"}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </Section>

      <AddRulesDialog
        open={addOpen}
        onOpenChange={(o) => {
          setAddOpen(o)
          if (!o) mutate()
        }}
        group={g}
      />
      <ConfirmDialog
        open={!!removeRule}
        onOpenChange={(o) => !o && setRemoveRule(null)}
        title="Remove inbound rule?"
        description={
          removeRule && (
            <>
              {ruleType(removeRule)} ({ruleProtocol(removeRule)} {portRange(removeRule)}) from <span className="font-mono">{ruleSource(removeRule)}</span> will be removed.
              Traffic it allowed stops for new connections; a published port is closed when the instance is next recreated.
            </>
          )
        }
        actionLabel="Remove rule"
        onConfirm={onRemoveRule}
      />
      <ConfirmDialog
        open={deleteOpen}
        onOpenChange={setDeleteOpen}
        title={`Delete security group ${g.name}?`}
        description={users.length ? `${users.length} instance(s) still use this group, so deletion will fail until they are terminated.` : "This cannot be undone."}
        actionLabel="Delete"
        onConfirm={onDelete}
      />
    </div>
  )
}
