import { Suspense } from "react"

import { BucketDetail } from "@/components/s3/bucket-detail"

export const metadata = { title: "Bucket - S3" }

export default function BucketPage() {
  return (
    <Suspense>
      <BucketDetail />
    </Suspense>
  )
}
