import { Suspense } from "react"

import { BusesList } from "@/components/events/buses-list"

export const metadata = { title: "Event buses" }

export default function Page() {
  return (
    <Suspense>
      <BusesList />
    </Suspense>
  )
}
