import { Suspense } from "react"

import { ParameterDetail } from "@/components/ssm/parameter-detail"

export const metadata = { title: "Parameter" }

export default function Page() {
  return (
    <Suspense>
      <ParameterDetail />
    </Suspense>
  )
}
