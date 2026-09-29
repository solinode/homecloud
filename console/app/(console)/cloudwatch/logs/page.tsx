import { Suspense } from "react"

import { LogGroupList } from "@/components/cloudwatch/log-groups"

export const metadata = { title: "Log groups" }

export default function Page() {
  return (
    <Suspense>
      <LogGroupList />
    </Suspense>
  )
}
