import { Suspense } from "react"

import { InstanceProfileDetail } from "@/components/iam/instance-profile-detail"

export const metadata = { title: "Instance profile | IAM" }

export default function Page() {
  return (
    <Suspense>
      <InstanceProfileDetail />
    </Suspense>
  )
}
