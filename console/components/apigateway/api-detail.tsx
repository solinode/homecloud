"use client"

import { useEffect, useState } from "react"
import Link from "next/link"
import { AlertCircle, ArrowLeft, ExternalLink, Loader2, Pencil, Play, Plus, Route as RouteIcon, Trash2 } from "lucide-react"
import { toast } from "sonner"

import { Button } from "@/components/ui/button"
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog"
import { Input } from "@/components/ui/input"
import { Switch } from "@/components/ui/switch"
import { Textarea } from "@/components/ui/textarea"
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip"
import { ConfirmDialog } from "@/components/console/confirm-dialog"
import { CopyButton, CopyableText } from "@/components/console/copy-button"
import { cellLinkClass } from "@/components/console/data-table"
import { EmptyState } from "@/components/console/empty-state"
import { ErrorState } from "@/components/console/error-state"
import { Field } from "@/components/console/form-field"
import { KeyValueGrid } from "@/components/console/key-value"
import { DetailSkeleton } from "@/components/console/loading"
import { PageHeader } from "@/components/console/page-header"
import { Section } from "@/components/console/section"
import { TimeAgo } from "@/components/console/time-ago"
import { functionHref } from "@/components/lambda/common"
import { ApiError, api, errorMessage, seg } from "@/lib/api"
import { formatDate } from "@/lib/format"
import { revalidate, useApi, useQueryParam } from "@/lib/hooks"
import type { ApiRoute, HttpApi, LambdaFunction } from "@/lib/types"

import { APIGW_PATH, DeleteApiDialog, MethodBadge, apiPath } from "./common"
import { AddRouteDialog } from "./route-dialog"
import { TryIt, type TryPreset } from "./try-it"

