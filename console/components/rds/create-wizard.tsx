"use client"

import { useEffect, useMemo, useState } from "react"
import Link from "next/link"
import { useRouter } from "next/navigation"
import { Check, Eye, EyeOff, Info, KeyRound, Loader2, Rocket } from "lucide-react"
import { toast } from "sonner"

import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select"
import { Skeleton } from "@/components/ui/skeleton"
import { Switch } from "@/components/ui/switch"
import { ErrorState } from "@/components/console/error-state"
import { Field } from "@/components/console/form-field"
import { PageHeader } from "@/components/console/page-header"
import { Section } from "@/components/console/section"
import { TagsEditor, rowsToTags, type TagRow } from "@/components/console/tags-editor"
import { api, errorMessage } from "@/lib/api"
import { formatMemoryMB, formatNumber } from "@/lib/format"
import { revalidate, useApi, useQueryParam } from "@/lib/hooks"
import type { CreateDbInput, DbInstance, Subnet } from "@/lib/types"
import { cn } from "@/lib/utils"
import { EngineLogo, FAMILIES, RDS_PATH, dbHref, formatVcpu, idError, inFamily, passwordError, supportsSnapshots, useEngines, type Family } from "./shared"

const AUTO_SUBNET = "auto"
const USER_RE = /^[a-zA-Z][a-zA-Z0-9_]{0,31}$/
const DBNAME_RE = /^[a-zA-Z_][a-zA-Z0-9_-]{0,62}$/

const ENGINE_BLURB: Record<string, string> = {
  postgres: "Open-source object-relational database with rich SQL, JSON and extensions.",
  mysql: "The world's most popular open-source relational database.",
  mariadb: "Community-developed, MySQL-compatible relational database.",
  mongodb: "Document database, compatible with Amazon DocumentDB clients and drivers.",
  redis: "In-memory key-value store with persistence (AOF), password auth and backups.",
  valkey: "Open-source, Redis-compatible in-memory store from the Linux Foundation.",
  memcached: "Simple, multi-threaded in-memory cache. No persistence, auth or backups.",
}

const defaultUser = (engine: string) => (engine === "postgres" ? "postgres" : "admin")
const defaultDb = (engine: string) => (engine === "postgres" ? "postgres" : engine === "mongodb" ? "admin" : "app")

