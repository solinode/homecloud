import { Suspense } from "react"

import { TaskDefinitionDetail } from "@/components/ecs/task-definition-detail"

export const metadata = { title: "Task definition" }

export default function Page() {
  return (
    <Suspense>
      <TaskDefinitionDetail />
    </Suspense>
  )
}
