import { Suspense } from "react"

import { InstanceTypesList } from "@/components/ec2/instance-types-list"

export const metadata = { title: "Instance types" }

export default function Page() {
  return (
    <Suspense>
      <InstanceTypesList />
    </Suspense>
  )
}
