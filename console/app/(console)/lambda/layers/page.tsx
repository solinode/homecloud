import { Suspense } from "react"

import { LayersPage } from "@/components/lambda/layers"

export const metadata = { title: "Layers" }

export default function Page() {
  return (
    <Suspense>
      <LayersPage />
    </Suspense>
  )
}
