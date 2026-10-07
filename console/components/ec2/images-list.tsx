"use client"

import { useEffect, useMemo, useState } from "react"
import { useRouter } from "next/navigation"
import { Layers, Loader2, Plus } from "lucide-react"
import { toast } from "sonner"

import { Button } from "@/components/ui/button"
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog"
import { Input } from "@/components/ui/input"
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select"
import { Switch } from "@/components/ui/switch"
import { Textarea } from "@/components/ui/textarea"
import { ActionsMenu } from "@/components/console/actions-menu"
import { ConfirmDialog } from "@/components/console/confirm-dialog"
import { CellLink, CellText, DataTable, type Column } from "@/components/console/data-table"
import { EmptyState } from "@/components/console/empty-state"
import { Field } from "@/components/console/form-field"
import { PageHeader } from "@/components/console/page-header"
import { StatusBadge } from "@/components/console/status-badge"
import { TimeAgo } from "@/components/console/time-ago"
import { api, errorMessage, seg } from "@/lib/api"
import { revalidate, useApi } from "@/lib/hooks"
import type { Image } from "@/lib/types"
import { Tag } from "@/components/console/tag"
import { instanceHref, isVMImage } from "./instance-actions"

const IMAGES_PATH = "/api/v1/ec2/images"
type Owner = "all" | "catalog" | "mine"

const isCatalog = (im: Image) => im.owner === "homecloud"

const columns: Column<Image>[] = [
  {
    id: "name",
    header: "Name",
    cell: (im) => (
      <CellText className="font-medium" max="16rem">
        {im.name}
      </CellText>
    ),
    value: (im) => im.name,
  },
  { id: "id", header: "AMI ID", cell: (im) => <CellText mono>{im.id}</CellText>, value: (im) => im.id },
  {
    id: "desc",
    header: "Description",
    cell: (im) => (
      <CellText muted max="18rem">
        {im.description}
      </CellText>
    ),
    value: (im) => im.description,
    hideBelow: "lg",
  },
  {
    id: "kind",
    header: "Type",
    cell: (im) => <Tag accent={isVMImage(im) ? "violet" : "info"}>{isVMImage(im) ? "VM" : "Container"}</Tag>,
    value: (im) => (isVMImage(im) ? "VM" : "Container"),
  },
  {
    id: "ref",
    header: "Docker image / VM base",
    cell: (im) => (
      <CellText mono max="16rem">
        {im.ref || im.vm_base}
      </CellText>
    ),
    value: (im) => im.ref || im.vm_base || "",
  },
  {
    id: "owner",
    header: "Owner",
    cell: (im) =>
      isCatalog(im) ? (
        "HomeCloud"
      ) : (
        <span>
          Me <span className="text-muted-foreground font-mono text-xs">({im.owner})</span>
        </span>
      ),
    value: (im) => (isCatalog(im) ? "HomeCloud" : "Me"),
    hideBelow: "sm",
  },
  {
    id: "boot",
    header: "Boot mode",
    cell: (im) => (isVMImage(im) ? "Virtual machine" : im.keep_alive ? "OS-like (keep alive)" : "Application"),
    value: (im) => (im.keep_alive ? "keep alive" : "application"),
    hideBelow: "md",
  },
  { id: "state", header: "Status", cell: (im) => <StatusBadge status={im.state || "available"} />, value: (im) => im.state, hideBelow: "md" },
  { id: "created", header: "Created", cell: (im) => <TimeAgo value={im.created_at} />, value: (im) => im.created_at ?? "", hideBelow: "lg" },
  {
    id: "source",
    header: "Source instance",
    cell: (im) =>
      im.source_instance ? (
        <CellLink href={instanceHref(im.source_instance)} mono>
          {im.source_instance}
        </CellLink>
      ) : (
        <span className="text-muted-foreground">-</span>
      ),
    value: (im) => im.source_instance ?? "",
    hideBelow: "lg",
  },
]

