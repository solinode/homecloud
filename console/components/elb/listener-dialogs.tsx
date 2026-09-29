"use client"

import { useEffect, useState } from "react"
import Link from "next/link"
import { AlertTriangle, Loader2 } from "lucide-react"
import { toast } from "sonner"

import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert"
import { Button } from "@/components/ui/button"
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog"
import { Input } from "@/components/ui/input"
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select"
import { CertificatePicker } from "@/components/acm/shared"
import { Field } from "@/components/console/form-field"
import { ApiError, api, errorMessage, seg } from "@/lib/api"
import { revalidate } from "@/lib/hooks"
import type { CreateListenerInput, CreateRuleInput, ElbListener, ListenerProtocol, LoadBalancer } from "@/lib/types"
import { cn } from "@/lib/utils"
import { ELB_PREFIX, LBS_PATH, TargetGroupSelect } from "./shared"

const validPort = (s: string) => /^\d+$/.test(s) && Number(s) >= 1 && Number(s) <= 65535

/** ReprovisionWarning explains that listener changes recreate the load balancer container. */
export function ReprovisionWarning({ lb }: { lb: LoadBalancer }) {
  return (
    <Alert className="border-amber-600/30 bg-amber-50 text-amber-800 dark:border-amber-400/30 dark:bg-amber-500/10 dark:text-amber-300">
      <AlertTriangle />
      <AlertTitle>Re-provisions the load balancer</AlertTitle>
      <AlertDescription className="text-current/90">
        Adding or removing a listener recreates the load balancer container, so all listeners drop traffic for a few seconds.
        {lb.scheme === "internet-facing" && " Listeners without a fixed host port may be published on a different host port afterwards."}
      </AlertDescription>
    </Alert>
  )
}

export type ListenerAction = "forward" | "redirect"

/** ListenerDraft is the editable form of a listener (create page rows and the add dialog). */
export interface ListenerDraft {
  protocol: ListenerProtocol
  port: string
  publicPort: string
  /** HTTPS: ACM certificate ARN */
  cert: string
  /** HTTP only: forward to a target group or redirect to HTTPS */
  action: ListenerAction
  tg: string
  redirectPort: string
}

export const emptyListenerDraft = (port = "80", tg = ""): ListenerDraft => ({
  protocol: "HTTP",
  port,
  publicPort: "",
  cert: "",
  action: "forward",
  tg,
  redirectPort: "443",
})

/** listenerDraftErrors validates one draft; keys: port, pub, cert, tg, redirect. */
export function listenerDraftErrors(d: ListenerDraft, opts: { scheme: string; usedPorts: string[] }): Record<string, string> {
  const e: Record<string, string> = {}
  if (!validPort(d.port)) e.port = "1-65535"
  else if (opts.usedPorts.includes(d.port)) e.port = `Port ${d.port} is already used by another listener`
  if (opts.scheme === "internet-facing" && d.publicPort && !validPort(d.publicPort)) e.pub = "1-65535, or empty for any free port"
  if (d.protocol === "HTTPS" && !d.cert) e.cert = "Choose a certificate"
  if (d.protocol === "HTTPS" || d.action === "forward") {
    if (!d.tg) e.tg = "Choose a target group"
  } else if (!validPort(d.redirectPort)) e.redirect = "Enter the HTTPS listener port"
  else if (d.redirectPort === d.port) e.redirect = "Redirect to a different port than this listener's"
  return e
}

export function listenerDraftInput(d: ListenerDraft, scheme: string): CreateListenerInput {
  const redirect = d.protocol === "HTTP" && d.action === "redirect"
  return {
    port: Number(d.port),
    protocol: d.protocol,
    certificate_arn: d.protocol === "HTTPS" ? d.cert : undefined,
    redirect_https_port: redirect ? Number(d.redirectPort) : undefined,
    public_port: scheme === "internet-facing" && d.publicPort ? Number(d.publicPort) : undefined,
    default_target_group: redirect ? "" : d.tg,
  }
}

/** Short text for a listener's default action, e.g. "forward to web-tg" or "redirect to HTTPS:443". */
export function draftSummary(d: ListenerDraft) {
  if (d.protocol === "HTTP" && d.action === "redirect") return `${d.protocol}:${d.port || "?"} → redirect to HTTPS:${d.redirectPort || "?"}`
  return `${d.protocol}:${d.port || "?"} → ${d.tg || "?"}`
}

/**
 * ListenerFields edits protocol, ports, certificate and default action.
 * httpsPorts lists the HTTPS listener ports a redirect can point at.
 */
