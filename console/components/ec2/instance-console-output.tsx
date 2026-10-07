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
import type { ConsoleOutput, Instance } from "@/lib/types"
import { cn } from "@/lib/utils"
import { INSTANCES_PATH, isVMInstance } from "./instance-actions"

// Docker log lines start with an RFC 3339 timestamp.
const TS = /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(\.\d+)?Z /gm

export function InstanceConsoleOutput({ instance }: { instance: Instance }) {
  const [tail, setTail] = useState("500")
  const [auto, setAuto] = useState(true)
  const [timestamps, setTimestamps] = useState(false)
  const [fetchedAt, setFetchedAt] = useState<number | null>(null)
  const preRef = useRef<HTMLDivElement>(null)
  const stick = useRef(true)

  const { data, error, isLoading, isValidating, mutate } = useApi<ConsoleOutput>(`${INSTANCES_PATH}/${seg(instance.id)}/console-output`, {
    query: { tail },
    refreshInterval: auto ? 5000 : 0,
  })

  // Track whether the user scrolled away from the bottom (TerminalPane forwards its ref to the scrolling body).
  const hasError = !!error
  useEffect(() => {
    const el = preRef.current
    if (!el) return
    const onScroll = () => {
      stick.current = el.scrollHeight - el.scrollTop - el.clientHeight < 40
    }
    el.addEventListener("scroll", onScroll)
    return () => el.removeEventListener("scroll", onScroll)
  }, [hasError])

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
    a.download = `${instance.id}-console-output.txt`
    a.click()
    setTimeout(() => URL.revokeObjectURL(url), 1000)
  }

  return (
    <Section
      title={isVMInstance(instance) ? "System log" : "Console output"}
      description={isVMInstance(instance) ? "The VM serial console: boot messages, cloud-init and user data output." : "The instance's stdout and stderr (the container log), including user data output."}
      actions={
        <>
          <div className="flex items-center gap-2">
            <Switch id="co-auto" checked={auto} onCheckedChange={setAuto} />
            <Label htmlFor="co-auto" className="text-sm font-normal">
              Auto-refresh
            </Label>
          </div>
          <div className="flex items-center gap-2">
            <Switch id="co-ts" checked={timestamps} onCheckedChange={setTimestamps} />
            <Label htmlFor="co-ts" className="text-sm font-normal">
              Timestamps
            </Label>
          </div>
          <Select value={tail} onValueChange={setTail}>
            <SelectTrigger size="sm" className="w-36" aria-label="Lines">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {["100", "500", "2000"].map((t) => (
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
          <TerminalPane
            ref={preRef}
            title={`${instance.id} · ${isVMInstance(instance) ? "serial console" : "container log"}`}
            copyValue={text}
            height="460px"
          >
            {isLoading && !data ? (
              <span className={term.muted}>Loading console output...</span>
            ) : text ? (
              <div className="break-words whitespace-pre-wrap">{text}</div>
            ) : (
              <span className={term.muted}>
                {instance.state === "terminated" ? "The instance is terminated; its console output is gone." : "No output yet."}
              </span>
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
