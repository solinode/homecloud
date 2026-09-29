import { Suspense } from "react"

import { PoolsList } from "@/components/cognito/pools-list"

export const metadata = { title: "Cognito user pools" }

export default function Page() {
  return (
    <Suspense>
      <PoolsList />
    </Suspense>
  )
}
