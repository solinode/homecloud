import { Suspense } from "react"

import { EventHistory } from "@/components/cloudtrail/event-history"

export const metadata = { title: "Event history" }

export default function Page() {
  return (
    <Suspense>
      <EventHistory />
    </Suspense>
  )
}
