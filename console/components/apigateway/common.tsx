"use client"

import { useRouter } from "next/navigation"
import { toast } from "sonner"

import { ConfirmDialog } from "@/components/console/confirm-dialog"
import { api, seg } from "@/lib/api"
import { revalidate } from "@/lib/hooks"
import type { HttpApi } from "@/lib/types"
import { cn } from "@/lib/utils"

export const APIGW_PATH = "/api/v1/apigateway"
export const APIS_PATH = "/api/v1/apigateway/apis"
export const apiPath = (id: string) => `${APIS_PATH}/${seg(id)}`

export const apiHref = (id: string) => `/apigateway/api/?id=${encodeURIComponent(id)}`

export const ROUTE_METHODS = ["ANY", "GET", "POST", "PUT", "PATCH", "DELETE", "HEAD", "OPTIONS"] as const

const METHOD_TONE: Record<string, string> = {
  GET: "text-emerald-700 bg-emerald-50 ring-emerald-600/20 dark:text-emerald-300 dark:bg-emerald-500/10 dark:ring-emerald-400/20",
  POST: "text-blue-700 bg-blue-50 ring-blue-600/20 dark:text-blue-300 dark:bg-blue-500/10 dark:ring-blue-400/20",
  PUT: "text-amber-700 bg-amber-50 ring-amber-600/20 dark:text-amber-300 dark:bg-amber-500/10 dark:ring-amber-400/20",
  PATCH: "text-violet-700 bg-violet-50 ring-violet-600/20 dark:text-violet-300 dark:bg-violet-500/10 dark:ring-violet-400/20",
  DELETE: "text-red-700 bg-red-50 ring-red-600/20 dark:text-red-300 dark:bg-red-500/10 dark:ring-red-400/20",
}
const METHOD_NEUTRAL = "text-slate-600 bg-slate-100 ring-slate-500/20 dark:text-slate-300 dark:bg-slate-500/15 dark:ring-slate-400/20"

export function MethodBadge({ method, className }: { method: string; className?: string }) {
  return (
    <span
      className={cn(
        "inline-flex min-w-14 justify-center rounded px-1.5 py-0.5 font-mono text-[11px] font-semibold ring-1 ring-inset",
        METHOD_TONE[method] ?? METHOD_NEUTRAL,
        className,
      )}
    >
      {method}
    </span>
  )
}

/** validRoutePath checks an HTTP API route path ("/items/{id}", "/files/{proxy+}"). */
export function routePathError(path: string): string | null {
  const p = path.trim()
  if (!p.startsWith("/")) return "The path must start with /"
  if (/\s/.test(p)) return "The path cannot contain spaces"
  if (p.includes("?")) return "Leave out the query string; the function receives it separately"
  const segs = p.split("/").slice(1)
  for (let i = 0; i < segs.length; i++) {
    const s = segs[i]
    if (s.includes("{") || s.includes("}")) {
      if (!/^\{[A-Za-z0-9_.-]+\+?\}$/.test(s)) return `"${s}" must be a whole segment like {id} or {proxy+}`
      if (s.endsWith("+}") && i !== segs.length - 1) return "A greedy {name+} parameter must be the last segment"
    }
  }
  return null
}

/** DeleteApiDialog deletes an HTTP API after the user types its name. */
export function DeleteApiDialog({ api: target, onOpenChange, redirect }: { api: HttpApi | null; onOpenChange: (o: boolean) => void; redirect?: boolean }) {
  const router = useRouter()
  return (
    <ConfirmDialog
      open={!!target}
      onOpenChange={onOpenChange}
      title={`Delete API ${target?.name ?? ""}`}
      description="The API, its routes and its invoke URL are deleted. The target functions are not affected."
      confirmText={target?.name}
      onConfirm={async () => {
        if (!target) return
        await api.del(apiPath(target.id))
        toast.success(`Deleted API ${target.name}`)
        if (redirect) router.push("/apigateway/")
        await revalidate(APIGW_PATH)
      }}
    />
  )
}