export function ListenerFields({
  draft,
  onChange,
  idPrefix,
  vpcId,
  scheme,
  errors,
  httpsPorts,
  compact,
}: {
  draft: ListenerDraft
  onChange: (d: ListenerDraft) => void
  idPrefix: string
  vpcId: string
  scheme: string
  errors: Record<string, string | undefined>
  httpsPorts: string[]
  compact?: boolean
}) {
  const set = (patch: Partial<ListenerDraft>) => onChange({ ...draft, ...patch })
  const redirect = draft.protocol === "HTTP" && draft.action === "redirect"
  return (
    <div className="flex flex-col gap-3">
      <div className={cn("grid grid-cols-2 items-start gap-3", !compact && "sm:grid-cols-[8rem_7rem_9rem]")}>
        <Field label="Protocol" htmlFor={`${idPrefix}-proto`}>
          <Select
            value={draft.protocol}
            onValueChange={(v) => {
              const protocol = v as ListenerProtocol
              const patch: Partial<ListenerDraft> = { protocol }
              if (protocol === "HTTPS" && draft.port === "80") patch.port = "443"
              if (protocol === "HTTP" && draft.port === "443") patch.port = "80"
              set(patch)
            }}
          >
            <SelectTrigger id={`${idPrefix}-proto`} className="h-8 w-full font-mono">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="HTTP" className="font-mono">
                HTTP
              </SelectItem>
              <SelectItem value="HTTPS" className="font-mono">
                HTTPS
              </SelectItem>
            </SelectContent>
          </Select>
        </Field>
        <Field label="Port" htmlFor={`${idPrefix}-port`} error={errors.port}>
          <Input id={`${idPrefix}-port`} inputMode="numeric" value={draft.port} onChange={(e) => set({ port: e.target.value })} className="h-8" />
        </Field>
        <Field label="Host port" htmlFor={`${idPrefix}-pub`} optional error={errors.pub} className={cn(compact && "col-span-2")}>
          <Input
            id={`${idPrefix}-pub`}
            inputMode="numeric"
            value={scheme === "internal" ? "" : draft.publicPort}
            disabled={scheme === "internal"}
            onChange={(e) => set({ publicPort: e.target.value })}
            placeholder={scheme === "internal" ? "Not published" : "Any free port"}
            className="h-8"
          />
        </Field>
      </div>
      {draft.protocol === "HTTPS" && (
        <Field
          label="Certificate (from ACM)"
          htmlFor={`${idPrefix}-cert`}
          error={errors.cert}
          help={
            <>
              TLS 1.2/1.3 terminates at the load balancer; targets receive plain HTTP.{" "}
              <Link href="/acm/?request=1" className="text-primary hover:underline" target="_blank">
                Request a certificate
              </Link>
            </>
          }
        >
          <CertificatePicker id={`${idPrefix}-cert`} value={draft.cert} onChange={(v) => set({ cert: v })} invalid={!!errors.cert} />
        </Field>
      )}
      {draft.protocol === "HTTP" && (
        <Field label="Default action" htmlFor={`${idPrefix}-action`}>
          <Select value={draft.action} onValueChange={(v) => set({ action: v as ListenerAction, redirectPort: draft.redirectPort || httpsPorts[0] || "443" })}>
            <SelectTrigger id={`${idPrefix}-action`} className="h-8 w-full">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="forward">Forward to a target group</SelectItem>
              <SelectItem value="redirect">Redirect to HTTPS (301)</SelectItem>
            </SelectContent>
          </Select>
        </Field>
      )}
      {redirect ? (
        <Field
          label="HTTPS port to redirect to"
          htmlFor={`${idPrefix}-redir`}
          error={errors.redirect}
          help={
            draft.redirectPort && !httpsPorts.includes(draft.redirectPort)
              ? `There is no HTTPS listener on port ${draft.redirectPort} yet; add one or the redirect leads nowhere.`
              : "Every request gets a 301 to https://<host>:<port> with the same path. Internet-facing redirects use the HTTPS listener's host port."
          }
        >
          <Input id={`${idPrefix}-redir`} inputMode="numeric" value={draft.redirectPort} onChange={(e) => set({ redirectPort: e.target.value })} className="h-8 w-32" />
        </Field>
      ) : (
        <Field label="Forward to" htmlFor={`${idPrefix}-tg`} error={errors.tg}>
          <TargetGroupSelect id={`${idPrefix}-tg`} value={draft.tg} onChange={(v) => set({ tg: v })} vpcId={vpcId} invalid={!!errors.tg} />
        </Field>
      )}
    </div>
  )
}

