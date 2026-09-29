import { Suspense } from "react"

import { KeysList } from "@/components/kms/keys-list"

export const metadata = { title: "KMS keys" }

export default function Page() {
  return (
    <Suspense>
      <KeysList />
    </Suspense>
  )
}
