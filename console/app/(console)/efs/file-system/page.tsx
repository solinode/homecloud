import { Suspense } from "react"

import { FileSystemDetail } from "@/components/efs/file-system-detail"

export const metadata = { title: "File system" }

export default function Page() {
  return (
    <Suspense>
      <FileSystemDetail />
    </Suspense>
  )
}
