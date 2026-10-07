"use client"

import { useState } from "react"
import Link from "next/link"
import { useRouter } from "next/navigation"
import { FlaskConical, FunctionSquare, Globe, Lock, Plus } from "lucide-react"

import { Button } from "@/components/ui/button"
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip"
import { ActionsMenu, type ActionItem } from "@/components/console/actions-menu"
import { CellLink, CellText, DataTable, type Column } from "@/components/console/data-table"
import { EmptyState } from "@/components/console/empty-state"
import { PageHeader } from "@/components/console/page-header"
import { TimeAgo } from "@/components/console/time-ago"
import { formatBytes, formatMemoryMB } from "@/lib/format"
import { useApi } from "@/lib/hooks"
import type { LambdaFunction } from "@/lib/types"

import { DeleteFunctionDialog, FUNCTIONS_PATH, FunctionStateBadge, RuntimeBadge, formatTimeout, functionHref, useRuntimeLabels } from "./common"

export function FunctionUrlIndicator({ fn }: { fn: LambdaFunction }) {
  if (!fn.function_url?.enabled) return <span className="text-muted-foreground">-</span>
  const Icon = fn.function_url.auth_type === "HC_IAM" ? Lock : Globe
  return (
    <Tooltip>
      <TooltipTrigger asChild>
        <span className="text-primary inline-flex items-center gap-1 text-xs font-medium whitespace-nowrap">
          <Icon className="size-3.5" /> {fn.function_url.auth_type === "HC_IAM" ? "IAM" : "Public"}
        </span>
      </TooltipTrigger>
      <TooltipContent>
        <span className="font-mono">{fn.function_url.url}</span>
      </TooltipContent>
    </Tooltip>
  )
}

export function FunctionsList() {
  const router = useRouter()
  const { data, error, isLoading, isValidating, mutate } = useApi<LambdaFunction[]>(FUNCTIONS_PATH, { refreshInterval: 15_000 })
  const labels = useRuntimeLabels()
  const [selected, setSelected] = useState<string[]>([])
  const [deleting, setDeleting] = useState<LambdaFunction | null>(null)
  const sel = data?.find((f) => f.name === selected[0]) ?? null

  const columns: Column<LambdaFunction>[] = [
    {
      id: "name",
      header: "Function name",
      value: (f) => f.name,
      cell: (f) => <CellLink href={functionHref(f.name)}>{f.name}</CellLink>,
    },
    {
      id: "description",
      header: "Description",
      value: (f) => f.description,
      cell: (f) => <CellText max="20rem">{f.description}</CellText>,
      hideBelow: "lg",
    },
    {
      id: "runtime",
      header: "Runtime",
      value: (f) => (f.package_type === "Image" ? "Image" : (labels.get(f.runtime) ?? f.runtime)),
      cell: (f) => (f.package_type === "Image" ? <RuntimeBadge runtime="Container image" /> : <RuntimeBadge runtime={f.runtime} label={labels.get(f.runtime)} />),
    },
    { id: "state", header: "State", value: (f) => f.state, cell: (f) => <FunctionStateBadge fn={f} />, hideBelow: "sm" },
    {
      id: "arch",
      header: "Architecture",
      value: (f) => (f.architectures ?? []).join(","),
      cell: (f) => <CellText mono>{(f.architectures ?? []).join(", ") || "x86_64"}</CellText>,
      hideBelow: "lg",
    },
    { id: "memory", header: "Memory", value: (f) => f.memory_mb, cell: (f) => <span className="whitespace-nowrap tabular-nums">{formatMemoryMB(f.memory_mb)}</span>, hideBelow: "sm" },
    { id: "timeout", header: "Timeout", value: (f) => f.timeout_seconds, cell: (f) => <span className="whitespace-nowrap tabular-nums">{formatTimeout(f.timeout_seconds)}</span>, hideBelow: "md" },
    { id: "size", header: "Code size", value: (f) => f.code_size, cell: (f) => <span className="whitespace-nowrap tabular-nums">{formatBytes(f.code_size)}</span>, hideBelow: "lg" },
    {
      id: "url",
      header: "Function URL",
      value: (f) => (f.function_url?.enabled ? f.function_url.auth_type : ""),
      cell: (f) => <FunctionUrlIndicator fn={f} />,
      hideBelow: "md",
    },
    { id: "modified", header: "Last modified", value: (f) => f.last_modified, cell: (f) => <TimeAgo value={f.last_modified} />, hideBelow: "sm" },
  ]

  const actions: ActionItem[] = [
    { label: "View details", onSelect: () => sel && router.push(functionHref(sel.name)), disabled: !sel },
    { label: "Test", icon: <FlaskConical />, onSelect: () => sel && router.push(functionHref(sel.name, "test")), disabled: !sel },
    { label: "Edit code", onSelect: () => sel && router.push(functionHref(sel.name, "code")), disabled: !sel },
    { separator: true },
    { label: "Delete", destructive: true, onSelect: () => setDeleting(sel), disabled: !sel },
  ]

  const createButton = (
    <Button size="sm" asChild>
      <Link href="/lambda/create/">
        <Plus /> Create function
      </Link>
    </Button>
  )

  return (
    <div className="flex flex-col gap-4">
      <PageHeader
        title="Functions"
        description="Run code without managing servers. Each function runs in a warm, memory-limited container; every invocation is logged to CloudWatch."
        breadcrumbs={[{ label: "Lambda", href: "/lambda/" }, { label: "Functions" }]}
      />
      <DataTable
        title="Functions"
        data={data}
        columns={columns}
        rowId={(f) => f.name}
        loading={isLoading}
        error={error}
        onRefresh={() => mutate()}
        refreshing={isValidating}
        selection="single"
        selected={selected}
        onSelectedChange={setSelected}
        searchPlaceholder="Filter by function name, description or runtime"
        defaultSort={{ id: "name" }}
        actions={
          <>
            <ActionsMenu items={actions} disabled={!sel} />
            {createButton}
          </>
        }
        empty={
          <EmptyState
            icon={FunctionSquare}
            title="No functions"
            description="Create a Python or Node.js function from a starter template, then invoke it from the console, a function URL, API Gateway or an SQS queue."
            action={createButton}
          />
        }
      />
      <DeleteFunctionDialog fn={deleting} onOpenChange={(o) => !o && setDeleting(null)} />
    </div>
  )
}
