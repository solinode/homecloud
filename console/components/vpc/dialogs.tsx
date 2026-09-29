"use client"

import { useEffect, useMemo, useState } from "react"
import { Info, Loader2 } from "lucide-react"
import { toast } from "sonner"

import { Alert, AlertDescription } from "@/components/ui/alert"
import { Button } from "@/components/ui/button"
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog"
import { Input } from "@/components/ui/input"
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select"
import { Switch } from "@/components/ui/switch"
import { Textarea } from "@/components/ui/textarea"
import { Field } from "@/components/console/form-field"
import { api, errorMessage } from "@/lib/api"
import { revalidate } from "@/lib/hooks"
import type { SecurityGroup, Subnet, Vpc } from "@/lib/types"
import { formatNumber } from "@/lib/format"

import {
  cidrSize,
  newRuleDraft,
  parseCidr,
  ruleBody,
  suggestSubnetCidr,
  validateRule,
  validateSubnetCidr,
  validateVpcCidr,
  VpcSelect,
  type RuleDraft,
} from "./common"
import { RuleFields, RulesEditor } from "./rules-editor"

const AZS = ["local-1a", "local-1b", "local-1c"]

/** suggestVpcCidr proposes a 10.x.0.0/16 that does not collide with existing VPCs. */
function suggestVpcCidr(vpcs: Vpc[] | undefined): string {
  const used = (vpcs ?? []).map((v) => parseCidr(v.cidr)).filter(Boolean)
  for (let i = 100; i < 250; i++) {
    const c = parseCidr(`10.${i}.0.0/16`)!
    if (!used.some((u) => u && (u.base & 0xffff0000) >>> 0 === c.base)) return c.text
  }
  return "10.100.0.0/16"
}

