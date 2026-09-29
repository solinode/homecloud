import { Suspense } from "react"

import { ApiList } from "@/components/apigateway/api-list"

export const metadata = { title: "APIs" }

export default function Page() {
  return (
    <Suspense>
      <ApiList />
    </Suspense>
  )
}
