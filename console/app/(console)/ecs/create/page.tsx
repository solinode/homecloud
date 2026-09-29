import { Suspense } from "react"

import { CreateService } from "@/components/ecs/create-service"

export const metadata = { title: "Create service" }

export default function Page() {
  return (
    <Suspense>
      <CreateService />
    </Suspense>
  )
}
