import { Suspense } from "react"

import { CreateTable } from "@/components/dynamodb/create-table"

export const metadata = { title: "Create table" }

export default function Page() {
  return (
    <Suspense>
      <CreateTable />
    </Suspense>
  )
}
