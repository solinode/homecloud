import { Suspense } from "react"

import { CertificateDetail } from "@/components/acm/certificate-detail"

export const metadata = { title: "Certificate" }

export default function Page() {
  return (
    <Suspense>
      <CertificateDetail />
    </Suspense>
  )
}
