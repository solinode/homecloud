import { Suspense } from "react"

import { FunctionsList } from "@/components/lambda/functions-list"

export const metadata = { title: "Functions" }

export default function Page() {
  return (
    <Suspense>
      <FunctionsList />
    </Suspense>
  )
}
