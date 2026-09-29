export interface TrailEvent {
  id: string
  time: string
  user: string
  user_arn: string
  access_key?: string
  action: string
  resource: string
  method: string
  path: string
  status: number
  source_ip: string
  user_agent: string
  latency_ms: number
}
