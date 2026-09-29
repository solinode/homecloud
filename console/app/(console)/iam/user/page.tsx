import { Suspense } from "react"

import { UserDetail } from "@/components/iam/user-detail"

export const metadata = { title: "User | IAM" }

export default function Page() {
  return (
    <Suspense>
      <UserDetail />
    </Suspense>
  )
}
