import { Suspense } from "react"

import { CreateSecret } from "@/components/secrets/create-secret"

export const metadata = { title: "Store a new secret - Secrets Manager" }

export default function CreateSecretPage() {
  return (
    <Suspense>
      <CreateSecret />
    </Suspense>
  )
}
