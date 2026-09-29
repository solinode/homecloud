import { Suspense } from "react"

import { CreateGroup } from "@/components/autoscaling/create-group"

export const metadata = { title: "Create Auto Scaling group" }

export default function Page() {
  return (
    <Suspense>
      <CreateGroup />
    </Suspense>
  )
}
