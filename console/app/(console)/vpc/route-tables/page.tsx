import { Suspense } from "react"

import { RouteTables } from "@/components/vpc/route-tables"

export const metadata = { title: "Route tables" }

export default function Page() {
  return (
    <Suspense>
      <RouteTables />
    </Suspense>
  )
}
