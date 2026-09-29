import { Suspense } from "react"

import { SnapshotsPage } from "@/components/rds/snapshots"

export const metadata = { title: "Snapshots" }

export default function Page() {
  return (
    <Suspense>
      <SnapshotsPage family="rds" />
    </Suspense>
  )
}
