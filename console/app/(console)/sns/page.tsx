import { Suspense } from "react"

import { TopicsList } from "@/components/sns/topics-list"

export const metadata = { title: "Topics" }

export default function Page() {
  return (
    <Suspense>
      <TopicsList />
    </Suspense>
  )
}
