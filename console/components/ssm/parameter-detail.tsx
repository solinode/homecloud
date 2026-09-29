"use client"

import { useEffect, useState } from "react"
import Link from "next/link"
import { useRouter } from "next/navigation"
import { AlertCircle, ArrowLeft, EyeOff, Info, KeyRound, Loader2, Pencil, RefreshCw, Tag, Trash2 } from "lucide-react"
import { toast } from "sonner"

import { Alert, AlertDescription } from "@/components/ui/alert"
import { Button } from "@/components/ui/button"
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { Switch } from "@/components/ui/switch"
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs"
import { ConfirmDialog } from "@/components/console/confirm-dialog"
import { CopyButton, CopyableText } from "@/components/console/copy-button"
import { EmptyState } from "@/components/console/empty-state"
import { ErrorState } from "@/components/console/error-state"
import { Field } from "@/components/console/form-field"
import { KeyValueGrid } from "@/components/console/key-value"
import { DetailSkeleton, TableSkeleton } from "@/components/console/loading"
import { PageHeader } from "@/components/console/page-header"
import { Section } from "@/components/console/section"
import { TagList } from "@/components/console/tags-editor"
import { TimeAgo } from "@/components/console/time-ago"
import { keyHref, keyIdFromArn, keyLabel, useKmsKeys } from "@/components/kms/shared"
import { api, errorMessage } from "@/lib/api"
import { formatDate } from "@/lib/format"
import { revalidate, useApi, useQueryParam, useSetQueryParam } from "@/lib/hooks"
import type { SsmParameter, SsmParameterValue, SsmParameterVersion } from "@/lib/types"
import { cn } from "@/lib/utils"

import { PARAMETER_PATH, SSM_PATH, TypeBadge, editParameterHref, listItems, useParameter } from "./shared"

const TABS = ["overview", "history"] as const
type Tab = (typeof TABS)[number]

const LABEL_RE = /^[a-zA-Z_.-][a-zA-Z0-9_.-]{0,99}$/

/** Name in the ARN-free form used by the IAM principal ARN ("arn:...:user/alice" -> "alice"). */
const principalName = (arn: string) => (arn ? arn.slice(arn.lastIndexOf("/") + 1) || arn : "")

function BackButton() {
  return (
    <Button variant="outline" size="sm" asChild>
      <Link href="/ssm/">
        <ArrowLeft /> Back to parameters
      </Link>
    </Button>
  )
}

function Mask() {
  return <span className="text-muted-foreground font-mono tracking-widest">••••••••</span>
}

function Labels({ labels }: { labels?: string[] | null }) {
  if (!labels?.length) return <span className="text-muted-foreground">-</span>
  return (
    <span className="flex flex-wrap gap-1">
      {labels.map((l) => (
        <span key={l} className="bg-muted inline-flex items-center gap-1 rounded-md border px-1.5 py-0.5 font-mono text-xs">
          <Tag className="text-muted-foreground size-3" />
          {l}
        </span>
      ))}
    </span>
  )
}

