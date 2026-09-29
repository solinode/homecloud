"use client"

import { useMemo, useState } from "react"
import Link from "next/link"
import { Rocket } from "lucide-react"

import { Button } from "@/components/ui/button"
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select"
import { DataTable, type Column } from "@/components/console/data-table"
import { PageHeader } from "@/components/console/page-header"
import { formatMemoryMB } from "@/lib/format"
import type { InstanceType } from "@/lib/types"
import { groupByFamily, useInstanceTypes } from "./instance-actions"

const columns: Column<InstanceType>[] = [
  { id: "name", header: "Instance type", cell: (t) => <span className="font-mono text-[13px] font-medium">{t.name}</span>, value: (t) => t.name },
  { id: "family", header: "Family", cell: (t) => t.family, value: (t) => t.family },
  { id: "vcpus", header: "vCPUs", cell: (t) => <span className="tabular-nums">{t.vcpus}</span>, value: (t) => t.vcpus },
  { id: "memory", header: "Memory", cell: (t) => <span className="tabular-nums">{formatMemoryMB(t.memory_mb)}</span>, value: (t) => t.memory_mb },
  {
    id: "ratio",
    header: "Memory per vCPU",
    cell: (t) => <span className="tabular-nums">{formatMemoryMB(Math.round(t.memory_mb / t.vcpus))}</span>,
    value: (t) => t.memory_mb / t.vcpus,
    hideBelow: "sm",
  },
]

export function InstanceTypesList() {
  const { data, error, isLoading, isValidating, mutate } = useInstanceTypes()
  const [family, setFamily] = useState("all")
  const families = useMemo(() => groupByFamily(data ?? []).map(([f, list]) => ({ f, n: list.length })), [data])
  const rows = useMemo(() => (data ?? []).filter((t) => family === "all" || t.family === family), [data, family])

  return (
    <div className="flex flex-col gap-4">
      <PageHeader
        title="Instance types"
        description="Each type sets the CPU and memory limits of the instance's container. CPU is capped at this host's core count."
        breadcrumbs={[{ label: "EC2", href: "/ec2/" }, { label: "Instance types" }]}
      />
      <DataTable
        title="Instance types"
        data={data ? rows : undefined}
        columns={columns}
        rowId={(t) => t.name}
        loading={isLoading}
        error={error}
        onRefresh={() => mutate()}
        refreshing={isValidating}
        pageSize={50}
        searchPlaceholder="Find instance type"
        actions={
          <Button size="sm" asChild>
            <Link href="/ec2/launch/">
              <Rocket /> Launch instance
            </Link>
          </Button>
        }
        filters={
          <Select value={family} onValueChange={setFamily}>
            <SelectTrigger size="sm" className="h-8 w-64" aria-label="Family">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="all">All families</SelectItem>
              {families.map(({ f, n }) => (
                <SelectItem key={f} value={f}>
                  {f} <span className="text-muted-foreground text-xs">({n})</span>
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        }
      />
    </div>
  )
}
