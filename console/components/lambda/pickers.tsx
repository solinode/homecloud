"use client"

import { useEffect, useMemo, useState } from "react"
import Link from "next/link"
import { ArrowDown, ArrowUp, ExternalLink, Plus, X } from "lucide-react"

import { Button } from "@/components/ui/button"
import { Checkbox } from "@/components/ui/checkbox"
import { Input } from "@/components/ui/input"
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select"
import { Tag } from "@/components/console/tag"
import { seg } from "@/lib/api"
import { useApi } from "@/lib/hooks"
import type { LambdaFunction, LayerVersion, Queue, Topic } from "@/lib/types"

import { FUNCTIONS_PATH, LAYERS_PATH, arnParts, layerHref, splitLayerArn } from "./common"

export const MAX_LAYERS = 5

/**
 * LayersPicker edits a function's ordered list of layer version ARNs (up to 5).
 * Layers are merged into /opt in order; later layers overwrite earlier files.
 */
export function LayersPicker({
  value,
  onChange,
  runtime,
  architecture,
}: {
  value: string[]
  onChange: (v: string[]) => void
  runtime?: string
  architecture?: string
}) {
  const [compatibleOnly, setCompatibleOnly] = useState(true)
  const layers = useApi<LayerVersion[]>(LAYERS_PATH, {
    query: compatibleOnly ? { runtime: runtime || undefined, architecture: architecture || undefined } : undefined,
  })
  const [layer, setLayer] = useState("")
  const [version, setVersion] = useState("")
  const [arn, setArn] = useState("")
  const versions = useApi<LayerVersion[]>(layer ? `${LAYERS_PATH}/${seg(layer)}/versions` : null)

  useEffect(() => {
    setVersion(versions.data?.[0] ? String(versions.data[0].version) : "")
  }, [versions.data])

  const full = value.length >= MAX_LAYERS
  const add = (a: string) => {
    if (!a || value.includes(a) || full) return
    onChange([...value, a])
  }
  const chosen = versions.data?.find((v) => String(v.version) === version)
  const arnOk = /^arn:aws:lambda:[^:]*:[^:]*:layer:[A-Za-z0-9_-]+:\d+$/.test(arn.trim())

  const move = (i: number, d: number) => {
    const n = [...value]
    const j = i + d
    if (j < 0 || j >= n.length) return
    ;[n[i], n[j]] = [n[j], n[i]]
    onChange(n)
  }

  return (
    <div className="flex flex-col gap-3">
      {value.length > 0 ? (
        <ol className="bg-card divide-y rounded-md border">
          {value.map((a, i) => {
            const p = splitLayerArn(a)
            return (
              <li key={a} className="flex items-center gap-2 px-3 py-2 text-sm">
                <span className="text-faint w-5 shrink-0 font-mono text-xs tabular-nums">{i + 1}.</span>
                <span className="min-w-0 flex-1">
                  <span className="flex min-w-0 items-center gap-1.5">
                    <Link href={layerHref(p.name)} target="_blank" className="text-primary truncate font-medium underline-offset-2 hover:underline">
                      {p.name}
                    </Link>
                    <Tag>v{p.version}</Tag>
                  </span>
                  <span className="text-muted-foreground block truncate font-mono text-xs" title={a}>
                    {a}
                  </span>
                </span>
                <Button type="button" size="icon" variant="ghost" className="size-7" onClick={() => move(i, -1)} disabled={i === 0} aria-label="Move up">
                  <ArrowUp />
                </Button>
                <Button type="button" size="icon" variant="ghost" className="size-7" onClick={() => move(i, 1)} disabled={i === value.length - 1} aria-label="Move down">
                  <ArrowDown />
                </Button>
                <Button type="button" size="icon" variant="ghost" className="size-7" onClick={() => onChange(value.filter((x) => x !== a))} aria-label={`Remove ${p.name}`}>
                  <X />
                </Button>
              </li>
            )
          })}
        </ol>
      ) : (
        <p className="text-muted-foreground text-sm">No layers.</p>
      )}

      {!full && (
        <div className="flex flex-col gap-2 rounded-md border border-dashed p-3">
          <div className="grid grid-cols-1 gap-2 sm:grid-cols-[minmax(0,1fr)_8rem_auto] sm:items-center">
            <Select value={layer} onValueChange={setLayer} disabled={!layers.data}>
              <SelectTrigger className="w-full" aria-label="Layer">
                <SelectValue placeholder={layers.data ? (layers.data.length ? "Choose a layer" : "No layers") : "Loading layers..."} />
              </SelectTrigger>
              <SelectContent>
                {(layers.data ?? []).map((l) => (
                  <SelectItem key={l.name} value={l.name}>
                    {l.name}
                    {l.description && <span className="text-muted-foreground ml-2 text-xs">{l.description}</span>}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
            <Select value={version} onValueChange={setVersion} disabled={!versions.data?.length}>
              <SelectTrigger className="w-full" aria-label="Layer version">
                <SelectValue placeholder="Version" />
              </SelectTrigger>
              <SelectContent>
                {(versions.data ?? []).map((v) => (
                  <SelectItem key={v.version} value={String(v.version)}>
                    Version {v.version}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
            <Button type="button" size="sm" variant="outline" disabled={!chosen || value.includes(chosen.arn)} onClick={() => chosen && add(chosen.arn)}>
              <Plus /> Add
            </Button>
          </div>
          {(runtime || architecture) && (
            <label className="text-muted-foreground flex items-center gap-1.5 text-xs">
              <Checkbox checked={compatibleOnly} onCheckedChange={(v) => setCompatibleOnly(v === true)} className="size-3.5" />
              Only layers compatible with {[runtime, architecture].filter(Boolean).join(" / ")}
            </label>
          )}
          <div className="flex flex-col gap-2 sm:flex-row sm:items-center">
            <Input
              value={arn}
              onChange={(e) => setArn(e.target.value)}
              placeholder="or a layer version ARN: arn:aws:lambda:region:account:layer:name:1"
              className="h-8 font-mono text-xs"
              spellCheck={false}
              aria-label="Layer version ARN"
            />
            <Button
              type="button"
              size="sm"
              variant="outline"
              disabled={!arnOk}
              onClick={() => {
                add(arn.trim())
                setArn("")
              }}
            >
              <Plus /> Add ARN
            </Button>
          </div>
          <Link href="/lambda/layers/" target="_blank" className="text-primary inline-flex items-center gap-1 self-start text-xs hover:underline">
            Manage layers <ExternalLink className="size-3" />
          </Link>
        </div>
      )}
      {full && <p className="text-muted-foreground text-xs">A function can use at most {MAX_LAYERS} layers.</p>}
    </div>
  )
}

export type DestinationKind = "sqs" | "sns" | "lambda" | "events"

const KIND_LABEL: Record<DestinationKind, string> = { sqs: "SQS queue", sns: "SNS topic", lambda: "Lambda function", events: "EventBridge event bus" }

export const destinationKind = (arn: string): DestinationKind | "" => {
  const svc = arn.split(":")[2]
  return svc === "sqs" || svc === "sns" || svc === "lambda" || svc === "events" ? svc : ""
}

/** DestinationPicker selects the ARN of an SQS queue, SNS topic, Lambda function or EventBridge bus. */
export function DestinationPicker({
  value,
  onChange,
  kinds = ["sqs", "sns", "lambda", "events"],
  selfArn,
  id,
}: {
  value: string
  onChange: (arn: string) => void
  kinds?: DestinationKind[]
  /** the function being configured: excluded from Lambda targets; its region/account build the default bus ARN */
  selfArn: string
  id?: string
}) {
  const [kind, setKind] = useState<DestinationKind>(() => (destinationKind(value) as DestinationKind) || kinds[0])
  useEffect(() => {
    const k = destinationKind(value)
    if (k) setKind(k)
  }, [value])
  const queues = useApi<Queue[]>(kind === "sqs" ? "/api/v1/sqs/queues" : null)
  const topics = useApi<Topic[]>(kind === "sns" ? "/api/v1/sns/topics" : null)
  const fns = useApi<LambdaFunction[]>(kind === "lambda" ? FUNCTIONS_PATH : null)
  const { region, account } = arnParts(selfArn)
  const selfName = selfArn.split(":")[6]

  const options = useMemo(() => {
    switch (kind) {
      case "sqs":
        return queues.data?.map((q) => ({ arn: q.arn, label: q.name }))
      case "sns":
        return topics.data?.map((t) => ({ arn: t.arn, label: t.name }))
      case "lambda":
        return fns.data?.filter((f) => f.name !== selfName).map((f) => ({ arn: f.arn, label: f.name }))
      case "events":
        return [{ arn: `arn:aws:events:${region}:${account}:event-bus/default`, label: "default" }]
    }
  }, [kind, queues.data, topics.data, fns.data, region, account, selfName])

  const known = !value || !!options?.some((o) => o.arn === value)
  return (
    <div className="flex min-w-0 flex-col gap-2 sm:flex-row">
      <Select
        value={kind}
        onValueChange={(v) => {
          setKind(v as DestinationKind)
          onChange("")
        }}
      >
        <SelectTrigger className="w-full sm:w-52" aria-label="Destination type">
          <SelectValue />
        </SelectTrigger>
        <SelectContent>
          {kinds.map((k) => (
            <SelectItem key={k} value={k}>
              {KIND_LABEL[k]}
            </SelectItem>
          ))}
        </SelectContent>
      </Select>
      <Select value={value} onValueChange={onChange} disabled={!options}>
        <SelectTrigger id={id} className="w-full min-w-0 sm:flex-1" aria-label="Destination">
          <SelectValue placeholder={options ? (options.length ? `Choose ${KIND_LABEL[kind].toLowerCase()}` : `No ${KIND_LABEL[kind].toLowerCase()}s`) : "Loading..."} />
        </SelectTrigger>
        <SelectContent>
          {!known && <SelectItem value={value}>{value}</SelectItem>}
          {(options ?? []).map((o) => (
            <SelectItem key={o.arn} value={o.arn}>
              {o.label}
            </SelectItem>
          ))}
        </SelectContent>
      </Select>
    </div>
  )
}
