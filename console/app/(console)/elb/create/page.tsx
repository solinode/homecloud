import { Suspense } from "react"

import { CreateLoadBalancer } from "@/components/elb/lb-create"

export const metadata = { title: "Create load balancer" }

export default function Page() {
  return (
    <Suspense>
      <CreateLoadBalancer />
    </Suspense>
  )
}
