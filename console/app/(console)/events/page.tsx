import { Suspense } from "react"

import { RulesList } from "@/components/events/rules-list"

export const metadata = { title: "EventBridge rules" }

export default function Page() {
  return (
    <Suspense>
      <RulesList />
    </Suspense>
  )
}
