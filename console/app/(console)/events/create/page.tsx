import { Suspense } from "react"

import { RuleWizard } from "@/components/events/rule-wizard"

export const metadata = { title: "Create rule" }

export default function Page() {
  return (
    <Suspense>
      <RuleWizard />
    </Suspense>
  )
}
