"use client"

import { useEffect, useState } from "react"
import { useRouter } from "next/navigation"
import { Fingerprint, Info, KeyRound, Loader2, ShieldCheck } from "lucide-react"
import { toast } from "sonner"

import { Alert, AlertDescription } from "@/components/ui/alert"
import { Button } from "@/components/ui/button"
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { Select, SelectContent, SelectGroup, SelectItem, SelectLabel, SelectTrigger, SelectValue } from "@/components/ui/select"
import { Switch } from "@/components/ui/switch"
import { Field } from "@/components/console/form-field"
import { OptionCard, OptionGroup } from "@/components/console/option-card"
import { TagsEditor, rowsToTags, type TagRow } from "@/components/console/tags-editor"
import { api, errorMessage } from "@/lib/api"
import { revalidate } from "@/lib/hooks"
import type { CreateKmsKeyInput, KmsKey } from "@/lib/types"

import { AliasInput, KEYS_PATH, KMS_PATH, KeySpecTag, KeyUsageTag, SYMMETRIC_SPEC, USAGE_LABEL, aliasError, keyHref, keyLabel, normalizeAlias, type KeyKind } from "./shared"

const KINDS: { kind: KeyKind; icon: typeof KeyRound; title: string; blurb: string }[] = [
  { kind: "symmetric", icon: KeyRound, title: "Symmetric", blurb: "One AES-256 key that encrypts and decrypts. Used by Secrets Manager and Parameter Store." },
  { kind: "asymmetric", icon: ShieldCheck, title: "Asymmetric", blurb: "An RSA or elliptic curve key pair for public-key encryption or digital signatures." },
  { kind: "hmac", icon: Fingerprint, title: "HMAC", blurb: "A secret key that generates and verifies hash-based message authentication codes." },
]

const RSA_SPECS = ["RSA_2048", "RSA_3072", "RSA_4096"]
const ECC_SPECS = [
  { spec: "ECC_NIST_P256", hint: "secp256r1" },
  { spec: "ECC_NIST_P384", hint: "secp384r1" },
  { spec: "ECC_NIST_P521", hint: "secp521r1" },
]
const HMAC_SPECS = ["HMAC_224", "HMAC_256", "HMAC_384", "HMAC_512"]

type AsymUsage = "ENCRYPT_DECRYPT" | "SIGN_VERIFY"

const ASYM_USAGES: { usage: AsymUsage; blurb: string }[] = [
  { usage: "ENCRYPT_DECRYPT", blurb: "Encrypt with the public key, decrypt inside KMS. RSA only." },
  { usage: "SIGN_VERIFY", blurb: "Sign inside KMS, verify with the public key. RSA or elliptic curve." },
]

