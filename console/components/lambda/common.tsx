"use client"

import { useMemo } from "react"
import { useRouter } from "next/navigation"
import { toast } from "sonner"

import { ConfirmDialog } from "@/components/console/confirm-dialog"
import { StatusBadge } from "@/components/console/status-badge"
import { Tag, type TagAccent } from "@/components/console/tag"
import { api, seg } from "@/lib/api"
import { revalidate, useApi } from "@/lib/hooks"
import type { LambdaAccountSettings, LambdaFunction, LambdaRuntime } from "@/lib/types"

export const LAMBDA_PATH = "/api/v1/lambda"
export const FUNCTIONS_PATH = "/api/v1/lambda/functions"
export const MAPPINGS_PATH = "/api/v1/lambda/event-source-mappings"
export const LAYERS_PATH = "/api/v1/lambda/layers"

export const fnPath = (name: string) => `${FUNCTIONS_PATH}/${seg(name)}`

export const functionHref = (name: string, tab?: string) => `/lambda/function/?name=${encodeURIComponent(name)}${tab ? `&tab=${tab}` : ""}`
export const queueHref = (name: string) => `/sqs/queue/?name=${encodeURIComponent(name)}`

export const FUNCTION_NAME_RE = /^[a-zA-Z0-9_-]{1,64}$/
/** file.function: a module path and the exported function name. */
export const HANDLER_RE = /^[^\s]+\.[A-Za-z_$][\w$]*$/
export const ENV_KEY_RE = /^[a-zA-Z_][a-zA-Z0-9_]*$/

/** dottedRuntime: interpreted runtimes whose handler is file.function (Java/.NET/custom runtimes use other forms). */
export const dottedRuntime = (runtime: string) => /^(python|nodejs|ruby)/.test(runtime)

/** handlerError validates a handler string for a runtime, mirroring the backend. */
export function handlerError(runtime: string, handler: string): string | null {
  if (!handler || handler.length > 128) return "Enter a handler (at most 128 characters)"
  if (dottedRuntime(runtime)) {
    const i = handler.lastIndexOf(".")
    if (i <= 0 || i === handler.length - 1) return "Use the form file.function, e.g. index.handler"
  }
  return null
}

export function handlerHelp(runtime: string) {
  if (runtime.startsWith("java")) return "package.Class::method, e.g. example.Handler::handleRequest"
  if (runtime.startsWith("dotnet")) return "Assembly::Namespace.Class::Method"
  if (runtime.startsWith("provided")) return "Passed to your bootstrap as _HANDLER; the executable must be named bootstrap."
  return "file.function: the module (without extension) and the exported function that receives the event."
}

export const ARCHITECTURES = ["x86_64", "arm64"] as const

export const layerHref = (name: string) => `/lambda/layers/?name=${encodeURIComponent(name)}`

/** splitLayerArn: arn:aws:lambda:r:a:layer:name:3 -> { name, version } */
export function splitLayerArn(arn: string): { name: string; version: string } {
  const p = arn.split(":")
  if (p.length >= 8 && p[5] === "layer") return { name: p[6], version: p[7] }
  return { name: arn, version: "" }
}

/** arnParts extracts region and account from an ARN. */
export function arnParts(arn: string): { region: string; account: string } {
  const p = arn.split(":")
  return { region: p[3] ?? "", account: p[4] ?? "" }
}

export function useAccountSettings() {
  return useApi<LambdaAccountSettings>(`${LAMBDA_PATH}/account`)
}

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

/** runtimeAccent tints the runtime Tag by language family. */
export function runtimeAccent(runtime: string): TagAccent {
  const r = runtime.toLowerCase()
  if (r.startsWith("python")) return "info"
  if (r.startsWith("nodejs")) return "success"
  if (r.startsWith("ruby")) return "danger"
  if (r.startsWith("java")) return "warning"
  if (r.startsWith("dotnet")) return "violet"
  if (r.startsWith("provided")) return "brand"
  return "neutral"
}

export function RuntimeBadge({ runtime, label }: { runtime: string; label?: string }) {
  return (
    <Tag accent={runtimeAccent(runtime)} title={runtime}>
      {label ?? runtime}
    </Tag>
  )
}

export function EnvironmentStateBadge({ state }: { state: string }) {
  return state === "Warm" ? (
    <StatusBadge status="warm" label="Warm" tone="success" />
  ) : (
    <StatusBadge status="idle" label={state || "Idle"} tone="neutral" />
  )
}

/** FunctionStateBadge shows State (Pending/Active/Failed/Inactive) with the reason as a tooltip. */
export function FunctionStateBadge({ fn }: { fn: Pick<LambdaFunction, "state" | "state_reason"> }) {
  const st = fn.state || "Active"
  const tone = st === "Active" ? "success" : st === "Failed" ? "danger" : st === "Pending" ? "warning" : "neutral"
  return (
    <span title={fn.state_reason || undefined}>
      <StatusBadge status={st.toLowerCase()} label={st} tone={tone} />
    </span>
  )
}

export function UpdateStatusBadge({ status }: { status?: string }) {
  if (!status) return <span className="text-muted-foreground">-</span>
  const tone = status === "Successful" ? "success" : status === "Failed" ? "danger" : "warning"
  return <StatusBadge status={status.toLowerCase()} label={status} tone={tone} />
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
  if (runtime.startsWith("ruby")) return [`${mod}.rb`]
  if (!dottedRuntime(runtime)) return []
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
