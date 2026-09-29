import { Suspense } from "react"

import { MetricsExplorer } from "@/components/cloudwatch/metrics-explorer"

export const metadata = { title: "Metrics" }

export default function Page() {
  return (
    <Suspense>
      <MetricsExplorer />
    </Suspense>
  )
}
