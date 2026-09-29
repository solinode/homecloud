import { Suspense } from "react"

import { RoleDetail } from "@/components/iam/role-detail"

export const metadata = { title: "Role | IAM" }

export default function Page() {
  return (
    <Suspense>
      <RoleDetail />
    </Suspense>
  )
}
