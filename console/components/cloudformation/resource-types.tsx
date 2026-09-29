"use client"

import { useMemo, useState } from "react"
import Link from "next/link"
import { Boxes, Plus } from "lucide-react"

import { Button } from "@/components/ui/button"
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select"
import { CopyButton } from "@/components/console/copy-button"
import { DataTable, type Column } from "@/components/console/data-table"
import { EmptyState } from "@/components/console/empty-state"
import { PageHeader } from "@/components/console/page-header"
import { useApi } from "@/lib/hooks"
import { CREATE_HREF, TYPES_PATH, TYPE_META, typeParts } from "./common"

interface TypeRow {
  type: string
  service: string
  resource: string
  consoleService: string
  consoleHref: string | null
  ref: string
  waits: string
}

const columns: Column<TypeRow>[] = [
  {
    id: "type",
    header: "Type",
    cell: (t) => (
      <span className="inline-flex items-center gap-1">
        <span className="font-mono text-[13px] font-medium whitespace-nowrap">{t.type}</span>
        <CopyButton value={t.type} label="Copy type" />
      </span>
    ),
    value: (t) => t.type,
  },
  { id: "resource", header: "Resource", cell: (t) => t.resource, value: (t) => t.resource, hideBelow: "sm" },
  {
    id: "ref",
    header: "Ref returns",
    cell: (t) => (t.ref ? t.ref : <span className="text-muted-foreground">Physical ID</span>),
    value: (t) => t.ref,
    hideBelow: "md",
  },
  {
    id: "waits",
    header: "Waits for",
    cell: (t) => (t.waits ? <span className="font-mono text-[13px]">{t.waits}</span> : <span className="text-muted-foreground">-</span>),
    value: (t) => t.waits,
    hideBelow: "lg",
  },
  {
    id: "console",
    header: "Console",
    cell: (t) =>
      t.consoleHref ? (
        <Link href={t.consoleHref} className="text-primary whitespace-nowrap hover:underline">
          {t.consoleService}
        </Link>
      ) : (
        <span className="text-muted-foreground">-</span>
      ),
    value: (t) => t.consoleService,
  },
]

export function ResourceTypes() {
  const { data, error, isLoading, isValidating, mutate } = useApi<string[]>(TYPES_PATH, { revalidateOnFocus: false })
  const [service, setService] = useState("all")

  const all = useMemo<TypeRow[] | undefined>(
    () =>
      data?.map((type) => {
        const p = typeParts(type)
        const m = TYPE_META[type]
        return {
          type,
          service: p.service,
          resource: p.resource,
          consoleService: m?.service ?? p.service,
          consoleHref: m?.consoleHref ?? null,
          ref: m?.ref ?? "",
          waits: m?.waits ?? "",
        }
      }),
    [data],
  )
  const services = useMemo(() => Array.from(new Set((all ?? []).map((t) => t.service))).sort(), [all])
  const rows = useMemo(() => (service === "all" ? all : all?.filter((t) => t.service === service)), [all, service])

  return (
    <div className="flex flex-col gap-4">
      <PageHeader
        title="Resource types"
        description={
          <>
            Types a template can declare. Properties are the create request body of the service&apos;s API, using its own field names; attributes for{" "}
            <span className="font-mono text-[13px]">Fn::GetAtt</span> are the fields of the service&apos;s response (PascalCase names such as Arn also match
            arn).
          </>
        }
        breadcrumbs={[{ label: "CloudFormation", href: "/cloudformation/" }, { label: "Resource types" }]}
        actions={
          <Button size="sm" asChild>
            <Link href={CREATE_HREF}>
              <Plus /> Create stack
            </Link>
          </Button>
        }
      />
      <DataTable
        title="Resource types"
        data={rows}
        count={rows?.length}
        columns={columns}
        rowId={(t) => t.type}
        loading={isLoading}
        error={error}
        onRefresh={() => mutate()}
        refreshing={isValidating}
        searchPlaceholder="Find a type, e.g. Queue or HC::S3"
        defaultSort={{ id: "type" }}
        pageSize={100}
        filters={
          <Select value={service} onValueChange={(v) => v && setService(v)}>
            <SelectTrigger size="sm" className="h-8 w-44" aria-label="Service filter">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="all">All services</SelectItem>
              {services.map((s) => (
                <SelectItem key={s} value={s}>
                  {s}
                  <span className="text-muted-foreground text-xs">({(all ?? []).filter((t) => t.service === s).length})</span>
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        }
        empty={<EmptyState icon={Boxes} title="No resource types" />}
      />
    </div>
  )
}
