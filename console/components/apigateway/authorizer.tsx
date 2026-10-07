"use client"

import { useEffect, useState } from "react"
import Link from "next/link"
import { AlertTriangle, Loader2, Lock, LockOpen, Pencil } from "lucide-react"
import { toast } from "sonner"

import { Alert, AlertDescription } from "@/components/ui/alert"
import { Button } from "@/components/ui/button"
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog"
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select"
import { ConfirmDialog } from "@/components/console/confirm-dialog"
import { CopyableText } from "@/components/console/copy-button"
import { Field } from "@/components/console/form-field"
import { KeyValueGrid } from "@/components/console/key-value"
import { Section } from "@/components/console/section"
import { Tag } from "@/components/console/tag"
import { api, errorMessage, seg } from "@/lib/api"
import { revalidate, useApi } from "@/lib/hooks"
import type { ApiRoute, AppClient, HttpApi, UserPool } from "@/lib/types"

import { APIGW_PATH, apiPath } from "./common"

const POOLS_PATH = "/api/v1/cognito/user-pools"
const poolHref = (id: string, tab?: string) => `/cognito/pool/?id=${encodeURIComponent(id)}${tab ? `&tab=${tab}` : ""}`
const ANY_CLIENT = "__any__"

/** AuthBadge is a route's authorization kind: JWT (locked) or None (public). */
export function AuthBadge({ authorization, className }: { authorization?: string; className?: string }) {
  const jwt = authorization === "JWT"
  const Icon = jwt ? Lock : LockOpen
  return (
    <Tag accent={jwt ? "success" : "neutral"} mono={false} className={className}>
      <Icon /> {jwt ? "JWT" : "None"}
    </Tag>
  )
}

/** AuthorizerSection shows and edits the API's Cognito JWT authorizer. */
export function AuthorizerSection({ api: a }: { api: HttpApi }) {
  const pools = useApi<UserPool[]>(POOLS_PATH)
  const [editing, setEditing] = useState(false)
  const [removing, setRemoving] = useState(false)
  const auth = a.authorizer
  const pool = pools.data?.find((p) => p.id === auth?.user_pool_id)
  const jwtRoutes = (a.routes ?? []).filter((r) => r.authorization === "JWT").length

  return (
    <Section
      title="Authorization"
      description="Routes whose authorization is JWT require an ID or access token issued by the authorizer's Cognito user pool. The function receives the token's claims in requestContext.authorizer.jwt.claims."
      actions={
        <>
          {auth && (
            <Button size="sm" variant="outline" className="text-destructive hover:text-destructive" onClick={() => setRemoving(true)}>
              Remove authorizer
            </Button>
          )}
          <Button size="sm" variant={auth ? "outline" : "default"} onClick={() => setEditing(true)}>
            <Pencil /> {auth ? "Edit authorizer" : "Set authorizer"}
          </Button>
        </>
      }
    >
      <div className="flex flex-col gap-3">
        {auth ? (
          <KeyValueGrid
            columns={3}
            items={[
              { label: "Type", value: "JWT (Cognito user pool)" },
              {
                label: "User pool",
                value: (
                  <span className="inline-flex flex-wrap items-center gap-1.5">
                    <Link href={poolHref(auth.user_pool_id)} className="text-primary hover:underline">
                      {pool?.name ?? auth.user_pool_id}
                    </Link>
                    <span className="text-muted-foreground font-mono text-xs">{auth.user_pool_id}</span>
                    {pools.data && !pool && <span className="text-danger text-xs">(pool not found: JWT routes return 401)</span>}
                  </span>
                ),
              },
              { label: "Audience (app client)", value: auth.audience ? <CopyableText value={auth.audience} /> : "Any app client of the pool" },
              { label: "Issuer", value: pool ? <CopyableText value={pool.issuer} /> : "", wide: true },
            ]}
          />
        ) : (
          <p className="text-muted-foreground text-sm">No authorizer. Every route is public.</p>
        )}
        {!auth && jwtRoutes > 0 && (
          <Alert variant="warning">
            <AlertTriangle />
            <AlertDescription>
              {jwtRoutes} route{jwtRoutes === 1 ? " requires" : "s require"} JWT but the API has no authorizer, so they always return 401.
            </AlertDescription>
          </Alert>
        )}
      </div>
      <AuthorizerDialog api={editing ? a : null} pools={pools.data} onClose={() => setEditing(false)} />
      <ConfirmDialog
        open={removing}
        onOpenChange={setRemoving}
        title="Remove the authorizer?"
        actionLabel="Remove"
        description={
          <p>
            Routes that require JWT keep requiring it, but with no authorizer every request to them returns 401. Switch them to None first to make them public.
          </p>
        }
        onConfirm={async () => {
          await api.patch(apiPath(a.id), { authorizer: { user_pool_id: "" } })
          toast.success("Authorizer removed")
          await revalidate(APIGW_PATH)
        }}
      />
    </Section>
  )
}

