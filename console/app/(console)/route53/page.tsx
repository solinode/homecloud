import { Suspense } from "react"

import { ZonesList } from "@/components/route53/zones-list"

export const metadata = { title: "Route 53 hosted zones" }

export default function Page() {
  return (
    <Suspense>
      <ZonesList />
    </Suspense>
  )
}
