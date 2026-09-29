"use client"

import { useEffect, useMemo, useRef, useState } from "react"
import { Download, RefreshCw } from "lucide-react"

import { Button } from "@/components/ui/button"
import { Label } from "@/components/ui/label"
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select"
import { Switch } from "@/components/ui/switch"
import { ErrorState } from "@/components/console/error-state"
import { Section } from "@/components/console/section"
import { seg } from "@/lib/api"
import { formatTime } from "@/lib/format"
import { useApi } from "@/lib/hooks"
import type { DbInstance } from "@/lib/types"
import { cn } from "@/lib/utils"
import { DB_INSTANCES_PATH } from "./shared"

// Docker log lines start with an RFC 3339 timestamp.
const TS = /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(\.\d+)?Z /gm

/** DbLogs shows the engine's container log (the database's error/general log). */
export function DbLogs({ inst, noun }: { inst: DbInstance; noun: string }) {
  const [tail, setTail] = useState("500")
  const [auto, setAuto] = useState(false)
  const [timestamps, setTimestamps] = useState(false)
  const [fetchedAt, setFetchedAt] = useState<number | null>(null)
  const preRef = useRef<HTMLPreElement>(null)
  const stick = useRef(true)

  const { data, error, isLoading, isValidating, mutate } = useApi<{ output: string }>(`${DB_INSTANCES_PATH}/${seg(inst.id)}/logs`, {
    query: { tail },
    refreshInterval: auto ? 5000 : 0,
    revalidateOnFocus: false,
  })

  useEffect(() => {
    if (data) setFetchedAt(Date.now())
  }, [data])

  const text = useMemo(() => {
    const out = data?.output ?? ""
    return timestamps ? out : out.replace(TS, "")
  }, [data, timestamps])

  // Keep the view pinned to the bottom unless the user scrolled up.
  useEffect(() => {
    const el = preRef.current
    if (el && stick.current) el.scrollTop = el.scrollHeight
  }, [text])

  const download = () => {
    const blob = new Blob([data?.output ?? ""], { type: "text/plain" })
    const url = URL.createObjectURL(blob)
    const a = document.createElement("a")
    a.href = url
    a.download = `${inst.id}.log`
    a.click()
    setTimeout(() => URL.revokeObjectURL(url), 1000)
  }

  return (
    <Section
      title="Logs"
      description={`The ${inst.engine} server's output: startup messages, errors, slow or failed statements.`}
      actions={
        <>
          <div className="flex items-center gap-2">
            <Switch id="log-auto" checked={auto} onCheckedChange={setAuto} />
            <Label htmlFor="log-auto" className="text-sm font-normal">
              Auto-refresh
            </Label>
          </div>
          <div className="flex items-center gap-2">
            <Switch id="log-ts" checked={timestamps} onCheckedChange={setTimestamps} />
            <Label htmlFor="log-ts" className="text-sm font-normal">
              Timestamps
            </Label>
          </div>
          <Select value={tail} onValueChange={setTail}>
            <SelectTrigger size="sm" className="w-36" aria-label="Lines">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {["100", "500", "2000", "5000"].map((t) => (
                <SelectItem key={t} value={t}>
                  Last {t} lines
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
          <Button variant="outline" size="icon" className="size-8" onClick={() => mutate()} aria-label="Refresh">
            <RefreshCw className={cn(isValidating && "animate-spin")} />
          </Button>
          <Button variant="outline" size="sm" onClick={download} disabled={!data?.output}>
            <Download /> Download
          </Button>
        </>
      }
    >
      {error ? (
        <ErrorState error={error} onRetry={() => mutate()} />
      ) : (
        <div className="flex flex-col gap-2">
          <pre
            ref={preRef}
            onScroll={(e) => {
              const el = e.currentTarget
              stick.current = el.scrollHeight - el.scrollTop - el.clientHeight < 40
            }}
            className="h-[460px] overflow-auto rounded-md border border-zinc-800 bg-zinc-950 p-3 font-mono text-[12.5px] leading-relaxed whitespace-pre-wrap text-zinc-100"
          >
            {isLoading && !data ? (
              <span className="text-zinc-500">Loading logs...</span>
            ) : text ? (
              text
            ) : (
              <span className="text-zinc-500">{inst.container_id ? "No log output yet." : `The ${noun} has no container yet.`}</span>
            )}
          </pre>
          <p className="text-muted-foreground text-xs">
            {fetchedAt ? `Last updated ${formatTime(fetchedAt)}` : ""}
            {auto ? " · refreshing every 5 seconds" : ""}
          </p>
        </div>
      )}
    </Section>
  )
}
