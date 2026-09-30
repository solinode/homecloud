import { Suspense } from "react"

import { InternetGateways } from "@/components/vpc/internet-gateways"

export const metadata = { title: "Internet gateways" }

export default function Page() {
  return (
    <Suspense>
      <InternetGateways />
    </Suspense>
  )
}
