"use client"

import { useMemo, useState } from "react"

import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select"
import { PageHeader } from "@/components/console/page-header"
import { useApi } from "@/lib/hooks"
import type { Subscription } from "@/lib/types"

import { PROTOCOL_LABEL, SNS_POLL, SUBSCRIPTIONS_PATH } from "./common"
import { SubscriptionsTable } from "./subscriptions"

/** SubscriptionsPage lists the subscriptions of every topic. */
export function SubscriptionsPage() {
  const { data, error, isLoading, isValidating, mutate } = useApi<Subscription[]>(SUBSCRIPTIONS_PATH, { refreshInterval: SNS_POLL })
  const [protocol, setProtocol] = useState("all")
  const rows = useMemo(() => (data && protocol !== "all" ? data.filter((s) => s.protocol === protocol) : data), [data, protocol])

  return (
    <div className="flex flex-col gap-4">
      <PageHeader
        title="Subscriptions"
        description="Every endpoint subscribed to a topic in this account, with delivery statistics."
        breadcrumbs={[{ label: "SNS", href: "/sns/" }, { label: "Subscriptions" }]}
      />
      <SubscriptionsTable
        data={rows}
        loading={isLoading}
        error={error}
        onRefresh={() => mutate()}
        refreshing={isValidating}
        filters={
          <Select value={protocol} onValueChange={setProtocol}>
            <SelectTrigger size="sm" className="h-8 w-40" aria-label="Protocol filter">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="all">All protocols</SelectItem>
              {Object.entries(PROTOCOL_LABEL).map(([k, v]) => (
                <SelectItem key={k} value={k}>
                  {v}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        }
      />
    </div>
  )
}
