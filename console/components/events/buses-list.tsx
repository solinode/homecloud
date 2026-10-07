"use client"

import { useState } from "react"
import Link from "next/link"
import { Cable, Plus } from "lucide-react"
import { toast } from "sonner"

import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { ActionsMenu } from "@/components/console/actions-menu"
import { ConfirmDialog } from "@/components/console/confirm-dialog"
import { CellLink, CellText, DataTable, type Column } from "@/components/console/data-table"
import { EmptyState } from "@/components/console/empty-state"
import { Field } from "@/components/console/form-field"
import { FormDialog } from "@/components/console/form-dialog"
import { PageHeader } from "@/components/console/page-header"
import { TimeAgo } from "@/components/console/time-ago"
import { api, seg } from "@/lib/api"
import { revalidate, useApi } from "@/lib/hooks"
import type { EventBus } from "@/lib/types"

import { BUSES_PATH } from "./bus-select"

const busRulesHref = (name: string) => (name === "default" ? "/events/" : `/events/?bus=${encodeURIComponent(name)}`)

const columns: Column<EventBus>[] = [
  { id: "name", header: "Name", cell: (b) => <CellLink href={busRulesHref(b.name)}>{b.name}</CellLink>, value: (b) => b.name },
  { id: "desc", header: "Description", cell: (b) => <CellText muted>{b.description}</CellText>, value: (b) => b.description, hideBelow: "md" },
  { id: "arn", header: "ARN", cell: (b) => <CellText mono max="28rem">{b.arn}</CellText>, value: (b) => b.arn, hideBelow: "lg" },
  { id: "created", header: "Created", cell: (b) => (b.created_at ? <TimeAgo value={b.created_at} /> : <span className="text-muted-foreground">-</span>), value: (b) => b.created_at, hideBelow: "lg" },
  {
    id: "rules",
    header: "",
    cell: (b) => (
      <Link href={busRulesHref(b.name)} onClick={(e) => e.stopPropagation()} className="text-primary text-sm whitespace-nowrap hover:underline">
        View rules
      </Link>
    ),
  },
]

function CreateBusDialog({ onClose }: { onClose: () => void }) {
  const [name, setName] = useState("")
  const [desc, setDesc] = useState("")
  return (
    <FormDialog
      open
      onOpenChange={(o) => !o && onClose()}
      title="Create event bus"
      description="Events published to a bus are matched only against that bus's rules."
      submitLabel="Create event bus"
      disabled={!name.trim()}
      onSubmit={async () => {
        await api.post(BUSES_PATH, { name: name.trim(), description: desc.trim() || undefined })
        toast.success(`Created ${name.trim()}`)
        await revalidate(BUSES_PATH)
      }}
    >
      <Field label="Name" htmlFor="bus-name" help="Letters, digits and / . - _ (not default or aws.*).">
        <Input id="bus-name" value={name} onChange={(e) => setName(e.target.value)} autoFocus autoComplete="off" />
      </Field>
      <Field label="Description" htmlFor="bus-desc" optional>
        <Input id="bus-desc" value={desc} onChange={(e) => setDesc(e.target.value)} />
      </Field>
    </FormDialog>
  )
}

export function BusesList() {
  const { data, error, isLoading, isValidating, mutate } = useApi<EventBus[]>(BUSES_PATH)
  const [selected, setSelected] = useState<string[]>([])
  const [dialog, setDialog] = useState<"create" | "delete" | null>(null)
  const sel = data?.find((b) => b.name === selected[0])

  return (
    <div className="flex flex-col gap-4">
      <PageHeader
        title="Event buses"
        description="The default bus receives events from HomeCloud services. Custom buses isolate your own applications' events."
        breadcrumbs={[{ label: "EventBridge", href: "/events/" }, { label: "Event buses" }]}
      />
      <DataTable
        title="Event buses"
        data={data}
        columns={columns}
        rowId={(b) => b.name}
        loading={isLoading}
        error={error}
        onRefresh={() => mutate()}
        refreshing={isValidating}
        selection="single"
        selected={selected}
        onSelectedChange={setSelected}
        searchPlaceholder="Find event bus"
        defaultSort={{ id: "name" }}
        actions={
          <>
            <ActionsMenu
              disabled={!sel}
              items={[{ label: "Delete event bus", destructive: true, onSelect: () => setDialog("delete"), disabled: sel?.name === "default", hint: "The default bus cannot be deleted" }]}
            />
            <Button size="sm" onClick={() => setDialog("create")}>
              <Plus /> Create event bus
            </Button>
          </>
        }
        empty={
          <EmptyState
            icon={Cable}
            title="No event buses"
            description="Create a custom bus to keep your application's events and rules apart from the default bus."
            action={
              <Button size="sm" onClick={() => setDialog("create")}>
                <Plus /> Create event bus
              </Button>
            }
          />
        }
      />
      {dialog === "create" && <CreateBusDialog onClose={() => setDialog(null)} />}
      {sel && (
        <ConfirmDialog
          open={dialog === "delete"}
          onOpenChange={(o) => !o && setDialog(null)}
          title={`Delete event bus ${sel.name}?`}
          description="The bus and all of its rules are deleted."
          confirmText={sel.name}
          onConfirm={async () => {
            await api.del(`${BUSES_PATH}/${seg(sel.name)}`)
            toast.success(`Deleted ${sel.name}`)
            setSelected([])
            await revalidate("/api/v1/events/")
          }}
        />
      )}
    </div>
  )
}
