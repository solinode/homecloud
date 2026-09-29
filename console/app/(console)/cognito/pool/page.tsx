import { Suspense } from "react"

import { PoolDetail } from "@/components/cognito/pool-detail"

export const metadata = { title: "User pool" }

export default function Page() {
  return (
    <Suspense>
      <PoolDetail />
    </Suspense>
  )
}
