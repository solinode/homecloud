"use client"

import { useEffect, useState } from "react"
import Link from "next/link"
import { AlertCircle, ArrowRight, CheckCircle2, Loader2, Lock, LockOpen, XCircle } from "lucide-react"

import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert"
import { Button } from "@/components/ui/button"
import { Checkbox } from "@/components/ui/checkbox"
import { Label } from "@/components/ui/label"
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select"
import { Tabs, TabsList, TabsTrigger } from "@/components/ui/tabs"
import { Textarea } from "@/components/ui/textarea"
import { CodeBlock } from "@/components/console/code-block"
import { ErrorState } from "@/components/console/error-state"
import { Field } from "@/components/console/form-field"
import { PageHeader } from "@/components/console/page-header"
import { Section } from "@/components/console/section"
import { TagsEditor, rowsToTags, type TagRow } from "@/components/console/tags-editor"
import { api, errorMessage } from "@/lib/api"
import { useApi, useQueryParam } from "@/lib/hooks"
import type { KmsDecryptResult, KmsEncryptResult, KmsKey, KmsPublicKey } from "@/lib/types"

import { KEYS_PATH, KMS_PATH, KeyPicker, keyHref, keyIdFromArn, keyLabel, useKmsKeys } from "./shared"

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
      <Tabs value={op} onValueChange={(v) => switchOp(v as Op)}>
        <TabsList aria-label="Operation">
          <TabsTrigger value="encrypt">
            <Lock /> Encrypt
          </TabsTrigger>
          <TabsTrigger value="decrypt">
            <LockOpen /> Decrypt
          </TabsTrigger>
        </TabsList>
      </Tabs>

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
              <div className="flex justify-end">
                <Button variant="outline" size="sm" onClick={useForDecrypt}>
                  Decrypt it <ArrowRight />
                </Button>
              </div>
              <CodeBlock title="Ciphertext blob (base64)" code={encrypted.ciphertext_blob} wrap maxHeight="18rem" copyLabel="Copy ciphertext" />
              <p className="text-muted-foreground text-xs">
                Encrypted with <KeyRef arn={encrypted.key_id} keys={keys.data} />.
              </p>
            </>
          ) : op === "decrypt" && decrypted ? (
            <>
              <CodeBlock
                title={decryptedText !== null ? "Plaintext" : "Plaintext (binary, base64)"}
                code={decryptedText ?? decrypted.plaintext}
                wrap
                maxHeight="18rem"
                copyLabel="Copy plaintext"
              />
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

type AsymOp = "encrypt" | "decrypt" | "sign" | "verify" | "mac" | "verify-mac"

const OP_LABEL: Record<AsymOp, string> = { encrypt: "Encrypt", decrypt: "Decrypt", sign: "Sign", verify: "Verify", mac: "Generate MAC", "verify-mac": "Verify MAC" }

/** hmacAlgorithm: HMAC keys support exactly one MAC algorithm, matching the key spec. */
const hmacAlgorithm = (spec: string) => `HMAC_SHA_${spec.replace("HMAC_", "")}`

/**
 * AsymmetricTool runs the operations of an asymmetric (RSA / ECC) or HMAC
 * key: encrypt / decrypt, sign / verify or generate / verify MAC, and shows
 * the public key.
 */
export function AsymmetricTool({ k }: { k: KmsKey }) {
  const hmac = k.key_spec.startsWith("HMAC_")
  const signing = k.key_usage === "SIGN_VERIFY"
  const ops: AsymOp[] = hmac ? ["mac", "verify-mac"] : signing ? ["sign", "verify"] : ["encrypt", "decrypt"]
  const pub = useApi<KmsPublicKey>(hmac ? null : `${KEYS_PATH}/${encodeURIComponent(k.id)}/public-key`)
  const algorithms = hmac ? [hmacAlgorithm(k.key_spec)] : ((signing ? pub.data?.signing_algorithms : pub.data?.encryption_algorithms) ?? [])
  const [op, setOp] = useState<AsymOp>(ops[0])
  const [alg, setAlg] = useState("")
  const [message, setMessage] = useState("")
  const [digest, setDigest] = useState(false)
  const [proof, setProof] = useState("") // signature, MAC or ciphertext (base64)
  const [pending, setPending] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [result, setResult] = useState<{ label: string; value: string; valid?: boolean; text?: string | null } | null>(null)

  useEffect(() => {
    if (!alg && algorithms.length) setAlg(algorithms[0])
  }, [alg, algorithms])

  const needsMessage = op !== "decrypt"
  const needsProof = op === "verify" || op === "verify-mac" || op === "decrypt"
  const proofLabel = op === "decrypt" ? "Ciphertext blob (base64)" : op === "verify" ? "Signature (base64)" : "MAC (base64)"
  const msgB64 = digest ? message.trim() : utf8ToBase64(message)

  const run = async () => {
    setPending(true)
    setError(null)
    setResult(null)
    try {
      const p = proof.replace(/\s+/g, "")
      const mt = digest ? "DIGEST" : "RAW"
      if (op === "encrypt") {
        const r = await api.post<KmsEncryptResult>(`${KMS_PATH}/encrypt`, { key_id: k.id, plaintext: msgB64, encryption_algorithm: alg })
        setResult({ label: "Ciphertext blob (base64)", value: r.ciphertext_blob })
      } else if (op === "decrypt") {
        const r = await api.post<KmsDecryptResult>(`${KMS_PATH}/decrypt`, { key_id: k.id, ciphertext_blob: p, encryption_algorithm: alg })
        setResult({ label: "Plaintext", value: r.plaintext, text: base64ToUtf8(r.plaintext) })
      } else if (op === "sign") {
        const r = await api.post<{ signature: string }>(`${KMS_PATH}/sign`, { key_id: k.id, message: msgB64, message_type: mt, algorithm: alg })
        setResult({ label: "Signature (base64)", value: r.signature })
      } else if (op === "mac") {
        const r = await api.post<{ mac: string }>(`${KMS_PATH}/generate-mac`, { key_id: k.id, message: msgB64, algorithm: alg })
        setResult({ label: "MAC (base64)", value: r.mac })
      } else {
        const body = op === "verify" ? { key_id: k.id, message: msgB64, message_type: mt, algorithm: alg, signature: p } : { key_id: k.id, message: msgB64, algorithm: alg, mac: p }
        const r = await api.post<{ valid: boolean }>(`${KMS_PATH}/${op}`, body)
        setResult({ label: op === "verify" ? "Signature" : "MAC", value: "", valid: r.valid })
      }
    } catch (e) {
      setError(errorMessage(e))
    } finally {
      setPending(false)
    }
  }

  const useOutput = () => {
    if (!result?.value) return
    setProof(result.value)
    setResult(null)
    setOp(op === "encrypt" ? "decrypt" : op === "sign" ? "verify" : "verify-mac")
  }

  return (
    <div className="flex flex-col gap-4">
      <Tabs
        value={op}
        onValueChange={(v) => {
          setOp(v as AsymOp)
          setError(null)
          setResult(null)
        }}
      >
        <TabsList aria-label="Operation">
          {ops.map((o) => (
            <TabsTrigger key={o} value={o}>
              {OP_LABEL[o]}
            </TabsTrigger>
          ))}
        </TabsList>
      </Tabs>
      <div className="grid grid-cols-1 gap-4 lg:grid-cols-2">
        <Section title="Input" bodyClassName="flex flex-col gap-4">
          <Field label="Algorithm" htmlFor="asym-alg">
            <Select value={alg} onValueChange={setAlg} disabled={algorithms.length <= 1}>
              <SelectTrigger id="asym-alg" className="w-full font-mono text-[13px]">
                <SelectValue placeholder={pub.isLoading ? "Loading..." : "Choose an algorithm"} />
              </SelectTrigger>
              <SelectContent>
                {algorithms.map((a) => (
                  <SelectItem key={a} value={a} className="font-mono text-[13px]">
                    {a}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </Field>
          {needsMessage && (
            <Field
              label={op === "encrypt" ? "Plaintext" : digest ? "Message digest (base64)" : "Message"}
              htmlFor="asym-msg"
              help={signing ? undefined : `${new TextEncoder().encode(message).length} bytes (UTF-8)`}
            >
              <Textarea id="asym-msg" value={message} onChange={(e) => setMessage(e.target.value)} rows={5} spellCheck={false} className="font-mono text-[13px]" />
            </Field>
          )}
          {signing && needsMessage && (
            <div className="flex items-center gap-2">
              <Checkbox id="asym-digest" checked={digest} onCheckedChange={(v) => setDigest(v === true)} />
              <Label htmlFor="asym-digest" className="text-sm font-normal">
                The message is a precomputed digest (base64)
              </Label>
            </div>
          )}
          {needsProof && (
            <Field label={proofLabel} htmlFor="asym-proof">
              <Textarea id="asym-proof" value={proof} onChange={(e) => setProof(e.target.value)} rows={4} spellCheck={false} className="font-mono text-[13px] break-all" />
            </Field>
          )}
          <div>
            <Button onClick={run} disabled={pending || !alg || k.state !== "Enabled" || (needsMessage && !message) || (needsProof && !proof.trim())}>
              {pending && <Loader2 className="animate-spin" />} {OP_LABEL[op]}
            </Button>
          </div>
        </Section>
        <Section title="Output" bodyClassName="flex flex-col gap-3">
          {error && (
            <Alert variant="destructive">
              <AlertCircle />
              <AlertTitle>{OP_LABEL[op]} failed</AlertTitle>
              <AlertDescription className="break-words">{error}</AlertDescription>
            </Alert>
          )}
          {result?.valid !== undefined ? (
            <Alert variant={result.valid ? "success" : "destructive"}>
              {result.valid ? <CheckCircle2 /> : <XCircle />}
              <AlertTitle>{result.valid ? `${result.label} is valid` : `${result.label} is not valid`}</AlertTitle>
              <AlertDescription>{result.valid ? "The message matches." : "The message was changed, or a different key or algorithm was used."}</AlertDescription>
            </Alert>
          ) : result ? (
            <>
              {op !== "decrypt" && (
                <div className="flex justify-end">
                  <Button variant="outline" size="sm" onClick={useOutput}>
                    {op === "encrypt" ? "Decrypt it" : "Verify it"} <ArrowRight />
                  </Button>
                </div>
              )}
              <CodeBlock
                title={result.text === null ? `${result.label} (binary, base64)` : result.label}
                code={result.text ?? result.value}
                wrap
                maxHeight="18rem"
              />
            </>
          ) : (
            !error && <p className="text-muted-foreground text-sm">The result appears here.</p>
          )}
        </Section>
      </div>
      {!hmac && (
        <Section title="Public key" description="Share the public key to let others encrypt data for this key or verify its signatures without calling KMS.">
          {pub.error ? (
            <ErrorState error={pub.error} onRetry={() => pub.mutate()} />
          ) : pub.data ? (
            <CodeBlock title="Public key (PEM)" code={pub.data.pem} wrap copyLabel="Copy public key" />
          ) : (
            <p className="text-muted-foreground text-sm">Loading public key...</p>
          )}
        </Section>
      )}
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
