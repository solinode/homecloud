import { Suspense } from "react"

import { StateMachineDetailPage } from "@/components/sfn/state-machine-detail"

export const metadata = { title: "State machine" }

export default function Page() {
  return (
    <Suspense>
      <StateMachineDetailPage />
    </Suspense>
  )
}
