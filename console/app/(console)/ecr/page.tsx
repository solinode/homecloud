import { Suspense } from "react"

import { RepositoryList } from "@/components/ecr/repository-list"

export const metadata = { title: "Repositories" }

export default function Page() {
  return (
    <Suspense>
      <RepositoryList />
    </Suspense>
  )
}
