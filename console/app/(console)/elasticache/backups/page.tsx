import { Suspense } from "react"

import { SnapshotsPage } from "@/components/rds/snapshots"

export const metadata = { title: "ElastiCache backups" }

export default function Page() {
  return (
    <Suspense>
      <SnapshotsPage family="elasticache" />
    </Suspense>
  )
}
