import { Suspense } from "react"

import { RegisterTaskDefinition } from "@/components/ecs/register-task-definition"

export const metadata = { title: "Register task definition" }

export default function Page() {
  return (
    <Suspense>
      <RegisterTaskDefinition />
    </Suspense>
  )
}
