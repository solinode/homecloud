"use client"

import { useState } from "react"
import { AlertTriangle, Download, KeyRound, Plus, Upload } from "lucide-react"
import { toast } from "sonner"

import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Textarea } from "@/components/ui/textarea"
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select"
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog"
import { ActionsMenu } from "@/components/console/actions-menu"
import { ConfirmDialog } from "@/components/console/confirm-dialog"
import { CellText, DataTable, type Column } from "@/components/console/data-table"
import { Tag } from "@/components/console/tag"
import { EmptyState } from "@/components/console/empty-state"
import { Field } from "@/components/console/form-field"
import { FormDialog, KEEP_OPEN } from "@/components/console/form-dialog"
import { PageHeader } from "@/components/console/page-header"
import { TimeAgo } from "@/components/console/time-ago"
import { api, seg } from "@/lib/api"
import { revalidate, useApi } from "@/lib/hooks"
import type { KeyPair } from "@/lib/types"

export const KEY_PAIRS_PATH = "/api/v1/ec2/key-pairs"

/** downloadText saves text as a file through the browser. */
export function downloadText(filename: string, text: string) {
  const url = URL.createObjectURL(new Blob([text], { type: "application/x-pem-file" }))
  const a = document.createElement("a")
  a.href = url
  a.download = filename
  a.click()
  // Revoke after the click has started the download (some browsers cancel it otherwise).
  setTimeout(() => URL.revokeObjectURL(url), 0)
}

const columns: Column<KeyPair>[] = [
  { id: "name", header: "Name", cell: (k) => <CellText className="font-medium">{k.name}</CellText>, value: (k) => k.name },
  { id: "type", header: "Type", cell: (k) => <Tag>{k.type.toUpperCase()}</Tag>, value: (k) => k.type, hideBelow: "sm" },
  { id: "fp", header: "Fingerprint", cell: (k) => <CellText mono muted max="20rem">{k.fingerprint}</CellText>, value: (k) => k.fingerprint, hideBelow: "md" },
  { id: "id", header: "ID", cell: (k) => <CellText mono>{k.id}</CellText>, value: (k) => k.id, hideBelow: "lg" },
  { id: "created", header: "Created", cell: (k) => <TimeAgo value={k.created_at} />, value: (k) => k.created_at, hideBelow: "lg" },
]

/** CreateKeyPairDialog creates a key pair and offers the private key once for download. */
export function CreateKeyPairDialog({ onClose, onCreated }: { onClose: () => void; onCreated?: (k: KeyPair) => void }) {
  const [name, setName] = useState("")
  const [type, setType] = useState("ed25519")
  const [created, setCreated] = useState<{ key_pair: KeyPair; private_key: string } | null>(null)

  if (created) {
    const file = `${created.key_pair.name}.pem`
    return (
      <Dialog open onOpenChange={(o) => !o && onClose()}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Key pair created</DialogTitle>
            <DialogDescription>
              <span className="font-mono">{created.key_pair.name}</span> is ready. Download the private key to log in to instances launched with it.
            </DialogDescription>
          </DialogHeader>
          <Alert variant="warning">
            <AlertTriangle />
            <AlertTitle>Download the private key now</AlertTitle>
            <AlertDescription>
              It is shown once and is not stored. Keep it safe (<span className="font-mono text-xs">chmod 400 {file}</span>).
            </AlertDescription>
          </Alert>
          <DialogFooter>
            <Button variant="outline" onClick={onClose}>
              Done
            </Button>
            <Button onClick={() => downloadText(file, created.private_key)}>
              <Download /> Download {file}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    )
  }
  return (
    <FormDialog
      open
      onOpenChange={(o) => !o && onClose()}
      title="Create key pair"
      description="HomeCloud keeps the public key and generates the private key for you to download."
      submitLabel="Create key pair"
      disabled={!name.trim()}
      onSubmit={async () => {
        const r = await api.post<{ key_pair: KeyPair; private_key: string }>(KEY_PAIRS_PATH, { name: name.trim(), type })
        await revalidate(KEY_PAIRS_PATH)
        onCreated?.(r.key_pair)
        setCreated(r)
        return KEEP_OPEN
      }}
    >
      <Field label="Name" htmlFor="kp-name" help="Up to 255 ASCII characters.">
        <Input id="kp-name" value={name} onChange={(e) => setName(e.target.value)} placeholder="my-key" autoFocus autoComplete="off" />
      </Field>
      <Field label="Key pair type" htmlFor="kp-type" help="ED25519 keys are shorter and faster; use RSA for older SSH clients.">
        <Select value={type} onValueChange={setType}>
          <SelectTrigger id="kp-type" className="w-full">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="ed25519">ED25519</SelectItem>
            <SelectItem value="rsa">RSA</SelectItem>
          </SelectContent>
        </Select>
      </Field>
    </FormDialog>
  )
}

