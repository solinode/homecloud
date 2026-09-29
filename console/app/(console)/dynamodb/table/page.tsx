import { Suspense } from "react"

import { TableDetail } from "@/components/dynamodb/table-detail"

export const metadata = { title: "DynamoDB table" }

export default function Page() {
  return (
    <Suspense>
      <TableDetail />
    </Suspense>
  )
}
