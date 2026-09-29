import { Suspense } from "react"

import { LoadBalancersList } from "@/components/elb/lb-list"

export const metadata = { title: "Load balancers" }

export default function Page() {
  return (
    <Suspense>
      <LoadBalancersList />
    </Suspense>
  )
}
