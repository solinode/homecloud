import { Suspense } from "react"

import { ParameterForm } from "@/components/ssm/parameter-form"

export const metadata = { title: "Create parameter" }

export default function Page() {
  return (
    <Suspense>
      <ParameterForm />
    </Suspense>
  )
}
