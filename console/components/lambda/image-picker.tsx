"use client"

import { useState } from "react"
import Link from "next/link"
import { ExternalLink, Loader2, Rocket } from "lucide-react"
import { toast } from "sonner"

import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select"
import { CopyableText } from "@/components/console/copy-button"
import { Field } from "@/components/console/form-field"
import { KeyValueGrid } from "@/components/console/key-value"
import { Section } from "@/components/console/section"
import { api, errorMessage } from "@/lib/api"
import { revalidate, useApi } from "@/lib/hooks"
import type { EcrRepository, EcrRepositoryDetail, LambdaFunction } from "@/lib/types"

import { LAMBDA_PATH, fnPath } from "./common"

const REPOS = "/api/v1/ecr/repositories"

/** ImageUriPicker: a container image URI field with a browser for the images in ECR. */
export function ImageUriPicker({ id, value, onChange, invalid }: { id?: string; value: string; onChange: (v: string) => void; invalid?: boolean }) {
  const repos = useApi<EcrRepository[]>(REPOS, { revalidateOnFocus: false })
  const [repo, setRepo] = useState("")
  const detail = useApi<EcrRepositoryDetail>(repo ? `${REPOS}/${repo.split("/").map(encodeURIComponent).join("/")}` : null)
  const r = repos.data?.find((x) => x.name === repo)
  const options = (detail.data?.images ?? []).flatMap((img) =>
    img.tags?.length ? img.tags.map((t) => ({ uri: `${r?.uri ?? ""}:${t}`, label: t })) : [{ uri: img.uri, label: img.digest.slice(0, 19) }],
  )

  return (
    <div className="flex min-w-0 flex-col gap-2">
      <Input
        id={id}
        value={value}
        onChange={(e) => onChange(e.target.value)}
        placeholder="localhost:5500/my-repo:latest"
        className="max-w-2xl font-mono"
        spellCheck={false}
        aria-invalid={invalid}
      />
      {repos.data && repos.data.length > 0 && (
        <div className="flex max-w-2xl flex-col gap-2 sm:flex-row sm:items-center">
          <span className="text-muted-foreground shrink-0 text-xs">Browse ECR:</span>
          <Select value={repo} onValueChange={setRepo}>
            <SelectTrigger className="h-8 w-full min-w-0 sm:w-56" aria-label="ECR repository">
              <SelectValue placeholder="Repository" />
            </SelectTrigger>
            <SelectContent>
              {repos.data.map((x) => (
                <SelectItem key={x.name} value={x.name}>
                  {x.name}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
          <Select value={options.some((o) => o.uri === value) ? value : ""} onValueChange={onChange} disabled={!repo || !detail.data}>
            <SelectTrigger className="h-8 w-full min-w-0 sm:w-56" aria-label="Image">
              <SelectValue placeholder={repo && detail.data && !options.length ? "No images" : "Image tag"} />
            </SelectTrigger>
            <SelectContent>
              {options.map((o) => (
                <SelectItem key={o.uri} value={o.uri}>
                  {o.label}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </div>
      )}
      {repos.data && repos.data.length === 0 && (
        <p className="text-muted-foreground text-xs">
          Push an image to{" "}
          <Link href="/ecr/" target="_blank" className="text-primary inline-flex items-center gap-0.5 hover:underline">
            ECR <ExternalLink className="size-3" />
          </Link>{" "}
          or enter any registry URI the host can pull, e.g. public.ecr.aws/...
        </p>
      )}
    </div>
  )
}

/** ImageCodeTab replaces the code editor for container image functions. */
export function ImageCodeTab({ fn }: { fn: LambdaFunction }) {
  const [uri, setUri] = useState(fn.image_uri ?? "")
  const [entry, setEntry] = useState("")
  const [cmd, setCmd] = useState("")
  const [workdir, setWorkdir] = useState("")
  const [editingCfg, setEditingCfg] = useState(false)
  const [pending, setPending] = useState<string | null>(null)
  const ic = fn.image_config ?? {}

  const deploy = async (e: React.FormEvent) => {
    e.preventDefault()
    if (!uri.trim()) return
    setPending("image")
    try {
      await api.put(`${fnPath(fn.name)}/code`, { image_uri: uri.trim() })
      toast.success(`Deploying ${uri.trim()}`)
      await revalidate(LAMBDA_PATH)
    } catch (err) {
      toast.error(errorMessage(err))
    } finally {
      setPending(null)
    }
  }

  const split = (s: string) =>
    s.trim()
      ? s
          .split(",")
          .map((x) => x.trim())
          .filter(Boolean)
      : []
  const saveCfg = async (e: React.FormEvent) => {
    e.preventDefault()
    setPending("cfg")
    try {
      await api.patch(fnPath(fn.name), { image_config: { EntryPoint: split(entry), Command: split(cmd), WorkingDirectory: workdir.trim() } })
      toast.success("Saved the image configuration")
      await revalidate(LAMBDA_PATH)
      setEditingCfg(false)
    } catch (err) {
      toast.error(errorMessage(err))
    } finally {
      setPending(null)
    }
  }

  return (
    <div className="flex flex-col gap-4">
      <Section title="Container image" description="The function runs this image. Deploying pulls the image again, even when the tag is unchanged.">
        <form onSubmit={deploy} className="flex flex-col gap-4">
          <KeyValueGrid columns={2} items={[{ label: "Current image URI", value: <CopyableText value={fn.image_uri ?? ""} />, wide: true }]} />
          <Field label="Image URI" htmlFor="img-uri">
            <ImageUriPicker id="img-uri" value={uri} onChange={setUri} />
          </Field>
          <div className="flex justify-end border-t pt-4">
            <Button type="submit" disabled={!!pending || !uri.trim()}>
              {pending === "image" ? <Loader2 className="animate-spin" /> : <Rocket />} Deploy new image
            </Button>
          </div>
        </form>
      </Section>
      <Section
        title="Image configuration"
        description="Overrides for the image's ENTRYPOINT, CMD and WORKDIR."
        actions={
          !editingCfg && (
            <Button
              variant="outline"
              size="sm"
              onClick={() => {
                setEntry((ic.EntryPoint ?? []).join(", "))
                setCmd((ic.Command ?? []).join(", "))
                setWorkdir(ic.WorkingDirectory ?? "")
                setEditingCfg(true)
              }}
            >
              Edit
            </Button>
          )
        }
      >
        {editingCfg ? (
          <form onSubmit={saveCfg} className="flex flex-col gap-4">
            <Field label="Entrypoint" htmlFor="img-entry" optional help="Comma-separated, e.g. /lambda-entrypoint.sh">
              <Input id="img-entry" value={entry} onChange={(e) => setEntry(e.target.value)} className="font-mono" />
            </Field>
            <Field label="Command" htmlFor="img-cmd" optional help="Comma-separated, e.g. app.handler">
              <Input id="img-cmd" value={cmd} onChange={(e) => setCmd(e.target.value)} className="font-mono" />
            </Field>
            <Field label="Working directory" htmlFor="img-wd" optional>
              <Input id="img-wd" value={workdir} onChange={(e) => setWorkdir(e.target.value)} className="font-mono" />
            </Field>
            <div className="flex flex-wrap justify-end gap-2 border-t pt-4">
              <Button type="button" variant="outline" onClick={() => setEditingCfg(false)} disabled={!!pending}>
                Cancel
              </Button>
              <Button type="submit" disabled={!!pending}>
                {pending === "cfg" && <Loader2 className="animate-spin" />}
                Save
              </Button>
            </div>
          </form>
        ) : (
          <KeyValueGrid
            columns={3}
            items={[
              { label: "Entrypoint", value: <span className="font-mono text-[13px]">{(ic.EntryPoint ?? []).join(" ") || "Image default"}</span> },
              { label: "Command", value: <span className="font-mono text-[13px]">{(ic.Command ?? []).join(" ") || "Image default"}</span> },
              { label: "Working directory", value: <span className="font-mono text-[13px]">{ic.WorkingDirectory || "Image default"}</span> },
            ]}
          />
        )}
      </Section>
    </div>
  )
}
