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
  attachments: { users: string[]; groups: string[] }
}

export interface IamSummary {
  account_id: string
  users: number
  groups: number
  policies: number
  access_keys: number
  mfa_devices: number
}

export interface SimulationResult {
  action: string
  resource: string
  decision: "allowed" | "implicitDeny" | "explicitDeny"
}