export function ParameterDetail() {
  const name = useQueryParam("name")
  const tabParam = useQueryParam("tab")
  const setParam = useSetQueryParam()
  const router = useRouter()
  const tab: Tab = (TABS as readonly string[]).includes(tabParam) ? (tabParam as Tab) : "overview"
  const { param, notFound, error, isLoading, isValidating, mutate } = useParameter(name)
  const [deleting, setDeleting] = useState(false)

  const crumbs = [{ label: "Systems Manager", href: "/ssm/" }, { label: "Parameter Store", href: "/ssm/" }, { label: name || "Parameter" }]

  if (!name) {
    return (
      <>
        <PageHeader title="Parameter" breadcrumbs={crumbs} />
        <EmptyState title="No parameter selected" description="Open a parameter from the Parameter Store list." action={<BackButton />} />
      </>
    )
  }
  if ((error && !param) || notFound) {
    return (
      <div className="flex flex-col gap-4">
        <PageHeader title={<span className="break-all">{name}</span>} breadcrumbs={crumbs} />
        {notFound ? (
          <Section>
            <EmptyState icon={AlertCircle} title="Parameter not found" description={`Parameter ${name} does not exist.`} action={<BackButton />} />
          </Section>
        ) : (
          <ErrorState error={error} onRetry={() => mutate()} />
        )}
      </div>
    )
  }
  if (isLoading || !param) return <DetailSkeleton />

  const refresh = () => {
    mutate()
    revalidate(PARAMETER_PATH)
  }

  return (
    <div className="flex flex-col gap-4">
      <PageHeader
        title={<span className="break-all">{param.name}</span>}
        badge={<TypeBadge type={param.type} />}
        description={param.description || undefined}
        breadcrumbs={crumbs}
        actions={
          <>
            <Button variant="outline" size="sm" onClick={refresh} aria-label="Refresh">
              <RefreshCw className={cn(isValidating && "animate-spin")} />
            </Button>
            <Button variant="outline" size="sm" asChild>
              <Link href={editParameterHref(param.name)}>
                <Pencil /> Edit
              </Link>
            </Button>
            <Button variant="outline" size="sm" onClick={() => setDeleting(true)} className="text-destructive hover:text-destructive">
              <Trash2 /> Delete
            </Button>
          </>
        }
      />

      <Tabs value={tab} onValueChange={(v) => setParam("tab", v === "overview" ? null : v)}>
        <TabsList>
          <TabsTrigger value="overview">Overview</TabsTrigger>
          <TabsTrigger value="history">
            History <span className="text-muted-foreground text-xs">({param.version})</span>
          </TabsTrigger>
        </TabsList>
        <TabsContent value="overview">
          <Overview p={param} />
        </TabsContent>
        <TabsContent value="history">
          <History p={param} onChanged={refresh} />
        </TabsContent>
      </Tabs>

      <ConfirmDialog
        open={deleting}
        onOpenChange={setDeleting}
        title="Delete parameter"
        confirmText={param.name}
        description={<p>The parameter and all {param.version > 1 ? `${param.version} versions` : "its history"} are deleted permanently. Applications reading it will fail.</p>}
        onConfirm={async () => {
          await api.del(PARAMETER_PATH, { name: param.name })
          toast.success(`Parameter ${param.name} deleted`)
          await revalidate(SSM_PATH)
          router.push("/ssm/")
        }}
      />
    </div>
  )
}

function KmsKeyLink({ arn }: { arn: string }) {
  const keys = useKmsKeys()
  if (!arn) return <span className="text-muted-foreground">-</span>
  const id = keyIdFromArn(arn)
  const k = keys.data?.find((x) => x.id === id)
  return (
    <span className="inline-flex items-center gap-1">
      <Link href={keyHref(id)} className="text-primary font-mono text-[13px] break-all hover:underline" title={arn}>
        {k ? keyLabel(k) : id}
      </Link>
      <CopyButton value={arn} label="Copy key ARN" />
    </span>
  )
}

function Overview({ p }: { p: SsmParameter }) {
  return (
    <div className="flex flex-col gap-4">
      <Section title="Parameter details">
        <KeyValueGrid
          columns={3}
          items={[
            { label: "Name", value: <CopyableText value={p.name} /> },
            { label: "Type", value: <TypeBadge type={p.type} /> },
            { label: "Version", value: <span className="tabular-nums">{p.version}</span> },
            { label: "Last modified", value: <span>{formatDate(p.last_modified)} (<TimeAgo value={p.last_modified} />)</span> },
            { label: "Last modified by", value: p.last_modified_by ? <span title={p.last_modified_by}>{principalName(p.last_modified_by)}</span> : "" },
            { label: "Tier / data type", value: `Standard · ${p.data_type || "text"}` },
            ...(p.type === "SecureString" ? [{ label: "KMS key", value: <KmsKeyLink arn={p.key_id} /> }] : []),
            { label: "Description", value: p.description, wide: p.type !== "SecureString" },
            { label: "ARN", value: <CopyableText value={p.arn} />, wide: true },
          ]}
        />
      </Section>
      <ValuePanel p={p} />
      <Section title="Tags">
        <TagList tags={p.tags} />
      </Section>
    </div>
  )
}

