import { Suspense } from "react"

import { StackForm } from "@/components/cloudformation/stack-form"

export const metadata = { title: "Create stack" }

export default function Page() {
  return (
    <Suspense>
      <StackForm />
    </Suspense>
  )
}
