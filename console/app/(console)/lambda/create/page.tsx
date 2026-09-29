import { Suspense } from "react"

import { CreateFunction } from "@/components/lambda/create-function"

export const metadata = { title: "Create function" }

export default function Page() {
  return (
    <Suspense>
      <CreateFunction />
    </Suspense>
  )
}
