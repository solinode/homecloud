import { Suspense } from "react"

import { ServicesList } from "@/components/ecs/services-list"

export const metadata = { title: "ECS services" }

export default function Page() {
  return (
    <Suspense>
      <ServicesList />
    </Suspense>
  )
}
