import { Suspense } from "react"

import { AlarmList } from "@/components/cloudwatch/alarm-list"

export const metadata = { title: "Alarms" }

export default function Page() {
  return (
    <Suspense>
      <AlarmList />
    </Suspense>
  )
}
