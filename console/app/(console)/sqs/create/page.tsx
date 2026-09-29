import { Suspense } from "react"

import { CreateQueue } from "@/components/sqs/create-queue"

export const metadata = { title: "Create queue" }

export default function Page() {
  return (
    <Suspense>
      <CreateQueue />
    </Suspense>
  )
}
