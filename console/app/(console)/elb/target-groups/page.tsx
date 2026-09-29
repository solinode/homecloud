import { Suspense } from "react"

import { TargetGroupsList } from "@/components/elb/tg-list"

export const metadata = { title: "Target groups" }

export default function Page() {
  return (
    <Suspense>
      <TargetGroupsList />
    </Suspense>
  )
}