export function AddListenerDialog({ lb, open, onOpenChange }: { lb: LoadBalancer; open: boolean; onOpenChange: (o: boolean) => void }) {
  const [draft, setDraft] = useState<ListenerDraft>(emptyListenerDraft())
  const [submitted, setSubmitted] = useState(false)
  const [pending, setPending] = useState(false)

  useEffect(() => {
    if (!open) return
    const used = new Set(lb.listeners.map((l) => String(l.port)))
    const hasHttps = lb.listeners.some((l) => l.protocol === "HTTPS")
    const d = emptyListenerDraft(["80", "8080", "8000", "8081", "8888", "3000"].find((p) => !used.has(p)) ?? "", lb.listeners.find((l) => l.default_target_group)?.default_target_group ?? "")
    if (!hasHttps && !used.has("443")) {
      d.protocol = "HTTPS"
      d.port = "443"
    }
    d.redirectPort = String(lb.listeners.find((l) => l.protocol === "HTTPS")?.port ?? 443)
    setDraft(d)
    setSubmitted(false)
  }, [open, lb])

  const errors = listenerDraftErrors(draft, { scheme: lb.scheme, usedPorts: lb.listeners.map((l) => String(l.port)) })
  const shown = submitted ? errors : {}
  const httpsPorts = lb.listeners.filter((l) => l.protocol === "HTTPS").map((l) => String(l.port))

  const submit = async (e: React.FormEvent) => {
    e.preventDefault()
    setSubmitted(true)
    if (Object.keys(errors).length) return
    setPending(true)
    try {
      await api.post(`${LBS_PATH}/${seg(lb.name)}/listeners`, listenerDraftInput(draft, lb.scheme))
      toast.success(`Added listener ${draft.protocol}:${draft.port}; re-provisioning ${lb.name}`)
      await revalidate(ELB_PREFIX)
      onOpenChange(false)
    } catch (e) {
      toast.error(errorMessage(e))
    } finally {
      setPending(false)
    }
  }

  return (
    <Dialog open={open} onOpenChange={(o) => !pending && onOpenChange(o)}>
      <DialogContent className="max-h-[90vh] overflow-y-auto sm:max-w-lg">
        <form onSubmit={submit} className="flex flex-col gap-4">
          <DialogHeader>
            <DialogTitle>Add listener</DialogTitle>
            <DialogDescription>Accept HTTP or HTTPS on another port and forward it to a target group, or redirect plain HTTP to HTTPS.</DialogDescription>
          </DialogHeader>
          <ListenerFields draft={draft} onChange={setDraft} idPrefix="al" vpcId={lb.vpc_id} scheme={lb.scheme} errors={shown} httpsPorts={httpsPorts} compact />
          <ReprovisionWarning lb={lb} />
          <DialogFooter>
            <Button type="button" variant="outline" onClick={() => onOpenChange(false)} disabled={pending}>
              Cancel
            </Button>
            <Button type="submit" disabled={pending}>
              {pending && <Loader2 className="animate-spin" />}
              Add listener
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}

// nginx rejects two identical `location` blocks in one server; a host-less rule is also copied into every host's server block.
function conflictingRule(listener: ElbListener, path: string, host: string) {
  const p = path || "/"
  return listener.rules.find((r) => (r.path_prefix || "/") === p && (r.host_header ?? "") === host) ??
    listener.rules.find((r) => (r.path_prefix || "/") === p && (!r.host_header || !host))
}

const BAD_CHARS = /[\s;{}'"\\]/

export function AddRuleDialog({ lb, listener, onClose }: { lb: LoadBalancer; listener: ElbListener | null; onClose: () => void }) {
  const [priority, setPriority] = useState("")
  const [path, setPath] = useState("")
  const [host, setHost] = useState("")
  const [tg, setTg] = useState("")
  const [submitted, setSubmitted] = useState(false)
  const [pending, setPending] = useState(false)
  const [serverErr, setServerErr] = useState<string | null>(null)

  useEffect(() => {
    if (!listener) return
    setServerErr(null)
    setPriority("")
    setPath("")
    setHost("")
    setTg("")
    setSubmitted(false)
  }, [listener])

  const errors: Record<string, string> = {}
  const p = path.trim()
  const h = host.trim().toLowerCase()
  if (!p && !h) errors.cond = "Add a path prefix, a host header, or both"
  if (p && !p.startsWith("/")) errors.path = "Must start with /"
  else if (BAD_CHARS.test(p)) errors.path = "No spaces, quotes, braces or semicolons"
  if (BAD_CHARS.test(h)) errors.host = "No spaces, quotes, braces or semicolons"
  if (priority && (!/^\d+$/.test(priority) || Number(priority) < 1 || Number(priority) > 50000)) errors.priority = "1-50000"
  if (!tg) errors.tg = "Choose a target group"
  const conflict = listener && !errors.cond && !errors.path && !errors.host ? conflictingRule(listener, p, h) : undefined
  if (conflict) {
    errors.cond = `Overlaps rule ${conflict.priority} (${[conflict.host_header && `host ${conflict.host_header}`, `path ${conflict.path_prefix || "/"}`]
      .filter(Boolean)
      .join(", ")}): nginx cannot route the same path twice for one host. Delete that rule first.`
  }
  const err = (k: string) => (submitted ? errors[k] : undefined)

  const submit = async (e: React.FormEvent) => {
    e.preventDefault()
    setSubmitted(true)
    if (!listener || Object.keys(errors).length) return
    const body: CreateRuleInput = {
      priority: priority ? Number(priority) : undefined,
      path_prefix: p || undefined,
      host_header: h || undefined,
      target_group: tg,
    }
    setPending(true)
    try {
      setServerErr(null)
      await api.post(`${LBS_PATH}/${seg(lb.name)}/listeners/${seg(listener.id)}/rules`, body)
      toast.success(`Added rule to listener ${listener.protocol}:${listener.port}`)
      await revalidate(ELB_PREFIX)
      onClose()
    } catch (e) {
      // 409 ResourceConflict: a rule with the same host header + path prefix already exists on this listener.
      if (e instanceof ApiError && e.status === 409) setServerErr(errorMessage(e))
      else toast.error(errorMessage(e))
    } finally {
      setPending(false)
    }
  }

  return (
    <Dialog open={!!listener} onOpenChange={(o) => !o && !pending && onClose()}>
      <DialogContent className="sm:max-w-lg">
        <form onSubmit={submit} className="flex flex-col gap-4">
          <DialogHeader>
            <DialogTitle>Add rule to {listener?.protocol ?? "HTTP"}:{listener?.port}</DialogTitle>
            <DialogDescription>
              Requests matching every condition are forwarded to the rule&apos;s target group; everything else goes to the default action. Applied without
              downtime.
            </DialogDescription>
          </DialogHeader>
          <Field label="Path prefix" htmlFor="ar-path" optional error={err("path")} help="Matches request paths starting with this prefix, e.g. /api/.">
            <Input id="ar-path" value={path} onChange={(e) => setPath(e.target.value)} placeholder="/api/" className="font-mono" autoFocus spellCheck={false} />
          </Field>
          <Field label="Host header" htmlFor="ar-host" optional error={err("host")} help="Matches the Host header exactly, e.g. api.example.com.">
            <Input id="ar-host" value={host} onChange={(e) => setHost(e.target.value)} placeholder="api.example.com" className="font-mono" spellCheck={false} />
          </Field>
          {err("cond") && <p className="text-destructive -mt-2 text-xs">{err("cond")}</p>}
          {serverErr && <p className="text-destructive text-xs" role="alert">{serverErr}</p>}
          <div className="grid grid-cols-1 gap-3 sm:grid-cols-[8rem_minmax(0,1fr)]">
            <Field label="Priority" htmlFor="ar-priority" optional error={err("priority")}>
              <Input
                id="ar-priority"
                inputMode="numeric"
                value={priority}
                onChange={(e) => setPriority(e.target.value)}
                placeholder={String((listener?.rules.length ?? 0) + 1)}
              />
            </Field>
            <Field label="Forward to" htmlFor="ar-tg" error={err("tg")}>
              <TargetGroupSelect id="ar-tg" value={tg} onChange={setTg} vpcId={lb.vpc_id} invalid={!!err("tg")} />
            </Field>
          </div>
          <p className="text-muted-foreground text-xs">
            Path rules use nginx prefix matching: the longest matching prefix wins, so priority only orders rules in the list and in the generated config.
          </p>
          <DialogFooter>
            <Button type="button" variant="outline" onClick={onClose} disabled={pending}>
              Cancel
            </Button>
            <Button type="submit" disabled={pending}>
              {pending && <Loader2 className="animate-spin" />}
              Add rule
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}
