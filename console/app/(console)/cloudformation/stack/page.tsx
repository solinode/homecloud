import { Suspense } from "react"

import { StackDetail } from "@/components/cloudformation/stack-detail"

export const metadata = { title: "Stack" }

export default function Page() {
  return (
    <Suspense>
      <StackDetail />
    </Suspense>
  )
}