export function ImagesList() {
  const router = useRouter()
  const { data, error, isLoading, isValidating, mutate } = useApi<Image[]>(IMAGES_PATH)
  const [owner, setOwner] = useState<Owner>("all")
  const [selected, setSelected] = useState<string[]>([])
  const [deleting, setDeleting] = useState<Image | null>(null)
  const [registering, setRegistering] = useState(false)

  const rows = useMemo(
    () => (data ?? []).filter((im) => (owner === "all" ? true : owner === "catalog" ? isCatalog(im) : !isCatalog(im))),
    [data, owner],
  )
  const sel = rows.find((im) => im.id === selected[0])

  return (
    <div className="flex flex-col gap-4">
      <PageHeader
        title="Amazon Machine Images (AMIs)"
        description="An AMI is a Docker image plus how to boot it. Use the HomeCloud catalog, capture an instance with Create image, or register any Docker image."
        breadcrumbs={[{ label: "EC2", href: "/ec2/" }, { label: "AMIs" }]}
      />
      <DataTable
        title="AMIs"
        data={data ? rows : undefined}
        columns={columns}
        rowId={(im) => im.id}
        loading={isLoading}
        error={error}
        onRefresh={() => mutate()}
        refreshing={isValidating}
        selection="single"
        selected={selected}
        onSelectedChange={setSelected}
        searchPlaceholder="Find AMI by name, ID or image"
        filters={
          <Select value={owner} onValueChange={(v) => setOwner(v as Owner)}>
            <SelectTrigger size="sm" className="h-8 w-48" aria-label="Owner">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="all">All images</SelectItem>
              <SelectItem value="catalog">HomeCloud catalog</SelectItem>
              <SelectItem value="mine">Owned by me</SelectItem>
            </SelectContent>
          </Select>
        }
        actions={
          <>
            <ActionsMenu
              disabled={!sel}
              items={[
                { label: "Launch instance from AMI", onSelect: () => sel && router.push(`/ec2/launch/?image=${encodeURIComponent(sel.id)}`), disabled: !sel },
                { separator: true },
                {
                  label: "Deregister AMI",
                  destructive: true,
                  onSelect: () => sel && setDeleting(sel),
                  disabled: !sel || isCatalog(sel),
                  hint: "Catalog images cannot be deregistered",
                },
              ]}
            />
            <Button size="sm" variant="outline" disabled={!sel} onClick={() => sel && router.push(`/ec2/launch/?image=${encodeURIComponent(sel.id)}`)}>
              Launch instance from AMI
            </Button>
            <Button size="sm" onClick={() => setRegistering(true)}>
              <Plus /> Register image
            </Button>
          </>
        }
        empty={
          <EmptyState
            icon={Layers}
            title={owner === "mine" ? "You don't own any AMIs" : "No AMIs"}
            description="Create an image from an instance (Actions → Create image) or register a Docker image as an AMI."
            action={
              <Button size="sm" onClick={() => setRegistering(true)}>
                <Plus /> Register image
              </Button>
            }
          />
        }
      />

      <ConfirmDialog
        open={!!deleting}
        onOpenChange={(o) => !o && setDeleting(null)}
        title={`Deregister ${deleting?.name ?? "AMI"}?`}
        description={
          <>
            <span className="font-mono">{deleting?.id}</span> will no longer be available for new launches. Running instances are not affected.
            {deleting?.source_instance ? " The captured Docker image is removed from this host." : ""}
          </>
        }
        actionLabel="Deregister"
        onConfirm={async () => {
          if (!deleting) return
          await api.del(`${IMAGES_PATH}/${seg(deleting.id)}`)
          toast.success(`Deregistered ${deleting.id}`)
          setSelected([])
          await mutate()
        }}
      />
      <RegisterImageDialog open={registering} onOpenChange={setRegistering} />
    </div>
  )
}

function RegisterImageDialog({ open, onOpenChange }: { open: boolean; onOpenChange: (o: boolean) => void }) {
  const [name, setName] = useState("")
  const [description, setDescription] = useState("")
  const [ref, setRef] = useState("")
  const [keepAlive, setKeepAlive] = useState(true)
  const [submitted, setSubmitted] = useState(false)
  const [pending, setPending] = useState(false)

  useEffect(() => {
    if (open) {
      setName("")
      setDescription("")
      setRef("")
      setKeepAlive(true)
      setSubmitted(false)
    }
  }, [open])

  const errors = {
    name: !name.trim() ? "Enter a name" : name.length > 128 ? "At most 128 characters" : undefined,
    ref: !ref.trim()
      ? "Enter a Docker image reference"
      : /\s/.test(ref.trim()) || !/^[a-z0-9]/i.test(ref.trim())
        ? "Use a reference like nginx:alpine or ghcr.io/org/app:1.2"
        : undefined,
  }

  const submit = async (e: React.FormEvent) => {
    e.preventDefault()
    setSubmitted(true)
    if (errors.name || errors.ref) return
    setPending(true)
    try {
      const im = await api.post<Image>(IMAGES_PATH, { name: name.trim(), description: description.trim(), ref: ref.trim(), keep_alive: keepAlive })
      toast.success(`Registered ${im.id}`)
      await revalidate(IMAGES_PATH)
      onOpenChange(false)
    } catch (err) {
      toast.error(errorMessage(err))
    } finally {
      setPending(false)
    }
  }

  return (
    <Dialog open={open} onOpenChange={(o) => !pending && onOpenChange(o)}>
      <DialogContent>
        <form onSubmit={submit} className="flex flex-col gap-4">
          <DialogHeader>
            <DialogTitle>Register image</DialogTitle>
            <DialogDescription>Register any Docker image as an AMI. It is pulled on the first launch.</DialogDescription>
          </DialogHeader>
          <Field label="Name" htmlFor="reg-name" error={submitted ? errors.name : undefined}>
            <Input id="reg-name" value={name} onChange={(e) => setName(e.target.value)} placeholder="My app server" autoFocus />
          </Field>
          <Field label="Description" htmlFor="reg-desc" optional>
            <Textarea id="reg-desc" rows={2} value={description} onChange={(e) => setDescription(e.target.value)} />
          </Field>
          <Field
            label="Docker image reference"
            htmlFor="reg-ref"
            error={submitted ? errors.ref : undefined}
            help="Any image the Docker daemon can pull, e.g. redis:7, ghcr.io/org/app:1.2"
          >
            <Input id="reg-ref" value={ref} onChange={(e) => setRef(e.target.value)} placeholder="redis:7-alpine" className="font-mono" spellCheck={false} />
          </Field>
          <div className="flex items-start gap-3 rounded-md border p-3">
            <Switch id="reg-keep" checked={keepAlive} onCheckedChange={setKeepAlive} className="mt-0.5" />
            <label htmlFor="reg-keep" className="flex flex-col gap-0.5 text-sm">
              <span className="font-medium">Keep alive (boot like a VM)</span>
              <span className="text-muted-foreground text-xs">
                On: the image&apos;s entrypoint is replaced by a boot script that runs user data once and keeps the instance up until stopped (for OS
                images such as ubuntu or debian). Off: the image runs its own process (for application images such as nginx or redis); the instance
                stops when that process exits.
              </span>
            </label>
          </div>
          <DialogFooter>
            <Button type="button" variant="outline" onClick={() => onOpenChange(false)} disabled={pending}>
              Cancel
            </Button>
            <Button type="submit" disabled={pending}>
              {pending && <Loader2 className="animate-spin" />}
              Register image
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}
