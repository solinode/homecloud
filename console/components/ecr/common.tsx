"use client"

import { useEffect, useState } from "react"
import { useRouter } from "next/navigation"
import { AlertTriangle, CheckCircle2, Circle, Info, Loader2 } from "lucide-react"
import { toast } from "sonner"

import { Button } from "@/components/ui/button"
import { Checkbox } from "@/components/ui/checkbox"
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { Skeleton } from "@/components/ui/skeleton"
import { Switch } from "@/components/ui/switch"
import { Textarea } from "@/components/ui/textarea"
import { ConfirmDialog } from "@/components/console/confirm-dialog"
import { CopyButton, CopyableText } from "@/components/console/copy-button"
import { ErrorState } from "@/components/console/error-state"
import { Field } from "@/components/console/form-field"
import { Section } from "@/components/console/section"
import { StatusBadge } from "@/components/console/status-badge"
import { TagsEditor, rowsToTags, type TagRow } from "@/components/console/tags-editor"
import { api, ApiError, errorMessage } from "@/lib/api"
import { revalidate, useApi } from "@/lib/hooks"
import type { EcrRepository, EcrStatus } from "@/lib/types"
import { cn } from "@/lib/utils"

export const ECR_PATH = "/api/v1/ecr"
export const REPOS_PATH = `${ECR_PATH}/repositories`

/**
 * repoPath encodes a repository name for the `{name...}` API routes: each
 * "/"-separated segment is escaped, the slashes themselves stay raw.
 */
export const repoPath = (name: string) => name.split("/").map(encodeURIComponent).join("/")
export const repoApiPath = (name: string) => `${REPOS_PATH}/${repoPath(name)}`
export const repoHref = (name: string) => `/ecr/repository/?name=${encodeURIComponent(name)}`

/** shortDigest renders "sha256:92a293dfa5e0" (12 hex digits, like docker). */
export const shortDigest = (d: string) => (d.startsWith("sha256:") ? `sha256:${d.slice(7, 19)}` : d.slice(0, 19))

const NAME_RE = /^[a-z0-9]+(?:[._-][a-z0-9]+)*(?:\/[a-z0-9]+(?:[._-][a-z0-9]+)*)*$/

export function repoNameChecks(name: string) {
  return [
    { label: "2 to 256 characters", ok: name.length >= 2 && name.length <= 256 },
    { label: "Lowercase letters, digits and . _ - separators", ok: /^[a-z0-9._\-/]+$/.test(name) },
    { label: "Namespaces separated by / (e.g. team/app); no leading, trailing or doubled separators", ok: NAME_RE.test(name) },
  ]
}

export const validRepoName = (name: string) => repoNameChecks(name).every((c) => c.ok)

/** useRegistryStatus polls quickly until the registry container is available. */
export function useRegistryStatus() {
  return useApi<EcrStatus>(`${ECR_PATH}/status`, { refreshInterval: (d) => (d?.status === "available" ? 60_000 : 3_000) })
}

/** CommandBlock is one copyable shell command. */
export function CommandBlock({ command, className }: { command: string; className?: string }) {
  return (
    <div className={cn("bg-muted/50 flex items-start gap-2 rounded-md border px-3 py-2", className)}>
      <pre className="min-w-0 flex-1 overflow-x-auto font-mono text-[12.5px] leading-5 whitespace-pre">{command}</pre>
      <CopyButton value={command} label="Copy command" className="mt-px" />
    </div>
  )
}

/** CommandSteps renders numbered shell steps, each with its own copy button. */
export function CommandSteps({ steps }: { steps: { title: React.ReactNode; command: string }[] }) {
  return (
    <ol className="flex flex-col gap-4">
      {steps.map((s, i) => (
        <li key={i} className="flex gap-3">
          <span className="bg-primary/10 text-primary flex size-6 shrink-0 items-center justify-center rounded-full text-xs font-semibold">{i + 1}</span>
          <div className="flex min-w-0 flex-1 flex-col gap-1.5">
            <p className="text-sm">{s.title}</p>
            <CommandBlock command={s.command} />
          </div>
        </li>
      ))}
    </ol>
  )
}

