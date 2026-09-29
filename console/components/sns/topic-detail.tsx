"use client"

import { useEffect, useState } from "react"
import Link from "next/link"
import { useRouter } from "next/navigation"
import { AlertCircle, ArrowLeft, Check, Loader2, Pencil, Send, Tags as TagsIcon, Trash2, X } from "lucide-react"
import { toast } from "sonner"

import { Button } from "@/components/ui/button"
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog"
import { Input } from "@/components/ui/input"
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs"
import { ConfirmDialog } from "@/components/console/confirm-dialog"
import { CopyableText } from "@/components/console/copy-button"
import { EmptyState } from "@/components/console/empty-state"
import { ErrorState } from "@/components/console/error-state"
import { KeyValueGrid } from "@/components/console/key-value"
import { DetailSkeleton } from "@/components/console/loading"
import { PageHeader } from "@/components/console/page-header"
import { Section } from "@/components/console/section"
import { TagList, TagsEditor, rowsToTags, tagsToRows, type TagRow } from "@/components/console/tags-editor"
import { TimeAgo } from "@/components/console/time-ago"
import { ApiError, api, errorMessage } from "@/lib/api"
import { formatDate, formatNumber } from "@/lib/format"
import { revalidate, useApi, useQueryParam, useSetQueryParam } from "@/lib/hooks"
import type { TopicDetail as TopicDetailT } from "@/lib/types"

import { SNS_PATH, SNS_POLL, topicPath } from "./common"
import { PublishPanel } from "./publish-panel"
import { SubscriptionsTable } from "./subscriptions"

const TABS = ["subscriptions", "publish", "tags"] as const
type Tab = (typeof TABS)[number]

export function TopicDetail() {
  const router = useRouter()
  const name = useQueryParam("name")
  const tabParam = useQueryParam("tab")
  const setParam = useSetQueryParam()
  const tab: Tab = (TABS as readonly string[]).includes(tabParam) ? (tabParam as Tab) : "subscriptions"

  const { data: topic, error, isLoading, isValidating, mutate } = useApi<TopicDetailT>(name ? topicPath(name) : null, { refreshInterval: SNS_POLL })
  const [deleting, setDeleting] = useState(false)
  const [tagging, setTagging] = useState(false)

  const crumbs = [{ label: "SNS", href: "/sns/" }, { label: "Topics", href: "/sns/" }, { label: name || "Topic" }]

  if (!name) {
    return (
      <>
        <PageHeader title="Topic" breadcrumbs={crumbs} />
        <EmptyState title="No topic selected" description="Open a topic from the topics list." action={<BackButton />} />
      </>
    )
  }
  if (error && !topic) {
    const notFound = error instanceof ApiError && error.status === 404
    return (
      <div className="flex flex-col gap-4">
        <PageHeader title={name} breadcrumbs={crumbs} />
        {notFound ? (
          <Section>
            <EmptyState icon={AlertCircle} title="Topic not found" description={`Topic ${name} does not exist. It may have been deleted.`} action={<BackButton />} />
          </Section>
        ) : (
          <ErrorState error={error} onRetry={() => mutate()} />
        )}
      </div>
    )
  }
  if (isLoading || !topic) return <DetailSkeleton />

  return (
    <div className="flex flex-col gap-4">
      <PageHeader
        title={topic.name}
        description={topic.display_name || undefined}
        breadcrumbs={crumbs}
        actions={
          <>
            <Button variant="outline" size="sm" onClick={() => mutate()} disabled={isValidating} aria-label="Refresh">
              {isValidating ? <Loader2 className="animate-spin" /> : null}
              Refresh
            </Button>
            <Button variant="outline" size="sm" onClick={() => setDeleting(true)}>
              <Trash2 /> Delete
            </Button>
            <Button size="sm" onClick={() => setParam("tab", "publish")}>
              <Send /> Publish message
            </Button>
          </>
        }
      />

      <Section title="Details">
        <KeyValueGrid
          columns={3}
          items={[
            { label: "Name", value: topic.name },
            { label: "Display name", value: <DisplayNameField topic={topic} /> },
            { label: "Type", value: topic.fifo ? "FIFO" : "Standard" },
            { label: "Subscriptions", value: formatNumber(topic.subscriptions) },
            { label: "Messages published", value: formatNumber(topic.messages_published) },
            { label: "Created", value: <span>{formatDate(topic.created_at)} (<TimeAgo value={topic.created_at} />)</span> },
            { label: "ARN", value: <CopyableText value={topic.arn} />, wide: true },
          ]}
        />
      </Section>

      <Tabs value={tab} onValueChange={(v) => setParam("tab", v === "subscriptions" ? null : v)}>
        <TabsList>
          <TabsTrigger value="subscriptions">Subscriptions ({topic.subscriptions})</TabsTrigger>
          <TabsTrigger value="publish">Publish message</TabsTrigger>
          <TabsTrigger value="tags">Tags</TabsTrigger>
        </TabsList>
        <TabsContent value="subscriptions">
          <SubscriptionsTable
            data={topic.subscription_list ?? []}
            topic={topic.name}
            onRefresh={() => mutate()}
            refreshing={isValidating}
            description="Endpoints that receive a copy of each message published to this topic. Delivery statistics update after each publish."
          />
        </TabsContent>
        <TabsContent value="publish">
          <PublishPanel topic={topic} />
        </TabsContent>
        <TabsContent value="tags">
          <Section
            title="Tags"
            actions={
              <Button variant="outline" size="sm" onClick={() => setTagging(true)}>
                <TagsIcon /> Manage tags
              </Button>
            }
          >
            <TagList tags={topic.tags} />
          </Section>
        </TabsContent>
      </Tabs>

      <ConfirmDialog
        open={deleting}
        onOpenChange={setDeleting}
        title={`Delete ${topic.name}?`}
        description={
          <>
            The topic and its {formatNumber(topic.subscriptions)} subscription{topic.subscriptions === 1 ? "" : "s"} are deleted. Subscribed queues and
            functions are not affected.
          </>
        }
        confirmText={topic.name}
        onConfirm={async () => {
          await api.del(topicPath(topic.name))
          toast.success(`Deleted topic ${topic.name}`)
          router.push("/sns/")
          await revalidate(SNS_PATH)
        }}
      />
      <TagsDialog topic={tagging ? topic : null} onClose={() => setTagging(false)} />
    </div>
  )
}

