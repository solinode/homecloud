import { Suspense } from "react"

import { StateMachineEditor } from "@/components/sfn/state-machine-editor"

export const metadata = { title: "Create state machine" }

export default function Page() {
  return (
    <Suspense>
      <StateMachineEditor />
    </Suspense>
  )
}
