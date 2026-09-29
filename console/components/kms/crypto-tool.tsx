"use client"

import { useEffect, useState } from "react"
import Link from "next/link"
import { AlertCircle, ArrowRight, Loader2, Lock, LockOpen } from "lucide-react"

import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert"
import { Button } from "@/components/ui/button"
import { Textarea } from "@/components/ui/textarea"
import { CopyButton } from "@/components/console/copy-button"
import { ErrorState } from "@/components/console/error-state"
import { Field } from "@/components/console/form-field"
import { PageHeader } from "@/components/console/page-header"
import { Section } from "@/components/console/section"
import { TagsEditor, rowsToTags, type TagRow } from "@/components/console/tags-editor"
import { api, errorMessage } from "@/lib/api"
import { useQueryParam } from "@/lib/hooks"
import type { KmsDecryptResult, KmsEncryptResult, KmsKey } from "@/lib/types"
import { cn } from "@/lib/utils"

import { KMS_PATH, KeyPicker, keyHref, keyIdFromArn, keyLabel, useKmsKeys } from "./shared"

const MAX_PLAINTEXT = 4096

export function utf8ToBase64(s: string): string {
  const bytes = new TextEncoder().encode(s)
  let bin = ""
  for (const b of bytes) bin += String.fromCharCode(b)
  return btoa(bin)
}

/** base64ToUtf8 returns null when the bytes are not valid UTF-8 text. */
export function base64ToUtf8(b64: string): string | null {
  try {
    const bin = atob(b64)
    const bytes = Uint8Array.from(bin, (c) => c.charCodeAt(0))
    return new TextDecoder("utf-8", { fatal: true }).decode(bytes)
  } catch {
    return null
  }
}

type Op = "encrypt" | "decrypt"

function KeyRef({ arn, keys }: { arn: string; keys?: KmsKey[] }) {
  const id = keyIdFromArn(arn)
  const k = keys?.find((x) => x.id === id)
  return (
    <Link href={keyHref(id)} className="text-primary font-mono text-[13px] break-all hover:underline">
      {k ? keyLabel(k) : id}
    </Link>
  )
}

/**
 * CryptoTool encrypts and decrypts small payloads with KMS. With `keyId` the key
 * is preselected (and fixed when `lockKey` is set, as on the key detail page).
 */
