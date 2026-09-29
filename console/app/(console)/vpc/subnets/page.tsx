import { Suspense } from "react"

import { SubnetList } from "@/components/vpc/subnet-list"

export const metadata = { title: "Subnets" }

export default function Page() {
  return (
    <Suspense>
      <SubnetList />
    </Suspense>
  )
}
