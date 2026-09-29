import { Suspense } from "react"

import { RoleCreate } from "@/components/iam/role-create"

export const metadata = { title: "Create role | IAM" }

export default function Page() {
  return (
    <Suspense>
      <RoleCreate />
    </Suspense>
  )
}
