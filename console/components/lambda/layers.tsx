"use client"

import { useEffect, useRef, useState } from "react"
import Link from "next/link"
import { FileArchive, Layers, Loader2, Plus, Upload, X } from "lucide-react"
import { toast } from "sonner"

import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Checkbox } from "@/components/ui/checkbox"
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog"
import { Input } from "@/components/ui/input"
import { ActionsMenu, type ActionItem } from "@/components/console/actions-menu"
import { ConfirmDialog } from "@/components/console/confirm-dialog"
import { CopyableText } from "@/components/console/copy-button"
import { DataTable, cellLinkClass, type Column } from "@/components/console/data-table"
import { EmptyState } from "@/components/console/empty-state"
import { Field } from "@/components/console/form-field"
import { PageHeader } from "@/components/console/page-header"
import { TimeAgo } from "@/components/console/time-ago"
import { api, errorMessage, seg } from "@/lib/api"
import { formatBytes } from "@/lib/format"
import { revalidate, useApi, useQueryParam } from "@/lib/hooks"
import type { LambdaFunction, LayerVersion } from "@/lib/types"

import { ARCHITECTURES, FUNCTIONS_PATH, LAYERS_PATH, MAX_ZIP_BYTES, fileToBase64, functionHref, layerHref, useRuntimes } from "./common"

const LAYER_NAME_RE = /^[a-zA-Z0-9_-]{1,140}$/

function Compat({ items }: { items?: string[] | null }) {
  if (!items?.length) return <span className="text-muted-foreground">Any</span>
  return (
    <span className="flex flex-wrap gap-1">
      {items.map((r) => (
        <Badge key={r} variant="outline" className="font-mono text-[11px] font-normal">
          {r}
        </Badge>
      ))}
    </span>
  )
}

/** LayersPage lists layers, or one layer's versions when ?name= is set. */
export function LayersPage() {
  const name = useQueryParam("name")
  return name ? <LayerVersions name={name} /> : <LayersList />
}

function LayersList() {
  const layers = useApi<LayerVersion[]>(LAYERS_PATH)
  const [publishing, setPublishing] = useState(false)

  const columns: Column<LayerVersion>[] = [
    {
      id: "name",
      header: "Name",
      value: (l) => l.name,
      cell: (l) => (
        <Link href={layerHref(l.name)} className={cellLinkClass()}>
          {l.name}
        </Link>
      ),
    },
    { id: "version", header: "Latest version", value: (l) => l.version, cell: (l) => l.version },
    { id: "description", header: "Description", value: (l) => l.description, cell: (l) => l.description || "-", hideBelow: "md" },
    { id: "runtimes", header: "Compatible runtimes", value: (l) => (l.compatible_runtimes ?? []).join(","), cell: (l) => <Compat items={l.compatible_runtimes} />, hideBelow: "lg" },
    {
      id: "arch",
      header: "Architectures",
      value: (l) => (l.compatible_architectures ?? []).join(","),
      cell: (l) => <Compat items={l.compatible_architectures} />,
      hideBelow: "lg",
    },
    { id: "created", header: "Created", value: (l) => l.created_at, cell: (l) => <TimeAgo value={l.created_at} />, hideBelow: "sm" },
  ]

  return (
    <div className="flex flex-col gap-4">
      <PageHeader
        title="Layers"
        description="A layer is a .zip archive of libraries or other dependencies, extracted to /opt in the execution environment of functions that use it."
        breadcrumbs={[{ label: "Lambda", href: "/lambda/" }, { label: "Layers" }]}
        actions={
          <Button size="sm" onClick={() => setPublishing(true)}>
            <Plus /> Create layer
          </Button>
        }
      />
      <DataTable
        title="Layers"
        data={layers.data}
        columns={columns}
        rowId={(l) => l.name}
        loading={layers.isLoading}
        error={layers.error}
        onRefresh={() => layers.mutate()}
        refreshing={layers.isValidating}
        searchPlaceholder="Filter layers"
        defaultSort={{ id: "name" }}
        empty={
          <EmptyState
            icon={Layers}
            title="No layers"
            description="Package shared libraries once and add them to many functions."
            action={
              <Button size="sm" onClick={() => setPublishing(true)}>
                <Plus /> Create layer
              </Button>
            }
          />
        }
      />
      <PublishLayerDialog open={publishing} onOpenChange={setPublishing} />
    </div>
  )
}