export function CreateVpcDialog({
  open,
  onOpenChange,
  vpcs,
  onCreated,
}: {
  open: boolean
  onOpenChange: (o: boolean) => void
  vpcs?: Vpc[]
  onCreated?: (v: Vpc) => void
}) {
  const [name, setName] = useState("")
  const [cidr, setCidr] = useState("")
  const [internet, setInternet] = useState(true)
  const [touched, setTouched] = useState(false)
  const [pending, setPending] = useState(false)

  useEffect(() => {
    if (open) {
      setName("")
      setCidr(suggestVpcCidr(vpcs))
      setInternet(true)
      setTouched(false)
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open])

  const cidrErr = validateVpcCidr(cidr)
  const parsed = parseCidr(cidr)

  const submit = async (e: React.FormEvent) => {
    e.preventDefault()
    setTouched(true)
    if (cidrErr) return
    setPending(true)
    try {
      const v = await api.post<Vpc>("/api/v1/vpc/vpcs", { name: name.trim(), cidr: parsed?.text ?? cidr, internet_access: internet })
      toast.success(`VPC ${v.name || v.id} created`)
      revalidate("/api/v1/vpc")
      onOpenChange(false)
      onCreated?.(v)
    } catch (err) {
      toast.error(errorMessage(err))
    } finally {
      setPending(false)
    }
  }

  return (
    <Dialog open={open} onOpenChange={(o) => !pending && onOpenChange(o)}>
      <DialogContent className="max-h-[90vh] overflow-y-auto sm:max-w-lg">
        <form onSubmit={submit} className="flex flex-col gap-5">
          <DialogHeader>
            <DialogTitle>Create VPC</DialogTitle>
            <DialogDescription>A VPC is an isolated Docker bridge network. Add subnets to it to launch resources.</DialogDescription>
          </DialogHeader>
          <Field label="Name tag" htmlFor="vpc-name" optional>
            <Input id="vpc-name" autoFocus value={name} onChange={(e) => setName(e.target.value)} placeholder="my-vpc" maxLength={128} />
          </Field>
          <Field
            label="IPv4 CIDR block"
            htmlFor="vpc-cidr"
            error={touched || cidr ? cidrErr : undefined}
            help={parsed && !cidrErr ? `${formatNumber(cidrSize(parsed), 0)} addresses (${parsed.text}). Between /16 and /28.` : "Between /16 and /28, e.g. 10.100.0.0/16"}
          >
            <Input
              id="vpc-cidr"
              className="font-mono"
              value={cidr}
              onChange={(e) => setCidr(e.target.value.trim())}
              placeholder="10.100.0.0/16"
              aria-invalid={!!(touched && cidrErr)}
              spellCheck={false}
            />
          </Field>
          <div className="flex items-start justify-between gap-4 rounded-md border p-3">
            <div className="flex flex-col gap-0.5">
              <label htmlFor="vpc-internet" className="text-sm font-medium">
                Internet access
              </label>
              <p className="text-muted-foreground text-xs">
                {internet
                  ? "Resources can reach the internet (outbound traffic is allowed)."
                  : "Isolated network: resources can only talk to each other, with no outbound internet access."}
              </p>
            </div>
            <Switch id="vpc-internet" checked={internet} onCheckedChange={setInternet} />
          </div>
          <DialogFooter>
            <Button type="button" variant="outline" onClick={() => onOpenChange(false)} disabled={pending}>
              Cancel
            </Button>
            <Button type="submit" disabled={pending}>
              {pending && <Loader2 className="animate-spin" />}
              Create VPC
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}

export function CreateSubnetDialog({
  open,
  onOpenChange,
  vpcs,
  defaultVpcId,
}: {
  open: boolean
  onOpenChange: (o: boolean) => void
  vpcs?: Vpc[]
  defaultVpcId?: string
}) {
  const [vpcId, setVpcId] = useState("")
  const [name, setName] = useState("")
  const [cidr, setCidr] = useState("")
  const [az, setAz] = useState("local-1a")
  const [touched, setTouched] = useState(false)
  const [pending, setPending] = useState(false)

  const vpc = vpcs?.find((v) => v.id === vpcId)

  useEffect(() => {
    if (open) {
      const initial = defaultVpcId || vpcs?.find((v) => !v.default)?.id || vpcs?.[0]?.id || ""
      setVpcId(initial)
      setName("")
      setCidr(suggestSubnetCidr(vpcs?.find((v) => v.id === initial)))
      setAz("local-1a")
      setTouched(false)
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open])

  const cidrErr = validateSubnetCidr(cidr, vpc)
  const parsed = parseCidr(cidr)

  const submit = async (e: React.FormEvent) => {
    e.preventDefault()
    setTouched(true)
    if (!vpcId || cidrErr) return
    setPending(true)
    try {
      const sn = await api.post<Subnet>("/api/v1/vpc/subnets", { vpc_id: vpcId, name: name.trim(), cidr: parsed?.text ?? cidr, availability_zone: az })
      toast.success(`Subnet ${sn.name || sn.id} created`)
      revalidate("/api/v1/vpc")
      onOpenChange(false)
    } catch (err) {
      toast.error(errorMessage(err))
    } finally {
      setPending(false)
    }
  }

  return (
    <Dialog open={open} onOpenChange={(o) => !pending && onOpenChange(o)}>
      <DialogContent className="max-h-[90vh] overflow-y-auto sm:max-w-lg">
        <form onSubmit={submit} className="flex flex-col gap-5">
          <DialogHeader>
            <DialogTitle>Create subnet</DialogTitle>
            <DialogDescription>A subnet is a range of IP addresses in your VPC. Instances get a static private IP from it.</DialogDescription>
          </DialogHeader>
          <Field label="VPC" htmlFor="subnet-vpc" error={touched && !vpcId ? "Select a VPC" : undefined}>
            <VpcSelect
              id="subnet-vpc"
              value={vpcId}
              vpcs={vpcs}
              onChange={(v) => {
                setVpcId(v)
                setCidr(suggestSubnetCidr(vpcs?.find((x) => x.id === v)))
              }}
            />
          </Field>
          <Field label="Subnet name" htmlFor="subnet-name" optional>
            <Input id="subnet-name" value={name} onChange={(e) => setName(e.target.value)} placeholder="private-a" maxLength={128} />
          </Field>
          <div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
            <Field
              label="IPv4 CIDR block"
              htmlFor="subnet-cidr"
              error={touched || cidr ? cidrErr : undefined}
              help={
                parsed && !cidrErr
                  ? `${formatNumber(Math.max(0, cidrSize(parsed) - 5), 0)} usable addresses`
                  : vpc
                    ? `Inside ${vpc.cidr}, /28 or larger`
                    : undefined
              }
            >
              <Input
                id="subnet-cidr"
                className="font-mono"
                value={cidr}
                spellCheck={false}
                onChange={(e) => setCidr(e.target.value.trim())}
                placeholder="10.100.1.0/24"
                aria-invalid={!!(touched && cidrErr)}
              />
            </Field>
            <Field label="Availability Zone" htmlFor="subnet-az">
              <Select value={az} onValueChange={setAz}>
                <SelectTrigger id="subnet-az" className="w-full">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {AZS.map((z) => (
                    <SelectItem key={z} value={z}>
                      {z}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </Field>
          </div>
          <p className="text-muted-foreground text-xs">The first four addresses and the last address of every subnet are reserved.</p>
          <DialogFooter>
            <Button type="button" variant="outline" onClick={() => onOpenChange(false)} disabled={pending}>
              Cancel
            </Button>
            <Button type="submit" disabled={pending}>
              {pending && <Loader2 className="animate-spin" />}
              Create subnet
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}

export function CreateSecurityGroupDialog({
  open,
  onOpenChange,
  vpcs,
  groups,
  onCreated,
}: {
  open: boolean
  onOpenChange: (o: boolean) => void
  vpcs?: Vpc[]
  groups?: SecurityGroup[]
  onCreated?: (g: SecurityGroup) => void
}) {
  const [vpcId, setVpcId] = useState("")
  const [name, setName] = useState("")
  const [description, setDescription] = useState("")
  const [rules, setRules] = useState<RuleDraft[]>([])
  const [touched, setTouched] = useState(false)
  const [pending, setPending] = useState(false)

  useEffect(() => {
    if (open) {
      setVpcId(vpcs?.find((v) => v.default)?.id ?? vpcs?.[0]?.id ?? "")
      setName("")
      setDescription("")
      setRules([])
      setTouched(false)
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open])

  const nameErr = useMemo(() => {
    const n = name.trim()
    if (!n) return "Name is required"
    if (n.length > 255) return "At most 255 characters"
    if (n.toLowerCase().startsWith("sg-")) return "Names cannot start with sg-"
    if (groups?.some((g) => g.vpc_id === vpcId && g.name === n)) return "A group with this name already exists in this VPC"
    return null
  }, [name, groups, vpcId])
  const rulesValid = rules.every((r) => Object.keys(validateRule(r)).length === 0)

  const submit = async (e: React.FormEvent) => {
    e.preventDefault()
    setTouched(true)
    if (nameErr || !vpcId || !rulesValid) return
    setPending(true)
    try {
      const g = await api.post<SecurityGroup>("/api/v1/vpc/security-groups", {
        vpc_id: vpcId,
        name: name.trim(),
        description: description.trim(),
        ingress: rules.map(ruleBody),
      })
      toast.success(`Security group ${g.name} created`)
      revalidate("/api/v1/vpc")
      onOpenChange(false)
      onCreated?.(g)
    } catch (err) {
      toast.error(errorMessage(err))
    } finally {
      setPending(false)
    }
  }

  return (
    <Dialog open={open} onOpenChange={(o) => !pending && onOpenChange(o)}>
      <DialogContent className="max-h-[90vh] overflow-y-auto sm:max-w-3xl">
        <form onSubmit={submit} className="flex flex-col gap-5">
          <DialogHeader>
            <DialogTitle>Create security group</DialogTitle>
            <DialogDescription>A security group decides which ports of an instance are published on the host.</DialogDescription>
          </DialogHeader>
          <div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
            <Field label="Name" htmlFor="sg-name" error={touched ? nameErr : undefined}>
              <Input id="sg-name" autoFocus value={name} onChange={(e) => setName(e.target.value)} placeholder="web-servers" aria-invalid={!!(touched && nameErr)} />
            </Field>
            <Field label="VPC" htmlFor="sg-vpc" error={touched && !vpcId ? "Select a VPC" : undefined}>
              <VpcSelect id="sg-vpc" value={vpcId} vpcs={vpcs} onChange={setVpcId} />
            </Field>
          </div>
          <Field label="Description" htmlFor="sg-desc" optional>
            <Textarea id="sg-desc" rows={2} value={description} onChange={(e) => setDescription(e.target.value)} placeholder="Allows HTTP and HTTPS from anywhere" maxLength={255} />
          </Field>
          <div className="flex flex-col gap-2">
            <h3 className="text-sm font-semibold">Inbound rules</h3>
            <RulesEditor rules={rules} onChange={setRules} showErrors={touched} />
          </div>
          <Alert>
            <Info />
            <AlertDescription>
              Every inbound rule publishes its ports on the host; Docker assigns each a host port, shown on the instance details page. All outbound traffic is allowed.
            </AlertDescription>
          </Alert>
          <DialogFooter>
            <Button type="button" variant="outline" onClick={() => onOpenChange(false)} disabled={pending}>
              Cancel
            </Button>
            <Button type="submit" disabled={pending}>
              {pending && <Loader2 className="animate-spin" />}
              Create security group
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}

export function AddRulesDialog({ open, onOpenChange, group }: { open: boolean; onOpenChange: (o: boolean) => void; group: SecurityGroup }) {
  const [rules, setRules] = useState<RuleDraft[]>([])
  const [touched, setTouched] = useState(false)
  const [pending, setPending] = useState(false)

  useEffect(() => {
    if (open) {
      setRules([newRuleDraft("HTTP")])
      setTouched(false)
    }
  }, [open])

  const valid = rules.length > 0 && rules.every((r) => Object.keys(validateRule(r)).length === 0)

  const submit = async (e: React.FormEvent) => {
    e.preventDefault()
    setTouched(true)
    if (!valid) return
    setPending(true)
    let added = 0
    try {
      for (const r of rules) {
        await api.post<SecurityGroup>(`/api/v1/vpc/security-groups/${encodeURIComponent(group.id)}/ingress`, ruleBody(r))
        added++
      }
      toast.success(added === 1 ? "Inbound rule added" : `${added} inbound rules added`)
      onOpenChange(false)
    } catch (err) {
      toast.error(added ? `${added} rule(s) added, then: ${errorMessage(err)}` : errorMessage(err))
      setRules((rs) => rs.slice(added))
    } finally {
      setPending(false)
      revalidate("/api/v1/vpc")
    }
  }

  return (
    <Dialog open={open} onOpenChange={(o) => !pending && onOpenChange(o)}>
      <DialogContent className="max-h-[90vh] overflow-y-auto sm:max-w-3xl">
        <form onSubmit={submit} className="flex flex-col gap-5">
          <DialogHeader>
            <DialogTitle>Add inbound rules</DialogTitle>
            <DialogDescription>
              {group.name} (<span className="font-mono">{group.id}</span>)
            </DialogDescription>
          </DialogHeader>
          {rules.length === 1 ? (
            <div className="flex flex-col gap-3">
              <RuleFields rule={rules[0]} showErrors={touched} onChange={(r) => setRules([r])} />
              <Button type="button" variant="outline" size="sm" className="self-start" onClick={() => setRules([...rules, newRuleDraft("HTTPS")])}>
                Add another rule
              </Button>
            </div>
          ) : (
            <RulesEditor rules={rules} onChange={setRules} showErrors={touched} />
          )}
          <Alert>
            <Info />
            <AlertDescription>New rules apply to instances launched from now on. Running instances keep their current published ports until relaunched.</AlertDescription>
          </Alert>
          <DialogFooter>
            <Button type="button" variant="outline" onClick={() => onOpenChange(false)} disabled={pending}>
              Cancel
            </Button>
            <Button type="submit" disabled={pending || rules.length === 0}>
              {pending && <Loader2 className="animate-spin" />}
              Save rules
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}
