"use client"

import { useState } from "react"
import { Eye, EyeOff, KeyRound } from "lucide-react"

import { Button } from "@/components/ui/button"
import { Section } from "@/components/console/section"
import { KeyValueGrid } from "@/components/console/key-value"
import { CopyButton, CopyableText } from "@/components/console/copy-button"
import { ErrorState } from "@/components/console/error-state"
import { Skeleton } from "@/components/ui/skeleton"
import { useApi } from "@/lib/hooks"
import type { S3Credentials } from "@/lib/types"
import { CodeBlock } from "@/components/console/code-block"

/** Bucket name rules, mirroring validBucket in cli/internal/svc/s3/s3.go. */
export function bucketNameChecks(name: string) {
  const charsOk = /^[a-z0-9.-]*$/.test(name)
  const edgesOk = name.length > 0 && /^[a-z0-9]/.test(name) && /[a-z0-9]$/.test(name)
  return [
    { label: "Between 3 and 63 characters", ok: name.length >= 3 && name.length <= 63 },
    { label: "Only lowercase letters, digits, dots (.) and hyphens (-)", ok: name.length > 0 && charsOk },
    { label: "Starts and ends with a letter or digit", ok: edgesOk },
  ]
}

export function validBucketName(name: string) {
  return bucketNameChecks(name).every((c) => c.ok)
}

export const bucketHref = (name: string, extra?: Record<string, string>) => {
  const p = new URLSearchParams({ name, ...extra })
  return `/s3/bucket/?${p.toString()}`
}

export function objectExt(key: string): string {
  const base = key.slice(key.lastIndexOf("/") + 1)
  const i = base.lastIndexOf(".")
  return i > 0 ? base.slice(i + 1).toLowerCase() : ""
}

/** CodeBlock is re-exported from the console design system (kms and secrets import it from here). */
export { CodeBlock } from "@/components/console/code-block"

/** SecretText shows a masked value with reveal and copy controls. */
export function SecretText({ value }: { value: string }) {
  const [shown, setShown] = useState(false)
  return (
    <span className="inline-flex max-w-full items-center gap-1">
      <span className="truncate font-mono text-[13px]">{shown ? value : "•".repeat(Math.min(24, value.length || 8))}</span>
      <button
        type="button"
        onClick={() => setShown(!shown)}
        aria-label={shown ? "Hide" : "Show"}
        className="text-muted-foreground hover:text-foreground hover:bg-accent inline-flex size-6 shrink-0 items-center justify-center rounded"
      >
        {shown ? <EyeOff className="size-3.5" /> : <Eye className="size-3.5" />}
      </button>
      <CopyButton value={value} label="Copy secret" />
    </span>
  )
}

/** S3AccessCard shows the S3-compatible endpoint credentials for SDKs and the AWS CLI. */
export function S3AccessCard({ bucket }: { bucket?: string }) {
  const { data, error, isLoading, mutate } = useApi<S3Credentials>("/api/v1/s3/credentials", { revalidateOnFocus: false })
  const [open, setOpen] = useState(!!bucket)

  const example = data
    ? [
        `export AWS_ACCESS_KEY_ID=${data.access_key_id}`,
        `export AWS_SECRET_ACCESS_KEY=${data.secret_access_key}`,
        `export AWS_DEFAULT_REGION=${data.region}`,
        "",
        `aws --endpoint-url ${data.endpoint} s3 ls${bucket ? ` s3://${bucket}/` : ""}`,
      ].join("\n")
    : ""

  return (
    <Section
      title={
        <span className="flex items-center gap-2">
          <KeyRound className="text-muted-foreground size-4" /> S3 API access
        </span>
      }
      description="The HomeCloud S3 endpoint speaks the S3 protocol: point any AWS SDK or the AWS CLI at it."
      actions={
        <Button variant="outline" size="sm" onClick={() => setOpen(!open)}>
          {open ? "Hide" : "Show"} details
        </Button>
      }
      className={open ? undefined : "[&>header]:border-b-0"}
      bodyClassName={open ? undefined : "hidden"}
    >
      {error ? (
        <ErrorState error={error} onRetry={() => mutate()} />
      ) : isLoading || !data ? (
        <Skeleton className="h-24" />
      ) : (
        <div className="flex flex-col gap-4">
          <KeyValueGrid
            columns={2}
            items={[
              { label: "Endpoint", value: <CopyableText value={data.endpoint} /> },
              { label: "Region", value: <CopyableText value={data.region} /> },
              { label: "Access key ID", value: <CopyableText value={data.access_key_id} /> },
              { label: "Secret access key", value: <SecretText value={data.secret_access_key} /> },
            ]}
          />
          <CodeBlock title="AWS CLI example" code={example} />
        </div>
      )}
    </Section>
  )
}