function LayerVersions({ name }: { name: string }) {
  const versions = useApi<LayerVersion[]>(`${LAYERS_PATH}/${seg(name)}/versions`)
  const functions = useApi<LambdaFunction[]>(FUNCTIONS_PATH)
  const [selected, setSelected] = useState<string[]>([])
  const [publishing, setPublishing] = useState(false)
  const [deleting, setDeleting] = useState<LayerVersion | null>(null)
  const sel = versions.data?.find((v) => String(v.version) === selected[0]) ?? null
  const usedBy = (arn: string) => (functions.data ?? []).filter((f) => f.layers?.includes(arn))

  const columns: Column<LayerVersion>[] = [
    { id: "version", header: "Version", value: (v) => v.version, cell: (v) => <span className="font-medium">{v.version}</span> },
    { id: "description", header: "Description", value: (v) => v.description, cell: (v) => v.description || "-", hideBelow: "sm" },
    { id: "runtimes", header: "Compatible runtimes", value: (v) => (v.compatible_runtimes ?? []).join(","), cell: (v) => <Compat items={v.compatible_runtimes} />, hideBelow: "md" },
    {
      id: "arch",
      header: "Architectures",
      value: (v) => (v.compatible_architectures ?? []).join(","),
      cell: (v) => <Compat items={v.compatible_architectures} />,
      hideBelow: "lg",
    },
    { id: "size", header: "Size", value: (v) => v.code_size, cell: (v) => formatBytes(v.code_size), hideBelow: "md" },
    {
      id: "used",
      header: "Used by ($LATEST)",
      value: (v) => usedBy(v.arn).length,
      cell: (v) => {
        const fns = usedBy(v.arn)
        return fns.length ? (
          <span className="flex flex-wrap gap-x-2">
            {fns.map((f) => (
              <Link key={f.name} href={functionHref(f.name, "configuration")} className={cellLinkClass()} onClick={(e) => e.stopPropagation()}>
                {f.name}
              </Link>
            ))}
          </span>
        ) : (
          <span className="text-muted-foreground">-</span>
        )
      },
      hideBelow: "md",
    },
    { id: "arn", header: "ARN", value: (v) => v.arn, cell: (v) => <CopyableText value={v.arn} display={`…:${v.name}:${v.version}`} />, hideBelow: "lg" },
    { id: "created", header: "Created", value: (v) => v.created_at, cell: (v) => <TimeAgo value={v.created_at} />, hideBelow: "sm" },
  ]

  const items: ActionItem[] = [{ label: "Delete version", destructive: true, onSelect: () => setDeleting(sel), disabled: !sel }]

  return (
    <div className="flex flex-col gap-4">
      <PageHeader
        title={name}
        description={versions.data?.[0]?.layer_arn}
        breadcrumbs={[{ label: "Lambda", href: "/lambda/" }, { label: "Layers", href: "/lambda/layers/" }, { label: name }]}
        actions={
          <Button size="sm" onClick={() => setPublishing(true)}>
            <Upload /> Create version
          </Button>
        }
      />
      <DataTable
        title="Versions"
        description="Versions are immutable. Deleting one keeps it working for functions that already use it."
        data={versions.data}
        columns={columns}
        rowId={(v) => String(v.version)}
        loading={versions.isLoading}
        error={versions.error}
        onRefresh={() => versions.mutate()}
        refreshing={versions.isValidating}
        selection="single"
        selected={selected}
        onSelectedChange={setSelected}
        noSearch
        defaultSort={{ id: "version", desc: true }}
        actions={<ActionsMenu items={items} disabled={!sel} />}
        empty={
          <EmptyState
            icon={Layers}
            title="No versions"
            description="This layer has no versions left."
            action={
              <Button size="sm" onClick={() => setPublishing(true)}>
                <Upload /> Create version
              </Button>
            }
          />
        }
      />
      <PublishLayerDialog open={publishing} onOpenChange={setPublishing} layer={name} base={versions.data?.[0]} />
      <ConfirmDialog
        open={!!deleting}
        onOpenChange={(o) => !o && setDeleting(null)}
        title={`Delete ${name} version ${deleting?.version ?? ""}?`}
        description="The version can no longer be added to functions. Functions that already use it keep working."
        onConfirm={async () => {
          if (!deleting) return
          await api.del(`${LAYERS_PATH}/${seg(name)}/versions/${deleting.version}`)
          toast.success(`Deleted ${name} version ${deleting.version}`)
          setSelected([])
          await revalidate(LAYERS_PATH)
        }}
      />
    </div>
  )
}