function ValuePanel({ p }: { p: SsmParameter }) {
  const secure = p.type === "SecureString"
  // Plain values are fetched right away; SecureStrings stay encrypted until revealed.
  const plain = useApi<SsmParameterValue>(secure ? null : PARAMETER_PATH, { query: { name: p.name }, refreshInterval: 15_000 })
  const [revealed, setRevealed] = useState<SsmParameterValue | null>(null)
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState<string | null>(null)

  // Hide a revealed value when a new version is stored.
  useEffect(() => setRevealed(null), [p.version])

  const reveal = async () => {
    setLoading(true)
    setError(null)
    try {
      setRevealed(await api.get<SsmParameterValue>(PARAMETER_PATH, { name: p.name, with_decryption: true }))
    } catch (e) {
      setError(errorMessage(e))
    } finally {
      setLoading(false)
    }
  }

  const v = secure ? revealed : plain.data
  return (
    <Section
      title="Value"
      actions={
        secure ? (
          revealed ? (
            <>
              <CopyButton value={revealed.value} size="sm" toastMessage="Value copied" />
              <Button variant="outline" size="sm" onClick={() => setRevealed(null)}>
                <EyeOff /> Hide
              </Button>
            </>
          ) : (
            <Button size="sm" onClick={reveal} disabled={loading}>
              {loading ? <Loader2 className="animate-spin" /> : <KeyRound />} Show decrypted value
            </Button>
          )
        ) : (
          v && <CopyButton value={v.value} size="sm" toastMessage="Value copied" />
        )
      }
    >
      {error && (
        <Alert variant="destructive" className="mb-3">
          <AlertCircle />
          <AlertDescription className="break-words">{error}</AlertDescription>
        </Alert>
      )}
      {!secure && plain.error && !plain.data ? (
        <ErrorState error={plain.error} onRetry={() => plain.mutate()} />
      ) : v ? (
        <div className="flex flex-col gap-3">
          {p.type === "StringList" && (
            <div className="flex flex-wrap gap-1.5">
              {listItems(v.value).map((it, i) => (
                <span key={i} className="bg-muted rounded-md border px-2 py-0.5 font-mono text-xs">
                  {it}
                </span>
              ))}
            </div>
          )}
          <pre className="bg-muted/40 max-h-96 overflow-auto rounded-md border p-3 font-mono text-[13px] break-all whitespace-pre-wrap">{v.value}</pre>
          {v.labels?.length ? (
            <div className="text-muted-foreground flex items-center gap-2 text-xs">
              Labels on this version: <Labels labels={v.labels} />
            </div>
          ) : null}
        </div>
      ) : secure ? (
        <div className="flex flex-col gap-2">
          <Mask />
          <p className="text-muted-foreground text-sm">
            This SecureString is encrypted with KMS. Choose <span className="text-foreground font-medium">Show decrypted value</span> to decrypt it; this requires
            kms:Decrypt on the key.
          </p>
        </div>
      ) : (
        <TableSkeleton rows={1} cols={1} />
      )}
    </Section>
  )
}

function History({ p, onChanged }: { p: SsmParameter; onChanged: () => void }) {
  const secure = p.type === "SecureString"
  const [decrypt, setDecrypt] = useState(false)
  const { data, error, isLoading, isValidating, mutate } = useApi<SsmParameterVersion[]>(`${PARAMETER_PATH}/history`, {
    query: { name: p.name, with_decryption: secure && decrypt ? true : undefined },
    refreshInterval: 15_000,
  })
  const [labeling, setLabeling] = useState<SsmParameterVersion | null>(null)
  const versions = [...(data ?? [])].reverse()

  return (
    <Section
      title="Parameter history"
      description="Up to 100 versions are kept. Labels point at one version each; attaching an existing label to another version moves it there."
      flush
      actions={
        <>
          {secure && (
            <div className="flex items-center gap-2">
              <Switch id="ssm-history-decrypt" checked={decrypt} onCheckedChange={setDecrypt} />
              <Label htmlFor="ssm-history-decrypt" className="text-sm font-normal whitespace-nowrap">
                Show decrypted values
              </Label>
            </div>
          )}
          <Button variant="outline" size="icon" className="size-8" onClick={() => mutate()} aria-label="Refresh history">
            <RefreshCw className={cn(isValidating && "animate-spin")} />
          </Button>
        </>
      }
    >
      {error && !data ? (
        <div className="p-4">
          <ErrorState error={error} onRetry={() => mutate()} />
        </div>
      ) : isLoading && !data ? (
        <TableSkeleton rows={3} cols={4} />
      ) : (
        <div className="overflow-x-auto">
          {error && (
            <div className="px-4 pt-3">
              <ErrorState error={error} onRetry={() => mutate()} />
            </div>
          )}
          <table className="w-full text-sm">
            <thead>
              <tr className="bg-muted/40 text-muted-foreground border-b text-left text-xs">
                <th className="px-4 py-2 font-semibold">Version</th>
                <th className="px-3 py-2 font-semibold">Value</th>
                <th className="hidden px-3 py-2 font-semibold md:table-cell">Type</th>
                <th className="hidden px-3 py-2 font-semibold sm:table-cell">Last modified</th>
                <th className="hidden px-3 py-2 font-semibold lg:table-cell">Modified by</th>
                <th className="px-3 py-2 font-semibold">Labels</th>
                <th className="px-4 py-2" />
              </tr>
            </thead>
            <tbody>
              {versions.map((v) => {
                const masked = secure && !decrypt
                return (
                  <tr key={v.version} className="border-b last:border-0">
                    <td className="px-4 py-2 align-top tabular-nums">
                      {v.version}
                      {v.version === p.version && <span className="text-muted-foreground ml-1.5 text-xs">(current)</span>}
                    </td>
                    <td className="max-w-[16rem] px-3 py-2 align-top sm:max-w-md">
                      {masked ? (
                        <Mask />
                      ) : (
                        <span className="flex items-start gap-1">
                          <span className="line-clamp-3 min-w-0 font-mono text-[13px] break-all whitespace-pre-wrap" title={v.value}>
                            {v.value}
                          </span>
                          <CopyButton value={v.value} label={`Copy version ${v.version}`} />
                        </span>
                      )}
                    </td>
                    <td className="hidden px-3 py-2 align-top md:table-cell">
                      <TypeBadge type={p.type} />
                    </td>
                    <td className="hidden px-3 py-2 align-top whitespace-nowrap sm:table-cell">
                      <TimeAgo value={v.last_modified} />
                    </td>
                    <td className="hidden px-3 py-2 align-top lg:table-cell" title={v.modified_by}>
                      {principalName(v.modified_by) || "-"}
                    </td>
                    <td className="px-3 py-2 align-top">
                      <Labels labels={v.labels} />
                    </td>
                    <td className="px-4 py-2 text-right align-top">
                      <Button variant="outline" size="sm" onClick={() => setLabeling(v)}>
                        <Tag /> <span className="hidden sm:inline">Attach labels</span>
                      </Button>
                    </td>
                  </tr>
                )
              })}
            </tbody>
          </table>
          {secure && (
            <p className="text-muted-foreground flex items-center gap-1.5 border-t px-4 py-2 text-xs">
              <Info className="size-3.5 shrink-0" /> Type and KMS key are tracked per parameter, not per version; the table shows the current type.
            </p>
          )}
        </div>
      )}

      <LabelDialog
        name={p.name}
        version={labeling}
        onClose={() => setLabeling(null)}
        onSaved={() => {
          mutate()
          onChanged()
        }}
      />
    </Section>
  )
}

