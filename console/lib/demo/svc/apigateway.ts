import { badRequest, conflict, getState, notFound, type DemoService, type Router } from "../engine"
import { DAY, ago, hex, nowIso, stableId } from "../util"
import { NAMES } from "../ids"
import type { ApiRoute, HttpApi } from "@/lib/types"

// The console models HTTP APIs as a flat list of routes that each invoke a
// Lambda function. `endpoint` points at a host that never resolves, so the
// "Try it" panel shows its network-error message instead of hitting a server.
const ENDPOINT_BASE = "https://demo.homecloud.example/apigw"

interface ApigwState {
  apis: HttpApi[]
}
const S = (): ApigwState => getState().apigateway as ApigwState

const METHODS = ["ANY", "GET", "POST", "PUT", "PATCH", "DELETE", "HEAD", "OPTIONS"]

const route = (method: string, path: string, fn: string, authorization: "NONE" | "JWT" = "NONE"): ApiRoute => ({
  id: hex(7),
  method,
  path,
  function_name: fn,
  authorization,
})

function seed(): ApigwState {
  const id1 = stableId("", NAMES.api, 10)
  const id2 = stableId("", "shop-webhooks", 10)
  const id3 = stableId("", "shop-internal-admin", 10)
  return {
    apis: [
      {
        id: id1,
        name: NAMES.api,
        description: "Public storefront API used by the web and mobile apps",
        cors: true,
        authorizer: null,
        endpoint: `${ENDPOINT_BASE}/${id1}`,
        created_at: ago(74 * DAY),
        routes: [
          route("GET", "/products", "shop-api-handler"),
          route("GET", "/products/{id}", "shop-api-handler"),
          route("POST", "/orders", NAMES.paymentsFn),
          route("POST", "/payments", NAMES.paymentsFn),
          route("ANY", "/files/{proxy+}", "shop-image-resizer"),
        ],
      },
      {
        id: id2,
        name: "shop-webhooks",
        description: "Inbound webhooks from the payment provider and the warehouse",
        cors: false,
        authorizer: null,
        endpoint: `${ENDPOINT_BASE}/${id2}`,
        created_at: ago(51 * DAY),
        routes: [route("POST", "/webhooks/payments", NAMES.paymentsFn), route("POST", "/webhooks/inventory", "shop-inventory-sync")],
      },
      {
        id: id3,
        name: "shop-internal-admin",
        description: "Back-office endpoints (staff only)",
        cors: true,
        authorizer: null,
        endpoint: `${ENDPOINT_BASE}/${id3}`,
        created_at: ago(20 * DAY),
        routes: [route("GET", "/reports/latest", "shop-nightly-report"), route("POST", "/notifications/test", "shop-notifier")],
      },
    ],
  }
}

function fnExists(name: string): boolean {
  const l = getState().lambda as { functions?: { name: string }[] } | undefined
  return !!l?.functions?.some((f) => f.name === name)
}

function checkRoute(rt: Partial<ApiRoute>): ApiRoute {
  const method = (rt.method || "ANY").toUpperCase()
  if (!METHODS.includes(method)) throw badRequest(`unsupported method "${rt.method}"`)
  const path = rt.path ?? ""
  if (!path.startsWith("/")) throw badRequest("route path must start with /")
  const fn = rt.function_name ?? ""
  if (!fnExists(fn)) throw notFound("function", fn)
  const authorization = (rt.authorization || "NONE").toUpperCase()
  if (authorization !== "NONE" && authorization !== "JWT") throw badRequest("authorization must be NONE or JWT")
  return { id: rt.id || hex(7), method, path, function_name: fn, authorization }
}

const getApi = (id: string): HttpApi => {
  const a = S().apis.find((x) => x.id === id)
  if (!a) throw notFound("api", id)
  return a
}

function routes(r: Router) {
  r.get("/api/v1/apigateway/apis", () => S().apis)
  r.post("/api/v1/apigateway/apis", ({ body: b }) => {
    b = b ?? {}
    if (!String(b.name ?? "").trim()) throw badRequest("name is required")
    const id = hex(10)
    const a: HttpApi = {
      id,
      name: String(b.name),
      description: b.description ?? "",
      routes: [],
      cors: !!b.cors,
      authorizer: b.authorizer?.user_pool_id ? b.authorizer : null,
      endpoint: `${ENDPOINT_BASE}/${id}`,
      created_at: nowIso(),
    }
    for (const rt of b.routes ?? []) a.routes!.push(checkRoute(rt))
    S().apis.push(a)
    return a
  })
  r.get("/api/v1/apigateway/apis/:id", ({ params }) => getApi(params.id))
  r.patch("/api/v1/apigateway/apis/:id", ({ params, body: b }) => {
    const a = getApi(params.id)
    b = b ?? {}
    if (b.name !== undefined) a.name = b.name
    if (b.description !== undefined) a.description = b.description
    if (b.cors !== undefined) a.cors = !!b.cors
    if (b.authorizer) a.authorizer = b.authorizer.user_pool_id ? { user_pool_id: b.authorizer.user_pool_id, audience: b.authorizer.audience ?? "" } : null
    return a
  })
  r.del("/api/v1/apigateway/apis/:id", ({ params }) => {
    getApi(params.id)
    S().apis = S().apis.filter((a) => a.id !== params.id)
  })
  r.post("/api/v1/apigateway/apis/:id/routes", ({ params, body }) => {
    const a = getApi(params.id)
    const rt = checkRoute(body ?? {})
    if ((a.routes ?? []).some((x) => x.method === rt.method && x.path === rt.path)) throw conflict(`route "${rt.method} ${rt.path}" already exists`)
    a.routes = [...(a.routes ?? []), rt]
    return a
  })
  r.patch("/api/v1/apigateway/apis/:id/routes/:route", ({ params, body: b }) => {
    const a = getApi(params.id)
    const i = (a.routes ?? []).findIndex((x) => x.id === params.route)
    if (i < 0) throw notFound("route", params.route)
    const next = checkRoute({ ...a.routes![i], ...(b ?? {}) })
    if (a.routes!.some((x, j) => j !== i && x.method === next.method && x.path === next.path)) throw conflict(`route "${next.method} ${next.path}" already exists`)
    a.routes![i] = next
    return a
  })
  r.del("/api/v1/apigateway/apis/:id/routes/:route", ({ params }) => {
    const a = getApi(params.id)
    if (!(a.routes ?? []).some((x) => x.id === params.route)) throw notFound("route", params.route)
    a.routes = a.routes!.filter((x) => x.id !== params.route)
    return a
  })
}

const service: DemoService = { name: "apigateway", seed, routes }

export default service
