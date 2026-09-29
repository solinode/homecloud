import { Suspense } from "react"

import { GroupsList } from "@/components/autoscaling/groups-list"

export const metadata = { title: "Auto Scaling groups" }

export default function Page() {
  return (
    <Suspense>
      <GroupsList />
    </Suspense>
  )
}
