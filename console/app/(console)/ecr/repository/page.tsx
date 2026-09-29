import { Suspense } from "react"

import { RepositoryDetail } from "@/components/ecr/repository-detail"

export const metadata = { title: "Repository" }

export default function Page() {
  return (
    <Suspense>
      <RepositoryDetail />
    </Suspense>
  )
}
