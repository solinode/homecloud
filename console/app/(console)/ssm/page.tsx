import { Suspense } from "react"

import { ParametersList } from "@/components/ssm/parameters-list"

export const metadata = { title: "Parameter Store" }

export default function Page() {
  return (
    <Suspense>
      <ParametersList />
    </Suspense>
  )
}
