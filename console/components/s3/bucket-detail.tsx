"use client"

import { useState } from "react"
import Link from "next/link"
import { useRouter } from "next/navigation"
import { Copy, HardDrive, RefreshCw, Trash2 } from "lucide-react"
import { toast } from "sonner"

import { Button } from "@/components/ui/button"
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs"
import { ActionsMenu } from "@/components/console/actions-menu"
import { copyText } from "@/components/console/copy-button"
import { EmptyState } from "@/components/console/empty-state"
import { ErrorState } from "@/components/console/error-state"
import { DetailSkeleton } from "@/components/console/loading"
import { PageHeader } from "@/components/console/page-header"
import { StatusBadge } from "@/components/console/status-badge"
import { seg } from "@/lib/api"
import { revalidate, useApi, useQueryParam, useSetQueryParam } from "@/lib/hooks"
import type { Bucket, BucketDetail as BucketDetailT } from "@/lib/types"
import { cn } from "@/lib/utils"

import { DeleteBucketDialog } from "./bucket-dialogs"
import { ObjectsTab } from "./objects-tab"
import { PropertiesTab } from "./properties-tab"

const TABS = ["objects", "properties"] as const

export function BucketDetail() {
  const name = useQueryParam("name")
  const tabParam = useQueryParam("tab")
  const prefix = useQueryParam("prefix")
  const setParam = useSetQueryParam()
  const router = useRouter()
  const tab = (TABS as readonly string[]).includes(tabParam) ? tabParam : "objects"
  const path = name ? `/api/v1/s3/buckets/${seg(name)}` : null
  const { data, error, isLoading, isValidating, mutate } = useApi<BucketDetailT>(path, { revalidateOnFocus: false })
  const buckets = useApi<Bucket[]>(name ? "/api/v1/s3/buckets" : null)
  const listing = buckets.data?.find((b) => b.name === name)
  const [deleteOpen, setDeleteOpen] = useState(false)

  const crumbs = [{ label: "Amazon S3", href: "/s3/" }, { label: "Buckets", href: "/s3/" }, { label: name || "Bucket" }]

  if (!name) {
    return (
      <div>
        <PageHeader title="Bucket" breadcrumbs={crumbs} />
        <EmptyState
          icon={HardDrive}
          title="No bucket selected"
          description="Choose a bucket from the bucket list."
          action={
            <Button asChild size="sm">
              <Link href="/s3/">View buckets</Link>
            </Button>
          }
        />
      </div>
    )
  }

  if (error && !data) {
    return (
      <div>
        <PageHeader title={name} breadcrumbs={crumbs} />
        <ErrorState error={error} onRetry={() => mutate()} />
      </div>
    )
  }

  const onChanged = () => {
    mutate()
    revalidate("/api/v1/s3/buckets")
  }

  return (
    <div className="flex flex-col">
      <PageHeader
        title={name}
        breadcrumbs={crumbs}
        badge={
          data && (
            <div className="flex flex-wrap items-center gap-1.5">
              {data.public && <StatusBadge status="public" label="Public" tone="warning" />}
              {data.versioning === "Enabled" && <StatusBadge status="enabled" label="Versioning enabled" tone="info" />}
              {data.website && <StatusBadge status="enabled" label="Website" tone="info" />}
            </div>
          )
        }
        actions={
          <>
            <Button variant="outline" size="sm" onClick={onChanged} aria-label="Refresh bucket">
              <RefreshCw className={cn(isValidating && "animate-spin")} />
            </Button>
            <ActionsMenu
              items={[
                {
                  label: "Copy ARN",
                  icon: <Copy />,
                  disabled: !data,
                  onSelect: async () => {
                    if (data && (await copyText(data.arn))) toast.success("ARN copied")
                  },
                },
                {
                  label: "Copy S3 URI",
                  icon: <Copy />,
                  onSelect: async () => {
                    if (await copyText(`s3://${name}`)) toast.success("S3 URI copied")
                  },
                },
                { separator: true },
                { label: "Delete bucket", icon: <Trash2 />, destructive: true, onSelect: () => setDeleteOpen(true) },
              ]}
            />
          </>
        }
      />

      <Tabs
        value={tab}
        onValueChange={(v) => setParam("tab", v === "objects" ? null : v)}
      >
        <TabsList>
          <TabsTrigger value="objects">Objects</TabsTrigger>
          <TabsTrigger value="properties">Properties</TabsTrigger>
        </TabsList>
        <TabsContent value="objects">
          <ObjectsTab bucket={name} prefix={prefix} onPrefixChange={(p) => setParam("prefix", p || null)} />
        </TabsContent>
        <TabsContent value="properties">
          {isLoading || !data ? <DetailSkeleton /> : <PropertiesTab bucket={data} listing={listing} onChanged={onChanged} />}
        </TabsContent>
      </Tabs>

      <DeleteBucketDialog bucket={name} open={deleteOpen} onOpenChange={setDeleteOpen} onDeleted={() => router.push("/s3/")} />
    </div>
  )
}
