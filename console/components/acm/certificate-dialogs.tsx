"use client"

import { useEffect, useRef, useState } from "react"
import { useRouter } from "next/navigation"
import { FileUp, Loader2 } from "lucide-react"
import { toast } from "sonner"

import { Button } from "@/components/ui/button"
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog"
import { Input } from "@/components/ui/input"
import { Textarea } from "@/components/ui/textarea"
import { ConfirmDialog } from "@/components/console/confirm-dialog"
import { Field } from "@/components/console/form-field"
import { TagsEditor, rowsToTags, type TagRow } from "@/components/console/tags-editor"
import { api, errorMessage, seg } from "@/lib/api"
import { revalidate } from "@/lib/hooks"
import type { Certificate, ImportCertificateInput, RequestCertificateInput } from "@/lib/types"

import { ACM_PATH, CERTS_PATH, certHref } from "./shared"

/** Mirrors domainRe in acm.go (optionally a leading "*."). */
const DOMAIN_RE = /^(\*\.)?([a-zA-Z0-9]([a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?\.)*[a-zA-Z0-9]([a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?$/
const IPV4 = /^(25[0-5]|2[0-4]\d|1\d\d|[1-9]?\d)(\.(25[0-5]|2[0-4]\d|1\d\d|[1-9]?\d)){3}$/
const looksIPv6 = (s: string) => s.includes(":") && /^[0-9a-fA-F:.]+$/.test(s)

export const validName = (s: string) => DOMAIN_RE.test(s) || IPV4.test(s) || looksIPv6(s)

function splitNames(text: string) {
  return text
    .split(/[\s,]+/)
    .map((s) => s.trim())
    .filter(Boolean)
}

export function RequestCertificateDialog({ open, onOpenChange }: { open: boolean; onOpenChange: (o: boolean) => void }) {
  const router = useRouter()
  const [domain, setDomain] = useState("")
  const [sans, setSans] = useState("")
  const [days, setDays] = useState("395")
  const [tags, setTags] = useState<TagRow[]>([])
  const [touched, setTouched] = useState(false)
  const [pending, setPending] = useState(false)

  useEffect(() => {
    if (!open) return
    setDomain("")
    setSans("")
    setDays("395")
    setTags([])
    setTouched(false)
  }, [open])

  const sanList = splitNames(sans)
  const errors: Record<string, string> = {}
  if (!domain.trim()) errors.domain = "Enter a domain name or IP address"
  else if (!validName(domain.trim())) errors.domain = "Not a valid domain name or IP address"
  const badSan = sanList.find((s) => !validName(s))
  if (badSan) errors.sans = `${badSan} is not a valid domain name or IP address`
  const n = Number(days)
  if (!Number.isInteger(n) || n < 1 || n > 825) errors.days = "1-825 days"
  const err = (k: string) => (touched ? errors[k] : undefined)

  const submit = async (e: React.FormEvent) => {
    e.preventDefault()
    setTouched(true)
    if (Object.keys(errors).length) return
    const body: RequestCertificateInput = {
      domain_name: domain.trim(),
      subject_alternative_names: sanList.length ? sanList : undefined,
      valid_days: n,
      tags: rowsToTags(tags),
    }
    setPending(true)
    try {
      const c = await api.post<Certificate>(CERTS_PATH, body)
      toast.success(`Issued a certificate for ${c.domain_name}`)
      await revalidate(ACM_PATH)
      onOpenChange(false)
      router.push(certHref(c.id))
    } catch (err) {
      toast.error(errorMessage(err))
    } finally {
      setPending(false)
    }
  }

  return (
    <Dialog open={open} onOpenChange={(o) => !pending && onOpenChange(o)}>
      <DialogContent className="max-h-[90vh] overflow-y-auto sm:max-w-xl">
        <form onSubmit={submit} className="flex flex-col gap-4">
          <DialogHeader>
            <DialogTitle>Request a private certificate</DialogTitle>
            <DialogDescription>
              Issued immediately by this installation&apos;s private certificate authority (ECDSA P-256). Clients trust it once they trust the HomeCloud private CA.
            </DialogDescription>
          </DialogHeader>
          <Field label="Fully qualified domain name" htmlFor="req-domain" error={err("domain")} help="Also the certificate's common name. Use *.example.internal for a wildcard.">
            <Input id="req-domain" autoFocus value={domain} onChange={(e) => setDomain(e.target.value)} placeholder="app.home.arpa" className="font-mono" spellCheck={false} autoComplete="off" />
          </Field>
          <Field
            label="Additional names"
            htmlFor="req-sans"
            optional
            error={err("sans")}
            help="Subject alternative names: domain names or IP addresses, one per line or separated by commas."
          >
            <Textarea id="req-sans" rows={3} value={sans} onChange={(e) => setSans(e.target.value)} placeholder={"www.app.home.arpa\n192.168.1.10"} className="font-mono text-[13px]" spellCheck={false} />
          </Field>
          <Field label="Validity (days)" htmlFor="req-days" error={err("days")} help="1-825 days. Browsers reject leaf certificates valid for more than 398 days.">
            <Input id="req-days" type="number" min={1} max={825} value={days} onChange={(e) => setDays(e.target.value)} className="w-32" />
          </Field>
          <Field label="Tags" optional>
            <TagsEditor rows={tags} onChange={setTags} />
          </Field>
          <DialogFooter>
            <Button type="button" variant="outline" onClick={() => onOpenChange(false)} disabled={pending}>
              Cancel
            </Button>
            <Button type="submit" disabled={pending}>
              {pending && <Loader2 className="animate-spin" />}
              Request
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}

/** PemField is a PEM textarea with a "Choose file" button that loads a local file into it. */
function PemField({
  id,
  label,
  optional,
  value,
  onChange,
  placeholder,
  error,
  help,
}: {
  id: string
  label: string
  optional?: boolean
  value: string
  onChange: (v: string) => void
  placeholder: string
  error?: string
  help?: string
}) {
  const fileRef = useRef<HTMLInputElement>(null)
  const load = async (f: File | undefined) => {
    if (!f) return
    if (f.size > 1 << 20) {
      toast.error(`${f.name} is larger than 1 MB; that is not a PEM file`)
      return
    }
    onChange(await f.text())
  }
  return (
    <Field label={label} optional={optional} htmlFor={id} error={error} help={help}>
      <input
        ref={fileRef}
        type="file"
        accept=".pem,.crt,.cer,.key,.txt,application/x-pem-file"
        className="hidden"
        onChange={(e) => {
          load(e.target.files?.[0])
          e.target.value = ""
        }}
      />
      <Textarea id={id} rows={5} value={value} onChange={(e) => onChange(e.target.value)} placeholder={placeholder} className="font-mono text-[12px]" spellCheck={false} aria-invalid={!!error} />
      <div className="flex flex-wrap items-center gap-2">
        <Button type="button" variant="outline" size="sm" className="h-7" onClick={() => fileRef.current?.click()}>
          <FileUp /> Choose file
        </Button>
        <span className="text-faint text-xs">or paste the PEM text above</span>
      </div>
    </Field>
  )
}

export function ImportCertificateDialog({ open, onOpenChange }: { open: boolean; onOpenChange: (o: boolean) => void }) {
  const router = useRouter()
  const [cert, setCert] = useState("")
  const [key, setKey] = useState("")
  const [chain, setChain] = useState("")
  const [tags, setTags] = useState<TagRow[]>([])
  const [touched, setTouched] = useState(false)
  const [pending, setPending] = useState(false)

  useEffect(() => {
    if (!open) return
    setCert("")
    setKey("")
    setChain("")
    setTags([])
    setTouched(false)
  }, [open])

  const errors: Record<string, string> = {}
  if (!cert.includes("-----BEGIN CERTIFICATE-----")) errors.cert = "Paste a PEM certificate (-----BEGIN CERTIFICATE-----)"
  if (!/-----BEGIN (EC |RSA |ENCRYPTED )?PRIVATE KEY-----/.test(key)) errors.key = "Paste the PEM private key (-----BEGIN PRIVATE KEY-----)"
  else if (key.includes("ENCRYPTED PRIVATE KEY")) errors.key = "Encrypted private keys are not supported; export the key without a passphrase"
  if (chain.trim() && !chain.includes("-----BEGIN CERTIFICATE-----")) errors.chain = "The chain must be PEM certificates"
  const err = (k: string) => (touched ? errors[k] : undefined)

  const submit = async (e: React.FormEvent) => {
    e.preventDefault()
    setTouched(true)
    if (Object.keys(errors).length) return
    const body: ImportCertificateInput = { certificate: cert.trim(), private_key: key.trim(), certificate_chain: chain.trim() || undefined, tags: rowsToTags(tags) }
    setPending(true)
    try {
      const c = await api.post<Certificate>(`${CERTS_PATH}/import`, body)
      toast.success(`Imported the certificate for ${c.domain_name}`)
      await revalidate(ACM_PATH)
      onOpenChange(false)
      router.push(certHref(c.id))
    } catch (err) {
      toast.error(errorMessage(err))
    } finally {
      setPending(false)
    }
  }

  return (
    <Dialog open={open} onOpenChange={(o) => !pending && onOpenChange(o)}>
      <DialogContent className="max-h-[90vh] overflow-y-auto sm:max-w-2xl">
        <form onSubmit={submit} className="flex flex-col gap-4">
          <DialogHeader>
            <DialogTitle>Import a certificate</DialogTitle>
            <DialogDescription>
              Bring a certificate issued elsewhere, e.g. by Let&apos;s Encrypt. The private key is stored encrypted and never returned by the API. Imported certificates
              are renewed by importing a new one.
            </DialogDescription>
          </DialogHeader>
          <PemField id="imp-cert" label="Certificate body" value={cert} onChange={setCert} placeholder="-----BEGIN CERTIFICATE-----" error={err("cert")} help="The leaf certificate (cert.pem)." />
          <PemField id="imp-key" label="Certificate private key" value={key} onChange={setKey} placeholder="-----BEGIN PRIVATE KEY-----" error={err("key")} help="Unencrypted PEM key matching the certificate (privkey.pem)." />
          <PemField
            id="imp-chain"
            label="Certificate chain"
            optional
            value={chain}
            onChange={setChain}
            placeholder="-----BEGIN CERTIFICATE-----"
            error={err("chain")}
            help="Intermediate certificates (chain.pem), served after the leaf."
          />
          <Field label="Tags" optional>
            <TagsEditor rows={tags} onChange={setTags} />
          </Field>
          <DialogFooter>
            <Button type="button" variant="outline" onClick={() => onOpenChange(false)} disabled={pending}>
              Cancel
            </Button>
            <Button type="submit" disabled={pending}>
              {pending && <Loader2 className="animate-spin" />}
              Import
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}

/** DeleteCertificateDialog deletes a certificate after the user types its domain name. */
export function DeleteCertificateDialog({ cert, onClose, redirect }: { cert: Certificate | null; onClose: () => void; redirect?: boolean }) {
  const router = useRouter()
  return (
    <ConfirmDialog
      open={!!cert}
      onOpenChange={(o) => !o && onClose()}
      title={`Delete certificate for ${cert?.domain_name ?? ""}`}
      confirmText={cert?.domain_name}
      description={
        <p>
          The certificate and its private key are deleted permanently. {cert?.type === "PRIVATE" ? "You can request a new one from the private CA at any time." : "You would need to import it again."}
        </p>
      }
      onConfirm={async () => {
        if (!cert) return
        await api.del(`${CERTS_PATH}/${seg(cert.id)}`)
        toast.success(`Deleted the certificate for ${cert.domain_name}`)
        if (redirect) router.push("/acm/")
        await revalidate(ACM_PATH)
      }}
    />
  )
}
