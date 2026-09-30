import { Suspense } from "react"

import { SchedulerPage } from "@/components/events/scheduler"

export const metadata = { title: "Schedules" }

export default function Page() {
  return (
    <Suspense>
      <SchedulerPage />
    </Suspense>
  )
}
