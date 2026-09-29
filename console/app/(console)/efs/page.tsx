import { Suspense } from "react"

import { FileSystemList } from "@/components/efs/file-system-list"

export const metadata = { title: "File systems" }

export default function Page() {
  return (
    <Suspense>
      <FileSystemList />
    </Suspense>
  )
}