export function CreateWizard({ family }: { family: Family }) {
  const cfg = FAMILIES[family]
  const router = useRouter()
  const engineParam = useQueryParam("engine")
  const engines = useEngines()
  const subnets = useApi<Subnet[]>("/api/v1/vpc/subnets", { revalidateOnFocus: false })

  const [engineName, setEngineName] = useState("")
  const [version, setVersion] = useState("")
  const [cls, setCls] = useState("")
  const [id, setId] = useState("")
  const [user, setUser] = useState("")
  const [autoPass, setAutoPass] = useState(true)
  const [pass, setPass] = useState("")
  const [pass2, setPass2] = useState("")
  const [showPass, setShowPass] = useState(false)
  const [dbName, setDbName] = useState("")
  const [subnetId, setSubnetId] = useState(AUTO_SUBNET)
  const [pub, setPub] = useState(false)
  const [port, setPort] = useState("")
  const [retention, setRetention] = useState("1")
  const [protect, setProtect] = useState(false)
  const [tagRows, setTagRows] = useState<TagRow[]>([])
  const [submitted, setSubmitted] = useState(false)
  const [pending, setPending] = useState(false)

  const familyEngines = useMemo(() => (engines.data?.engines ?? []).filter((e) => inFamily(cfg, e.kind)), [engines.data, cfg])
  const classes = useMemo(() => (engines.data?.classes ?? []).filter((c) => c.kind === cfg.classKind), [engines.data, cfg])
  const engine = familyEngines.find((e) => e.name === engineName)
  const cl = classes.find((c) => c.name === cls)
  const subnet = subnets.data?.find((s) => s.id === subnetId)
  const backups = !!engine && supportsSnapshots(engine.name)

  // Defaults once the catalog loads: ?engine=, else the first engine of the family.
  useEffect(() => {
    if (engineName || !familyEngines.length) return
    setEngineName(familyEngines.find((e) => e.name === engineParam)?.name ?? familyEngines[0].name)
    setCls(classes[0]?.name ?? "")
  }, [familyEngines, classes, engineParam, engineName])

  // Engine-dependent defaults.
  const en = engine?.name
  const latest = engine?.versions[0]
  useEffect(() => {
    if (!en) return
    setVersion(latest ?? "")
    setUser(defaultUser(en))
    setDbName("")
    if (!supportsSnapshots(en)) setRetention("0")
    else setRetention((r) => (r === "0" ? "1" : r))
  }, [en, latest])

  // ---- validation ----
  const errors = useMemo(() => {
    const e: Record<string, string> = {}
    const idErr = idError(id, cfg.idLabel)
    if (idErr) e.id = idErr
    if (!engine) e.engine = "Choose an engine"
    if (!cls) e.cls = "Choose a class"
    if (engine?.has_users) {
      if (!USER_RE.test(user)) e.user = "1-32 letters, digits or underscores, starting with a letter"
      else if (user === "root" && engine.kind === "relational") e.user = "root is reserved; choose another name"
    }
    if (engine?.has_password && !autoPass) {
      const pe = passwordError(pass)
      if (pe) e.pass = pe
      else if (pass !== pass2) e.pass2 = "Passwords do not match"
    }
    if (engine?.has_database && dbName && !DBNAME_RE.test(dbName)) e.dbName = "Letters, digits, underscores and hyphens, starting with a letter or underscore"
    if (pub && port) {
      const p = Number(port)
      if (!Number.isInteger(p) || p < 1024 || p > 65535) e.port = "A port from 1024 to 65535, or empty for any free port"
    }
    const r = Number(retention)
    if (backups && (!Number.isInteger(r) || r < 0 || r > 35)) e.retention = "0-35 days"
    const keys = tagRows.map((t) => t.key.trim()).filter(Boolean)
    if (new Set(keys).size !== keys.length) e.tags = "Tag keys must be unique"
    return e
  }, [id, cfg.idLabel, engine, cls, user, autoPass, pass, pass2, dbName, pub, port, retention, backups, tagRows])
  const err = (k: string) => (submitted ? errors[k] : undefined)
  const valid = Object.keys(errors).length === 0

  const create = async () => {
    setSubmitted(true)
    if (!valid || !engine) {
      toast.error("Fix the highlighted fields first")
      return
    }
    const body: CreateDbInput = {
      id,
      engine: engine.name,
      engine_version: version || undefined,
      class: cls,
      master_username: engine.has_users ? user : undefined,
      master_password: engine.has_password && !autoPass ? pass : undefined,
      db_name: engine.has_database && dbName ? dbName : undefined,
      subnet_id: subnetId === AUTO_SUBNET ? undefined : subnetId,
      publicly_accessible: pub,
      port: pub && port ? Number(port) : undefined,
      backup_retention_days: backups ? Number(retention) : 0,
      deletion_protection: protect,
      tags: rowsToTags(tagRows),
    }
    setPending(true)
    try {
      const inst = await api.post<DbInstance>(`${RDS_PATH}/instances`, body)
      toast.success(`Creating ${cfg.noun} ${inst.id}`, {
        description: inst.secret_name ? `Credentials are stored in the secret ${inst.secret_name}.` : "This takes a minute or two.",
      })
      await revalidate(RDS_PATH)
      router.push(dbHref(inst.kind, inst.id))
    } catch (e) {
      toast.error(errorMessage(e))
      setPending(false)
    }
  }

  const title = `Create ${cfg.noun}`

  return (
    <div className="flex flex-col gap-4">
      <PageHeader
        title={title}
        description={
          family === "rds"
            ? "Each database runs as a container with CPU and memory limits, a private endpoint in your VPC, a persistent data volume and master credentials in Secrets Manager."
            : "Each cluster runs as a container with CPU and memory limits and a private endpoint in your VPC. Redis and Valkey get an auth token in Secrets Manager and persistence."
        }
        breadcrumbs={[{ label: cfg.service, href: cfg.base }, { label: cfg.Nouns, href: cfg.base }, { label: title }]}
      />

      <form
        onSubmit={(e) => {
          e.preventDefault()
          create()
        }}
        className="grid grid-cols-1 items-start gap-4 xl:grid-cols-[minmax(0,1fr)_320px]"
      >
        <div className="flex min-w-0 flex-col gap-4">
          {/* ---- Engine ---- */}
          <Section title="Engine options">
            {engines.error ? (
              <ErrorState error={engines.error} onRetry={() => engines.mutate()} />
            ) : !engines.data ? (
              <div className="grid grid-cols-1 gap-3 sm:grid-cols-2 2xl:grid-cols-4">
                {Array.from({ length: 4 }, (_, i) => (
                  <Skeleton key={i} className="h-28 rounded-lg" />
                ))}
              </div>
            ) : (
              <div className="flex flex-col gap-4">
                <div className="grid grid-cols-1 gap-3 sm:grid-cols-2 2xl:grid-cols-4" role="radiogroup" aria-label="Engine">
                  {familyEngines.map((e) => {
                    const active = e.name === engineName
                    return (
                      <button
                        key={e.name}
                        type="button"
                        role="radio"
                        aria-checked={active}
                        onClick={() => setEngineName(e.name)}
                        className={cn(
                          "relative flex flex-col gap-2 rounded-lg border p-3 text-left transition-colors",
                          active ? "border-primary bg-primary/5 ring-primary ring-1 dark:bg-primary/10" : "hover:bg-muted/50",
                        )}
                      >
                        {active && (
                          <span className="bg-primary text-primary-foreground absolute top-2 right-2 flex size-5 items-center justify-center rounded-full">
                            <Check className="size-3.5" />
                          </span>
                        )}
                        <span className="flex items-center gap-2.5 pr-6">
                          <EngineLogo engine={e.name} size="lg" />
                          <span className="flex min-w-0 flex-col">
                            <span className="text-sm font-medium">{e.label.replace(" (DocumentDB compatible)", "")}</span>
                            <span className="text-muted-foreground text-xs">
                              {e.kind === "document" ? "DocumentDB compatible" : `Versions ${e.versions.join(", ")}`}
                            </span>
                          </span>
                        </span>
                        <span className="text-muted-foreground line-clamp-2 text-xs">{ENGINE_BLURB[e.name]}</span>
                      </button>
                    )
                  })}
                </div>
                {engine && (
                  <Field label="Engine version" htmlFor="db-version" className="max-w-xs">
                    <Select value={version} onValueChange={(v) => v && setVersion(v)}>
                      <SelectTrigger id="db-version" className="w-full">
                        <SelectValue />
                      </SelectTrigger>
                      <SelectContent>
                        {engine.versions.map((v, i) => (
                          <SelectItem key={v} value={v}>
                            {engine.label.replace(" (DocumentDB compatible)", "")} {v}
                            {i === 0 && <span className="text-muted-foreground text-xs">(latest)</span>}
                          </SelectItem>
                        ))}
                      </SelectContent>
                    </Select>
                  </Field>
                )}
              </div>
            )}
          </Section>

          {/* ---- Settings ---- */}
          <Section title="Settings">
            <div className="flex flex-col gap-5">
              <Field
                label={cfg.idLabel}
                htmlFor="db-id"
                error={err("id")}
                help={`Unique name for the ${cfg.noun}; also its DNS name inside the VPC (${id || "<id>"}.${family === "rds" ? "rds" : "elasticache"}.internal). Lowercase letters, digits and hyphens, starting with a letter.`}
              >
                <Input
                  id="db-id"
                  value={id}
                  onChange={(e) => setId(e.target.value.toLowerCase())}
                  placeholder={family === "rds" ? "e.g. orders-db" : "e.g. session-cache"}
                  className="max-w-md font-mono"
                  autoComplete="off"
                />
              </Field>

              {engine && (engine.has_users || engine.has_password) ? (
                <div className="flex flex-col gap-4 rounded-md border p-4">
                  <div className="flex items-center gap-2 text-sm font-medium">
                    <KeyRound className="text-muted-foreground size-4" />
                    {engine.has_users ? "Credentials settings" : "Auth token"}
                  </div>
                  {engine.has_users && (
                    <Field label="Master username" htmlFor="db-user" error={err("user")} help="Login ID for the master user.">
                      <Input id="db-user" value={user} onChange={(e) => setUser(e.target.value)} className="max-w-md font-mono" autoComplete="off" />
                    </Field>
                  )}
                  <div className="flex items-start gap-3">
                    <Switch id="db-autopass" checked={autoPass} onCheckedChange={setAutoPass} className="mt-0.5" />
                    <Label htmlFor="db-autopass" className="flex flex-col items-start gap-0.5">
                      <span>Auto-generate a {engine.has_users ? "password" : "token"}</span>
                      <span className="text-muted-foreground text-xs font-normal">
                        A strong 24-character value is generated and stored in the secret <span className="font-mono">rds!{id || "<id>"}</span>. View it
                        later with <em>Show credentials</em>.
                      </span>
                    </Label>
                  </div>
                  {!autoPass && (
                    <div className="grid max-w-2xl grid-cols-1 gap-4 sm:grid-cols-2">
                      <Field
                        label={engine.has_users ? "Master password" : "Auth token"}
                        htmlFor="db-pass"
                        error={err("pass")}
                        help="At least 8 characters. No quotes, slashes, @ or spaces."
                      >
                        <div className="relative">
                          <Input
                            id="db-pass"
                            type={showPass ? "text" : "password"}
                            value={pass}
                            onChange={(e) => setPass(e.target.value)}
                            className="pr-9 font-mono"
                            autoComplete="new-password"
                          />
                          <button
                            type="button"
                            onClick={() => setShowPass(!showPass)}
                            className="text-muted-foreground hover:text-foreground absolute top-1/2 right-2 -translate-y-1/2"
                            aria-label={showPass ? "Hide password" : "Show password"}
                          >
                            {showPass ? <EyeOff className="size-4" /> : <Eye className="size-4" />}
                          </button>
                        </div>
                      </Field>
                      <Field label="Confirm" htmlFor="db-pass2" error={err("pass2")}>
                        <Input
                          id="db-pass2"
                          type={showPass ? "text" : "password"}
                          value={pass2}
                          onChange={(e) => setPass2(e.target.value)}
                          className="font-mono"
                          autoComplete="new-password"
                        />
                      </Field>
                    </div>
                  )}
                </div>
              ) : engine ? (
                <p className="text-muted-foreground flex gap-1.5 text-sm">
                  <Info className="mt-0.5 size-4 shrink-0" />
                  {engine.label} has no authentication: any client inside the VPC (or on this host, when public) can connect.
                </p>
              ) : null}
            </div>
          </Section>

          {/* ---- Class ---- */}
          <Section title={family === "rds" ? "Instance configuration" : "Node type"} description="CPU and memory limits applied to the container. You can change the class later.">
            {!engines.data ? (
              <Skeleton className="h-24 rounded-lg" />
            ) : (
              <div className="grid grid-cols-1 gap-2 sm:grid-cols-2 2xl:grid-cols-4" role="radiogroup" aria-label="Class">
                {classes.map((c) => {
                  const active = c.name === cls
                  return (
                    <button
                      key={c.name}
                      type="button"
                      role="radio"
                      aria-checked={active}
                      onClick={() => setCls(c.name)}
                      className={cn(
                        "flex items-center justify-between gap-2 rounded-lg border px-3 py-2.5 text-left transition-colors",
                        active ? "border-primary bg-primary/5 ring-primary ring-1 dark:bg-primary/10" : "hover:bg-muted/50",
                      )}
                    >
                      <span className="flex flex-col">
                        <span className="font-mono text-[13px] font-medium">{c.name}</span>
                        <span className="text-muted-foreground text-xs">
                          {formatVcpu(c.vcpus)} · {formatMemoryMB(c.memory_mb)}
                        </span>
                      </span>
                      {active && <Check className="text-primary size-4" />}
                    </button>
                  )
                })}
              </div>
            )}
            {err("cls") && <p className="text-destructive mt-2 text-xs">{err("cls")}</p>}
          </Section>

          {/* ---- Connectivity ---- */}
          <Section title="Connectivity">
            <div className="flex flex-col gap-5">
              <Field
                label="Subnet"
                htmlFor="db-subnet"
                help={
                  subnet ? (
                    <>
                      VPC{" "}
                      <Link href="/vpc/" className="text-primary font-mono hover:underline">
                        {subnet.vpc_id}
                      </Link>{" "}
                      · {subnet.cidr} · {subnet.availability_zone} · {formatNumber(subnet.available_ips)} available IPs
                    </>
                  ) : (
                    "HomeCloud places the " + cfg.noun + " in a subnet of the default VPC."
                  )
                }
              >
                {subnets.error ? (
                  <ErrorState error={subnets.error} onRetry={() => subnets.mutate()} />
                ) : (
                  <Select value={subnetId} onValueChange={setSubnetId}>
                    <SelectTrigger id="db-subnet" className="w-full max-w-xl">
                      <SelectValue />
                    </SelectTrigger>
                    <SelectContent>
                      <SelectItem value={AUTO_SUBNET}>No preference (default VPC)</SelectItem>
                      {(subnets.data ?? []).map((s) => (
                        <SelectItem key={s.id} value={s.id} disabled={s.available_ips <= 0}>
                          <span className="font-medium">{s.name || s.id}</span>
                          <span className="text-muted-foreground font-mono text-xs">
                            {s.id} · {s.vpc_id} · {s.cidr} · {s.availability_zone}
                          </span>
                        </SelectItem>
                      ))}
                    </SelectContent>
                  </Select>
                )}
              </Field>

              <div className="flex flex-col gap-3">
                <div className="flex items-start gap-3">
                  <Switch id="db-public" checked={pub} onCheckedChange={setPub} className="mt-0.5" />
                  <Label htmlFor="db-public" className="flex flex-col items-start gap-0.5">
                    <span>Public access</span>
                    <span className="text-muted-foreground text-xs font-normal">
                      Publishes the {cfg.noun} port on this host so clients outside the VPC (your laptop, other machines on the LAN) can connect. Otherwise
                      only resources inside the VPC can reach it.
                    </span>
                  </Label>
                </div>
                {pub && (
                  <Field label="Host port" htmlFor="db-port" optional error={err("port")} help="Leave empty to use any free port. A fixed port must be free on this host." className="max-w-xs pl-12">
                    <Input id="db-port" type="number" min={1024} max={65535} value={port} onChange={(e) => setPort(e.target.value)} placeholder={engine ? String(engine.default_port) : ""} />
                  </Field>
                )}
              </div>
            </div>
          </Section>

          {/* ---- Additional configuration ---- */}
          <Section title="Additional configuration">
            <div className="flex flex-col gap-5">
              {engine?.has_database && (
                <Field
                  label="Initial database name"
                  htmlFor="db-name"
                  optional
                  error={err("dbName")}
                  help={`Created at launch. Leave empty for "${defaultDb(engine.name)}".`}
                >
                  <Input id="db-name" value={dbName} onChange={(e) => setDbName(e.target.value)} placeholder={defaultDb(engine.name)} className="max-w-md font-mono" />
                </Field>
              )}
              {backups ? (
                <Field
                  label="Backup retention period (days)"
                  htmlFor="db-retention"
                  error={err("retention")}
                  help={`A daily automated ${cfg.snap} is taken and kept for this many days. 0 turns automated ${cfg.snaps} off.`}
                >
                  <Input id="db-retention" type="number" min={0} max={35} value={retention} onChange={(e) => setRetention(e.target.value)} className="w-28" />
                </Field>
              ) : engine ? (
                <p className="text-muted-foreground flex gap-1.5 text-sm">
                  <Info className="mt-0.5 size-4 shrink-0" />
                  {engine.label} keeps data only in memory, so {cfg.snaps} are not available.
                </p>
              ) : null}
              <div className="flex items-start gap-3">
                <Switch id="db-protect" checked={protect} onCheckedChange={setProtect} className="mt-0.5" />
                <Label htmlFor="db-protect" className="flex flex-col items-start gap-0.5">
                  <span>Enable deletion protection</span>
                  <span className="text-muted-foreground text-xs font-normal">Protects the {cfg.noun} from being deleted accidentally. Turn it off before deleting.</span>
                </Label>
              </div>
              <Field label="Tags" optional error={err("tags")}>
                <TagsEditor rows={tagRows} onChange={setTagRows} />
              </Field>
            </div>
          </Section>
        </div>

        {/* ---- Summary ---- */}
        <aside className="xl:sticky xl:top-4">
          <Section title="Summary">
            <div className="flex flex-col gap-4">
              <dl className="flex flex-col gap-3 text-sm">
                <SummaryItem label="Engine">
                  {engine ? (
                    <span className="flex items-center gap-2">
                      <EngineLogo engine={engine.name} size="xs" />
                      {engine.label.replace(" (DocumentDB compatible)", "")} {version}
                    </span>
                  ) : (
                    "-"
                  )}
                </SummaryItem>
                <SummaryItem label={cfg.idLabel}>
                  <span className={cn("font-mono text-[13px]", !id && "text-muted-foreground")}>{id || "not set"}</span>
                </SummaryItem>
                <SummaryItem label={family === "rds" ? "Class" : "Node type"}>
                  <span className="font-mono text-[13px]">{cls || "-"}</span>
                  {cl && <span className="text-muted-foreground text-xs"> ({formatVcpu(cl.vcpus)}, {formatMemoryMB(cl.memory_mb)})</span>}
                </SummaryItem>
                {engine && (engine.has_users || engine.has_password) && (
                  <SummaryItem label="Credentials">
                    {engine.has_users && <span className="font-mono text-[13px]">{user || "-"}</span>}
                    {engine.has_users && " · "}
                    {autoPass ? "auto-generated" : "custom"} {engine.has_users ? "password" : "token"}
                  </SummaryItem>
                )}
                {engine?.has_database && (
                  <SummaryItem label="Database">
                    <span className="font-mono text-[13px]">{dbName || defaultDb(engine.name)}</span>
                  </SummaryItem>
                )}
                <SummaryItem label="Network">
                  {subnet ? subnet.name || subnet.id : "Default VPC"} · {pub ? `public${port ? ` on port ${port}` : ""}` : "private"}
                </SummaryItem>
                {backups && (
                  <SummaryItem label="Backups">{Number(retention) > 0 ? `Daily, kept ${retention} day${retention === "1" ? "" : "s"}` : "Automated off"}</SummaryItem>
                )}
                <SummaryItem label="Deletion protection">{protect ? "Enabled" : "Disabled"}</SummaryItem>
              </dl>
              {submitted && !valid && <p className="text-destructive text-xs">Some settings need attention. Check the highlighted fields.</p>}
              <div className="flex flex-col gap-2 border-t pt-4">
                <Button type="submit" disabled={pending || !engine}>
                  {pending ? <Loader2 className="animate-spin" /> : <Rocket />}
                  {title}
                </Button>
                <Button type="button" variant="outline" asChild>
                  <Link href={cfg.base}>Cancel</Link>
                </Button>
              </div>
              <p className="text-muted-foreground text-xs">
                The first create pulls the engine&apos;s Docker image, which can take a few minutes. The {cfg.noun} shows <em>Creating</em> until it accepts
                connections.
              </p>
            </div>
          </Section>
        </aside>
      </form>
    </div>
  )
}

function SummaryItem({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div>
      <dt className="text-muted-foreground text-xs font-medium">{label}</dt>
      <dd className="min-w-0 break-words">{children}</dd>
    </div>
  )
}