/** RegistryPanel shows the registry endpoint and how to push to it. */
export function RegistryPanel() {
  const { data, error, isLoading, mutate } = useRegistryStatus()
  return (
    <Section title="Registry" description="A private Docker registry on this host. Repositories appear here automatically after the first docker push.">
      {error ? (
        <ErrorState error={error} onRetry={() => mutate()} />
      ) : isLoading || !data ? (
        <div className="flex flex-col gap-3">
          <Skeleton className="h-5 w-64" />
          <Skeleton className="h-10 w-full" />
        </div>
      ) : (
        <div className="flex flex-col gap-4">
          <dl className="grid grid-cols-1 gap-x-8 gap-y-3 sm:grid-cols-3">
            <div className="min-w-0">
              <dt className="text-muted-foreground mb-0.5 text-xs font-medium">Registry host</dt>
              <dd className="text-sm">
                <CopyableText value={data.registry} />
              </dd>
            </div>
            <div className="min-w-0">
              <dt className="text-muted-foreground mb-0.5 text-xs font-medium">Status</dt>
              <dd className="text-sm">
                <StatusBadge status={data.status} />
              </dd>
            </div>
            <div className="min-w-0">
              <dt className="text-muted-foreground mb-0.5 text-xs font-medium">Authentication</dt>
              <dd className="text-sm">None (loopback only)</dd>
            </div>
          </dl>
          {data.status !== "available" && (
            <p className="text-muted-foreground flex items-center gap-2 text-sm">
              <Loader2 className="size-4 animate-spin" /> The registry container is {data.status}. Pushes and image listings work once it is available.
            </p>
          )}
          <CommandSteps
            steps={[
              { title: "Tag a local image with the registry host", command: `docker tag myapp:latest ${data.registry}/myapp:latest` },
              { title: "Push it. The repository is created on first push.", command: `docker push ${data.registry}/myapp:latest` },
              { title: "Pull it anywhere on this machine", command: `docker pull ${data.registry}/myapp:latest` },
            ]}
          />
          <p className="text-muted-foreground flex gap-1.5 text-xs">
            <Info className="mt-px size-3.5 shrink-0" />
            <span>
              No <span className="font-mono">docker login</span> is needed: the registry listens on 127.0.0.1 only and Docker trusts{" "}
              <span className="font-mono">localhost</span> registries over plain HTTP. Namespaced names such as{" "}
              <span className="font-mono">{data.registry}/team/app</span> work too.
            </span>
          </p>
        </div>
      )}
    </Section>
  )
}

