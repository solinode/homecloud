import { Suspense } from "react"

import { SendEvents } from "@/components/events/send-events"

export const metadata = { title: "Send events" }

export default function Page() {
  return (
    <Suspense>
      <SendEvents />
    </Suspense>
  )
}
