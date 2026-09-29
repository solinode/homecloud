"use client"

import { useEffect, useState } from "react"
import Link from "next/link"
import { useRouter } from "next/navigation"
import { Loader2, Network, Plus } from "lucide-react"
import { toast } from "sonner"

import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Checkbox } from "@/components/ui/checkbox"
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { Switch } from "@/components/ui/switch"
import { ActionsMenu, type ActionItem } from "@/components/console/actions-menu"
import { CopyableText } from "@/components/console/copy-button"
import { DataTable, cellLinkClass, type Column } from "@/components/console/data-table"
import { EmptyState } from "@/components/console/empty-state"
import { Field } from "@/components/console/form-field"
import { PageHeader } from "@/components/console/page-header"
import { TimeAgo } from "@/components/console/time-ago"
import { api, errorMessage } from "@/lib/api"
import { revalidate, useApi } from "@/lib/hooks"
import type { HttpApi } from "@/lib/types"

import { APIGW_PATH, APIS_PATH, DeleteApiDialog, apiHref, routePathError } from "./common"
import { RouteFields, type RouteDraft } from "./route-dialog"

export function CorsBadge({ cors }: { cors: boolean }) {
  return cors ? (
    <Badge variant="secondary" className="font-normal">
      Enabled
    </Badge>
  ) : (
    <span className="text-muted-foreground text-sm">Off</span>
  )
}

export function ApiList() {
  const router = useRouter()
  const { data, error, isLoading, isValidating, mutate } = useApi<HttpApi[]>(APIS_PATH, { refreshInterval: 15_000 })
  const [selected, setSelected] = useState<string[]>([])
  const [creating, setCreating] = useState(false)
  const [deleting, setDeleting] = useState<HttpApi | null>(null)
  const sel = data?.find((a) => a.id === selected[0]) ?? null

  const columns: Column<HttpApi>[] = [
    {
      id: "name",
      header: "Name",
      value: (a) => a.name,
      cell: (a) => (
        <Link href={apiHref(a.id)} onClick={(e) => e.stopPropagation()} className={cellLinkClass()}>
          {a.name}
        </Link>
      ),
    },
    { id: "id", header: "API ID", value: (a) => a.id, cell: (a) => <span className="font-mono text-[13px]">{a.id}</span>, hideBelow: "sm" },
    {
      id: "endpoint",
      header: "Invoke URL",
      value: (a) => a.endpoint,
      cell: (a) => (
        <span onClick={(e) => e.stopPropagation()}>
          <CopyableText value={a.endpoint} className="max-w-72" />
        </span>
      ),
      hideBelow: "md",
    },
    { id: "routes", header: "Routes", value: (a) => a.routes?.length ?? 0, cell: (a) => a.routes?.length ?? 0 },
    {
      id: "authorizer",
      header: "Authorizer",
      value: (a) => a.authorizer?.user_pool_id ?? "",
      cell: (a) =>
        a.authorizer ? (
          <span className="whitespace-nowrap">
            Cognito <span className="text-muted-foreground font-mono text-xs">{a.authorizer.user_pool_id}</span>
          </span>
        ) : (
          <span className="text-muted-foreground">None</span>
        ),
      hideBelow: "lg",
    },
    { id: "cors", header: "CORS", value: (a) => (a.cors ? "enabled" : "off"), cell: (a) => <CorsBadge cors={a.cors} />, hideBelow: "lg" },
    { id: "created", header: "Created", value: (a) => a.created_at, cell: (a) => <TimeAgo value={a.created_at} />, hideBelow: "sm" },
  ]

  const items: ActionItem[] = [
    { label: "View details", onSelect: () => sel && router.push(apiHref(sel.id)), disabled: !sel },
    { separator: true },
    { label: "Delete", destructive: true, onSelect: () => setDeleting(sel), disabled: !sel },
  ]

  const createButton = (
    <Button size="sm" onClick={() => setCreating(true)}>
      <Plus /> Create API
    </Button>
  )

  return (
    <div className="flex flex-col gap-4">
      <PageHeader
        title="APIs"
        description="HTTP APIs route requests to Lambda functions. Each API gets a public invoke URL on this host."
        breadcrumbs={[{ label: "API Gateway", href: "/apigateway/" }, { label: "APIs" }]}
      />
      <DataTable
        title="APIs"
        data={data}
        columns={columns}
        rowId={(a) => a.id}
        loading={isLoading}
        error={error}
        onRefresh={() => mutate()}
        refreshing={isValidating}
        selection="single"
        selected={selected}
        onSelectedChange={setSelected}
        searchPlaceholder="Filter by name, ID or URL"
        defaultSort={{ id: "name" }}
        actions={
          <>
            <ActionsMenu items={items} disabled={!sel} />
            {createButton}
          </>
        }
        empty={
          <EmptyState
            icon={Network}
            title="No APIs"
            description="Create an HTTP API and add routes like GET /items/{id} that invoke your functions with API Gateway v2 events."
            action={createButton}
          />
        }
      />
      <CreateApiDialog open={creating} onOpenChange={setCreating} />
      <DeleteApiDialog api={deleting} onOpenChange={(o) => !o && setDeleting(null)} />
    </div>
  )
}

