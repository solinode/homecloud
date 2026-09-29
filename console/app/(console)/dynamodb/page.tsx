import { Suspense } from "react"

import { TablesList } from "@/components/dynamodb/tables-list"

export const metadata = { title: "DynamoDB tables" }

export default function Page() {
  return (
    <Suspense>
      <TablesList />
    </Suspense>
  )
}
