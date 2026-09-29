import { Suspense } from "react"

import { VpcList } from "@/components/vpc/vpc-list"

export const metadata = { title: "Your VPCs" }

export default function Page() {
  return (
    <Suspense>
      <VpcList />
    </Suspense>
  )
}
