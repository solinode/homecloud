"use client"

import { useEffect, useState } from "react"
import Link from "next/link"
import { AlertCircle, ArrowLeft, Info, Loader2, TerminalSquare } from "lucide-react"
import { toast } from "sonner"

import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert"
import { Button } from "@/components/ui/button"
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog"
import { Input } from "@/components/ui/input"
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs"
import { ActionsMenu, type ActionItem } from "@/components/console/actions-menu"
import { CopyableText } from "@/components/console/copy-button"
import { EmptyState } from "@/components/console/empty-state"
import { ErrorState } from "@/components/console/error-state"
import { Field } from "@/components/console/form-field"
import { KeyValueGrid } from "@/components/console/key-value"
import { DetailSkeleton } from "@/components/console/loading"
import { PageHeader } from "@/components/console/page-header"
import { Section } from "@/components/console/section"
import { StatusBadge } from "@/components/console/status-badge"
import { TagList, TagsEditor, rowsToTags, tagsToRows, type TagRow } from "@/components/console/tags-editor"
import { TimeAgo } from "@/components/console/time-ago"
import { ApiError, api, errorMessage, seg } from "@/lib/api"
import { formatDate, formatMemoryMB } from "@/lib/format"
import { revalidate, useApi, useQueryParam, useSetQueryParam } from "@/lib/hooks"
import { ASG_GROUP_TAG } from "@/lib/types"
import type { Instance } from "@/lib/types"
import { INSTANCES_PATH, PublicPorts, instanceLabel, isTransitional, pollInterval, useInstanceActions } from "./instance-actions"
import { InstanceConsoleOutput } from "./instance-console-output"
import { InstanceMonitoring } from "./instance-monitoring"
import { InstanceRunCommand } from "./instance-run-command"
import { InstanceTerminal } from "./instance-terminal"

const TABS = ["details", "monitoring", "console", "command", "connect"] as const
type Tab = (typeof TABS)[number]

