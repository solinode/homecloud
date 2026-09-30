import { Suspense } from "react"

import { LogsInsights } from "@/components/cloudwatch/insights"

export const metadata = { title: "Logs Insights" }

export default function Page() {
  return (
    <Suspense>
      <LogsInsights />
    </Suspense>
  )
}
