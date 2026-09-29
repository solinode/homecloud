"use client"

import { useEffect, useState } from "react"
import { AlertTriangle, Loader2 } from "lucide-react"
import { toast } from "sonner"

import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert"
import { Button } from "@/components/ui/button"
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog"
import { Input } from "@/components/ui/input"
import { Field } from "@/components/console/form-field"
import { ApiError, api, errorMessage, seg } from "@/lib/api"
import { revalidate } from "@/lib/hooks"
import type { CreateListenerInput, CreateRuleInput, ElbListener, LoadBalancer } from "@/lib/types"
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

export function AddListenerDialog({ lb, open, onOpenChange }: { lb: LoadBalancer; open: boolean; onOpenChange: (o: boolean) => void }) {
  const [port, setPort] = useState("")
  const [publicPort, setPublicPort] = useState("")
  const [tg, setTg] = useState("")
  const [submitted, setSubmitted] = useState(false)
  const [pending, setPending] = useState(false)

  useEffect(() => {
    if (!open) return
    const used = new Set(lb.listeners.map((l) => String(l.port)))
    setPort(["80", "8080", "8000", "8081", "8888", "3000"].find((p) => !used.has(p)) ?? "")
    setPublicPort("")
    setTg(lb.listeners[0]?.default_target_group ?? "")
    setSubmitted(false)
  }, [open, lb])

  const errors: Record<string, string> = {}
  if (!validPort(port)) errors.port = "Enter a port from 1 to 65535"
  else if (lb.listeners.some((l) => String(l.port) === port)) errors.port = `A listener already uses port ${port}`
  if (publicPort && !validPort(publicPort)) errors.pub = "1-65535, or empty for any free port"
  if (!tg) errors.tg = "Choose a target group"
  const err = (k: string) => (submitted ? errors[k] : undefined)

  const submit = async (e: React.FormEvent) => {
    e.preventDefault()
    setSubmitted(true)
    if (Object.keys(errors).length) return
    const body: CreateListenerInput = {
      port: Number(port),
      protocol: "HTTP",
      public_port: lb.scheme === "internet-facing" && publicPort ? Number(publicPort) : undefined,
      default_target_group: tg,
    }
    setPending(true)
    try {
      await api.post(`${LBS_PATH}/${seg(lb.name)}/listeners`, body)
      toast.success(`Added listener HTTP:${port}; re-provisioning ${lb.name}`)
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
      <DialogContent className="sm:max-w-lg">
        <form onSubmit={submit} className="flex flex-col gap-4">
          <DialogHeader>
            <DialogTitle>Add listener</DialogTitle>
            <DialogDescription>Accept HTTP on another port and forward it to a target group.</DialogDescription>
          </DialogHeader>
          <div className="grid grid-cols-2 gap-3">
            <Field label="Port" htmlFor="al-port" error={err("port")}>
              <Input id="al-port" inputMode="numeric" value={port} onChange={(e) => setPort(e.target.value)} autoFocus />
            </Field>
            <Field label="Host port" htmlFor="al-pub" optional error={err("pub")}>
              <Input
                id="al-pub"
                inputMode="numeric"
                value={publicPort}
                onChange={(e) => setPublicPort(e.target.value)}
                disabled={lb.scheme === "internal"}
                placeholder={lb.scheme === "internal" ? "Not published" : "Any free port"}
              />
            </Field>
          </div>
          <Field label="Default action: forward to" htmlFor="al-tg" error={err("tg")}>
            <TargetGroupSelect id="al-tg" value={tg} onChange={setTg} vpcId={lb.vpc_id} invalid={!!err("tg")} />
          </Field>
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
      toast.success(`Added rule to listener HTTP:${listener.port}`)
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
            <DialogTitle>Add rule to HTTP:{listener?.port}</DialogTitle>
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
