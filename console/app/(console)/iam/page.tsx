import { Suspense } from "react"

import { IamDashboard } from "@/components/iam/iam-dashboard"

export const metadata = { title: "IAM" }

export default function Page() {
  return (
    <Suspense>
      <IamDashboard />
    </Suspense>
  )
}
