// Types for the ECS API (cli/internal/svc/ecs/ecs.go).
import type { Tags } from "./common"

export type EcsTaskDefinitionStatus = "ACTIVE" | "INACTIVE"

export interface EcsSecretRef {
  /** Environment variable name inside the container. */
  name: string
  /** Secrets Manager secret name, optionally "name:json-key". */
  value_from: string
}

export interface EcsTaskDefinition {
  family: string
  revision: number
  arn: string
  image: string
  command?: string[] | null
  entrypoint?: string[] | null
  /** vCPUs (0.125-16). */
  cpu: number
  memory_mb: number
  container_port?: number
  environment?: Record<string, string> | null
  secrets?: EcsSecretRef[] | null
  status: EcsTaskDefinitionStatus | string
  created_at: string
}

export interface EcsRegisterTaskDefinitionInput {
  family: string
  image: string
  command?: string[]
  entrypoint?: string[]
  cpu?: number
  memory_mb?: number
  container_port?: number
  environment?: Record<string, string>
  secrets?: EcsSecretRef[]
}

export interface EcsLoadBalancerBinding {
  target_group: string
  container_port: number
}

export interface EcsServiceEvent {
  time: string
  message: string
}

export type EcsTaskStatus = "PROVISIONING" | "RUNNING" | "STOPPED"

export interface EcsTask {
  id: string
  arn: string
  /** Owning service; empty for standalone (RunTask) tasks. */
  service?: string
  /** family:revision; a forced redeploy temporarily suffixes old tasks with "(redeploy)". */
  task_definition: string
  container_id?: string
  vpc_id: string
  subnet_id: string
  private_ip: string
  last_status: EcsTaskStatus | string
  desired_status: string
  stop_reason?: string
  exit_code?: number | null
  /** Standalone tasks only: "8000/tcp" -> host port. */
  public_ports?: Record<string, number> | null
  created_at: string
  started_at?: string | null
  stopped_at?: string | null
}

export interface EcsService {
  name: string
  arn: string
  /** family:revision */
  task_definition: string
  desired_count: number
  running_count: number
  pending_count: number
  /** "<name>.ecs.internal" */
  endpoint: string
  subnet_id: string
  security_groups: string[]
  load_balancer?: EcsLoadBalancerBinding | null
  /** ACTIVE | DRAINING (being deleted) */
  status: string
  created_at: string
  tags?: Tags | null
  /** Only on GET /services/{name}. */
  events?: EcsServiceEvent[]
  /** Only on GET /services/{name}. */
  tasks?: EcsTask[]
}

export interface EcsCreateServiceInput {
  name: string
  task_definition: string
  desired_count: number
  subnet_id?: string
  security_groups?: string[]
  load_balancer?: { target_group: string; container_port?: number }
  tags?: Tags
}

export interface EcsUpdateServiceInput {
  desired_count?: number
  task_definition?: string
  force_new_deployment?: boolean
}

export interface EcsRunTaskInput {
  task_definition: string
  subnet_id?: string
  security_groups?: string[]
  environment?: Record<string, string>
  command?: string[]
}

export interface EcsTaskLogs {
  output: string
}
