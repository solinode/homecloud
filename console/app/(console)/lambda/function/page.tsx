import { Suspense } from "react"

import { FunctionDetail } from "@/components/lambda/function-detail"

export const metadata = { title: "Function" }

export default function Page() {
  return (
    <Suspense>
      <FunctionDetail />
    </Suspense>
  )
}
