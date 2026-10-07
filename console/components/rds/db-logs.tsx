"use client"

import { useEffect, useMemo, useRef, useState } from "react"
import { Download, RefreshCw } from "lucide-react"

import { Button } from "@/components/ui/button"
import { Label } from "@/components/ui/label"
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select"
import { Switch } from "@/components/ui/switch"
import { TerminalPane, term } from "@/components/console/code-block"
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

const ERR_LINE = /\b(ERROR|FATAL|PANIC|CRITICAL)\b/i
const WARN_LINE = /\b(WARNING|WARN)\b/i

/** lineClass tints error and warning lines of the engine log. */
function lineClass(l: string): string | undefined {
  if (ERR_LINE.test(l)) return term.error
  if (WARN_LINE.test(l)) return term.warning
  return undefined
}

/** DbLogs shows the engine's container log (the database's error/general log). */
export function DbLogs({ inst, noun }: { inst: DbInstance; noun: string }) {
  const [tail, setTail] = useState("500")
  const [auto, setAuto] = useState(false)
  const [timestamps, setTimestamps] = useState(false)
  const [fetchedAt, setFetchedAt] = useState<number | null>(null)
  const paneRef = useRef<HTMLDivElement>(null)
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

  const lines = useMemo(() => text.replace(/\n$/, "").split("\n"), [text])

  // Keep the view pinned to the bottom unless the user scrolled up.
  useEffect(() => {
    const el = paneRef.current
    if (!el) return
    const onScroll = () => {
      stick.current = el.scrollHeight - el.scrollTop - el.clientHeight < 40
    }
    el.addEventListener("scroll", onScroll)
    return () => el.removeEventListener("scroll", onScroll)
  }, [error])
  useEffect(() => {
    const el = paneRef.current
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
          <TerminalPane ref={paneRef} title={`${inst.id} · ${inst.engine}`} copyValue={text} height="460px" bodyClassName="break-all whitespace-pre-wrap">
            {isLoading && !data ? (
              <span className={term.muted}>Loading logs...</span>
            ) : text ? (
              lines.map((l, i) => (
                <div key={i} className={lineClass(l)}>
                  {l || " "}
                </div>
              ))
            ) : (
              <span className={term.muted}>{inst.container_id ? "No log output yet." : `The ${noun} has no container yet.`}</span>
            )}
          </TerminalPane>
          <p className="text-muted-foreground text-xs">
            {fetchedAt ? `Last updated ${formatTime(fetchedAt)}` : ""}
            {auto ? " · refreshing every 5 seconds" : ""}
          </p>
        </div>
      )}
    </Section>
  )
}
