import { Suspense } from "react"

import { SecurityGroupList } from "@/components/vpc/security-group-list"

export const metadata = { title: "Security groups" }

export default function Page() {
  return (
    <Suspense>
      <SecurityGroupList />
    </Suspense>
  )
}
