import { Suspense } from "react"

import { CloudWatchOverview } from "@/components/cloudwatch/overview"

export const metadata = { title: "CloudWatch" }

export default function Page() {
  return (
    <Suspense>
      <CloudWatchOverview />
    </Suspense>
  )
}
