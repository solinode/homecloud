"use client"

import { useRouter } from "next/navigation"
import { toast } from "sonner"

import { ConfirmDialog } from "@/components/console/confirm-dialog"
import { Tag, methodAccent } from "@/components/console/tag"
import { api, seg } from "@/lib/api"
import { revalidate } from "@/lib/hooks"
import type { HttpApi } from "@/lib/types"
import { cn } from "@/lib/utils"

export const APIGW_PATH = "/api/v1/apigateway"
export const APIS_PATH = "/api/v1/apigateway/apis"
export const apiPath = (id: string) => `${APIS_PATH}/${seg(id)}`

export const apiHref = (id: string) => `/apigateway/api/?id=${encodeURIComponent(id)}`

export const ROUTE_METHODS = ["ANY", "GET", "POST", "PUT", "PATCH", "DELETE", "HEAD", "OPTIONS"] as const

/** MethodBadge is the HTTP method chip of a route (GET, POST ...). */
export function MethodBadge({ method, className }: { method: string; className?: string }) {
  return (
    <Tag accent={methodAccent(method)} className={cn("min-w-14 justify-center font-semibold", className)}>
      {method}
    </Tag>
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
