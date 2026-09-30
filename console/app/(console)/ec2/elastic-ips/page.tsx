import { Suspense } from "react"

import { ElasticIPsList } from "@/components/ec2/elastic-ips-list"

export const metadata = { title: "Elastic IPs" }

export default function Page() {
  return (
    <Suspense>
      <ElasticIPsList />
    </Suspense>
  )
}
