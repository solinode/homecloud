import { Suspense } from "react"

import { CryptoPage } from "@/components/kms/crypto-tool"

export const metadata = { title: "Encrypt / decrypt" }

export default function Page() {
  return (
    <Suspense>
      <CryptoPage />
    </Suspense>
  )
}
