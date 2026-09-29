import { Suspense } from "react"

import { InstanceDetail } from "@/components/ec2/instance-detail"

export const metadata = { title: "Instance" }

export default function Page() {
  return (
    <Suspense>
      <InstanceDetail />
    </Suspense>
  )
}