export function InstanceDetail() {
  const id = useQueryParam("id")
  const tabParam = useQueryParam("tab")
  const setParam = useSetQueryParam()
  const tab: Tab = (TABS as readonly string[]).includes(tabParam) ? (tabParam as Tab) : "details"

  const { data: inst, error, isLoading, isValidating, mutate } = useApi<Instance>(id ? `${INSTANCES_PATH}/${seg(id)}` : null, {
    refreshInterval: (d) => pollInterval(d ? [d.state] : []),
  })
  const actions = useInstanceActions({ onTerminated: () => mutate() })
  const [renaming, setRenaming] = useState(false)
  const [tagging, setTagging] = useState(false)

  const crumbs = [{ label: "EC2", href: "/ec2/" }, { label: "Instances", href: "/ec2/" }, { label: inst ? instanceLabel(inst) : id || "Instance" }]

  if (!id) {
    return (
      <>
        <PageHeader title="Instance" breadcrumbs={crumbs} />
        <EmptyState title="No instance selected" description="Open an instance from the instances list." action={<BackButton />} />
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
            <EmptyState
              icon={AlertCircle}
              title="Instance not found"
              description={`Instance ${id} does not exist. Terminated instances are removed from the list an hour after termination.`}
              action={<BackButton />}
            />
          </Section>
        ) : (
          <ErrorState error={error} onRetry={() => mutate()} />
        )}
      </div>
    )
  }
  if (isLoading || !inst) return <DetailSkeleton />

  const s = inst.state
  const stateItems: ActionItem[] = [
    { label: "Start instance", onSelect: () => actions.start([inst.id]), disabled: actions.busy || s !== "stopped" },
    { label: "Stop instance", onSelect: () => actions.stop([inst.id]), disabled: actions.busy || s !== "running" },
    { label: "Reboot instance", onSelect: () => actions.reboot([inst.id]), disabled: actions.busy || s !== "running" },
    { separator: true },
    {
      label: "Terminate instance",
      destructive: true,
      onSelect: () => actions.terminate([inst]),
      disabled: actions.busy || s === "terminated" || s === "shutting-down",
    },
  ]
  const actionItems: ActionItem[] = [
    { heading: "Instance settings" },
    { label: "Change instance type", onSelect: () => actions.changeType(inst), disabled: s !== "stopped", hint: "Stop the instance first" },
    { label: "Rename", onSelect: () => setRenaming(true), disabled: s === "terminated" },
    { label: "Manage tags", onSelect: () => setTagging(true), disabled: s === "terminated" },
    { heading: "Image" },
    { label: "Create image", onSelect: () => actions.createImage(inst), disabled: !inst.container_id || s === "terminated" },
  ]

  return (
    <div className="flex flex-col gap-4">
      <PageHeader
        title={inst.name || inst.id}
        badge={<StatusBadge status={s} />}
        description={
          inst.name ? (
            <span className="font-mono text-[13px]">{inst.id}</span>
          ) : (
            `${inst.instance_type} · ${inst.image_ref}`
          )
        }
        breadcrumbs={crumbs}
        actions={
          <>
            <Button variant="outline" size="sm" onClick={() => mutate()} disabled={isValidating} aria-label="Refresh">
              {isValidating ? <Loader2 className="animate-spin" /> : null}
              Refresh
            </Button>
            <Button variant="outline" size="sm" disabled={s !== "running"} onClick={() => setParam("tab", "connect")}>
              <TerminalSquare /> Connect
            </Button>
            <ActionsMenu label="Instance state" items={stateItems} />
            <ActionsMenu items={actionItems} />
          </>
        }
      />

      {inst.state_reason && (
        <Alert variant={s === "terminated" && inst.state_reason.startsWith("Client.UserInitiated") ? "default" : "destructive"}>
          {s === "terminated" && inst.state_reason.startsWith("Client.UserInitiated") ? <Info /> : <AlertCircle />}
          <AlertTitle>State transition reason</AlertTitle>
          <AlertDescription>
            <span className="font-mono text-[13px]">{inst.state_reason}</span>
          </AlertDescription>
        </Alert>
      )}
      {inst.tags?.[ASG_GROUP_TAG] && s !== "terminated" && (
        <Alert>
          <Info />
          <AlertTitle>Managed by an Auto Scaling group</AlertTitle>
          <AlertDescription>
            <p>
              This instance belongs to{" "}
              <Link href={`/ec2/autoscaling/group/?name=${encodeURIComponent(inst.tags[ASG_GROUP_TAG])}`} className="text-primary font-medium hover:underline">
                {inst.tags[ASG_GROUP_TAG]}
              </Link>
              . If it stops, the group terminates and replaces it; change the group&apos;s capacity instead of terminating it directly.
            </p>
          </AlertDescription>
        </Alert>
      )}
      {isTransitional(s) && (
        <p className="text-muted-foreground flex items-center gap-2 text-sm">
          <Loader2 className="size-4 animate-spin" /> The instance is {s}. This page refreshes automatically.
        </p>
      )}

      <Tabs value={tab} onValueChange={(v) => setParam("tab", v === "details" ? null : v)}>
        <TabsList>
          <TabsTrigger value="details">Details</TabsTrigger>
          <TabsTrigger value="monitoring">Monitoring</TabsTrigger>
          <TabsTrigger value="console">Console output</TabsTrigger>
          <TabsTrigger value="command">Run command</TabsTrigger>
          <TabsTrigger value="connect">Connect</TabsTrigger>
        </TabsList>
        <TabsContent value="details">
          <DetailsTab inst={inst} />
        </TabsContent>
        <TabsContent value="monitoring">
          <InstanceMonitoring id={inst.id} />
        </TabsContent>
        <TabsContent value="console">
          <InstanceConsoleOutput instance={inst} />
        </TabsContent>
        <TabsContent value="command">
          <InstanceRunCommand instance={inst} />
        </TabsContent>
        <TabsContent value="connect">
          <InstanceTerminal instance={inst} />
        </TabsContent>
      </Tabs>

      {actions.dialogs}
      <RenameDialog inst={renaming ? inst : null} onClose={() => setRenaming(false)} />
      <TagsDialog inst={tagging ? inst : null} onClose={() => setTagging(false)} />
    </div>
  )
}

function BackButton() {
  return (
    <Button variant="outline" size="sm" asChild>
      <Link href="/ec2/">
        <ArrowLeft /> Back to instances
      </Link>
    </Button>
  )
}

