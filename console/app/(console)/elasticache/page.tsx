import { Suspense } from "react"

import { DbList } from "@/components/rds/db-list"

export const metadata = { title: "ElastiCache clusters" }

export default function Page() {
  return (
    <Suspense>
      <DbList family="elasticache" />
    </Suspense>
  )
}
