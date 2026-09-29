import { Suspense } from "react"

import { InstanceProfilesList } from "@/components/iam/instance-profiles-list"

export const metadata = { title: "Instance profiles | IAM" }

export default function Page() {
  return (
    <Suspense>
      <InstanceProfilesList />
    </Suspense>
  )
}
