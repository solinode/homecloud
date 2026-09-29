import { Suspense } from "react"

import { TriggersList } from "@/components/lambda/triggers"

export const metadata = { title: "Event source mappings" }

export default function Page() {
  return (
    <Suspense>
      <TriggersList />
    </Suspense>
  )
}
