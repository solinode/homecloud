import { Suspense } from "react"

import { SecurityGroupDetail } from "@/components/vpc/security-group-detail"

export const metadata = { title: "Security group" }

export default function Page() {
  return (
    <Suspense>
      <SecurityGroupDetail />
    </Suspense>
  )
}
