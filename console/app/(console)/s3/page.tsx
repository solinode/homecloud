import { Suspense } from "react"

import { BucketList } from "@/components/s3/bucket-list"

export const metadata = { title: "Buckets - S3" }

export default function S3Page() {
  return (
    <Suspense>
      <BucketList />
    </Suspense>
  )
}