function DisplayNameField({ topic }: { topic: TopicDetailT }) {
  const [editing, setEditing] = useState(false)
  const [value, setValue] = useState(topic.display_name)
  const [pending, setPending] = useState(false)
  useEffect(() => setValue(topic.display_name), [topic.display_name, editing])

  const save = async () => {
    if (value.length > 100) {
      toast.error("Display names are at most 100 characters")
      return
    }
    setPending(true)
    try {
      await api.patch(topicPath(topic.name), { display_name: value.trim() })
      toast.success("Display name saved")
      await revalidate(SNS_PATH)
      setEditing(false)
    } catch (e) {
      toast.error(errorMessage(e))
    } finally {
      setPending(false)
    }
  }

  if (!editing) {
    return (
      <span className="inline-flex items-center gap-1">
        {topic.display_name || <span className="text-muted-foreground">-</span>}
        <Button variant="ghost" size="icon" className="size-6" onClick={() => setEditing(true)} aria-label="Edit display name">
          <Pencil className="size-3.5" />
        </Button>
      </span>
    )
  }
  return (
    <form
      className="flex items-center gap-1"
      onSubmit={(e) => {
        e.preventDefault()
        save()
      }}
    >
      <Input value={value} onChange={(e) => setValue(e.target.value)} className="h-8" autoFocus aria-label="Display name" />
      <Button type="submit" variant="ghost" size="icon" className="size-8" disabled={pending} aria-label="Save">
        {pending ? <Loader2 className="animate-spin" /> : <Check />}
      </Button>
      <Button type="button" variant="ghost" size="icon" className="size-8" onClick={() => setEditing(false)} disabled={pending} aria-label="Cancel">
        <X />
      </Button>
    </form>
  )
}

function TagsDialog({ topic, onClose }: { topic: TopicDetailT | null; onClose: () => void }) {
  const [rows, setRows] = useState<TagRow[]>([])
  const [pending, setPending] = useState(false)
  const [err, setErr] = useState<string | null>(null)
  useEffect(() => {
    if (topic) {
      setRows(tagsToRows(topic.tags))
      setErr(null)
    }
  }, [topic])

  const submit = async (e: React.FormEvent) => {
    e.preventDefault()
    if (!topic) return
    const keys = rows.map((r) => r.key.trim()).filter(Boolean)
    if (new Set(keys).size !== keys.length) {
      setErr("Tag keys must be unique")
      return
    }
    setPending(true)
    try {
      await api.patch(topicPath(topic.name), { tags: rowsToTags(rows) ?? {} })
      toast.success(`Saved tags for ${topic.name}`)
      await revalidate(SNS_PATH)
      onClose()
    } catch (e) {
      toast.error(errorMessage(e))
    } finally {
      setPending(false)
    }
  }

  return (
    <Dialog open={!!topic} onOpenChange={(o) => !o && !pending && onClose()}>
      <DialogContent className="sm:max-w-xl">
        <form onSubmit={submit} className="flex flex-col gap-4">
          <DialogHeader>
            <DialogTitle>Manage tags</DialogTitle>
            <DialogDescription>Tags are key/value labels for organizing topics.</DialogDescription>
          </DialogHeader>
          <TagsEditor rows={rows} onChange={(r) => (setRows(r), setErr(null))} />
          {err && <p className="text-destructive text-xs">{err}</p>}
          <DialogFooter>
            <Button type="button" variant="outline" onClick={onClose} disabled={pending}>
              Cancel
            </Button>
            <Button type="submit" disabled={pending}>
              {pending && <Loader2 className="animate-spin" />}
              Save tags
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}

function BackButton() {
  return (
    <Button variant="outline" size="sm" asChild>
      <Link href="/sns/">
        <ArrowLeft /> Back to topics
      </Link>
    </Button>
  )
}
