import { Suspense } from "react"

import { TasksList } from "@/components/ecs/tasks-list"

export const metadata = { title: "ECS tasks" }

export default function Page() {
  return (
    <Suspense>
      <TasksList />
    </Suspense>
  )
}