function PublishLayerDialog({ open, onOpenChange, layer, base }: { open: boolean; onOpenChange: (o: boolean) => void; layer?: string; base?: LayerVersion }) {
  const runtimes = useRuntimes()
  const [name, setName] = useState("")
  const [description, setDescription] = useState("")
  const [license, setLicense] = useState("")
  const [compat, setCompat] = useState<string[]>([])
  const [archs, setArchs] = useState<string[]>([])
  const [zip, setZip] = useState<File | null>(null)
  const [touched, setTouched] = useState(false)
  const [pending, setPending] = useState(false)
  const input = useRef<HTMLInputElement>(null)

  useEffect(() => {
    if (!open) return
    setName(layer ?? "")
    setDescription(base?.description ?? "")
    setLicense(base?.license_info ?? "")
    setCompat(base?.compatible_runtimes ?? [])
    setArchs(base?.compatible_architectures ?? [])
    setZip(null)
    setTouched(false)
  }, [open, layer, base])

  const errors: Record<string, string> = {}
  if (!LAYER_NAME_RE.test(name)) errors.name = "1-140 letters, digits, hyphens and underscores"
  if (!zip) errors.zip = "Choose a .zip file"
  else if (zip.size > MAX_ZIP_BYTES) errors.zip = `The archive is ${formatBytes(zip.size)}; the limit is 50 MB`
  const valid = Object.keys(errors).length === 0
  const toggle = (list: string[], v: string) => (list.includes(v) ? list.filter((x) => x !== v) : [...list, v])

  const submit = async (e: React.FormEvent) => {
    e.preventDefault()
    setTouched(true)
    if (!valid || !zip) return
    setPending(true)
    try {
      const lv = await api.post<LayerVersion>(LAYERS_PATH, {
        name,
        description,
        license_info: license,
        compatible_runtimes: compat,
        compatible_architectures: archs,
        zip_base64: await fileToBase64(zip),
      })
      toast.success(`Published ${lv.name} version ${lv.version}`)
      await revalidate(LAYERS_PATH)
      onOpenChange(false)
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
            <DialogTitle>{layer ? `Create version of ${layer}` : "Create layer"}</DialogTitle>
            <DialogDescription>
              Put files where the runtime looks for them, e.g. python/ for Python packages or nodejs/node_modules/ for Node.js; they end up under /opt.
            </DialogDescription>
          </DialogHeader>
          {!layer && (
            <Field label="Name" htmlFor="layer-name" error={touched || name ? errors.name : undefined}>
              <Input id="layer-name" value={name} onChange={(e) => setName(e.target.value)} className="font-mono" autoFocus spellCheck={false} placeholder="my-deps" />
            </Field>
          )}
          <Field label="Description" htmlFor="layer-desc" optional>
            <Input id="layer-desc" value={description} onChange={(e) => setDescription(e.target.value)} maxLength={256} />
          </Field>
          <Field label="Layer content" error={touched ? errors.zip : undefined} help="A .zip file up to 50 MB.">
            <input
              ref={input}
              type="file"
              accept=".zip,application/zip"
              className="hidden"
              onChange={(e) => {
                setZip(e.target.files?.[0] ?? null)
                e.target.value = ""
              }}
            />
            <div className="flex flex-wrap items-center gap-2">
              <Button type="button" variant="outline" size="sm" onClick={() => input.current?.click()}>
                <Upload /> {zip ? "Choose another file" : "Choose .zip file"}
              </Button>
              {zip && (
                <span className="bg-muted inline-flex max-w-full items-center gap-1.5 rounded-md border px-2 py-1 text-sm">
                  <FileArchive className="size-4 shrink-0" />
                  <span className="truncate font-mono text-[13px]">{zip.name}</span>
                  <span className="text-muted-foreground text-xs">{formatBytes(zip.size)}</span>
                  <button type="button" onClick={() => setZip(null)} aria-label="Remove file" className="text-muted-foreground hover:text-foreground">
                    <X className="size-3.5" />
                  </button>
                </span>
              )}
            </div>
          </Field>
          <Field label="Compatible architectures" optional>
            <div className="flex flex-wrap gap-4">
              {ARCHITECTURES.map((a) => (
                <label key={a} className="flex items-center gap-2 font-mono text-sm">
                  <Checkbox checked={archs.includes(a)} onCheckedChange={() => setArchs(toggle(archs, a))} />
                  {a}
                </label>
              ))}
            </div>
          </Field>
          <Field label="Compatible runtimes" optional help="Used to filter layers when adding them to a function.">
            <div className="grid grid-cols-1 gap-2 rounded-md border p-3 sm:grid-cols-2">
              {(runtimes.data ?? []).map((r) => (
                <label key={r.name} className="flex items-center gap-2 text-sm">
                  <Checkbox checked={compat.includes(r.name)} onCheckedChange={() => setCompat(toggle(compat, r.name))} />
                  {r.label}
                </label>
              ))}
            </div>
          </Field>
          <Field label="License" htmlFor="layer-license" optional>
            <Input id="layer-license" value={license} onChange={(e) => setLicense(e.target.value)} placeholder="MIT" maxLength={512} />
          </Field>
          <DialogFooter>
            <Button type="button" variant="outline" onClick={() => onOpenChange(false)} disabled={pending}>
              Cancel
            </Button>
            <Button type="submit" disabled={pending}>
              {pending && <Loader2 className="animate-spin" />}
              Create
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}
