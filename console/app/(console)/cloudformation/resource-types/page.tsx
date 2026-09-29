import { Suspense } from "react"

import { ResourceTypes } from "@/components/cloudformation/resource-types"

export const metadata = { title: "Resource types" }

export default function Page() {
  return (
    <Suspense>
      <ResourceTypes />
    </Suspense>
  )
}
