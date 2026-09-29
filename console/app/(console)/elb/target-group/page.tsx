import { Suspense } from "react"

import { TargetGroupDetail } from "@/components/elb/tg-detail"

export const metadata = { title: "Target group" }

export default function Page() {
  return (
    <Suspense>
      <TargetGroupDetail />
    </Suspense>
  )
}