export function CreateRepositoryDialog({ open, onOpenChange }: { open: boolean; onOpenChange: (o: boolean) => void }) {
  const router = useRouter()
  const [name, setName] = useState("")
  const [mutable, setMutable] = useState(true)
  const [description, setDescription] = useState("")
  const [tags, setTags] = useState<TagRow[]>([])
  const [pending, setPending] = useState(false)
  const [touched, setTouched] = useState(false)

  useEffect(() => {
    if (open) {
      setName("")
      setMutable(true)
      setDescription("")
      setTags([])
      setTouched(false)
    }
  }, [open])

  const checks = repoNameChecks(name)
  const valid = validRepoName(name)

  const submit = async (e: React.FormEvent) => {
    e.preventDefault()
    setTouched(true)
    if (!valid) return
    setPending(true)
    try {
      await api.post<EcrRepository>(REPOS_PATH, { name, tag_mutable: mutable, description: description.trim() || undefined, tags: rowsToTags(tags) })
      toast.success(`Repository ${name} created`)
      await revalidate(ECR_PATH)
      onOpenChange(false)
      router.push(repoHref(name))
    } catch (err) {
      toast.error(errorMessage(err))
    } finally {
      setPending(false)
    }
  }

  return (
    <Dialog open={open} onOpenChange={(o) => !pending && onOpenChange(o)}>
      <DialogContent className="max-h-[90vh] overflow-y-auto sm:max-w-xl">
        <form onSubmit={submit} className="flex flex-col gap-5">
          <DialogHeader>
            <DialogTitle>Create repository</DialogTitle>
            <DialogDescription>
              Creating a repository first is optional: pushing an image to a new name creates its repository automatically.
            </DialogDescription>
          </DialogHeader>

          <Field label="Repository name" htmlFor="repo-name">
            <Input
              id="repo-name"
              autoFocus
              autoComplete="off"
              spellCheck={false}
              value={name}
              onChange={(e) => setName(e.target.value)}
              onBlur={() => setTouched(true)}
              placeholder="team/my-app"
              className="font-mono"
              aria-invalid={touched && !valid}
            />
            <ul className="mt-1 flex flex-col gap-1 text-xs">
              {checks.map((c) => (
                <li
                  key={c.label}
                  className={cn(
                    "flex items-center gap-1.5",
                    c.ok ? "text-emerald-600 dark:text-emerald-400" : touched || name ? "text-destructive" : "text-muted-foreground",
                  )}
                >
                  {c.ok ? <CheckCircle2 className="size-3.5 shrink-0" /> : <Circle className="size-3.5 shrink-0" />}
                  {c.label}
                </li>
              ))}
            </ul>
          </Field>

          <div className="flex items-start justify-between gap-4 rounded-md border p-3">
            <div>
              <Label htmlFor="repo-mutable" className="font-medium">
                Tag mutability: {mutable ? "Mutable" : "Immutable"}
              </Label>
              <p className="text-muted-foreground mt-0.5 text-xs">Recorded as repository metadata. The registry itself always accepts re-pushed tags.</p>
            </div>
            <Switch id="repo-mutable" checked={mutable} onCheckedChange={setMutable} />
          </div>

          <Field label="Description" htmlFor="repo-desc" optional>
            <Textarea id="repo-desc" rows={2} value={description} onChange={(e) => setDescription(e.target.value)} placeholder="What this image is for" />
          </Field>

          <Field label="Tags" optional>
            <TagsEditor rows={tags} onChange={setTags} />
          </Field>

          <DialogFooter>
            <Button type="button" variant="outline" onClick={() => onOpenChange(false)} disabled={pending}>
              Cancel
            </Button>
            <Button type="submit" disabled={pending || (touched && !valid)}>
              {pending && <Loader2 className="animate-spin" />}
              Create repository
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}

/** DeleteRepositoryDialog deletes a repository; force=true also deletes its images. */
export function DeleteRepositoryDialog({
  name,
  imageCount,
  open,
  onOpenChange,
  onDeleted,
}: {
  name: string
  imageCount?: number
  open: boolean
  onOpenChange: (o: boolean) => void
  onDeleted?: () => void
}) {
  const [force, setForce] = useState(false)
  const [apiError, setApiError] = useState<string | null>(null)

  useEffect(() => {
    if (open) {
      setForce(false)
      setApiError(null)
    }
  }, [open])

  return (
    <ConfirmDialog
      open={open}
      onOpenChange={onOpenChange}
      title={`Delete repository ${name}?`}
      description={
        imageCount
          ? `The repository holds ${imageCount === 1 ? "1 image" : `${imageCount} images`}. A repository with images can only be deleted together with them.`
          : "Deleting a repository cannot be undone."
      }
      confirmText={name}
      actionLabel="Delete repository"
      onConfirm={async () => {
        setApiError(null)
        try {
          await api.del(repoApiPath(name), force ? { force: "true" } : undefined)
        } catch (e) {
          if (e instanceof ApiError && e.code === "RepositoryNotEmptyException") {
            setApiError("The repository still has images. Select “Force delete” to delete the images together with the repository.")
          } else {
            setApiError(errorMessage(e))
          }
          throw e
        }
        toast.success(`Repository ${name} deleted`)
        await revalidate(ECR_PATH)
        onDeleted?.()
      }}
    >
      <div className="flex items-start gap-3 rounded-md border p-3">
        <Checkbox id="repo-force" checked={force} onCheckedChange={(v) => setForce(v === true)} className="mt-0.5" />
        <div>
          <Label htmlFor="repo-force" className="font-medium">
            Force delete (also delete images)
          </Label>
          <p className="text-muted-foreground mt-0.5 text-xs">Every image and tag in the repository is permanently deleted from the registry.</p>
        </div>
      </div>
      {apiError && (
        <div className="border-destructive/30 bg-destructive/5 text-destructive flex items-start gap-2 rounded-md border p-3 text-sm">
          <AlertTriangle className="mt-0.5 size-4 shrink-0" />
          <span>{apiError}</span>
        </div>
      )}
    </ConfirmDialog>
  )
}

/** PushCommandsDialog lists the repository's push commands as numbered, copyable steps. */
export function PushCommandsDialog({
  repo,
  commands,
  open,
  onOpenChange,
}: {
  repo: EcrRepository
  commands: string[]
  open: boolean
  onOpenChange: (o: boolean) => void
}) {
  const titles = ["Build your image (skip if it already exists locally)", "Tag it with the repository URI", "Push it to the registry"]
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-h-[90vh] overflow-y-auto sm:max-w-2xl">
        <DialogHeader>
          <DialogTitle>Push commands for {repo.name}</DialogTitle>
          <DialogDescription>Run these from the directory that holds your Dockerfile, on the machine running HomeCloud.</DialogDescription>
        </DialogHeader>
        <CommandSteps steps={commands.map((c, i) => ({ title: titles[i] ?? `Step ${i + 1}`, command: c }))} />
        <p className="text-muted-foreground flex gap-1.5 text-xs">
          <Info className="mt-px size-3.5 shrink-0" />
          <span>
            Replace <span className="font-mono">latest</span> with any tag. No <span className="font-mono">docker login</span> is required for this local
            registry.
          </span>
        </p>
        <DialogFooter>
          <Button variant="outline" onClick={() => onOpenChange(false)}>
            Close
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
