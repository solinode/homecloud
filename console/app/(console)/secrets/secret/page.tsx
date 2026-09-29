import { Suspense } from "react"

import { SecretDetail } from "@/components/secrets/secret-detail"

export const metadata = { title: "Secret - Secrets Manager" }

export default function SecretPage() {
  return (
    <Suspense>
      <SecretDetail />
    </Suspense>
  )
}
