import { Suspense } from "react"

import { TaskDetail } from "@/components/ecs/task-detail"

export const metadata = { title: "ECS task" }

export default function Page() {
  return (
    <Suspense>
      <TaskDetail />
    </Suspense>
  )
}
