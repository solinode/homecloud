import { Suspense } from "react"

import { LoadBalancerDetail } from "@/components/elb/lb-detail"

export const metadata = { title: "Load balancer" }

export default function Page() {
  return (
    <Suspense>
      <LoadBalancerDetail />
    </Suspense>
  )
}