export function ApiDetail() {
  const id = useQueryParam("id")
  const { data: a, error, isLoading, isValidating, mutate } = useApi<HttpApi>(id ? apiPath(id) : null, { refreshInterval: 15_000 })
  const functions = useApi<LambdaFunction[]>("/api/v1/lambda/functions", { refreshInterval: 30_000 })
  const [editing, setEditing] = useState(false)
  const [deleting, setDeleting] = useState(false)
  const [adding, setAdding] = useState(false)
  const [deletingRoute, setDeletingRoute] = useState<ApiRoute | null>(null)
  const [corsPending, setCorsPending] = useState(false)
  const [preset, setPreset] = useState<TryPreset | null>(null)

  const crumbs = [{ label: "API Gateway", href: "/apigateway/" }, { label: "APIs", href: "/apigateway/" }, { label: a?.name || id || "API" }]

  if (!id) {
    return (
      <>
        <PageHeader title="API" breadcrumbs={crumbs} />
        <EmptyState title="No API selected" description="Open an API from the APIs list." action={<BackButton />} />
      </>
    )
  }
  if (error && !a) {
    const notFound = error instanceof ApiError && error.status === 404
    return (
      <div className="flex flex-col gap-4">
        <PageHeader title={id} breadcrumbs={crumbs} />
        {notFound ? (
          <Section>
            <EmptyState icon={AlertCircle} title="API not found" description={`API ${id} does not exist. It may have been deleted.`} action={<BackButton />} />
          </Section>
        ) : (
          <ErrorState error={error} onRetry={() => mutate()} />
        )}
      </div>
    )
  }
  if (isLoading || !a) return <DetailSkeleton />

  const routes = [...(a.routes ?? [])].sort((x, y) => x.path.localeCompare(y.path) || x.method.localeCompare(y.method))
  const fnNames = new Set((functions.data ?? []).map((f) => f.name))

  const toggleCors = async (v: boolean) => {
    setCorsPending(true)
    try {
      await api.patch(apiPath(a.id), { cors: v })
      toast.success(v ? "CORS enabled" : "CORS disabled")
      await revalidate(APIGW_PATH)
    } catch (e) {
      toast.error(errorMessage(e))
    } finally {
      setCorsPending(false)
    }
  }

  return (
    <div className="flex flex-col gap-4">
      <PageHeader
        title={a.name}
        description={a.description || undefined}
        breadcrumbs={crumbs}
        actions={
          <>
            <Button variant="outline" size="sm" onClick={() => mutate()} disabled={isValidating} aria-label="Refresh">
              {isValidating ? <Loader2 className="animate-spin" /> : null}
              Refresh
            </Button>
            <Button variant="outline" size="sm" onClick={() => setEditing(true)}>
              <Pencil /> Edit
            </Button>
            <Button variant="outline" size="sm" className="text-destructive hover:text-destructive" onClick={() => setDeleting(true)}>
              <Trash2 /> Delete
            </Button>
          </>
        }
      />

      <Section title="API details">
        <KeyValueGrid
          columns={3}
          items={[
            { label: "API ID", value: <CopyableText value={a.id} /> },
            { label: "Protocol", value: "HTTP (payload format 2.0)" },
            { label: "Created", value: <span>{formatDate(a.created_at)} (<TimeAgo value={a.created_at} />)</span> },
            {
              label: "Invoke URL",
              wide: true,
              value: (
                <span className="inline-flex max-w-full items-center gap-1">
                  <a href={a.endpoint} target="_blank" rel="noreferrer" className="text-primary inline-flex min-w-0 items-center gap-1 font-mono text-[13px] hover:underline">
                    <span className="truncate">{a.endpoint}</span>
                    <ExternalLink className="size-3.5 shrink-0" />
                  </a>
                  <CopyButton value={a.endpoint} label="Copy invoke URL" />
                </span>
              ),
            },
            {
              label: "CORS",
              value: (
                <span className="inline-flex items-center gap-2">
                  <Switch checked={a.cors} onCheckedChange={toggleCors} disabled={corsPending} aria-label="CORS" />
                  <span>{a.cors ? "Enabled (Access-Control-Allow-Origin: *)" : "Disabled"}</span>
                  {corsPending && <Loader2 className="text-muted-foreground size-4 animate-spin" />}
                </span>
              ),
              wide: true,
            },
            { label: "Description", value: a.description, wide: true },
          ]}
        />
      </Section>

      <Section
        title={`Routes (${routes.length})`}
        description="The most specific route wins: literal segments beat {params}, which beat {proxy+}; a specific method beats ANY."
        actions={
          <Button size="sm" onClick={() => setAdding(true)}>
            <Plus /> Add route
          </Button>
        }
        flush
      >
        {routes.length === 0 ? (
          <EmptyState
            icon={RouteIcon}
            title="No routes"
            description="Requests to the invoke URL return 404 until you add a route."
            action={
              <Button size="sm" onClick={() => setAdding(true)}>
                <Plus /> Add route
              </Button>
            }
          />
        ) : (
          <div className="overflow-x-auto">
            <table className="w-full text-sm">
              <thead>
                <tr className="bg-muted/40 border-b">
                  <th className="text-muted-foreground px-4 py-2 text-left text-xs font-semibold">Method</th>
                  <th className="text-muted-foreground px-3 py-2 text-left text-xs font-semibold">Path</th>
                  <th className="text-muted-foreground px-3 py-2 text-left text-xs font-semibold">Integration</th>
                  <th className="text-muted-foreground hidden px-3 py-2 text-left text-xs font-semibold lg:table-cell">Invoke URL</th>
                  <th className="px-4 py-2" />
                </tr>
              </thead>
              <tbody>
                {routes.map((r) => (
                  <tr key={r.id} className="hover:bg-muted/40 border-b last:border-0">
                    <td className="px-4 py-2">
                      <MethodBadge method={r.method} />
                    </td>
                    <td className="px-3 py-2 font-mono text-[13px] break-all">{r.path}</td>
                    <td className="px-3 py-2">
                      <span className="inline-flex items-center gap-1.5">
                        <span className="text-muted-foreground text-xs">Lambda</span>
                        <Link href={functionHref(r.function_name)} className={cellLinkClass()}>
                          {r.function_name}
                        </Link>
                        {functions.data && !fnNames.has(r.function_name) && (
                          <Tooltip>
                            <TooltipTrigger asChild>
                              <AlertCircle className="text-destructive size-4" aria-label="Function missing" />
                            </TooltipTrigger>
                            <TooltipContent>The function no longer exists; requests to this route fail.</TooltipContent>
                          </Tooltip>
                        )}
                      </span>
                    </td>
                    <td className="hidden px-3 py-2 lg:table-cell">
                      <CopyableText value={`${a.endpoint}${r.path}`} className="max-w-md" />
                    </td>
                    <td className="px-4 py-2">
                      <div className="flex justify-end gap-1">
                        <Button variant="ghost" size="sm" onClick={() => setPreset({ method: r.method, path: r.path, nonce: Date.now() })}>
                          <Play /> Try
                        </Button>
                        <Button
                          variant="ghost"
                          size="icon"
                          className="text-destructive hover:text-destructive size-8"
                          onClick={() => setDeletingRoute(r)}
                          aria-label={`Delete route ${r.method} ${r.path}`}
                        >
                          <Trash2 />
                        </Button>
                      </div>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </Section>

      <TryIt api={a} preset={preset} />

      <EditApiDialog api={editing ? a : null} onClose={() => setEditing(false)} />
      <AddRouteDialog api={a} open={adding} onOpenChange={setAdding} />
      <DeleteApiDialog api={deleting ? a : null} onOpenChange={setDeleting} redirect />
      <ConfirmDialog
        open={!!deletingRoute}
        onOpenChange={(o) => !o && setDeletingRoute(null)}
        title="Delete route?"
        description={
          deletingRoute && (
            <>
              Requests to <span className="font-mono">{`${deletingRoute.method} ${deletingRoute.path}`}</span> will no longer invoke{" "}
              <span className="font-mono">{deletingRoute.function_name}</span>.
            </>
          )
        }
        onConfirm={async () => {
          if (!deletingRoute) return
          await api.del(`${apiPath(a.id)}/routes/${seg(deletingRoute.id)}`)
          toast.success(`Deleted route ${deletingRoute.method} ${deletingRoute.path}`)
          await revalidate(APIGW_PATH)
        }}
      />
    </div>
  )
}

function BackButton() {
  return (
    <Button variant="outline" size="sm" asChild>
      <Link href="/apigateway/">
        <ArrowLeft /> Back to APIs
      </Link>
    </Button>
  )
}

function EditApiDialog({ api: target, onClose }: { api: HttpApi | null; onClose: () => void }) {
  const [name, setName] = useState("")
  const [description, setDescription] = useState("")
  const [pending, setPending] = useState(false)
  useEffect(() => {
    if (target) {
      setName(target.name)
      setDescription(target.description)
    }
  }, [target])
  const nameErr = name.trim() ? null : "Enter a name"

  const submit = async (e: React.FormEvent) => {
    e.preventDefault()
    if (!target || nameErr) return
    setPending(true)
    try {
      await api.patch(apiPath(target.id), { name: name.trim(), description: description.trim() })
      toast.success(`Saved API ${name.trim()}`)
      await revalidate(APIGW_PATH)
      onClose()
    } catch (err) {
      toast.error(errorMessage(err))
    } finally {
      setPending(false)
    }
  }

  return (
    <Dialog open={!!target} onOpenChange={(o) => !o && !pending && onClose()}>
      <DialogContent className="sm:max-w-lg">
        <form onSubmit={submit} className="flex flex-col gap-4">
          <DialogHeader>
            <DialogTitle>Edit API</DialogTitle>
            <DialogDescription>The API ID and invoke URL do not change.</DialogDescription>
          </DialogHeader>
          <Field label="Name" htmlFor="edit-api-name" error={nameErr}>
            <Input id="edit-api-name" value={name} onChange={(e) => setName(e.target.value)} autoFocus />
          </Field>
          <Field label="Description" htmlFor="edit-api-desc" optional>
            <Textarea id="edit-api-desc" rows={3} value={description} onChange={(e) => setDescription(e.target.value)} />
          </Field>
          <DialogFooter>
            <Button type="button" variant="outline" onClick={onClose} disabled={pending}>
              Cancel
            </Button>
            <Button type="submit" disabled={pending || !!nameErr}>
              {pending && <Loader2 className="animate-spin" />}
              Save
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}
