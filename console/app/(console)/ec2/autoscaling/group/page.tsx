import { Suspense } from "react"

import { GroupDetail } from "@/components/autoscaling/group-detail"

export const metadata = { title: "Auto Scaling group" }

export default function Page() {
  return (
    <Suspense>
      <GroupDetail />
    </Suspense>
  )
}
