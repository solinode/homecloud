import { Suspense } from "react"

import { QueueDetail } from "@/components/sqs/queue-detail"

export const metadata = { title: "Queue" }

export default function Page() {
  return (
    <Suspense>
      <QueueDetail />
    </Suspense>
  )
}
