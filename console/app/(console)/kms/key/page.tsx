import { Suspense } from "react"

import { KeyDetail } from "@/components/kms/key-detail"

export const metadata = { title: "KMS key" }

export default function Page() {
  return (
    <Suspense>
      <KeyDetail />
    </Suspense>
  )
}
