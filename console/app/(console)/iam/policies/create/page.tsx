import { Suspense } from "react"

import { PolicyCreate } from "@/components/iam/policy-create"

export const metadata = { title: "Create policy | IAM" }

export default function Page() {
  return (
    <Suspense>
      <PolicyCreate />
    </Suspense>
  )
}