function ImportDialog({ onClose }: { onClose: () => void }) {
  const [name, setName] = useState("")
  const [key, setKey] = useState("")
  return (
    <FormDialog
      open
      onOpenChange={(o) => !o && onClose()}
      title="Import key pair"
      description="Paste an OpenSSH public key (RSA or ED25519), for example the contents of ~/.ssh/id_ed25519.pub."
      submitLabel="Import key pair"
      disabled={!name.trim() || !key.trim()}
      onSubmit={async () => {
        await api.post(`${KEY_PAIRS_PATH}/import`, { name: name.trim(), public_key: key.trim() })
        await revalidate(KEY_PAIRS_PATH)
        toast.success(`Imported ${name.trim()}`)
      }}
    >
      <Field label="Name" htmlFor="kpi-name">
        <Input id="kpi-name" value={name} onChange={(e) => setName(e.target.value)} autoFocus autoComplete="off" />
      </Field>
      <Field label="Public key" htmlFor="kpi-key" help="One line: key type, base64 key and an optional comment.">
        <Textarea id="kpi-key" value={key} onChange={(e) => setKey(e.target.value)} rows={5} className="font-mono text-xs" spellCheck={false} placeholder="ssh-ed25519 AAAA... user@host" />
      </Field>
    </FormDialog>
  )
}

export function KeyPairsList() {
  const { data, error, isLoading, isValidating, mutate } = useApi<KeyPair[]>(KEY_PAIRS_PATH)
  const [selected, setSelected] = useState<string[]>([])
  const [dialog, setDialog] = useState<"create" | "import" | "delete" | null>(null)
  const sel = data?.find((k) => k.id === selected[0])

  return (
    <div className="flex flex-col gap-4">
      <PageHeader
        title="Key pairs"
        description="Public keys added to root's authorized_keys in instances launched with the key pair."
        breadcrumbs={[{ label: "EC2", href: "/ec2/" }, { label: "Key pairs" }]}
      />
      <DataTable
        title="Key pairs"
        data={data}
        columns={columns}
        rowId={(k) => k.id}
        loading={isLoading}
        error={error}
        onRefresh={() => mutate()}
        refreshing={isValidating}
        selection="single"
        selected={selected}
        onSelectedChange={setSelected}
        searchPlaceholder="Find key pair by name or fingerprint"
        defaultSort={{ id: "name" }}
        actions={
          <>
            <ActionsMenu disabled={!sel} items={[{ label: "Delete key pair", destructive: true, onSelect: () => setDialog("delete") }]} />
            <Button size="sm" variant="outline" onClick={() => setDialog("import")}>
              <Upload /> Import key pair
            </Button>
            <Button size="sm" onClick={() => setDialog("create")}>
              <Plus /> Create key pair
            </Button>
          </>
        }
        empty={
          <EmptyState
            icon={KeyRound}
            title="No key pairs"
            description="Create or import a key pair to log in to instances over SSH."
            action={
              <Button size="sm" onClick={() => setDialog("create")}>
                <Plus /> Create key pair
              </Button>
            }
          />
        }
      />
      {dialog === "create" && <CreateKeyPairDialog onClose={() => setDialog(null)} />}
      {dialog === "import" && <ImportDialog onClose={() => setDialog(null)} />}
      {sel && (
        <ConfirmDialog
          open={dialog === "delete"}
          onOpenChange={(o) => !o && setDialog(null)}
          title={`Delete key pair ${sel.name}?`}
          description="Instances already launched keep the public key."
          actionLabel="Delete"
          onConfirm={async () => {
            await api.del(`${KEY_PAIRS_PATH}/${seg(sel.name)}`)
            toast.success(`Deleted ${sel.name}`)
            setSelected([])
            await mutate()
          }}
        />
      )}
    </div>
  )
}
