"use client"

import { useEffect, useMemo, useState } from "react"
import { Info, Link2, ListOrdered, Loader2, type LucideIcon } from "lucide-react"
import { toast } from "sonner"

import { Alert, AlertDescription } from "@/components/ui/alert"
import { Button } from "@/components/ui/button"
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog"
import { Input } from "@/components/ui/input"
import { Select, SelectContent, SelectGroup, SelectItem, SelectLabel, SelectTrigger, SelectValue } from "@/components/ui/select"
import { Textarea } from "@/components/ui/textarea"
import { Field } from "@/components/console/form-field"
import { OptionCard, OptionGroup } from "@/components/console/option-card"
import { Tag, recordTypeAccent } from "@/components/console/tag"
import { api, errorMessage, seg } from "@/lib/api"
import { revalidate } from "@/lib/hooks"
import type { DnsRecord, HostedZone, RecordChange } from "@/lib/types"

import {
  R53_PATH,
  RECORD_HELP,
  RECORD_TYPES,
  ZONES_PATH,
  aliasKindLabel,
  fqdn,
  recordNameError,
  valueError,
  zoneLabel,
  type AliasKind,
  type AliasTarget,
} from "./shared"

type Routing = "value" | "alias"

const KINDS: AliasKind[] = ["instance", "task", "database", "load-balancer"]

const ROUTINGS: { value: Routing; title: string; description: string; icon: LucideIcon }[] = [
  { value: "value", title: "IP addresses", description: "Fixed IPv4 addresses you enter.", icon: ListOrdered },
  { value: "alias", title: "Alias to HomeCloud resource", description: "Follows an instance, task, database or load balancer.", icon: Link2 },
]

/** Relative name for the name field: "" for the apex, "www" for www.<zone>. */
function relativeName(name: string, zone: string): string {
  const full = fqdn(name, zone)
  if (full === zone) return ""
  return full.endsWith(`.${zone}`) ? full.slice(0, -(zone.length + 1)) : full
}

