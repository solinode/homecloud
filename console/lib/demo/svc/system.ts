import type { Health, WhoAmI } from "@/lib/types"
import { unavailable, type DemoService } from "../engine"
import { ACCOUNT, REGION, DAY } from "../util"

const LOADED = Date.now()
const BASE_UPTIME = 12 * 24 * 3600 + 5 * 3600 + 1234

const service: DemoService = {
  name: "system",
  seed: () => ({}),
  routes: (r) => {
    r.get("/api/v1/health", (): Health => ({
      status: "ok",
      version: "0.4.0",
      region: REGION,
      uptime_seconds: BASE_UPTIME + Math.floor((Date.now() - LOADED) / 1000),
    }))
    r.get("/api/v1/auth/whoami", (): WhoAmI => ({
      account_id: ACCOUNT,
      user_name: "demo-admin",
      arn: `arn:aws:iam::${ACCOUNT}:user/demo-admin`,
      root: false,
      region: REGION,
    }))
    r.post("/api/v1/auth/logout", () => ({ ok: true }))
    r.get("/api/v1/system/info", () => ({ version: "0.4.0", region: REGION, account_id: ACCOUNT, started_at: new Date(LOADED - BASE_UPTIME * 1000 - DAY * 0).toISOString() }))
    // Backups of the server's data directory need a real server.
    r.get("/api/v1/system/backup", () => {
      throw unavailable("Downloading a backup")
    })
    r.post("/api/v1/system/backup", () => {
      throw unavailable("Creating a backup")
    })
  },
}

export default service
