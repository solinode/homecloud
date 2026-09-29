import { Suspense } from "react"

import { PolicySimulator } from "@/components/iam/simulator"

export const metadata = { title: "Policy simulator | IAM" }

export default function Page() {
  return (
    <Suspense>
      <PolicySimulator />
    </Suspense>
  )
}
