"use client"

import { useEffect, useMemo, useState } from "react"
import Link from "next/link"
import { useRouter } from "next/navigation"
import { AlertTriangle, Loader2, Plus, Rocket } from "lucide-react"
import { toast } from "sonner"

import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select"
import { Switch } from "@/components/ui/switch"
import { ErrorState } from "@/components/console/error-state"
import { Field } from "@/components/console/form-field"
import { PageHeader } from "@/components/console/page-header"
import { Section } from "@/components/console/section"
import { TagsEditor, rowsToTags, type TagRow } from "@/components/console/tags-editor"
import { api, errorMessage } from "@/lib/api"
import { formatMemoryMB } from "@/lib/format"
import { revalidate, useApi, useQueryParam } from "@/lib/hooks"
import type { EcsCreateServiceInput, EcsService } from "@/lib/types"
import {
  ECS_PATH,
  SERVICES_PATH,
  TARGET_GROUPS_PATH,
  TaskDefinitionPicker,
  formatCpu,
  joinCommand,
  registerHref,
  serviceHref,
  useTaskDefinitions,
  type TargetGroupInfo,
} from "./common"
import { NetworkFields, useNetworkSelection } from "./network-fields"

const NAME_RE = /^[a-zA-Z0-9_-]{1,255}$/

