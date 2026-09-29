import type { Tags } from "./common"

export interface Subnet {
  id: string
  vpc_id: string
  name: string
  cidr: string
  availability_zone: string
  default: boolean
  created_at: string
  available_ips: number
  used_ips: number
}

export interface Vpc {
  id: string
  name: string
  cidr: string
  network: string
  default: boolean
  internet_access: boolean
  state: string
  created_at: string
  tags?: Tags | null
  subnets: Subnet[]
  arn: string
}

export interface SecurityGroupRule {
  id: string
  protocol: "tcp" | "udp"
  from_port: number
  to_port: number
  cidr: string
  description?: string
}

export interface SecurityGroup {
  id: string
  vpc_id: string
  name: string
  description: string
  ingress: SecurityGroupRule[]
  created_at: string
}
