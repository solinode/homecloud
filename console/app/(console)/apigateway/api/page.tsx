import { Suspense } from "react"

import { ApiDetail } from "@/components/apigateway/api-detail"

export const metadata = { title: "API" }

export default function Page() {
  return (
    <Suspense>
      <ApiDetail />
    </Suspense>
  )
}
