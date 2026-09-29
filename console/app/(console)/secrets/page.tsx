import { Suspense } from "react"

import { SecretList } from "@/components/secrets/secret-list"

export const metadata = { title: "Secrets - Secrets Manager" }

export default function SecretsPage() {
  return (
    <Suspense>
      <SecretList />
    </Suspense>
  )
}
