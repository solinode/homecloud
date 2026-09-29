import { Suspense } from "react"

import { ImagesList } from "@/components/ec2/images-list"

export const metadata = { title: "AMIs" }

export default function Page() {
  return (
    <Suspense>
      <ImagesList />
    </Suspense>
  )
}
