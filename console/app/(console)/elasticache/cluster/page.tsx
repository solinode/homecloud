import { Suspense } from "react"

import { DbDetail } from "@/components/rds/db-detail"

export const metadata = { title: "Cluster" }

export default function Page() {
  return (
    <Suspense>
      <DbDetail family="elasticache" />
    </Suspense>
  )
}
