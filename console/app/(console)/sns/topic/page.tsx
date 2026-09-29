import { Suspense } from "react"

import { TopicDetail } from "@/components/sns/topic-detail"

export const metadata = { title: "Topic" }

export default function Page() {
  return (
    <Suspense>
      <TopicDetail />
    </Suspense>
  )
}
