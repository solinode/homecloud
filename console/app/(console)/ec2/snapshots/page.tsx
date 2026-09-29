import { Suspense } from "react"

import { SnapshotsList } from "@/components/ec2/snapshots-list"

export const metadata = { title: "Snapshots" }

export default function Page() {
  return (
    <Suspense>
      <SnapshotsList />
    </Suspense>
  )
}
