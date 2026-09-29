"use client"

import type { ReactNode } from "react"

import { Skeleton } from "@/components/ui/skeleton"
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs"
import { ErrorState } from "@/components/console/error-state"
import { StatusBadge } from "@/components/console/status-badge"
import { TimeAgo } from "@/components/console/time-ago"
import { seg } from "@/lib/api"
import { useApi } from "@/lib/hooks"
import type { AlarmHistoryItem } from "@/lib/types"

/** AlarmTabs shows an alarm's details and its history (configuration and state changes, actions). */
export function AlarmTabs({ name, details }: { name: string; details: ReactNode }) {
  return (
    <Tabs defaultValue="details">
      <TabsList>
        <TabsTrigger value="details">Details</TabsTrigger>
        <TabsTrigger value="history">History</TabsTrigger>
      </TabsList>
      <TabsContent value="details" className="pt-3">
        {details}
      </TabsContent>
      <TabsContent value="history" className="pt-3">
        <AlarmHistory name={name} />
      </TabsContent>
    </Tabs>
  )
}

function AlarmHistory({ name }: { name: string }) {
  const { data, error, mutate } = useApi<AlarmHistoryItem[]>(`/api/v1/cloudwatch/alarms/${seg(name)}/history`, { refreshInterval: 15_000 })
  if (error) return <ErrorState error={error} onRetry={() => mutate()} />
  if (!data) return <Skeleton className="h-16 w-full" />
  if (data.length === 0) return <p className="text-muted-foreground text-sm">No history yet.</p>
  return (
    <div className="overflow-x-auto rounded-md border">
      <table className="w-full text-sm">
        <thead className="text-muted-foreground border-b text-left">
          <tr>
            <th className="px-3 py-2 font-medium">Time</th>
            <th className="px-3 py-2 font-medium">Type</th>
            <th className="px-3 py-2 font-medium">Description</th>
          </tr>
        </thead>
        <tbody>
          {data.map((h, i) => (
            <tr key={`${h.timestamp}-${i}`} className="border-b last:border-0">
              <td className="px-3 py-2 whitespace-nowrap">
                <TimeAgo value={h.timestamp} />
              </td>
              <td className="px-3 py-2 whitespace-nowrap">
                <StatusBadge status={h.type} label={h.type.replace(/([a-z])([A-Z])/g, "$1 $2")} tone="neutral" />
              </td>
              <td className="px-3 py-2">{h.summary}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  )
}
