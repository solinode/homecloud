import { Suspense } from "react"

import { TaskDefinitionsList } from "@/components/ecs/task-definitions-list"

export const metadata = { title: "Task definitions" }

export default function Page() {
  return (
    <Suspense>
      <TaskDefinitionsList />
    </Suspense>
  )
}