function DetailsTab({ inst }: { inst: Instance }) {
  return (
    <div className="flex flex-col gap-4">
      <Section title="Instance summary">
        <KeyValueGrid
          columns={3}
          items={[
            { label: "Instance ID", value: <CopyableText value={inst.id} /> },
            { label: "Instance state", value: <StatusBadge status={inst.state} /> },
            { label: "Name", value: inst.name },
            { label: "Instance type", value: <span className="font-mono text-[13px]">{inst.instance_type}</span> },
            { label: "vCPUs", value: String(inst.vcpus) },
            { label: "Memory", value: formatMemoryMB(inst.memory_mb) },
            {
              label: "AMI ID",
              value: (
                <Link href="/ec2/images/" className="text-primary font-mono text-[13px] hover:underline">
                  {inst.image_id}
                </Link>
              ),
            },
            { label: "Image reference", value: <CopyableText value={inst.image_ref} /> },
            {
              label: "Boot mode",
              value: inst.keep_alive ? "Keep alive (VM-like: runs user data, then idles)" : "Application (runs the image's own process)",
            },
            { label: "Launch time", value: <span>{formatDate(inst.launch_time)} (<TimeAgo value={inst.launch_time} />)</span> },
            { label: "Terminated at", value: inst.terminated_at ? formatDate(inst.terminated_at) : "" },
            { label: "Container ID", value: inst.container_id ? <CopyableText value={inst.container_id} display={inst.container_id.slice(0, 12)} /> : "" },
            ...(inst.tags?.[ASG_GROUP_TAG]
              ? [
                  {
                    label: "Auto Scaling group",
                    value: (
                      <Link href={`/ec2/autoscaling/group/?name=${encodeURIComponent(inst.tags[ASG_GROUP_TAG])}`} className="text-primary hover:underline">
                        {inst.tags[ASG_GROUP_TAG]}
                      </Link>
                    ),
                  },
                ]
              : []),
            { label: "Instance ARN", value: <CopyableText value={inst.arn} />, wide: true },
          ]}
        />
      </Section>

      <Section title="Networking">
        <KeyValueGrid
          columns={3}
          items={[
            { label: "Private IPv4 address", value: inst.private_ip ? <CopyableText value={inst.private_ip} /> : "" },
            { label: "Private DNS name", value: inst.private_dns ? <CopyableText value={inst.private_dns} /> : "" },
            { label: "Public host", value: inst.public_host ? <CopyableText value={inst.public_host} /> : "" },
            {
              label: "VPC ID",
              value: (
                <Link href="/vpc/" className="text-primary font-mono text-[13px] hover:underline">
                  {inst.vpc_id}
                </Link>
              ),
            },
            {
              label: "Subnet ID",
              value: (
                <Link href="/vpc/subnets/" className="text-primary font-mono text-[13px] hover:underline">
                  {inst.subnet_id}
                </Link>
              ),
            },
            { label: "Availability zone", value: inst.availability_zone },
            {
              label: "Security groups",
              value: inst.security_groups?.length ? (
                <span className="flex flex-wrap gap-x-3 gap-y-1">
                  {inst.security_groups.map((g) => (
                    <Link key={g} href={`/vpc/security-group/?id=${encodeURIComponent(g)}`} className="text-primary font-mono text-[13px] hover:underline">
                      {g}
                    </Link>
                  ))}
                </span>
              ) : (
                ""
              ),
            },
            {
              label: "Public ports",
              wide: true,
              value: (
                <PublicPorts
                  instance={inst}
                  empty={
                    inst.state === "running"
                      ? "None published. Add inbound rules to the instance's security groups and relaunch to publish ports."
                      : "Ports are published while the instance is running."
                  }
                />
              ),
            },
          ]}
        />
      </Section>

      <Section title="Storage" flush>
        {inst.volumes?.length ? (
          <div className="overflow-x-auto">
            <table className="w-full text-sm">
              <thead>
                <tr className="bg-muted/40 border-b">
                  <th className="text-muted-foreground px-4 py-2 text-left text-xs font-semibold">Volume ID</th>
                  <th className="text-muted-foreground px-3 py-2 text-left text-xs font-semibold">Mount path</th>
                  <th className="text-muted-foreground px-4 py-2 text-left text-xs font-semibold">Delete on termination</th>
                </tr>
              </thead>
              <tbody>
                {inst.volumes.map((v) => (
                  <tr key={v.volume_id} className="border-b last:border-0">
                    <td className="px-4 py-2">
                      <Link href="/ec2/volumes/" className="text-primary font-mono text-[13px] hover:underline">
                        {v.volume_id}
                      </Link>
                    </td>
                    <td className="px-3 py-2 font-mono text-[13px]">{v.mount_path}</td>
                    <td className="px-4 py-2">{v.delete_on_termination ? "Yes" : "No"}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        ) : (
          <p className="text-muted-foreground p-4 text-sm">No additional volumes. The instance uses only its image&apos;s root filesystem.</p>
        )}
        {inst.file_systems && inst.file_systems.length > 0 && (
          <div className="overflow-x-auto border-t">
            <table className="w-full text-sm">
              <thead>
                <tr className="bg-muted/40 border-b">
                  <th className="text-muted-foreground px-4 py-2 text-left text-xs font-semibold">File system (EFS)</th>
                  <th className="text-muted-foreground px-3 py-2 text-left text-xs font-semibold">Mount path</th>
                  <th className="text-muted-foreground px-4 py-2 text-left text-xs font-semibold">Access</th>
                </tr>
              </thead>
              <tbody>
                {inst.file_systems.map((m) => (
                  <tr key={`${m.file_system_id}:${m.mount_path}`} className="border-b last:border-0">
                    <td className="px-4 py-2">
                      <Link href={`/efs/file-system/?id=${encodeURIComponent(m.file_system_id)}`} className="text-primary font-mono text-[13px] hover:underline">
                        {m.file_system_id}
                      </Link>
                    </td>
                    <td className="px-3 py-2 font-mono text-[13px]">{m.mount_path}</td>
                    <td className="px-4 py-2">{m.read_only ? "Read-only" : "Read/write"}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </Section>

      <Section title="Tags">
        <TagList tags={inst.tags} />
      </Section>

      <Section title="User data">
        {inst.user_data ? (
          <pre className="bg-muted/50 max-h-80 overflow-auto rounded-md border p-3 font-mono text-[12.5px] whitespace-pre-wrap">{inst.user_data}</pre>
        ) : (
          <p className="text-muted-foreground text-sm">No user data.</p>
        )}
      </Section>
    </div>
  )
}

function RenameDialog({ inst, onClose }: { inst: Instance | null; onClose: () => void }) {
  const [name, setName] = useState("")
  const [pending, setPending] = useState(false)
  const [err, setErr] = useState<string | null>(null)
  useEffect(() => {
    if (inst) {
      setName(inst.name)
      setErr(null)
    }
  }, [inst])

  const submit = async (e: React.FormEvent) => {
    e.preventDefault()
    if (!inst) return
    if (name.length > 128) {
      setErr("Names are at most 128 characters")
      return
    }
    setPending(true)
    try {
      await api.patch(`${INSTANCES_PATH}/${seg(inst.id)}`, { name: name.trim() })
      toast.success(name.trim() ? `Renamed ${inst.id} to ${name.trim()}` : `Removed the name of ${inst.id}`)
      await revalidate(INSTANCES_PATH)
      onClose()
    } catch (e) {
      toast.error(errorMessage(e))
    } finally {
      setPending(false)
    }
  }

  return (
    <Dialog open={!!inst} onOpenChange={(o) => !o && !pending && onClose()}>
      <DialogContent>
        <form onSubmit={submit} className="flex flex-col gap-4">
          <DialogHeader>
            <DialogTitle>Rename instance</DialogTitle>
            <DialogDescription>The name is a display label. The DNS alias inside the VPC keeps the name given at launch.</DialogDescription>
          </DialogHeader>
          <Field label="Name" htmlFor="rename" error={err}>
            <Input id="rename" value={name} onChange={(e) => setName(e.target.value)} autoFocus placeholder={inst?.id} />
          </Field>
          <DialogFooter>
            <Button type="button" variant="outline" onClick={onClose} disabled={pending}>
              Cancel
            </Button>
            <Button type="submit" disabled={pending || name === inst?.name}>
              {pending && <Loader2 className="animate-spin" />}
              Save
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}

function TagsDialog({ inst, onClose }: { inst: Instance | null; onClose: () => void }) {
  const [rows, setRows] = useState<TagRow[]>([])
  const [pending, setPending] = useState(false)
  const [err, setErr] = useState<string | null>(null)
  useEffect(() => {
    if (inst) {
      setRows(tagsToRows(inst.tags))
      setErr(null)
    }
  }, [inst])

  const submit = async (e: React.FormEvent) => {
    e.preventDefault()
    if (!inst) return
    const keys = rows.map((r) => r.key.trim()).filter(Boolean)
    if (new Set(keys).size !== keys.length) {
      setErr("Tag keys must be unique")
      return
    }
    setPending(true)
    try {
      await api.patch(`${INSTANCES_PATH}/${seg(inst.id)}`, { tags: rowsToTags(rows) ?? {} })
      toast.success(`Saved tags for ${instanceLabel(inst)}`)
      await revalidate(INSTANCES_PATH)
      onClose()
    } catch (e) {
      toast.error(errorMessage(e))
    } finally {
      setPending(false)
    }
  }

  return (
    <Dialog open={!!inst} onOpenChange={(o) => !o && !pending && onClose()}>
      <DialogContent className="sm:max-w-xl">
        <form onSubmit={submit} className="flex flex-col gap-4">
          <DialogHeader>
            <DialogTitle>Manage tags</DialogTitle>
            <DialogDescription>Tags are key/value labels. They are also visible to the instance in /var/lib/homecloud/instance.json at launch.</DialogDescription>
          </DialogHeader>
          <TagsEditor rows={rows} onChange={(r) => (setRows(r), setErr(null))} />
          {err && <p className="text-destructive text-xs">{err}</p>}
          <DialogFooter>
            <Button type="button" variant="outline" onClick={onClose} disabled={pending}>
              Cancel
            </Button>
            <Button type="submit" disabled={pending}>
              {pending && <Loader2 className="animate-spin" />}
              Save tags
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}
