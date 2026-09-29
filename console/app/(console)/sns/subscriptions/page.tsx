import { Suspense } from "react"

import { SubscriptionsPage } from "@/components/sns/subscriptions-page"

export const metadata = { title: "Subscriptions" }

export default function Page() {
  return (
    <Suspense>
      <SubscriptionsPage />
    </Suspense>
  )
}
