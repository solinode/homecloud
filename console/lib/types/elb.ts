// Types for the Elastic Load Balancing API (cli/internal/svc/elb/elb.go).
import type { Tags } from "./common"

export type LoadBalancerScheme = "internet-facing" | "internal"

/** provisioning while the nginx container is (re)created; failed carries state_reason. */
export type LoadBalancerState = "provisioning" | "active" | "failed"

export interface ElbRule {
  id: string
  priority: number
  /** e.g. /api/ */
  path_prefix?: string
  /** e.g. api.example.com */
  host_header?: string
  target_group: string
}

export type ListenerProtocol = "HTTP" | "HTTPS"

export interface ElbListener {
  id: string
  port: number
  protocol: ListenerProtocol | string
  /** ACM certificate ARN (HTTPS listeners) */
  certificate_arn?: string
  /** HTTP listeners: redirect every request to the HTTPS listener on this port */
  redirect_https_port?: number
  /** requested host port (0/absent = any free port) */
  public_port?: number
  default_target_group: string
  rules: ElbRule[]
}

export interface LoadBalancer {
  name: string
  arn: string
  dns_name: string
  scheme: LoadBalancerScheme
  vpc_id: string
  subnet_id: string
  private_ip: string
  listeners: ElbListener[]
  state: LoadBalancerState | string
  state_reason?: string
  container_id?: string
  /** "80/tcp" -> host port (internet-facing only) */
  public_ports: Record<string, number>
  public_host: string
  created_at: string
  tags?: Tags | null
}

export interface CreateListenerInput {
  port: number
  protocol?: ListenerProtocol
  certificate_arn?: string
  redirect_https_port?: number
  public_port?: number
  /** may be empty for a pure HTTP-to-HTTPS redirect listener */
  default_target_group: string
}

export interface CreateLoadBalancerInput {
  name: string
  scheme: LoadBalancerScheme
  subnet_id: string
  listeners: CreateListenerInput[]
  tags?: Tags
}

export interface CreateRuleInput {
  priority?: number
  path_prefix?: string
  host_header?: string
  target_group: string
}

export interface ElbHealthCheck {
  path: string
  interval_seconds: number
  healthy_threshold: number
  unhealthy_threshold: number
}

export type TargetHealthState = "initial" | "healthy" | "unhealthy" | "unavailable" | "unused"

export interface ElbTarget {
  /** EC2 instance id or ECS task id */
  id: string
  port: number
  ip: string
  health: TargetHealthState | string
  reason?: string
}

export interface TargetGroup {
  name: string
  arn: string
  protocol: string
  port: number
  vpc_id: string
  health_check: ElbHealthCheck
  targets: ElbTarget[]
  created_at: string
}

export interface CreateTargetGroupInput {
  name: string
  port: number
  vpc_id?: string
  health_check?: Partial<ElbHealthCheck>
}

export interface LoadBalancerConfig {
  nginx_conf: string
}
