import { Suspense } from "react"

import { QueuesList } from "@/components/sqs/queues-list"

export const metadata = { title: "Queues" }

export default function Page() {
  return (
    <Suspense>
      <QueuesList />
    </Suspense>
  )
}
