"use client"

import { useState } from "react"
import Link from "next/link"
import { AlertCircle, ArrowLeft, Copy, Layers, Loader2, Terminal, Trash2 } from "lucide-react"
import { useRouter } from "next/navigation"
import { toast } from "sonner"

import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { ActionsMenu } from "@/components/console/actions-menu"
import { ConfirmDialog } from "@/components/console/confirm-dialog"
import { CopyButton, CopyableText, copyText } from "@/components/console/copy-button"
import { DataTable, type Column } from "@/components/console/data-table"
import { EmptyState } from "@/components/console/empty-state"
import { ErrorState } from "@/components/console/error-state"
import { KeyValueGrid } from "@/components/console/key-value"
import { DetailSkeleton } from "@/components/console/loading"
import { PageHeader } from "@/components/console/page-header"
import { Section } from "@/components/console/section"
import { TagList } from "@/components/console/tags-editor"
import { TimeAgo } from "@/components/console/time-ago"
import { ApiError, api } from "@/lib/api"
import { formatBytes, formatDate, pluralize } from "@/lib/format"
import { revalidate, useApi, useQueryParam } from "@/lib/hooks"
import type { EcrImage, EcrRepositoryDetail } from "@/lib/types"

import { CommandSteps, DeleteRepositoryDialog, ECR_PATH, PushCommandsDialog, repoApiPath, shortDigest } from "./common"

const validTime = (t?: string) => !!t && new Date(t).getFullYear() > 1970

