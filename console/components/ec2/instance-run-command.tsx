"use client"

import { useState } from "react"
import { History, Loader2, Play } from "lucide-react"
import { toast } from "sonner"

import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Textarea } from "@/components/ui/textarea"
import { CopyButton } from "@/components/console/copy-button"
import { EmptyState } from "@/components/console/empty-state"
import { Field } from "@/components/console/form-field"
import { Section } from "@/components/console/section"
import { StatusBadge } from "@/components/console/status-badge"
import { api, errorMessage, seg } from "@/lib/api"
import { formatTime } from "@/lib/format"
import type { CommandResult, Instance } from "@/lib/types"
import { cn } from "@/lib/utils"
import { INSTANCES_PATH } from "./instance-actions"

interface Run {
  key: number
  command: string
  timeout: number
  at: number
  result?: CommandResult
  error?: string
}

// Session history per instance survives tab switches (module scope).
const HISTORY = new Map<string, Run[]>()

export function InstanceRunCommand({ instance }: { instance: Instance }) {
  const [command, setCommand] = useState("")
  const [timeout, setTimeoutS] = useState("60")
  const [pending, setPending] = useState(false)
  const [runs, setRuns] = useState<Run[]>(() => HISTORY.get(instance.id) ?? [])
  const [current, setCurrent] = useState<number | null>(() => HISTORY.get(instance.id)?.[0]?.key ?? null)
  const [error, setError] = useState<string | null>(null)
  const running = instance.state === "running"

  const exec = async (cmd: string, t: number) => {
    if (!cmd.trim()) {
      setError("Enter a command")
      return
    }
    if (!Number.isInteger(t) || t < 1 || t > 600) {
      setError("Timeout must be 1-600 seconds")
      return
    }
    setError(null)
    setPending(true)
    const run: Run = { key: Date.now(), command: cmd, timeout: t, at: Date.now() }
    try {
      run.result = await api.post<CommandResult>(`${INSTANCES_PATH}/${seg(instance.id)}/commands`, { command: cmd, timeout_seconds: t })
    } catch (e) {
      run.error = errorMessage(e)
      toast.error(run.error)
    } finally {
      setPending(false)
    }
    const next = [run, ...runs].slice(0, 20)
    HISTORY.set(instance.id, next)
    setRuns(next)
    setCurrent(run.key)
  }

  const shown = runs.find((r) => r.key === current)

  return (
    <div className="grid grid-cols-1 gap-4 lg:grid-cols-[minmax(0,1fr)_280px]">
      <div className="flex min-w-0 flex-col gap-4">
        <Section title="Run command" description="Runs a shell command (/bin/sh -c) inside the instance and returns its output, like Systems Manager Run Command.">
          {!running ? (
            <p className="text-muted-foreground text-sm">
              The instance is <StatusBadge status={instance.state} />. Commands can only run on a running instance.
            </p>
          ) : (
            <form
              className="flex flex-col gap-3"
              onSubmit={(e) => {
                e.preventDefault()
                exec(command, Number(timeout))
              }}
            >
              <Field label="Command" htmlFor="rc-cmd" error={error} help="Press ⌘/Ctrl + Enter to run.">
                <Textarea
                  id="rc-cmd"
                  rows={5}
                  value={command}
                  onChange={(e) => setCommand(e.target.value)}
                  onKeyDown={(e) => {
                    if (e.key === "Enter" && (e.metaKey || e.ctrlKey)) {
                      e.preventDefault()
                      if (!pending) exec(command, Number(timeout))
                    }
                  }}
                  placeholder={"uname -a\ndf -h"}
                  className="font-mono text-[13px]"
                  spellCheck={false}
                  autoFocus
                />
              </Field>
              <div className="flex flex-wrap items-end gap-3">
                <Field label="Timeout (seconds)" htmlFor="rc-timeout">
                  <Input id="rc-timeout" type="number" min={1} max={600} value={timeout} onChange={(e) => setTimeoutS(e.target.value)} className="h-8 w-28" />
                </Field>
                <Button type="submit" disabled={pending}>
                  {pending ? <Loader2 className="animate-spin" /> : <Play />}
                  Run
                </Button>
              </div>
            </form>
          )}
        </Section>

        {shown && (
          <Section
            title={
              <span className="flex flex-wrap items-center gap-2">
                Output
                {shown.result ? (
                  <StatusBadge status={shown.result.status === "Success" ? "success" : "failed"} label={shown.result.status} />
                ) : (
                  <StatusBadge status="error" label="Error" />
                )}
              </span>
            }
            description={
              shown.result
                ? `Exit code ${shown.result.exit_code} · ${shown.result.duration_ms} ms · ${formatTime(shown.at)}`
                : `Could not run the command · ${formatTime(shown.at)}`
            }
            actions={
              running && (
                <Button variant="outline" size="sm" onClick={() => exec(shown.command, shown.timeout)} disabled={pending}>
                  <Play /> Run again
                </Button>
              )
            }
          >
            <div className="flex flex-col gap-3">
              <OutputBlock label="Command" text={shown.command} />
              {shown.error && <OutputBlock label="Error" text={shown.error} tone="err" />}
              {shown.result && (
                <>
                  <OutputBlock label="stdout" text={shown.result.stdout} />
                  {(shown.result.stderr || shown.result.status === "Failed") && <OutputBlock label="stderr" text={shown.result.stderr} tone="err" />}
                </>
              )}
            </div>
          </Section>
        )}
      </div>

      <Section title="History" description="This browser session" flush>
        {runs.length === 0 ? (
          <EmptyState icon={History} title="No commands yet" className="py-8" />
        ) : (
          <ul className="max-h-[560px] divide-y overflow-y-auto">
            {runs.map((r) => (
              <li key={r.key}>
                <button
                  type="button"
                  onClick={() => {
                    setCurrent(r.key)
                    setCommand(r.command)
                  }}
                  className={cn("hover:bg-muted/50 flex w-full flex-col gap-1 px-4 py-2 text-left", r.key === current && "bg-primary/5 dark:bg-primary/10")}
                >
                  <span className="truncate font-mono text-xs">{r.command.split("\n")[0]}</span>
                  <span className="text-muted-foreground flex items-center gap-2 text-xs">
                    {r.result ? (
                      <StatusBadge status={r.result.status === "Success" ? "success" : "failed"} label={`exit ${r.result.exit_code}`} />
                    ) : (
                      <StatusBadge status="error" label="error" />
                    )}
                    {formatTime(r.at)}
                  </span>
                </button>
              </li>
            ))}
          </ul>
        )}
      </Section>
    </div>
  )
}

function OutputBlock({ label, text, tone }: { label: string; text: string; tone?: "err" }) {
  return (
    <div className="flex flex-col gap-1">
      <div className="flex items-center justify-between">
        <span className="text-muted-foreground text-xs font-medium">{label}</span>
        {text && <CopyButton value={text} label={`Copy ${label}`} />}
      </div>
      <pre
        className={cn(
          "max-h-80 overflow-auto rounded-md border border-zinc-800 bg-zinc-950 p-3 font-mono text-[12.5px] leading-relaxed whitespace-pre-wrap",
          tone === "err" ? "text-red-300" : "text-zinc-100",
        )}
      >
        {text || <span className="text-zinc-500">(empty)</span>}
      </pre>
    </div>
  )
}