export function RecordDialog({
  zone,
  record,
  open,
  onClose,
  targets,
  targetsLoading,
}: {
  zone: HostedZone
  /** null creates a record */
  record: DnsRecord | null
  open: boolean
  onClose: () => void
  targets: AliasTarget[]
  targetsLoading: boolean
}) {
  const [name, setName] = useState("")
  const [type, setType] = useState("A")
  const [ttl, setTtl] = useState("300")
  const [routing, setRouting] = useState<Routing>("value")
  const [values, setValues] = useState("")
  const [alias, setAlias] = useState("")
  const [touched, setTouched] = useState(false)
  const [pending, setPending] = useState(false)

  useEffect(() => {
    if (!open) return
    setTouched(false)
    if (record) {
      setName(relativeName(record.name, zone.name))
      setType(record.type)
      setTtl(String(record.ttl || 300))
      setRouting(record.alias ? "alias" : "value")
      setValues((record.values ?? []).join("\n"))
      setAlias(record.alias ?? "")
    } else {
      setName("")
      setType("A")
      setTtl("300")
      setRouting("value")
      setValues("")
      setAlias("")
    }
  }, [open, record, zone.name])

  const lines = useMemo(
    () =>
      values
        .split("\n")
        .map((v) => v.trim())
        .filter(Boolean),
    [values],
  )
  const isAlias = type === "A" && routing === "alias"
  const full = fqdn(name.trim().toLowerCase() || "@", zone.name)

  const errors: Record<string, string> = {}
  const nameErr = recordNameError(name, zone.name)
  if (nameErr) errors.name = nameErr
  const t = Number(ttl)
  if (!/^\d+$/.test(ttl) || t < 1 || t > 604800) errors.ttl = "1-604800 seconds"
  if (isAlias) {
    if (!alias) errors.alias = "Choose a resource"
  } else {
    if (!lines.length) errors.values = "Enter at least one value"
    else if (type === "CNAME" && lines.length > 1) errors.values = "A CNAME has exactly one value"
    else {
      const bad = lines.map((v) => valueError(type, v)).find(Boolean)
      if (bad) errors.values = bad
    }
    if (type === "CNAME" && full === zone.name) errors.name = "A CNAME cannot be at the zone apex"
  }
  // Warn about duplicates the server would reject for CREATE.
  const dup = zone.records.find((r) => fqdn(r.name, zone.name) === full && r.type === type)
  const renamed = !record || fqdn(record.name, zone.name) !== full || record.type !== type
  if (dup && renamed) errors.name = `A ${type} record for ${zoneLabel(full)} already exists; edit it instead`
  const err = (k: string) => (touched ? errors[k] : undefined)

  const submit = async (e: React.FormEvent) => {
    e.preventDefault()
    setTouched(true)
    if (Object.keys(errors).length) return
    const rec: DnsRecord = {
      name: name.trim().toLowerCase() || "@",
      type,
      ttl: t,
      ...(isAlias ? { alias } : { values: lines }),
    }
    const changes: RecordChange[] = !record
      ? [{ action: "CREATE", record: rec }]
      : renamed
        ? [
            { action: "DELETE", record: record },
            { action: "CREATE", record: rec },
          ]
        : [{ action: "UPSERT", record: rec }]
    setPending(true)
    try {
      await api.post(`${ZONES_PATH}/${seg(zone.id)}/changes`, { changes })
      toast.success(`${record ? "Saved" : "Created"} ${type} record ${zoneLabel(full)}`)
      await revalidate(R53_PATH)
      onClose()
    } catch (e) {
      toast.error(errorMessage(e))
    } finally {
      setPending(false)
    }
  }

  const help = RECORD_HELP[type] ?? { placeholder: "", help: "" }
  const selectedTarget = targets.find((x) => x.id === alias)

  return (
    <Dialog open={open} onOpenChange={(o) => !o && !pending && onClose()}>
      <DialogContent className="max-h-[90vh] overflow-y-auto sm:max-w-xl">
        <form onSubmit={submit} className="flex flex-col gap-4">
          <DialogHeader>
            <DialogTitle>{record ? "Edit record" : "Create record"}</DialogTitle>
            <DialogDescription>
              Changes reach the DNS server within a few seconds. Clients may cache the previous answer for up to its TTL.
            </DialogDescription>
          </DialogHeader>

          <Field label="Record name" htmlFor="rec-name" error={err("name")} help={`Leave empty for the zone apex; use * for a wildcard. Full name: ${zoneLabel(full)}`}>
            <div className="flex">
              <Input
                id="rec-name"
                autoFocus
                value={name}
                onChange={(e) => setName(e.target.value)}
                placeholder="www"
                className="rounded-r-none font-mono text-[13px]"
                spellCheck={false}
                autoComplete="off"
                aria-invalid={!!err("name")}
              />
              <span
                className="bg-muted text-muted-foreground flex max-w-[55%] items-center rounded-r-md border border-l-0 px-2.5 font-mono text-[13px] whitespace-nowrap"
                title={`.${zoneLabel(zone.name)}`}
              >
                <span className="truncate">.{zoneLabel(zone.name)}</span>
              </span>
            </div>
          </Field>

          <div className="grid grid-cols-2 gap-3">
            <Field label="Record type" htmlFor="rec-type">
              <Select
                value={type}
                onValueChange={(v) => {
                  setType(v)
                  if (v !== "A") setRouting("value")
                }}
              >
                <SelectTrigger id="rec-type" className="w-full">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {RECORD_TYPES.map((x) => (
                    <SelectItem key={x} value={x}>
                      <Tag accent={recordTypeAccent(x)}>{x}</Tag>
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </Field>
            <Field label="TTL (seconds)" htmlFor="rec-ttl" error={err("ttl")}>
              <Input id="rec-ttl" inputMode="numeric" value={ttl} onChange={(e) => setTtl(e.target.value)} />
            </Field>
          </div>

          {type === "A" && (
            <Field label="Answer with">
              <OptionGroup label="Answer with">
                {ROUTINGS.map((o) => (
                  <OptionCard key={o.value} selected={routing === o.value} onSelect={() => setRouting(o.value)} icon={o.icon} title={o.title} description={o.description} />
                ))}
              </OptionGroup>
            </Field>
          )}

          {isAlias ? (
            <Field
              label="Route traffic to"
              htmlFor="rec-alias"
              error={err("alias")}
              help={
                selectedTarget
                  ? `${aliasKindLabel(selectedTarget.kind)} · ${selectedTarget.detail}. The record follows the resource's private IP and has no answer while it is not running.`
                  : "The record follows the resource's private IP, even when it changes. It has no answer while the resource is not running."
              }
            >
              <Select value={alias} onValueChange={(v) => v && setAlias(v)}>
                <SelectTrigger id="rec-alias" className="w-full" aria-invalid={!!err("alias")}>
                  <SelectValue placeholder={targetsLoading ? "Loading resources..." : "Choose a resource"} />
                </SelectTrigger>
                <SelectContent>
                  {alias && !selectedTarget && (
                    <SelectItem value={alias}>
                      <span className="font-mono text-[13px]">{alias}</span>
                      <span className="text-muted-foreground text-xs">(not found)</span>
                    </SelectItem>
                  )}
                  {KINDS.map((k) => {
                    const list = targets.filter((x) => x.kind === k)
                    if (!list.length) return null
                    return (
                      <SelectGroup key={k}>
                        <SelectLabel>{aliasKindLabel(k)}s</SelectLabel>
                        {list.map((x) => (
                          <SelectItem key={`${k}-${x.id}`} value={x.id}>
                            <span className="font-mono text-[13px]">{x.label}</span>
                            <span className="text-muted-foreground text-xs">{x.detail}</span>
                          </SelectItem>
                        ))}
                      </SelectGroup>
                    )
                  })}
                  {!targetsLoading && targets.length === 0 && <div className="text-muted-foreground px-2 py-1.5 text-sm">No instances, tasks, databases or load balancers</div>}
                </SelectContent>
              </Select>
            </Field>
          ) : (
            <Field label="Values" htmlFor="rec-values" error={err("values")} help={help.help}>
              <Textarea
                id="rec-values"
                rows={4}
                value={values}
                onChange={(e) => setValues(e.target.value)}
                placeholder={help.placeholder}
                className="font-mono text-[13px]"
                spellCheck={false}
                aria-invalid={!!err("values")}
              />
            </Field>
          )}

          {record && renamed && (
            <Alert variant="info">
              <Info />
              <AlertDescription>
                Changing the name or type replaces the {record.type} record {zoneLabel(fqdn(record.name, zone.name))} in one change batch.
              </AlertDescription>
            </Alert>
          )}

          <DialogFooter>
            <Button type="button" variant="outline" onClick={onClose} disabled={pending}>
              Cancel
            </Button>
            <Button type="submit" disabled={pending}>
              {pending && <Loader2 className="animate-spin" />}
              {record ? "Save record" : "Create record"}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}