function CreateApiDialog({ open, onOpenChange }: { open: boolean; onOpenChange: (o: boolean) => void }) {
  const router = useRouter()
  const [name, setName] = useState("")
  const [description, setDescription] = useState("")
  const [cors, setCors] = useState(true)
  const [withRoute, setWithRoute] = useState(true)
  const [route, setRoute] = useState<RouteDraft>({ method: "ANY", path: "/{proxy+}", function_name: "" })
  const [pending, setPending] = useState(false)
  const [touched, setTouched] = useState(false)

  useEffect(() => {
    if (open) {
      setName("")
      setDescription("")
      setCors(true)
      setWithRoute(true)
      setRoute({ method: "ANY", path: "/{proxy+}", function_name: "" })
      setTouched(false)
    }
  }, [open])

  const nameErr = name.trim() ? (name.length > 128 ? "At most 128 characters" : null) : "Enter a name"
  const routeErr = withRoute ? (routePathError(route.path) ?? (route.function_name ? null : "Choose a target function")) : null

  const submit = async (e: React.FormEvent) => {
    e.preventDefault()
    setTouched(true)
    if (nameErr || routeErr) return
    setPending(true)
    try {
      const out = await api.post<HttpApi>(APIS_PATH, {
        name: name.trim(),
        description: description.trim(),
        cors,
        routes: withRoute ? [{ method: route.method, path: route.path.trim(), function_name: route.function_name }] : [],
      })
      toast.success(`Created API ${out.name}`)
      await revalidate(APIGW_PATH)
      onOpenChange(false)
      router.push(apiHref(out.id))
    } catch (err) {
      toast.error(errorMessage(err))
    } finally {
      setPending(false)
    }
  }

  return (
    <Dialog open={open} onOpenChange={(o) => !pending && onOpenChange(o)}>
      <DialogContent className="max-h-[90vh] overflow-y-auto sm:max-w-xl">
        <form onSubmit={submit} className="flex flex-col gap-5">
          <DialogHeader>
            <DialogTitle>Create HTTP API</DialogTitle>
            <DialogDescription>An HTTP API forwards requests on its invoke URL to Lambda functions, matched by method and path.</DialogDescription>
          </DialogHeader>
          <Field label="API name" htmlFor="api-name" error={touched ? nameErr : undefined}>
            <Input id="api-name" autoFocus autoComplete="off" value={name} onChange={(e) => setName(e.target.value)} placeholder="my-api" />
          </Field>
          <Field label="Description" htmlFor="api-desc" optional>
            <Input id="api-desc" value={description} onChange={(e) => setDescription(e.target.value)} />
          </Field>
          <div className="flex items-start justify-between gap-4 rounded-md border p-3">
            <div>
              <Label htmlFor="api-cors" className="font-medium">
                CORS
              </Label>
              <p className="text-muted-foreground mt-0.5 text-xs">Add Access-Control-Allow-Origin: * to responses so browsers on other origins can call the API.</p>
            </div>
            <Switch id="api-cors" checked={cors} onCheckedChange={setCors} />
          </div>
          <div className="flex flex-col gap-3 rounded-md border p-3">
            <label className="flex items-center gap-2 text-sm font-medium">
              <Checkbox checked={withRoute} onCheckedChange={(v) => setWithRoute(v === true)} />
              Add a first route
            </label>
            {withRoute && <RouteFields value={route} onChange={setRoute} error={touched ? routeErr : null} idPrefix="create-route" />}
          </div>
          <DialogFooter>
            <Button type="button" variant="outline" onClick={() => onOpenChange(false)} disabled={pending}>
              Cancel
            </Button>
            <Button type="submit" disabled={pending}>
              {pending && <Loader2 className="animate-spin" />}
              Create API
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}