export function CreateService() {
  const router = useRouter()
  const familyParam = useQueryParam("family")
  const tds = useTaskDefinitions()
  const tgs = useApi<TargetGroupInfo[]>(TARGET_GROUPS_PATH, { revalidateOnFocus: false })
  const net = useNetworkSelection()

  const [name, setName] = useState("")
  const [td, setTd] = useState("")
  const [count, setCount] = useState("1")
  const [lbOn, setLbOn] = useState(false)
  const [tg, setTg] = useState("")
  const [port, setPort] = useState("")
  const [portTouched, setPortTouched] = useState(false)
  const [tagRows, setTagRows] = useState<TagRow[]>([])
  const [submitted, setSubmitted] = useState(false)
  const [pending, setPending] = useState(false)

  const taskDef = tds.data?.find((x) => `${x.family}:${x.revision}` === td)
  // The container port defaults to the task definition's.
  useEffect(() => {
    if (!portTouched) setPort(taskDef?.container_port ? String(taskDef.container_port) : "")
  }, [taskDef, portTouched])

  const targetGroup = tgs.data?.find((g) => g.name === tg)
  const vpcTgs = useMemo(() => (tgs.data ?? []).filter((g) => !net.vpcId || g.vpc_id === net.vpcId), [tgs.data, net.vpcId])
  const otherVpcTgs = (tgs.data?.length ?? 0) - vpcTgs.length

  const n = Number(count)
  const p = Number(port)
  const errors = useMemo(() => {
    const e: Record<string, string> = {}
    if (!NAME_RE.test(name)) e.name = "1-255 letters, digits, hyphens or underscores"
    if (!td) e.td = "Choose a task definition"
    if (!Number.isInteger(n) || n < 0 || n > 50) e.count = "Enter a whole number from 0 to 50"
    if (!net.subnetId) e.subnet = "Choose a subnet"
    if (lbOn) {
      if (!tg) e.tg = "Choose a target group"
      else if (targetGroup && net.vpcId && targetGroup.vpc_id !== net.vpcId) e.tg = "The target group must be in the service subnet's VPC"
      if (!Number.isInteger(p) || p < 1 || p > 65535) e.port = "Enter the port the container listens on (1-65535)"
    }
    return e
  }, [name, td, n, net.subnetId, net.vpcId, lbOn, tg, targetGroup, p])
  const err = (k: string) => (submitted ? errors[k] : undefined)
  const valid = Object.keys(errors).length === 0

  const create = async () => {
    setSubmitted(true)
    if (!valid) {
      toast.error("Fix the highlighted fields before creating the service")
      return
    }
    const body: EcsCreateServiceInput = {
      name,
      task_definition: td,
      desired_count: n,
      subnet_id: net.subnetId,
      security_groups: net.sgIds,
      load_balancer: lbOn ? { target_group: tg, container_port: p } : undefined,
      tags: rowsToTags(tagRows),
    }
    setPending(true)
    try {
      const s = await api.post<EcsService>(SERVICES_PATH, body)
      toast.success(`Created service ${s.name}`)
      await revalidate(ECS_PATH)
      router.push(serviceHref(s.name))
    } catch (e) {
      toast.error(errorMessage(e))
      setPending(false)
    }
  }

  const noTds = tds.data && tds.data.length === 0

  return (
    <div className="flex flex-col gap-4">
      <PageHeader
        title="Create service"
        description="The service keeps the desired number of tasks running from a task definition, replacing tasks that stop."
        breadcrumbs={[{ label: "ECS", href: "/ecs/" }, { label: "Services", href: "/ecs/" }, { label: "Create service" }]}
      />
      <form
        onSubmit={(e) => {
          e.preventDefault()
          create()
        }}
        className="grid grid-cols-1 items-start gap-4 xl:grid-cols-[minmax(0,1fr)_320px]"
      >
        <div className="flex min-w-0 flex-col gap-4">
          <Section title="Service details">
            <div className="flex flex-col gap-4">
              <Field
                label="Service name"
                htmlFor="svc-name"
                error={err("name")}
                help={
                  <>
                    Tasks are reachable inside the VPC at <span className="font-mono">{name || "<name>"}.ecs.internal</span>.
                  </>
                }
              >
                <Input id="svc-name" value={name} onChange={(e) => setName(e.target.value)} placeholder="e.g. web" className="max-w-md" autoFocus />
              </Field>
              <Field label="Desired tasks" htmlFor="svc-count" error={err("count")} help="0-50 tasks. You can scale later.">
                <Input id="svc-count" type="number" min={0} max={50} value={count} onChange={(e) => setCount(e.target.value)} className="w-28" />
              </Field>
            </div>
          </Section>

          <Section
            title="Task definition"
            actions={
              <Link href={registerHref()} className="text-primary inline-flex items-center gap-1 text-sm hover:underline">
                <Plus className="size-3.5" /> Register new
              </Link>
            }
          >
            {tds.error ? (
              <ErrorState error={tds.error} onRetry={() => tds.mutate()} />
            ) : noTds ? (
              <p className="text-muted-foreground rounded-lg border border-dashed p-6 text-center text-sm">
                You have no active task definitions.{" "}
                <Link href={registerHref()} className="text-primary hover:underline">
                  Register a task definition
                </Link>{" "}
                first.
              </p>
            ) : (
              <div className="flex flex-col gap-3">
                <Field label="Family and revision" error={err("td")} help="Defaults to the latest ACTIVE revision.">
                  <div className="max-w-xl">
                    <TaskDefinitionPicker value={td} onChange={setTd} idPrefix="svc-td" defaultFamily={familyParam || undefined} />
                  </div>
                </Field>
                {taskDef && (
                  <div className="bg-muted/40 grid max-w-xl grid-cols-2 gap-3 rounded-md border p-3 text-sm sm:grid-cols-4">
                    <Mini label="Image" value={<span className="font-mono text-xs break-all">{taskDef.image}</span>} className="col-span-2 sm:col-span-4" />
                    <Mini label="CPU" value={formatCpu(taskDef.cpu)} />
                    <Mini label="Memory" value={formatMemoryMB(taskDef.memory_mb)} />
                    <Mini label="Container port" value={taskDef.container_port || "-"} />
                    <Mini label="Command" value={<span className="font-mono text-xs break-all">{joinCommand(taskDef.command) || "image default"}</span>} />
                  </div>
                )}
              </div>
            )}
          </Section>

          <Section title="Networking" description="Every task gets a private IP in the subnet. Security groups control which ports other resources can reach.">
            <NetworkFields net={net} idPrefix="svc" error={err("subnet")} />
          </Section>

          <Section
            title="Load balancing"
            description="Register each running task as a target of an ELB target group. Stopped and replaced tasks are deregistered automatically."
            actions={
              <div className="flex items-center gap-2">
                <Switch id="lb-on" checked={lbOn} onCheckedChange={setLbOn} />
                <label htmlFor="lb-on" className="text-sm">
                  Use a load balancer
                </label>
              </div>
            }
          >
            {!lbOn ? (
              <p className="text-muted-foreground text-sm">No load balancer. Other resources in the VPC can reach the tasks at the service endpoint.</p>
            ) : tgs.error ? (
              <ErrorState error={tgs.error} onRetry={() => tgs.mutate()} />
            ) : (
              <div className="grid grid-cols-1 gap-4 sm:grid-cols-[minmax(0,1fr)_10rem]">
                <Field
                  label="Target group"
                  htmlFor="svc-tg"
                  error={err("tg")}
                  help={
                    tgs.data && vpcTgs.length === 0 ? (
                      <>
                        No target groups in this VPC.{" "}
                        <Link href="/elb/target-groups/" className="text-primary hover:underline">
                          Create a target group
                        </Link>
                        .
                      </>
                    ) : otherVpcTgs > 0 ? (
                      `${otherVpcTgs} target group${otherVpcTgs === 1 ? " is" : "s are"} hidden because they are in another VPC.`
                    ) : undefined
                  }
                >
                  <Select value={tg} onValueChange={(v) => v && setTg(v)} disabled={!tgs.data}>
                    <SelectTrigger id="svc-tg" className="w-full">
                      <SelectValue placeholder={tgs.data ? "Choose a target group" : "Loading target groups..."} />
                    </SelectTrigger>
                    <SelectContent>
                      {vpcTgs.map((g) => (
                        <SelectItem key={g.name} value={g.name}>
                          <span className="font-medium">{g.name}</span>
                          <span className="text-muted-foreground text-xs">
                            {g.protocol}:{g.port}
                          </span>
                        </SelectItem>
                      ))}
                    </SelectContent>
                  </Select>
                </Field>
                <Field label="Container port" htmlFor="svc-port" error={err("port")}>
                  <Input
                    id="svc-port"
                    type="number"
                    min={1}
                    max={65535}
                    value={port}
                    onChange={(e) => (setPort(e.target.value), setPortTouched(true))}
                    placeholder="8080"
                  />
                </Field>
                {taskDef && !taskDef.container_port && (
                  <p className="flex gap-2 text-xs text-amber-700 sm:col-span-2 dark:text-amber-400">
                    <AlertTriangle className="size-3.5 shrink-0" /> The task definition declares no container port; enter the port the container listens on.
                  </p>
                )}
              </div>
            )}
          </Section>

          <Section title="Tags">
            <TagsEditor rows={tagRows} onChange={setTagRows} />
          </Section>
        </div>

        <aside className="xl:sticky xl:top-4">
          <Section title="Summary">
            <div className="flex flex-col gap-4">
              <dl className="flex flex-col gap-3 text-sm">
                <SummaryItem label="Service name">{name ? <span className="font-medium">{name}</span> : "-"}</SummaryItem>
                <SummaryItem label="Endpoint">
                  <span className="font-mono text-xs break-all">{name ? `${name}.ecs.internal` : "-"}</span>
                </SummaryItem>
                <SummaryItem label="Task definition">
                  <span className="font-mono text-[13px]">{td || "-"}</span>
                  {taskDef && (
                    <div className="text-muted-foreground text-xs">
                      {formatCpu(taskDef.cpu)}, {formatMemoryMB(taskDef.memory_mb)} per task
                    </div>
                  )}
                </SummaryItem>
                <SummaryItem label="Desired tasks">{Number.isInteger(n) ? n : "-"}</SummaryItem>
                <SummaryItem label="Subnet">
                  {net.subnet ? (
                    <>
                      <div>{net.subnet.name || net.subnet.id}</div>
                      <div className="text-muted-foreground text-xs">{net.subnet.availability_zone}</div>
                    </>
                  ) : (
                    "-"
                  )}
                </SummaryItem>
                <SummaryItem label="Security groups">
                  {net.sgIds.length ? net.groups.filter((g) => net.sgIds.includes(g.id)).map((g) => g.name).join(", ") : "None"}
                </SummaryItem>
                <SummaryItem label="Load balancing">{lbOn ? (tg ? `${tg} → port ${port || "?"}` : "Target group not chosen") : "None"}</SummaryItem>
                {taskDef && Number.isInteger(n) && n > 1 && (
                  <SummaryItem label="Total reserved">
                    {formatCpu(taskDef.cpu * n)}, {formatMemoryMB(taskDef.memory_mb * n)}
                  </SummaryItem>
                )}
              </dl>
              {submitted && !valid && <p className="text-destructive text-xs">Some settings need attention. Check the highlighted fields.</p>}
              <div className="flex flex-col gap-2 border-t pt-4">
                <Button type="submit" disabled={pending || !!noTds}>
                  {pending ? <Loader2 className="animate-spin" /> : <Rocket />}
                  Create service
                </Button>
                <Button type="button" variant="outline" asChild>
                  <Link href="/ecs/">Cancel</Link>
                </Button>
              </div>
            </div>
          </Section>
        </aside>
      </form>
    </div>
  )
}

function Mini({ label, value, className }: { label: string; value: React.ReactNode; className?: string }) {
  return (
    <div className={className}>
      <div className="text-muted-foreground text-xs">{label}</div>
      <div>{value}</div>
    </div>
  )
}

function SummaryItem({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div>
      <dt className="text-muted-foreground text-xs font-medium">{label}</dt>
      <dd className="min-w-0 break-words">{children}</dd>
    </div>
  )
}
