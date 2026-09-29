import type { Tags } from "./common"
import type { FileSystemMount } from "./efs"

export type InstanceState = "pending" | "running" | "stopping" | "stopped" | "shutting-down" | "terminated"

export interface VolumeAttachment {
  volume_id: string
  mount_path: string
  delete_on_termination: boolean
}

export interface Instance {
  id: string
  name: string
  arn: string
  image_id: string
  image_ref: string
  instance_type: string
  vcpus: number
  memory_mb: number
  state: InstanceState
  state_reason?: string
  container_id?: string
  vpc_id: string
  subnet_id: string
  availability_zone: string
  private_ip: string
  private_dns: string
  security_groups: string[]
  volumes: VolumeAttachment[]
  /** EFS file systems mounted at launch */
  file_systems?: FileSystemMount[]
  user_data?: string
  keep_alive: boolean
  /** "80/tcp" -> host port */
  public_ports: Record<string, number>
  public_host: string
  launch_time: string
  terminated_at?: string
  tags?: Tags
}

export interface InstanceType {
  name: string
  vcpus: number
  memory_mb: number
  family: string
}

export interface Image {
  id: string
  name: string
  description: string
  ref: string
  platform: string
  keep_alive: boolean
  /** "homecloud" for the catalog, otherwise the account ID */
  owner: string
  state: string
  created_at?: string
  source_instance?: string
}

export interface Volume {
  id: string
  name: string
  size_gb: number
  state: "available" | "in-use" | string
  attached_to?: string
  mount_path?: string
  availability_zone: string
  created_at: string
  tags?: Tags
}

export interface RunInstancesInput {
  name?: string
  image_id: string
  instance_type?: string
  subnet_id?: string
  security_group_ids?: string[]
  user_data?: string
  count?: number
  tags?: Tags
  volumes?: { volume_id?: string; size_gb?: number; mount_path: string; delete_on_termination?: boolean }[]
  file_systems?: FileSystemMount[]
}

export interface ConsoleOutput {
  instance_id: string
  output: string
  timestamp?: string
}

export interface CommandResult {
  command_id: string
  instance_id: string
  status: "Success" | "Failed"
  exit_code: number
  stdout: string
  stderr: string
  duration_ms: number
}
