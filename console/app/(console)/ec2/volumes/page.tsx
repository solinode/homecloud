import { Suspense } from "react"

import { VolumesList } from "@/components/ec2/volumes-list"

export const metadata = { title: "Volumes" }

export default function Page() {
  return (
    <Suspense>
      <VolumesList />
    </Suspense>
  )
}
