import type { Tags } from "./common"

export interface PolicyStatement {
  Sid?: string
  Effect: "Allow" | "Deny"
  Action: string | string[]
  Resource: string | string[]
}

export interface PolicyDocument {
  Version: string
  Statement: PolicyStatement[]
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
  groups: string[]
  attached_policies: string[]
  inline_policies: Record<string, PolicyDocument> | null
  tags?: Tags | null
  /** only on GET /iam/users/{name} */
  access_keys?: AccessKey[]
}

export interface IamGroup {
  name: string
  arn: string
  created_at: string
  attached_policies: string[]
  members: string[]
}

export interface PolicySummary {
  name: string
  arn: string
  description: string
  managed: boolean
  created_at: string
  updated_at: string
  attachment_count: number
}

export interface Policy {
  name: string
  arn: string
  description: string
  managed: boolean
  document: PolicyDocument
  created_at: string
  updated_at: string
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
  Statement: TrustStatement[]
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
  mfa_devices: number
  roles?: number
}

export interface SimulationResult {
  action: string
  resource: string
  decision: "allowed" | "implicitDeny" | "explicitDeny"
}