export function RepositoryDetail() {
  const name = useQueryParam("name")
  const router = useRouter()
  const { data, error, isLoading, isValidating, mutate } = useApi<EcrRepositoryDetail>(name ? repoApiPath(name) : null, { refreshInterval: 15_000 })
  const [pushOpen, setPushOpen] = useState(false)
  const [deleting, setDeleting] = useState(false)
  const [imageTarget, setImageTarget] = useState<EcrImage | null>(null)
  const [selected, setSelected] = useState<string[]>([])

  const crumbs = [{ label: "Amazon ECR", href: "/ecr/" }, { label: "Repositories", href: "/ecr/" }, { label: name || "Repository" }]

  if (!name) {
    return (
      <>
        <PageHeader title="Repository" breadcrumbs={crumbs} />
        <EmptyState title="No repository selected" description="Open a repository from the repositories list." action={<BackButton />} />
      </>
    )
  }
  if (error && !data) {
    const notFound = error instanceof ApiError && error.status === 404
    return (
      <div className="flex flex-col gap-4">
        <PageHeader title={name} breadcrumbs={crumbs} />
        {notFound ? (
          <Section>
            <EmptyState icon={AlertCircle} title="Repository not found" description={`Repository ${name} does not exist.`} action={<BackButton />} />
          </Section>
        ) : (
          <ErrorState error={error} onRetry={() => mutate()} />
        )}
      </div>
    )
  }
  if (isLoading || !data) return <DetailSkeleton />

  const repo = data.repository
  const images = data.images ?? []
  const totalSize = images.reduce((a, i) => a + (i.size_bytes || 0), 0)
  const sel = images.find((i) => i.digest === selected[0])

  const columns: Column<EcrImage>[] = [
    {
      id: "tags",
      header: "Image tags",
      value: (i) => i.tags.join(" "),
      cell: (i) =>
        i.tags.length ? (
          <span className="flex flex-wrap gap-1">
            {i.tags.map((t) => (
              <Badge key={t} variant="secondary" className="font-mono">
                {t}
              </Badge>
            ))}
          </span>
        ) : (
          <span className="text-muted-foreground italic">untagged</span>
        ),
    },
    {
      id: "digest",
      header: "Digest",
      value: (i) => i.digest,
      cell: (i) => <CopyableText value={i.digest} display={shortDigest(i.digest)} />,
    },
    { id: "platform", header: "Platform", value: (i) => i.platform ?? "", cell: (i) => <span className="font-mono text-[13px]">{i.platform || "-"}</span>, hideBelow: "md" },
    {
      id: "size",
      header: "Size",
      value: (i) => i.size_bytes,
      cell: (i) => <span className="whitespace-nowrap tabular-nums">{formatBytes(i.size_bytes)}</span>,
      className: "text-right",
      headerClassName: "text-right",
      hideBelow: "sm",
    },
    {
      id: "pushed",
      header: "Pushed at",
      value: (i) => (validTime(i.pushed_at) ? i.pushed_at : ""),
      cell: (i) => (validTime(i.pushed_at) ? <TimeAgo value={i.pushed_at} /> : <span className="text-muted-foreground">-</span>),
      hideBelow: "lg",
    },
    {
      id: "pull",
      header: <span className="sr-only">Pull</span>,
      cell: (i) => <CopyButton value={pullCommand(repo.uri, i)} label="Copy pull command" />,
      className: "w-10",
    },
  ]

  return (
    <div className="flex flex-col gap-4">
      <PageHeader
        title={<span className="break-all">{repo.name}</span>}
        description={repo.description || <span className="font-mono text-[13px] break-all">{repo.uri}</span>}
        breadcrumbs={crumbs}
        actions={
          <>
            <Button variant="outline" size="sm" onClick={() => mutate()} disabled={isValidating}>
              {isValidating && <Loader2 className="animate-spin" />}
              Refresh
            </Button>
            <Button variant="outline" size="sm" onClick={() => setPushOpen(true)}>
              <Terminal /> View push commands
            </Button>
            <Button variant="destructive" size="sm" onClick={() => setDeleting(true)}>
              <Trash2 /> Delete
            </Button>
          </>
        }
      />

      <Section title="General information">
        <KeyValueGrid
          columns={3}
          items={[
            { label: "Repository name", value: <span className="font-mono text-[13px] break-all">{repo.name}</span> },
            { label: "URI", value: <CopyableText value={repo.uri} /> },
            { label: "Tag mutability", value: repo.tag_mutable ? "Mutable" : "Immutable" },
            { label: "Images", value: String(images.length) },
            { label: "Total size", value: formatBytes(totalSize) },
            { label: "Created", value: <span>{formatDate(repo.created_at)} (<TimeAgo value={repo.created_at} />)</span> },
            { label: "Description", value: repo.description ?? "" },
            { label: "ARN", value: <CopyableText value={repo.arn} />, wide: true },
          ]}
        />
      </Section>

      <DataTable
        title="Images"
        data={images}
        columns={columns}
        rowId={(i) => i.digest}
        onRefresh={() => mutate()}
        refreshing={isValidating}
        selection="single"
        selected={selected}
        onSelectedChange={setSelected}
        searchPlaceholder="Find images by tag or digest"
        defaultSort={{ id: "pushed", desc: true }}
        actions={
          <ActionsMenu
            disabled={!sel}
            items={[
              {
                label: "Copy URI (by digest)",
                icon: <Copy />,
                onSelect: async () => {
                  if (sel && (await copyText(sel.uri))) toast.success("Image URI copied")
                },
              },
              {
                label: "Copy pull command",
                icon: <Terminal />,
                onSelect: async () => {
                  if (sel && (await copyText(pullCommand(repo.uri, sel)))) toast.success("Pull command copied")
                },
              },
              { separator: true },
              { label: "Delete image", icon: <Trash2 />, destructive: true, onSelect: () => sel && setImageTarget(sel) },
            ]}
          />
        }
        empty={
          <EmptyState
            icon={Layers}
            title="No images"
            description="Push an image to this repository with the commands below."
            action={
              <Button size="sm" variant="outline" onClick={() => setPushOpen(true)}>
                <Terminal /> View push commands
              </Button>
            }
          />
        }
      />

      {images.length === 0 && data.push_commands?.length > 0 && (
        <Section title="Push commands">
          <CommandSteps
            steps={data.push_commands.map((c, i) => ({
              title: ["Build your image", "Tag it with the repository URI", "Push it"][i] ?? `Step ${i + 1}`,
              command: c,
            }))}
          />
        </Section>
      )}

      <Section title="Tags">
        <TagList tags={repo.tags} />
      </Section>

      <PushCommandsDialog repo={repo} commands={data.push_commands ?? []} open={pushOpen} onOpenChange={setPushOpen} />
      <DeleteRepositoryDialog
        name={repo.name}
        imageCount={images.length}
        open={deleting}
        onOpenChange={setDeleting}
        onDeleted={() => router.push("/ecr/")}
      />
      <ConfirmDialog
        open={!!imageTarget}
        onOpenChange={(o) => !o && setImageTarget(null)}
        title="Delete image?"
        description={
          imageTarget && (
            <div className="flex flex-col gap-2">
              <p>
                <span className="text-foreground font-mono text-[13px] break-all">{shortDigest(imageTarget.digest)}</span> is deleted from{" "}
                <span className="text-foreground font-medium">{repo.name}</span>
                {imageTarget.tags.length > 0 && (
                  <>
                    {" "}
                    together with its {pluralize(imageTarget.tags.length, "tag")}:{" "}
                    <span className="text-foreground font-mono text-[13px]">{imageTarget.tags.join(", ")}</span>
                  </>
                )}
                . This cannot be undone.
              </p>
            </div>
          )
        }
        actionLabel="Delete image"
        onConfirm={async () => {
          if (!imageTarget) return
          await api.del(`${ECR_PATH}/images`, { repository: repo.name, image: imageTarget.digest })
          toast.success(`Deleted ${shortDigest(imageTarget.digest)}`)
          setSelected([])
          await revalidate(ECR_PATH)
        }}
      />
    </div>
  )
}

function pullCommand(uri: string, img: EcrImage) {
  return img.tags.length ? `docker pull ${uri}:${img.tags[0]}` : `docker pull ${img.uri}`
}

function BackButton() {
  return (
    <Button variant="outline" size="sm" asChild>
      <Link href="/ecr/">
        <ArrowLeft /> Back to repositories
      </Link>
    </Button>
  )
}
