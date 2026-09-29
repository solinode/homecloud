import { Suspense } from "react"

import { PolicyDetail } from "@/components/iam/policy-detail"

export const metadata = { title: "Policy | IAM" }

export default function Page() {
  return (
    <Suspense>
      <PolicyDetail />
    </Suspense>
  )
}
