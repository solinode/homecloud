import { Suspense } from "react"

import { GroupDetail } from "@/components/iam/group-detail"

export const metadata = { title: "User group | IAM" }

export default function Page() {
  return (
    <Suspense>
      <GroupDetail />
    </Suspense>
  )
}