function LabelDialog({ name, version, onClose, onSaved }: { name: string; version: SsmParameterVersion | null; onClose: () => void; onSaved: () => void }) {
  const [text, setText] = useState("")
  const [pending, setPending] = useState(false)
  const [touched, setTouched] = useState(false)

  useEffect(() => {
    if (version) {
      setText("")
      setTouched(false)
    }
  }, [version])

  const labels = Array.from(new Set(text.split(",").map((s) => s.trim()).filter(Boolean)))
  const bad = labels.filter((l) => !LABEL_RE.test(l))
  const err = !labels.length
    ? "Enter at least one label."
    : bad.length
      ? `Invalid: ${bad.join(", ")}. Labels use letters, digits, . _ - (up to 100) and can't start with a digit.`
      : null

  const submit = async (e: React.FormEvent) => {
    e.preventDefault()
    setTouched(true)
    if (err || !version) return
    setPending(true)
    try {
      await api.post(`${PARAMETER_PATH}/labels`, { name, version: version.version, labels })
      toast.success(`Labels attached to version ${version.version}`)
      await revalidate(SSM_PATH)
      onSaved()
      onClose()
    } catch (e) {
      toast.error(errorMessage(e))
    } finally {
      setPending(false)
    }
  }

  return (
    <Dialog open={!!version} onOpenChange={(o) => !o && !pending && onClose()}>
      <DialogContent className="sm:max-w-md">
        <form onSubmit={submit} className="flex flex-col gap-4">
          <DialogHeader>
            <DialogTitle>Attach labels to version {version?.version}</DialogTitle>
            <DialogDescription>
              Read a labelled version with <span className="font-mono">{`${name}:<label>`}</span>. A label already on another version moves to this one.
            </DialogDescription>
          </DialogHeader>
          {version?.labels?.length ? (
            <div className="text-muted-foreground flex flex-wrap items-center gap-2 text-xs">
              Current labels: <Labels labels={version.labels} />
            </div>
          ) : null}
          <Field label="Labels" htmlFor="ssm-labels" error={touched || text ? err : undefined} help="Comma-separated, e.g. prod, stable">
            <Input id="ssm-labels" autoFocus autoComplete="off" spellCheck={false} value={text} onChange={(e) => setText(e.target.value)} placeholder="prod" className="font-mono text-[13px]" />
          </Field>
          <p className="text-muted-foreground text-xs">The API has no detach operation: to remove a label from this version, attach it to a different version.</p>
          <DialogFooter>
            <Button type="button" variant="outline" onClick={onClose} disabled={pending}>
              Cancel
            </Button>
            <Button type="submit" disabled={pending || (touched && !!err)}>
              {pending && <Loader2 className="animate-spin" />}
              Attach labels
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}
