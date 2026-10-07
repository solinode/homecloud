"use client"

import { useEffect, useState } from "react"
import Link from "next/link"
import { AlertTriangle, ExternalLink, Info } from "lucide-react"

import { Alert, AlertDescription } from "@/components/ui/alert"
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select"
import { CodeBlock } from "@/components/console/code-block"
import { CopyableText } from "@/components/console/copy-button"
import { CellLink } from "@/components/console/data-table"
import { Tag } from "@/components/console/tag"
import { KeyValueGrid } from "@/components/console/key-value"
import { Section } from "@/components/console/section"
import { useApi } from "@/lib/hooks"
import type { HttpApi, UserPool } from "@/lib/types"

import { useClients } from "./clients-tab"

function Snippet({ title, code, note }: { title: string; code: string; note?: React.ReactNode }) {
  return (
    <div className="flex flex-col gap-1.5">
      <CodeBlock title={title} code={code} copyLabel={`Copy: ${title}`} wrap />
      {note && <p className="text-muted-foreground text-xs">{note}</p>}
    </div>
  )
}

const json = (o: Record<string, unknown>) => JSON.stringify(o)
const sq = (s: string) => `'${s.replace(/'/g, `'\\''`)}'`

export function IntegrationTab({ pool }: { pool: UserPool }) {
  const clients = useClients(pool.id)
  const apis = useApi<HttpApi[]>("/api/v1/apigateway/apis")
  const [clientId, setClientId] = useState("")
  useEffect(() => {
    if (!clientId && clients.data?.length) setClientId(clients.data[0].id)
  }, [clients.data, clientId])
  const client = clients.data?.find((c) => c.id === clientId)

  const base = pool.issuer
  const discovery = `${base}/.well-known/openid-configuration`
  const cid = client?.id ?? "<client_id>"
  const secret: Record<string, string> = client?.has_secret ? { client_secret: "<client_secret>" } : {}
  const protectedApis = (apis.data ?? []).filter((a) => a.authorizer?.user_pool_id === pool.id)

  const signUp = `curl -s -X POST ${base}/sign-up \\
  -H 'Content-Type: application/json' \\
  -d ${sq(json({ client_id: cid, ...secret, username: "jane", password: "Example-passw0rd", attributes: { email: "jane@example.com" } }))}`
  const auth = `curl -s -X POST ${base}/auth \\
  -H 'Content-Type: application/json' \\
  -d ${sq(json({ client_id: cid, ...secret, flow: "USER_PASSWORD_AUTH", username: "jane", password: "Example-passw0rd" }))}`
  const respond = `curl -s -X POST ${base}/respond \\
  -H 'Content-Type: application/json' \\
  -d ${sq(json({ session: "<session from /auth>", new_password: "New-passw0rd" }))}`
  const refresh = `curl -s -X POST ${base}/auth \\
  -H 'Content-Type: application/json' \\
  -d ${sq(json({ client_id: cid, ...secret, flow: "REFRESH_TOKEN_AUTH", refresh_token: "<refresh_token>" }))}`
  const userinfo = `curl -s ${base}/userinfo -H "Authorization: Bearer $ACCESS_TOKEN"`
  const callApi = `curl -s ${protectedApis[0]?.endpoint ?? "http://<host>/apigw/<api-id>"}/me -H "Authorization: Bearer $ID_TOKEN"`

  return (
    <div className="flex flex-col gap-4">
      <Section title="Token verification" description="Tokens are RS256 JWTs. Verify them with any OIDC/JWT library using these URLs.">
        <KeyValueGrid
          columns={2}
          items={[
            { label: "Issuer (iss)", value: <CopyableText value={pool.issuer} />, wide: true },
            {
              label: "JWKS URI",
              value: (
                <span className="inline-flex max-w-full items-center gap-1">
                  <CopyableText value={pool.jwks_uri} />
                  <a href={pool.jwks_uri} target="_blank" rel="noreferrer" className="text-muted-foreground hover:text-foreground" aria-label="Open JWKS">
                    <ExternalLink className="size-3.5" />
                  </a>
                </span>
              ),
              wide: true,
            },
            {
              label: "OpenID discovery URL",
              value: (
                <span className="inline-flex max-w-full items-center gap-1">
                  <CopyableText value={discovery} />
                  <a href={discovery} target="_blank" rel="noreferrer" className="text-muted-foreground hover:text-foreground" aria-label="Open discovery document">
                    <ExternalLink className="size-3.5" />
                  </a>
                </span>
              ),
              wide: true,
            },
            { label: "User info endpoint", value: <CopyableText value={`${base}/userinfo`} />, wide: true },
            { label: "ID token audience (aud)", value: "The app client ID" },
            { label: "Access token", value: <span>client_id claim = app client ID; token_use = access</span> },
          ]}
        />
      </Section>

      <Section
        title="Sign users up and in"
        description="Public endpoints for your application; they need an app client but no HomeCloud credentials."
        actions={
          clients.data?.length ? (
            <Select value={clientId} onValueChange={setClientId}>
              <SelectTrigger className="h-8 w-56" aria-label="App client for the examples">
                <SelectValue placeholder="App client" />
              </SelectTrigger>
              <SelectContent>
                {clients.data.map((c) => (
                  <SelectItem key={c.id} value={c.id}>
                    {c.name || c.id}
                    <span className="text-muted-foreground font-mono text-xs">{c.id.slice(0, 8)}</span>
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          ) : null
        }
      >
        <div className="flex flex-col gap-4">
          {clients.data && clients.data.length === 0 && (
            <Alert variant="warning">
              <AlertTriangle />
              <AlertDescription>Create an app client on the App clients tab; the examples use its ID.</AlertDescription>
            </Alert>
          )}
          {!pool.self_sign_up && (
            <Alert variant="info">
              <Info />
              <AlertDescription>Self sign-up is disabled for this pool, so the sign-up call returns NotAuthorizedException.</AlertDescription>
            </Alert>
          )}
          <Snippet
            title="1. Sign up"
            code={signUp}
            note={pool.auto_confirm ? "New users are confirmed automatically." : "New users stay UNCONFIRMED until an administrator confirms them on the Users tab."}
          />
          <Snippet
            title="2. Sign in"
            code={auth}
            note="Returns id_token, access_token and refresh_token. Users with a temporary password get challenge NEW_PASSWORD_REQUIRED and a session instead."
          />
          <Snippet title="3. Answer NEW_PASSWORD_REQUIRED" code={respond} note="Sets the new password and returns tokens." />
          <Snippet title="4. Refresh tokens" code={refresh} />
          <Snippet title="5. Who am I" code={userinfo} note="Needs an access token. POST /sign-out with the same header revokes all of the user's tokens." />
          {client?.has_secret && <p className="text-muted-foreground text-xs">This client has a secret: replace &lt;client_secret&gt; with the value shown when it was created.</p>}
        </div>
      </Section>

      <Section title="Protect an API Gateway route" description="API Gateway verifies tokens from this pool before invoking your function.">
        <div className="flex flex-col gap-4 text-sm">
          <ol className="flex list-decimal flex-col gap-1.5 pl-5">
            <li>
              Open an API in{" "}
              <Link href="/apigateway/" className="text-primary hover:underline">
                API Gateway
              </Link>{" "}
              and choose <span className="font-medium">Set authorizer</span>: user pool <span className="font-mono text-[13px] break-all">{pool.id}</span>, and optionally an app client
              as the audience.
            </li>
            <li>
              Set the route&apos;s authorization to <span className="font-medium">JWT</span>. Requests without a valid ID or access token get 401.
            </li>
            <li>
              Your function receives the verified claims in <span className="font-mono text-[13px] break-all">event.requestContext.authorizer.jwt.claims</span>.
            </li>
          </ol>
          <Snippet title="Call a protected route" code={callApi} />
          <div>
            <span className="hc-eyebrow">APIs using this pool</span>
            {protectedApis.length ? (
              <ul className="mt-2 flex flex-col gap-1.5">
                {protectedApis.map((a) => (
                  <li key={a.id} className="flex min-w-0 flex-wrap items-center gap-2">
                    <CellLink href={`/apigateway/api/?id=${encodeURIComponent(a.id)}`} max="16rem">
                      {a.name}
                    </CellLink>
                    <Tag>{(a.routes ?? []).filter((r) => r.authorization === "JWT").length} JWT route(s)</Tag>
                    {a.authorizer?.audience && <span className="text-muted-foreground min-w-0 truncate font-mono text-xs">aud {a.authorizer.audience}</span>}
                  </li>
                ))}
              </ul>
            ) : (
              <p className="text-muted-foreground mt-1">None yet.</p>
            )}
          </div>
        </div>
      </Section>
    </div>
  )
}
