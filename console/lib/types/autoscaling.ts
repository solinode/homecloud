// Types for EC2 Auto Scaling (cli/internal/svc/autoscaling/autoscaling.go).
import type { FileSystemMount } from "./efs"

export interface LaunchConfig {
  image_id: string
  instance_type: string
  security_group_ids: string[] | null
  user_data?: string
  file_systems?: FileSystemMount[] | null
}

export type ScalingMetric = "CPUUtilization" | "MemoryUtilization"

export interface ScalingPolicy {
  name: string
  metric: ScalingMetric | string
  /** percent */
  target_value: number
  cooldown_seconds: number
}

export interface ScalingActivity {
  time: string
  description: string
  cause: string
  /** Successful | Failed */
  status: string
}

export interface AsgInstance {
  id: string
  state: string
  private_ip: string
  subnet_id: string
  launch_time: string
}

export interface AutoScalingGroup {
  name: string
  arn: string
  launch: LaunchConfig
  min_size: number
  max_size: number
  desired_capacity: number
  subnet_ids: string[] | null
  target_groups: string[] | null
  policies: ScalingPolicy[] | null
  health_check_grace_seconds: number
  suspended: boolean
  /** "Active" | "Delete in progress" */
  status: string
  instances: AsgInstance[] | null
  /** detail only */
  activities?: ScalingActivity[] | null
  created_at: string
  last_scaling?: string | null
}

export interface CreateAutoScalingGroupInput {
  name: string
  launch: LaunchConfig
  min_size: number
  max_size: number
  desired_capacity: number
  subnet_ids?: string[]
  target_groups?: string[]
  health_check_grace_seconds?: number
  policies?: Partial<ScalingPolicy>[]
}

export interface UpdateAutoScalingGroupInput {
  min_size?: number
  max_size?: number
  desired_capacity?: number
  launch?: LaunchConfig
  policies?: Partial<ScalingPolicy>[]
  suspended?: boolean
}

/** Tag the Auto Scaling service puts on the instances it launches. */
export const ASG_GROUP_TAG = "hc:autoscaling:groupName"
