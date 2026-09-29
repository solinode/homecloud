import { Suspense } from "react"

import { GroupsList } from "@/components/iam/groups-list"

export const metadata = { title: "User groups | IAM" }

export default function Page() {
  return (
    <Suspense>
      <GroupsList />
    </Suspense>
  )
}
