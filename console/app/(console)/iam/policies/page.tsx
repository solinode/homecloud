import { Suspense } from "react"

import { PoliciesList } from "@/components/iam/policies-list"

export const metadata = { title: "Policies | IAM" }

export default function Page() {
  return (
    <Suspense>
      <PoliciesList />
    </Suspense>
  )
}
