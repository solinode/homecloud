import { Suspense } from "react"

import { RuleDetail } from "@/components/events/rule-detail"

export const metadata = { title: "EventBridge rule" }

export default function Page() {
  return (
    <Suspense>
      <RuleDetail />
    </Suspense>
  )
}
