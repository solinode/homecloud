"use client"

import { useState } from "react"
import { ChevronDown, Download, ShieldCheck } from "lucide-react"

import { Button } from "@/components/ui/button"
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from "@/components/ui/collapsible"
import { CopyButton } from "@/components/console/copy-button"
import { ErrorState } from "@/components/console/error-state"
import { KeyValueGrid } from "@/components/console/key-value"
import { Section } from "@/components/console/section"
import { authUrl } from "@/lib/api"
import { formatDate } from "@/lib/format"
import { useApi } from "@/lib/hooks"
import type { PrivateCa } from "@/lib/types"
import { cn } from "@/lib/utils"

import { ACM_PATH } from "./shared"

const STEPS: { os: string; cmd: string; note?: string }[] = [
  { os: "macOS", cmd: "sudo security add-trusted-cert -d -r trustRoot -k /Library/Keychains/System.keychain homecloud-ca.pem" },
  { os: "Debian / Ubuntu", cmd: "sudo cp homecloud-ca.pem /usr/local/share/ca-certificates/homecloud-ca.crt && sudo update-ca-certificates" },
  { os: "Fedora / RHEL", cmd: "sudo cp homecloud-ca.pem /etc/pki/ca-trust/source/anchors/ && sudo update-ca-trust" },
  { os: "Windows (admin PowerShell)", cmd: "certutil -addstore -f Root homecloud-ca.pem" },
  { os: "One-off with curl", cmd: "curl --cacert homecloud-ca.pem https://app.home.arpa/" },
  { os: "Node.js", cmd: "NODE_EXTRA_CA_CERTS=homecloud-ca.pem node app.js" },
]

/** PrivateCaPanel shows the installation's private CA with a download link and trust instructions. */
export function PrivateCaPanel({ className }: { className?: string }) {
  const { data, error, mutate } = useApi<PrivateCa>(`${ACM_PATH}/ca`, { revalidateOnFocus: false })
  const [open, setOpen] = useState(false)
  return (
    <Section
      className={className}
      title={
        <span className="flex items-center gap-2">
          <ShieldCheck className="text-muted-foreground size-4" /> Private certificate authority
        </span>
      }
      description="Certificates you request are signed by this CA. Trust it once on each device and browser to accept them without warnings."
      actions={
        <>
          {data && <CopyButton value={data.certificate} size="sm" label="Copy PEM" toastMessage="CA certificate copied" />}
          <Button size="sm" variant="outline" asChild>
            <a href={authUrl(`${ACM_PATH}/ca`, { format: "pem" })} download="homecloud-ca.pem">
              <Download /> Download CA certificate
            </a>
          </Button>
        </>
      }
    >
      {error ? (
        <ErrorState error={error} onRetry={() => mutate()} />
      ) : (
        <div className="flex flex-col gap-4">
          <KeyValueGrid
            columns={3}
            items={[
              { label: "Subject", value: data?.subject ?? "..." },
              { label: "Valid until", value: data ? formatDate(data.not_after, false) : "..." },
              { label: "File", value: <span className="font-mono text-[13px]">homecloud-ca.pem</span> },
            ]}
          />
          <Collapsible open={open} onOpenChange={setOpen}>
            <CollapsibleTrigger className="text-primary inline-flex items-center gap-1 text-sm hover:underline">
              <ChevronDown className={cn("size-4 transition-transform", open && "rotate-180")} /> How to trust the CA
            </CollapsibleTrigger>
            <CollapsibleContent>
              <ul className="mt-3 flex flex-col gap-2">
                {STEPS.map((s) => (
                  <li key={s.os} className="flex flex-col gap-1">
                    <span className="text-muted-foreground text-xs font-medium">{s.os}</span>
                    <span className="bg-muted/50 flex items-start gap-1 rounded-md border px-2 py-1.5">
                      <code className="min-w-0 flex-1 font-mono text-[12px] break-all">{s.cmd}</code>
                      <CopyButton value={s.cmd} />
                    </span>
                  </li>
                ))}
              </ul>
              <p className="text-muted-foreground mt-3 text-xs">
                Firefox keeps its own trust store: Settings, Privacy &amp; Security, Certificates, View Certificates, Authorities, Import. On Android and iOS, install the
                file as a CA certificate and enable full trust for it.
              </p>
            </CollapsibleContent>
          </Collapsible>
        </div>
      )}
    </Section>
  )
}
