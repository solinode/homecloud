import { Suspense } from "react"

import { LogEventsViewer } from "@/components/cloudwatch/log-events"

export const metadata = { title: "Log events" }

export default function Page() {
  return (
    <Suspense>
      <LogEventsViewer />
    </Suspense>
  )
}
