import { Suspense } from "react"

import { StacksList } from "@/components/cloudformation/stacks-list"

export const metadata = { title: "Stacks" }

export default function Page() {
  return (
    <Suspense>
      <StacksList />
    </Suspense>
  )
}
