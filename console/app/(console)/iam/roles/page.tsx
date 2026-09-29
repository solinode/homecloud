import { Suspense } from "react"

import { RolesList } from "@/components/iam/roles-list"

export const metadata = { title: "Roles | IAM" }

export default function Page() {
  return (
    <Suspense>
      <RolesList />
    </Suspense>
  )
}
