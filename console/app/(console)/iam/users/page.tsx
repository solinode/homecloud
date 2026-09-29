import { Suspense } from "react"

import { UsersList } from "@/components/iam/users-list"

export const metadata = { title: "Users | IAM" }

export default function Page() {
  return (
    <Suspense>
      <UsersList />
    </Suspense>
  )
}
