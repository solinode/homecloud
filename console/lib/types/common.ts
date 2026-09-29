export type Tags = Record<string, string>

export interface Health {
  status: string
  version: string
  region: string
  uptime_seconds: number
}

export interface WhoAmI {
  account_id: string
  user_name: string
  arn: string
  root: boolean
  region: string
}

export interface OkResponse {
  ok: boolean
}
