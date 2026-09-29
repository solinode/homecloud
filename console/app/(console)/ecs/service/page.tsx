import { Suspense } from "react"

import { ServiceDetail } from "@/components/ecs/service-detail"

export const metadata = { title: "ECS service" }

export default function Page() {
  return (
    <Suspense>
      <ServiceDetail />
    </Suspense>
  )
}
