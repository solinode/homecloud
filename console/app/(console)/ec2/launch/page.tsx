import { Suspense } from "react"

import { LaunchWizard } from "@/components/ec2/launch-wizard"

export const metadata = { title: "Launch an instance" }

export default function Page() {
  return (
    <Suspense>
      <LaunchWizard />
    </Suspense>
  )
}
