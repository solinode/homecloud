import { Suspense } from "react"

import { StateMachinesList } from "@/components/sfn/state-machines-list"

export const metadata = { title: "State machines" }

export default function Page() {
  return (
    <Suspense>
      <StateMachinesList />
    </Suspense>
  )
}
