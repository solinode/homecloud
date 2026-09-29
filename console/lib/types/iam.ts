import type { Tags } from "./common"

export interface PolicyStatement {
  Sid?: string
  Effect: "Allow" | "Deny"
  /** A statement has Action or NotAction, and Resource or NotResource. */
  Action?: string | string[]
  NotAction?: string | string[]
  Resource?: string | string[]
  NotResource?: string | string[]
  /** {operator: {key: value | values}} */
  Condition?: Record<string, Record<string, string | string[] | boolean | number>>
}

export interface PolicyDocument {
  Version: string
  Id?: string
  /** AWS accepts a single statement object as well as an array. */
  Statement: PolicyStatement[] | PolicyStatement
}

export interface AccessKey {
  access_key_id: string
  user_name: string
  status: "Active" | "Inactive"
  created_at: string
  last_used?: string
}

export interface NewAccessKey {
  access_key_id: string
  secret_access_key: string
  user_name: string
  status: string
  created_at: string
}

export interface IamUser {
  name: string
  id: string
  arn: string
  root: boolean
  created_at: string
  console_access: boolean
  password_set_at: string | null
  last_login: string | null
  path?: string
  groups: string[]
  attached_policies: string[]
  inline_policies: Record<string, PolicyDocument> | null
  tags?: Tags | null
  /** ARN of the permissions boundary policy; "" = none. */
  permissions_boundary?: string
  /** only on GET /iam/users/{name} */
  access_keys?: AccessKey[]
}

export interface IamGroup {
  name: string
  id?: string
  path?: string
  arn: string
  created_at: string
  attached_policies: string[]
  inline_policies?: Record<string, PolicyDocument> | null
  members: string[]
}

export interface PolicySummary {
  name: string
  arn: string
  path?: string
  description: string
  /** AWS managed (arn:aws:iam::aws:policy/...): built in and read-only. */
  managed: boolean
  created_at: string
  updated_at: string
  attachment_count: number
  default_version?: string
}

export interface PolicyVersion {
  version_id: string
  is_default: boolean
  created_at: string
  document: PolicyDocument
}

export interface Policy {
  name: string
  id?: string
  arn: string
  path?: string
  description: string
  managed: boolean
  document: PolicyDocument
  created_at: string
  updated_at: string
  tags?: Tags | null
  default_version?: string
  versions?: { id: string; document: PolicyDocument; created_at: string }[]
}

export interface PolicyDetail {
  policy: Policy
  attachments: { users: string[]; groups: string[]; roles?: string[] }
}

/** A trust policy's Principal: "*" or {AWS, Service, Federated}. */
export type TrustPrincipal = "*" | { AWS?: string | string[]; Service?: string | string[]; Federated?: string | string[] }

export interface TrustStatement {
  Sid?: string
  Effect: "Allow" | "Deny"
  Principal: TrustPrincipal
  Action: string | string[]
  Condition?: Record<string, unknown>
}

export interface TrustPolicyDocument {
  Version: string
  Statement: TrustStatement[] | TrustStatement
}

export interface IamRole {
  name: string
  id: string
  arn: string
  path: string
  description: string
  assume_role_policy: TrustPolicyDocument
  attached_policies: string[]
  inline_policies: Record<string, PolicyDocument> | null
  max_session_duration: number
  created_at: string
  last_used?: string | null
  tags?: Tags | null
  trusted_services: string[]
  /** ARN of the permissions boundary policy; "" = none. */
  permissions_boundary?: string
  /** Names of the instance profiles that carry this role. */
  instance_profiles?: string[]
  /** Service principal of a service-linked role; "" otherwise. */
  service_linked?: string
}

/** An instance profile carries (at most) one role to EC2 instances. */
export interface InstanceProfile {
  name: string
  id: string
  arn: string
  path: string
  roles: string[]
  created_at: string
  tags?: Tags | null
}

/** Temporary credentials from POST /sts/assume-role. */
export interface TempCredentials {
  access_key_id: string
  secret_access_key: string
  session_token: string
  expiration: string
  assumed_role_arn?: string
  assumed_role_id?: string
}

export interface IamSummary {
  account_id: string
  users: number
  groups: number
  policies: number
  access_keys: number
  mfa_devices?: number
  roles?: number
  instance_profiles?: number
  customer_policies?: number
  managed_policies?: number
}

export interface SimulationResult {
  action: string
  resource: string
  decision: "allowed" | "implicitDeny" | "explicitDeny"
}
