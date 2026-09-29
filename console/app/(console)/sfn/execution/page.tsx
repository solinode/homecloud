import { Suspense } from "react"

import { ExecutionDetail } from "@/components/sfn/execution-detail"

export const metadata = { title: "Execution" }

export default function Page() {
  return (
    <Suspense>
      <ExecutionDetail />
    </Suspense>
  )
}
