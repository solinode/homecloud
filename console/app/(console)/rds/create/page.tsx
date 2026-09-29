import { Suspense } from "react"

import { CreateWizard } from "@/components/rds/create-wizard"

export const metadata = { title: "Create database" }

export default function Page() {
  return (
    <Suspense>
      <CreateWizard family="rds" />
    </Suspense>
  )
}