export function CryptoTool({ keyId, lockKey }: { keyId?: string; lockKey?: boolean }) {
  const keys = useKmsKeys()
  const [op, setOp] = useState<Op>("encrypt")
  const [key, setKey] = useState(keyId ?? "")
  const [plaintext, setPlaintext] = useState("")
  const [blob, setBlob] = useState("")
  const [context, setContext] = useState<TagRow[]>([])
  const [pending, setPending] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [encrypted, setEncrypted] = useState<KmsEncryptResult | null>(null)
  const [decrypted, setDecrypted] = useState<KmsDecryptResult | null>(null)

  useEffect(() => {
    if (keyId) setKey(keyId)
  }, [keyId])

  // Preselect the first enabled customer key (or any enabled key) when none is chosen.
  useEffect(() => {
    if (key || !keys.data) return
    const enabled = keys.data.filter((k) => k.state === "Enabled")
    const first = enabled.find((k) => !k.managed) ?? enabled[0]
    if (first) setKey(first.id)
  }, [key, keys.data])

  const selectedKey = keys.data?.find((k) => k.id === key)
  const bytes = new TextEncoder().encode(plaintext).length
  const tooBig = bytes > MAX_PLAINTEXT

  const switchOp = (o: Op) => {
    setOp(o)
    setError(null)
  }

  const encrypt = async () => {
    setPending(true)
    setError(null)
    setEncrypted(null)
    try {
      const r = await api.post<KmsEncryptResult>(`${KMS_PATH}/encrypt`, {
        key_id: key,
        plaintext: utf8ToBase64(plaintext),
        encryption_context: rowsToTags(context),
      })
      setEncrypted(r)
    } catch (e) {
      setError(errorMessage(e))
    } finally {
      setPending(false)
    }
  }

  const decrypt = async () => {
    setPending(true)
    setError(null)
    setDecrypted(null)
    try {
      const r = await api.post<KmsDecryptResult>(`${KMS_PATH}/decrypt`, {
        ciphertext_blob: blob.replace(/\s+/g, ""),
        encryption_context: rowsToTags(context),
      })
      setDecrypted(r)
    } catch (e) {
      setError(errorMessage(e))
    } finally {
      setPending(false)
    }
  }

  const useForDecrypt = () => {
    if (!encrypted) return
    setBlob(encrypted.ciphertext_blob)
    setDecrypted(null)
    switchOp("decrypt")
  }

  const decryptedText = decrypted ? base64ToUtf8(decrypted.plaintext) : null
  const canEncrypt = !!key && selectedKey?.state === "Enabled" && plaintext.length > 0 && !tooBig && !pending
  const canDecrypt = blob.trim().length > 0 && !pending

  return (
    <div className="flex flex-col gap-4">
      <div role="tablist" aria-label="Operation" className="bg-muted inline-flex w-fit rounded-lg p-1">
        {(["encrypt", "decrypt"] as const).map((o) => (
          <button
            key={o}
            type="button"
            role="tab"
            aria-selected={op === o}
            onClick={() => switchOp(o)}
            className={cn(
              "inline-flex items-center gap-1.5 rounded-md px-3 py-1.5 text-sm font-medium transition-colors",
              op === o ? "bg-background text-foreground shadow-xs" : "text-muted-foreground hover:text-foreground",
            )}
          >
            {o === "encrypt" ? <Lock className="size-4" /> : <LockOpen className="size-4" />}
            {o === "encrypt" ? "Encrypt" : "Decrypt"}
          </button>
        ))}
      </div>

      <div className="grid grid-cols-1 gap-4 lg:grid-cols-2">
        <Section title={op === "encrypt" ? "Input" : "Ciphertext"} bodyClassName="flex flex-col gap-4">
          {op === "encrypt" ? (
            <>
              <Field
                label="KMS key"
                htmlFor="crypto-key"
                help={lockKey ? undefined : "Only enabled keys can encrypt."}
                error={selectedKey && selectedKey.state !== "Enabled" ? `This key is ${selectedKey.state === "PendingDeletion" ? "pending deletion" : "disabled"} and cannot encrypt.` : undefined}
              >
                {keys.error ? (
                  <ErrorState error={keys.error} onRetry={() => keys.mutate()} />
                ) : (
                  <KeyPicker id="crypto-key" keys={keys.data} value={key} onChange={setKey} onlyEnabled symmetricOnly disabled={lockKey} />
                )}
              </Field>
              <Field
                label="Plaintext"
                htmlFor="crypto-plaintext"
                error={tooBig ? `Plaintext is limited to ${MAX_PLAINTEXT} bytes; encrypt a data key for larger payloads.` : undefined}
                help={`${bytes} / ${MAX_PLAINTEXT} bytes (UTF-8)`}
              >
                <Textarea
                  id="crypto-plaintext"
                  value={plaintext}
                  onChange={(e) => setPlaintext(e.target.value)}
                  placeholder="Text to encrypt"
                  rows={6}
                  spellCheck={false}
                  className="font-mono text-[13px]"
                  aria-invalid={tooBig}
                />
              </Field>
            </>
          ) : (
            <Field label="Ciphertext blob" htmlFor="crypto-blob" help="Base64 ciphertext returned by Encrypt. The key is identified from the blob itself.">
              <Textarea
                id="crypto-blob"
                value={blob}
                onChange={(e) => setBlob(e.target.value)}
                placeholder="AST..."
                rows={6}
                spellCheck={false}
                className="font-mono text-[13px] break-all"
              />
            </Field>
          )}
          <Field label="Encryption context" optional help="Key/value pairs bound to the ciphertext. Decrypt must supply exactly the same pairs.">
            <TagsEditor rows={context} onChange={setContext} keyPlaceholder="Context key" valuePlaceholder="Context value" addLabel="Add context pair" />
          </Field>
          <div>
            {op === "encrypt" ? (
              <Button onClick={encrypt} disabled={!canEncrypt}>
                {pending ? <Loader2 className="animate-spin" /> : <Lock />} Encrypt
              </Button>
            ) : (
              <Button onClick={decrypt} disabled={!canDecrypt}>
                {pending ? <Loader2 className="animate-spin" /> : <LockOpen />} Decrypt
              </Button>
            )}
          </div>
        </Section>

        <Section title="Output" bodyClassName="flex flex-col gap-3">
          {error && (
            <Alert variant="destructive">
              <AlertCircle />
              <AlertTitle>{op === "encrypt" ? "Encryption failed" : "Decryption failed"}</AlertTitle>
              <AlertDescription className="break-words">{error}</AlertDescription>
            </Alert>
          )}
          {op === "encrypt" && encrypted ? (
            <>
              <div className="flex flex-wrap items-center justify-between gap-2">
                <span className="text-muted-foreground text-xs font-medium">Ciphertext blob (base64)</span>
                <div className="flex flex-wrap gap-2">
                  <CopyButton value={encrypted.ciphertext_blob} size="sm" toastMessage="Ciphertext copied" />
                  <Button variant="outline" size="sm" onClick={useForDecrypt}>
                    Decrypt it <ArrowRight />
                  </Button>
                </div>
              </div>
              <Textarea readOnly value={encrypted.ciphertext_blob} rows={6} className="bg-muted/40 font-mono text-[12.5px] break-all" onFocus={(e) => e.target.select()} />
              <p className="text-muted-foreground text-xs">
                Encrypted with <KeyRef arn={encrypted.key_id} keys={keys.data} />.
              </p>
            </>
          ) : op === "decrypt" && decrypted ? (
            <>
              <div className="flex flex-wrap items-center justify-between gap-2">
                <span className="text-muted-foreground text-xs font-medium">{decryptedText !== null ? "Plaintext" : "Plaintext (binary, base64)"}</span>
                <CopyButton value={decryptedText ?? decrypted.plaintext} size="sm" toastMessage="Plaintext copied" />
              </div>
              <pre className="bg-muted/40 max-h-72 overflow-auto rounded-md border p-3 font-mono text-[13px] break-all whitespace-pre-wrap">{decryptedText ?? decrypted.plaintext}</pre>
              <p className="text-muted-foreground text-xs">
                Decrypted with <KeyRef arn={decrypted.key_id} keys={keys.data} />.
              </p>
            </>
          ) : (
            !error && (
              <p className="text-muted-foreground text-sm">
                {op === "encrypt" ? "The base64 ciphertext appears here." : "The decrypted plaintext and the key that was used appear here."}
              </p>
            )
          )}
        </Section>
      </div>
    </div>
  )
}

export function CryptoPage() {
  const keyId = useQueryParam("key")
  return (
    <div className="flex flex-col gap-4">
      <PageHeader
        title="Encrypt / decrypt"
        description="Try KMS encryption from the console. Payloads are limited to 4 KB; nothing you enter here is stored."
        breadcrumbs={[{ label: "KMS", href: "/kms/" }, { label: "Encrypt / decrypt" }]}
      />
      <CryptoTool keyId={keyId || undefined} />
    </div>
  )
}
