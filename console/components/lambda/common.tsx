"use client"

import { useMemo } from "react"
import { useRouter } from "next/navigation"
import { toast } from "sonner"

import { Badge } from "@/components/ui/badge"
import { ConfirmDialog } from "@/components/console/confirm-dialog"
import { StatusBadge } from "@/components/console/status-badge"
import { api, seg } from "@/lib/api"
import { revalidate, useApi } from "@/lib/hooks"
import type { LambdaFunction, LambdaRuntime } from "@/lib/types"

export const LAMBDA_PATH = "/api/v1/lambda"
export const FUNCTIONS_PATH = "/api/v1/lambda/functions"
export const MAPPINGS_PATH = "/api/v1/lambda/event-source-mappings"

export const fnPath = (name: string) => `${FUNCTIONS_PATH}/${seg(name)}`

export const functionHref = (name: string, tab?: string) => `/lambda/function/?name=${encodeURIComponent(name)}${tab ? `&tab=${tab}` : ""}`
export const queueHref = (name: string) => `/sqs/queue/?name=${encodeURIComponent(name)}`

export const FUNCTION_NAME_RE = /^[a-zA-Z0-9_-]{1,64}$/
/** file.function: a module path and the exported function name. */
export const HANDLER_RE = /^[^\s]+\.[A-Za-z_$][\w$]*$/
export const ENV_KEY_RE = /^[a-zA-Z_][a-zA-Z0-9_]*$/

export const MEMORY_PRESETS = [128, 256, 512, 1024, 2048, 4096]

/** useRuntimes lists the supported runtimes (they never change while the server runs). */
export function useRuntimes() {
  return useApi<LambdaRuntime[]>(`${LAMBDA_PATH}/runtimes`, { revalidateOnFocus: false })
}

/** useRuntimeLabels maps runtime names to labels ("python3.12" -> "Python 3.12"). */
export function useRuntimeLabels() {
  const { data } = useRuntimes()
  return useMemo(() => {
    const m = new Map<string, string>()
    for (const r of data ?? []) m.set(r.name, r.label)
    return m
  }, [data])
}

export function RuntimeBadge({ runtime, label }: { runtime: string; label?: string }) {
  return (
    <Badge variant="outline" className="font-normal" title={runtime}>
      {label ?? runtime}
    </Badge>
  )
}

export function EnvironmentStateBadge({ state }: { state: string }) {
  return state === "Warm" ? (
    <StatusBadge status="warm" label="Warm" tone="success" />
  ) : (
    <StatusBadge status="idle" label={state || "Idle"} tone="neutral" />
  )
}

export function formatTimeout(s: number) {
  if (s < 60) return `${s} s`
  const m = Math.floor(s / 60)
  const r = s % 60
  return r ? `${m} min ${r} s` : `${m} min`
}

/**
 * handlerFiles lists the files the runtime bootstrap would load for a handler
 * ("lambda_function.lambda_handler" -> lambda_function.py, "index.handler" -> index.js/.mjs/.cjs).
 */
export function handlerFiles(runtime: string, handler: string): string[] {
  const dot = handler.lastIndexOf(".")
  if (dot <= 0) return []
  const mod = handler.slice(0, dot)
  if (runtime.startsWith("python")) return [`${mod.replace(/\./g, "/")}.py`]
  return [mod, `${mod}.js`, `${mod}.mjs`, `${mod}.cjs`]
}

/** fileToBase64 reads a File as base64 (without the data: URL prefix). */
export function fileToBase64(file: File): Promise<string> {
  return new Promise((resolve, reject) => {
    const r = new FileReader()
    r.onload = () => {
      const s = String(r.result ?? "")
      const i = s.indexOf(",")
      resolve(i >= 0 ? s.slice(i + 1) : s)
    }
    r.onerror = () => reject(r.error ?? new Error("Could not read the file"))
    r.readAsDataURL(file)
  })
}

export const MAX_ZIP_BYTES = 50 << 20

/** DeleteFunctionDialog deletes a function after the user types its name. */
export function DeleteFunctionDialog({
  fn,
  onOpenChange,
  redirect,
}: {
  fn: Pick<LambdaFunction, "name"> | null
  onOpenChange: (open: boolean) => void
  redirect?: boolean
}) {
  const router = useRouter()
  return (
    <ConfirmDialog
      open={!!fn}
      onOpenChange={onOpenChange}
      title={`Delete function ${fn?.name ?? ""}`}
      description={
        <>
          The function, its code package and its event source mappings are deleted permanently. API Gateway routes that target it will start returning
          errors. Its CloudWatch log group is kept.
        </>
      }
      confirmText={fn?.name}
      onConfirm={async () => {
        if (!fn) return
        await api.del(fnPath(fn.name))
        toast.success(`Deleted function ${fn.name}`)
        if (redirect) router.push("/lambda/")
        await revalidate(LAMBDA_PATH)
      }}
    />
  )
}
