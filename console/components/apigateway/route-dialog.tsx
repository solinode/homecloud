"use client"

import { useEffect, useState } from "react"
import Link from "next/link"
import { Loader2 } from "lucide-react"
import { toast } from "sonner"

import { Button } from "@/components/ui/button"
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog"
import { Input } from "@/components/ui/input"
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select"
import { Field } from "@/components/console/form-field"
import { api, errorMessage } from "@/lib/api"
import { revalidate, useApi } from "@/lib/hooks"
import type { HttpApi, LambdaFunction } from "@/lib/types"

import { APIGW_PATH, ROUTE_METHODS, apiPath, routePathError } from "./common"

export interface RouteDraft {
  method: string
  path: string
  function_name: string
  /** NONE (default) or JWT */
  authorization?: string
}

/** RouteFields edits method, path and target function of a route. */
export function RouteFields({
  value,
  onChange,
  error,
  idPrefix,
  authorizer,
}: {
  value: RouteDraft
  onChange: (v: RouteDraft) => void
  error?: string | null
  idPrefix: string
  /** Show the authorization choice; undefined hides it, null means the API has no authorizer yet. */
  authorizer?: HttpApi["authorizer"]
}) {
  const functions = useApi<LambdaFunction[]>("/api/v1/lambda/functions")
  const pathErr = value.path ? routePathError(value.path) : null
  return (
    <div className="flex flex-col gap-4">
      <div className="grid grid-cols-[7.5rem_minmax(0,1fr)] gap-2">
        <Field label="Method" htmlFor={`${idPrefix}-method`}>
          <Select value={value.method} onValueChange={(m) => onChange({ ...value, method: m })}>
            <SelectTrigger id={`${idPrefix}-method`} className="w-full font-mono">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {ROUTE_METHODS.map((m) => (
                <SelectItem key={m} value={m} className="font-mono">
                  {m}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </Field>
        <Field label="Path" htmlFor={`${idPrefix}-path`} error={pathErr}>
          <Input
            id={`${idPrefix}-path`}
            value={value.path}
            onChange={(e) => onChange({ ...value, path: e.target.value })}
            placeholder="/items/{id}"
            className="font-mono"
            spellCheck={false}
            autoComplete="off"
          />
        </Field>
      </div>
      <p className="text-muted-foreground -mt-2 text-xs">
        Use <span className="font-mono">{"{name}"}</span> for a path parameter and a final <span className="font-mono">{"{proxy+}"}</span> to match
        everything below a prefix. ANY matches every method; literal segments win over parameters.
      </p>
      <Field
        label="Target function"
        htmlFor={`${idPrefix}-fn`}
        help={
          functions.data && functions.data.length === 0 ? (
            <>
              No functions yet.{" "}
              <Link href="/lambda/create/" className="text-primary hover:underline">
                Create a function
              </Link>{" "}
              first.
            </>
          ) : undefined
        }
      >
        <Select value={value.function_name} onValueChange={(f) => onChange({ ...value, function_name: f })} disabled={!functions.data}>
          <SelectTrigger id={`${idPrefix}-fn`} className="w-full">
            <SelectValue placeholder={functions.error ? errorMessage(functions.error) : functions.data ? "Choose a function" : "Loading functions..."} />
          </SelectTrigger>
          <SelectContent>
            {(functions.data ?? []).map((f) => (
              <SelectItem key={f.name} value={f.name}>
                {f.name}
                <span className="text-muted-foreground font-mono text-xs">{f.runtime}</span>
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
      </Field>
      {authorizer !== undefined && (
        <Field
          label="Authorization"
          htmlFor={`${idPrefix}-auth`}
          help={
            (value.authorization ?? "NONE") === "JWT"
              ? authorizer
                ? `Requests need "Authorization: Bearer <token>" with an ID or access token from user pool ${authorizer.user_pool_id}; others get 401.`
                : "This API has no authorizer yet: every request to a JWT route gets 401 until you set a Cognito user pool as its authorizer."
              : "Anyone who can reach the invoke URL can call this route."
          }
        >
          <Select value={value.authorization ?? "NONE"} onValueChange={(a) => onChange({ ...value, authorization: a })}>
            <SelectTrigger id={`${idPrefix}-auth`} className="w-full">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="NONE">None (public)</SelectItem>
              <SelectItem value="JWT">JWT (Cognito user pool token)</SelectItem>
            </SelectContent>
          </Select>
        </Field>
      )}
      {error && !pathErr && <p className="text-destructive text-xs">{error}</p>}
    </div>
  )
}

export function AddRouteDialog({ api: target, open, onOpenChange }: { api: HttpApi; open: boolean; onOpenChange: (o: boolean) => void }) {
  const [route, setRoute] = useState<RouteDraft>({ method: "GET", path: "/", function_name: "", authorization: "NONE" })
  const [pending, setPending] = useState(false)
  const [touched, setTouched] = useState(false)

  useEffect(() => {
    if (open) {
      setRoute({ method: "GET", path: "/", function_name: "", authorization: target.authorizer ? "JWT" : "NONE" })
      setTouched(false)
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open])

  const err =
    routePathError(route.path) ??
    (route.function_name ? null : "Choose a target function") ??
    ((target.routes ?? []).some((r) => r.method === route.method && r.path === route.path.trim()) ? `Route ${route.method} ${route.path} already exists` : null)

  const submit = async (e: React.FormEvent) => {
    e.preventDefault()
    setTouched(true)
    if (err) return
    setPending(true)
    try {
      await api.post<HttpApi>(`${apiPath(target.id)}/routes`, {
        method: route.method,
        path: route.path.trim(),
        function_name: route.function_name,
        authorization: route.authorization ?? "NONE",
      })
      toast.success(`Added route ${route.method} ${route.path.trim()}`)
      await revalidate(APIGW_PATH)
      onOpenChange(false)
    } catch (e) {
      toast.error(errorMessage(e))
    } finally {
      setPending(false)
    }
  }

  return (
    <Dialog open={open} onOpenChange={(o) => !pending && onOpenChange(o)}>
      <DialogContent className="sm:max-w-lg">
        <form onSubmit={submit} className="flex flex-col gap-5">
          <DialogHeader>
            <DialogTitle>Add route</DialogTitle>
            <DialogDescription>Requests matching the method and path invoke the function with an API Gateway v2 event.</DialogDescription>
          </DialogHeader>
          <RouteFields value={route} onChange={setRoute} error={touched ? err : null} idPrefix="add-route" authorizer={target.authorizer ?? null} />
          <DialogFooter>
            <Button type="button" variant="outline" onClick={() => onOpenChange(false)} disabled={pending}>
              Cancel
            </Button>
            <Button type="submit" disabled={pending}>
              {pending && <Loader2 className="animate-spin" />}
              Add route
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}
