import { Suspense } from "react"

import { DbList } from "@/components/rds/db-list"

export const metadata = { title: "Databases" }

export default function Page() {
  return (
    <Suspense>
      <DbList family="rds" />
    </Suspense>
  )
}
