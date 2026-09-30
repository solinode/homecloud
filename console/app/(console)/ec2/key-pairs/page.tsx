import { Suspense } from "react"

import { KeyPairsList } from "@/components/ec2/key-pairs-list"

export const metadata = { title: "Key pairs" }

export default function Page() {
  return (
    <Suspense>
      <KeyPairsList />
    </Suspense>
  )
}
