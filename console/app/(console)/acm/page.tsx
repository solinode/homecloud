import { Suspense } from "react"

import { CertificatesList } from "@/components/acm/certificates-list"

export const metadata = { title: "Certificates" }

export default function Page() {
  return (
    <Suspense>
      <CertificatesList />
    </Suspense>
  )
}
