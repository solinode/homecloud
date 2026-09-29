import { Suspense } from "react"

import { ZoneDetail } from "@/components/route53/zone-detail"

export const metadata = { title: "Hosted zone" }

export default function Page() {
  return (
    <Suspense>
      <ZoneDetail />
    </Suspense>
  )
}