function AuthorizerDialog({ api: a, pools, onClose }: { api: HttpApi | null; pools?: UserPool[]; onClose: () => void }) {
  const [poolId, setPoolId] = useState("")
  const [audience, setAudience] = useState(ANY_CLIENT)
  const [pending, setPending] = useState(false)
  const clients = useApi<AppClient[]>(poolId ? `${POOLS_PATH}/${seg(poolId)}/clients` : null)

  useEffect(() => {
    if (!a) return
    setPoolId(a.authorizer?.user_pool_id ?? "")
    setAudience(a.authorizer?.audience || ANY_CLIENT)
  }, [a])

  const submit = async (e: React.FormEvent) => {
    e.preventDefault()
    if (!a || !poolId) return
    setPending(true)
    try {
      await api.patch(apiPath(a.id), { authorizer: { user_pool_id: poolId, audience: audience === ANY_CLIENT ? "" : audience } })
      toast.success("Authorizer saved")
      await revalidate(APIGW_PATH)
      onClose()
    } catch (err) {
      toast.error(errorMessage(err))
    } finally {
      setPending(false)
    }
  }

  return (
    <Dialog open={!!a} onOpenChange={(o) => !o && !pending && onClose()}>
      <DialogContent className="sm:max-w-lg">
        <form onSubmit={submit} className="flex flex-col gap-4">
          <DialogHeader>
            <DialogTitle>{a?.authorizer ? "Edit authorizer" : "Set authorizer"}</DialogTitle>
            <DialogDescription>Tokens are verified with the pool&apos;s signing key; disabled, deleted or signed-out users are rejected.</DialogDescription>
          </DialogHeader>
          <Field
            label="Cognito user pool"
            htmlFor="authz-pool"
            help={
              pools && pools.length === 0 ? (
                <>
                  No user pools yet.{" "}
                  <Link href="/cognito/?create=1" className="text-primary hover:underline">
                    Create a user pool
                  </Link>{" "}
                  first.
                </>
              ) : undefined
            }
          >
            <Select
              value={poolId}
              onValueChange={(v) => {
                setPoolId(v)
                setAudience(ANY_CLIENT)
              }}
              disabled={!pools}
            >
              <SelectTrigger id="authz-pool" className="w-full">
                <SelectValue placeholder={pools ? "Choose a user pool" : "Loading user pools..."} />
              </SelectTrigger>
              <SelectContent>
                {(pools ?? []).map((p) => (
                  <SelectItem key={p.id} value={p.id}>
                    {p.name}
                    <span className="text-muted-foreground font-mono text-xs">{p.id}</span>
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </Field>
          <Field label="Audience (app client)" htmlFor="authz-aud" help="Restrict tokens to one app client, or accept tokens from any client of the pool.">
            <Select value={audience} onValueChange={setAudience} disabled={!poolId}>
              <SelectTrigger id="authz-aud" className="w-full">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value={ANY_CLIENT}>Any app client</SelectItem>
                {audience !== ANY_CLIENT && !(clients.data ?? []).some((c) => c.id === audience) && (
                  <SelectItem value={audience}>
                    <span className="font-mono text-xs">{audience}</span>
                  </SelectItem>
                )}
                {(clients.data ?? []).map((c) => (
                  <SelectItem key={c.id} value={c.id}>
                    {c.name || "(unnamed)"}
                    <span className="text-muted-foreground font-mono text-xs">{c.id}</span>
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </Field>
          <DialogFooter>
            <Button type="button" variant="outline" onClick={onClose} disabled={pending}>
              Cancel
            </Button>
            <Button type="submit" disabled={pending || !poolId}>
              {pending && <Loader2 className="animate-spin" />}
              Save
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}

/**
 * ChangeRouteAuthDialog switches a route between NONE and JWT. The API has no
 * route update call, so the route is deleted and re-created (it gets a new
 * ID); if re-creating fails the original route is restored.
 */
export function ChangeRouteAuthDialog({ api: a, route, onClose }: { api: HttpApi; route: ApiRoute | null; onClose: () => void }) {
  const next = route?.authorization === "JWT" ? "NONE" : "JWT"
  return (
    <ConfirmDialog
      open={!!route}
      onOpenChange={(o) => !o && onClose()}
      destructive={false}
      actionLabel={next === "JWT" ? "Require JWT" : "Make public"}
      title={next === "JWT" ? "Require a JWT on this route?" : "Make this route public?"}
      description={
        route && (
          <div className="flex flex-col gap-2">
            <p>
              <span className="text-foreground font-mono">
                {route.method} {route.path}
              </span>{" "}
              {next === "JWT"
                ? "will only accept requests with a valid Cognito token (Authorization: Bearer <token>)."
                : "will accept requests without a token."}
            </p>
            {next === "JWT" && !a.authorizer && (
              <p className="text-warning">The API has no authorizer yet, so the route will return 401 until you set one.</p>
            )}
          </div>
        )
      }
      onConfirm={async () => {
        if (!route) return
        await api.patch(`${apiPath(a.id)}/routes/${seg(route.id)}`, { authorization: next })
        toast.success(`${route.method} ${route.path} ${next === "JWT" ? "now requires a JWT" : "is now public"}`)
        await revalidate(APIGW_PATH)
      }}
    />
  )
}
