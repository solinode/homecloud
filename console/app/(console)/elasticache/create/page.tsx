import { Suspense } from "react"

import { CreateWizard } from "@/components/rds/create-wizard"

export const metadata = { title: "Create cluster" }

export default function Page() {
  return (
    <Suspense>
      <CreateWizard family="elasticache" />
    </Suspense>
  )
}