export function CreateKeyDialog({ open, onOpenChange }: { open: boolean; onOpenChange: (o: boolean) => void }) {
  const router = useRouter()
  const [kind, setKind] = useState<KeyKind>("symmetric")
  const [usage, setUsage] = useState<AsymUsage>("ENCRYPT_DECRYPT")
  const [asymSpec, setAsymSpec] = useState("RSA_2048")
  const [hmacSpec, setHmacSpec] = useState("HMAC_256")
  const [alias, setAlias] = useState("")
  const [description, setDescription] = useState("")
  const [rotation, setRotation] = useState(true)
  const [tags, setTags] = useState<TagRow[]>([])
  const [pending, setPending] = useState(false)
  const [touched, setTouched] = useState(false)

  useEffect(() => {
    if (open) {
      setKind("symmetric")
      setUsage("ENCRYPT_DECRYPT")
      setAsymSpec("RSA_2048")
      setHmacSpec("HMAC_256")
      setAlias("")
      setDescription("")
      setRotation(true)
      setTags([])
      setTouched(false)
    }
  }, [open])

  // ECC keys only sign; switching to encryption moves an ECC choice back to RSA.
  const chooseUsage = (u: AsymUsage) => {
    setUsage(u)
    if (u === "ENCRYPT_DECRYPT" && asymSpec.startsWith("ECC_")) setAsymSpec("RSA_2048")
  }

  const fullAlias = normalizeAlias(alias)
  const aliasErr = aliasError(fullAlias)

  const spec = kind === "symmetric" ? SYMMETRIC_SPEC : kind === "hmac" ? hmacSpec : asymSpec
  const keyUsage = kind === "symmetric" ? "ENCRYPT_DECRYPT" : kind === "hmac" ? "GENERATE_VERIFY_MAC" : usage

  const submit = async (e: React.FormEvent) => {
    e.preventDefault()
    setTouched(true)
    if (aliasErr) return
    setPending(true)
    try {
      const body: CreateKmsKeyInput = {
        description: description.trim(),
        alias: fullAlias || undefined,
        key_spec: spec,
        key_usage: keyUsage,
        rotation_enabled: kind === "symmetric" && rotation,
        tags: rowsToTags(tags),
      }
      const k = await api.post<KmsKey>(KEYS_PATH, body)
      toast.success(`Key ${keyLabel(k)} created`)
      revalidate(KMS_PATH)
      onOpenChange(false)
      router.push(keyHref(k.id))
    } catch (err) {
      toast.error(errorMessage(err))
    } finally {
      setPending(false)
    }
  }

  return (
    <Dialog open={open} onOpenChange={(o) => !pending && onOpenChange(o)}>
      <DialogContent className="max-h-[90vh] overflow-y-auto sm:max-w-2xl">
        <form onSubmit={submit} className="flex flex-col gap-5">
          <DialogHeader>
            <DialogTitle>Create key</DialogTitle>
            <DialogDescription>Choose the kind of key. The key spec and usage can&apos;t be changed after the key is created.</DialogDescription>
          </DialogHeader>

          <Field label="Key type">
            <OptionGroup label="Key type" columns={3}>
              {KINDS.map((t) => (
                <OptionCard key={t.kind} selected={kind === t.kind} onSelect={() => setKind(t.kind)} icon={t.icon} title={t.title} description={t.blurb} />
              ))}
            </OptionGroup>
          </Field>

          {kind === "asymmetric" && (
            <div className="flex flex-col gap-4">
              <Field label="Key usage" help="Choose what the key pair is for; it can't be changed later.">
                <OptionGroup label="Key usage" columns={2}>
                  {ASYM_USAGES.map((u) => (
                    <OptionCard key={u.usage} selected={usage === u.usage} onSelect={() => chooseUsage(u.usage)} title={USAGE_LABEL[u.usage]} description={u.blurb} />
                  ))}
                </OptionGroup>
              </Field>
              <Field
                label="Key spec"
                htmlFor="kms-spec"
                help={usage === "ENCRYPT_DECRYPT" ? "Elliptic curve keys can only sign and verify." : asymSpec.startsWith("ECC_") ? "ECDSA signatures." : "RSASSA PKCS #1 v1.5 or PSS signatures."}
              >
                <Select value={asymSpec} onValueChange={setAsymSpec}>
                  <SelectTrigger id="kms-spec" className="w-full font-mono text-[13px] sm:w-64">
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectGroup>
                      <SelectLabel>RSA</SelectLabel>
                      {RSA_SPECS.map((s) => (
                        <SelectItem key={s} value={s} className="font-mono text-[13px]">
                          {s}
                        </SelectItem>
                      ))}
                    </SelectGroup>
                    {usage === "SIGN_VERIFY" && (
                      <SelectGroup>
                        <SelectLabel>Elliptic curve</SelectLabel>
                        {ECC_SPECS.map((s) => (
                          <SelectItem key={s.spec} value={s.spec}>
                            <span className="font-mono text-[13px]">{s.spec}</span>
                            <span className="text-muted-foreground text-xs">{s.hint}</span>
                          </SelectItem>
                        ))}
                      </SelectGroup>
                    )}
                  </SelectContent>
                </Select>
              </Field>
            </div>
          )}

          {kind === "hmac" && (
            <Field label="Key spec" htmlFor="kms-hmac-spec" help="The MAC algorithm is HMAC with the matching SHA-2 digest.">
              <Select value={hmacSpec} onValueChange={setHmacSpec}>
                <SelectTrigger id="kms-hmac-spec" className="w-full font-mono text-[13px] sm:w-64">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {HMAC_SPECS.map((s) => (
                    <SelectItem key={s} value={s} className="font-mono text-[13px]">
                      {s}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </Field>
          )}

          <Field label="Alias" htmlFor="kms-alias" optional error={touched || alias ? aliasErr : undefined} help="A friendly name you can use instead of the key ID.">
            <AliasInput id="kms-alias" value={alias} onChange={setAlias} invalid={!!aliasErr && (touched || !!alias)} />
          </Field>

          <Field label="Description" htmlFor="kms-desc" optional>
            <Input id="kms-desc" value={description} onChange={(e) => setDescription(e.target.value)} placeholder="Encrypts the orders database backups" />
          </Field>

          {kind === "symmetric" ? (
            <div className="flex items-start justify-between gap-4 rounded-md border p-3">
              <div>
                <Label htmlFor="kms-rotation" className="font-medium">
                  Automatic key rotation
                </Label>
                <p className="text-muted-foreground mt-0.5 text-xs">
                  Creates new key material every year (you can change the period later). Older versions are kept, so data encrypted before a rotation still
                  decrypts.
                </p>
              </div>
              <Switch id="kms-rotation" checked={rotation} onCheckedChange={setRotation} />
            </div>
          ) : (
            <Alert variant="info">
              <Info />
              <AlertDescription>
                <p>{kind === "hmac" ? "HMAC" : "Asymmetric"} keys don&apos;t support automatic or on-demand rotation.</p>
                <p className="mt-1.5 flex flex-wrap items-center gap-1.5">
                  <KeySpecTag spec={spec} />
                  <KeyUsageTag usage={keyUsage} />
                </p>
              </AlertDescription>
            </Alert>
          )}

          <Field label="Tags" optional>
            <TagsEditor rows={tags} onChange={setTags} />
          </Field>

          <DialogFooter>
            <Button type="button" variant="outline" onClick={() => onOpenChange(false)} disabled={pending}>
              Cancel
            </Button>
            <Button type="submit" disabled={pending || (touched && !!aliasErr)}>
              {pending && <Loader2 className="animate-spin" />}
              Create key
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}
