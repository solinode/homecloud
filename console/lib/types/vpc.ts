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
  /** tcp or udp from the native API; the EC2 API also records icmp, icmpv6 and "-1" (all traffic) */
  protocol: string
  from_port: number
  to_port: number
  /** an IPv4 CIDR; empty when the rule's source is a group */
  cidr?: string
  /** the ID of a security group of the same VPC: "from every resource that has this group" */
  source_group?: string
  description?: string
}

export interface SecurityGroup {
  id: string
  vpc_id: string
  name: string
  description: string
  ingress: SecurityGroupRule[]
  /** the default group's rule allowing all inbound traffic from its own members is present */
  self_rule?: boolean
  created_at: string
}
