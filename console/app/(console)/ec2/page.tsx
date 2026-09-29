import { Suspense } from "react"

import { InstancesList } from "@/components/ec2/instances-list"

export const metadata = { title: "Instances" }

export default function Page() {
  return (
    <Suspense>
      <InstancesList />
    </Suspense>
  )
}
