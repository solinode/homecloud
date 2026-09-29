// Types for Cognito user pools (cli/internal/svc/cognito/cognito.go).

export interface PasswordPolicy {
  min_length: number
  require_uppercase: boolean
  require_lowercase: boolean
  require_numbers: boolean
  require_symbols: boolean
}

export interface UserPoolGroup {
  name: string
  description: string
  precedence: number
}

export interface UserPool {
  id: string
  arn: string
  name: string
  password_policy: PasswordPolicy
  auto_confirm: boolean
  self_sign_up: boolean
  groups: UserPoolGroup[]
  created_at: string
  users: number
  clients: number
  issuer: string
  jwks_uri: string
}

export interface CreateUserPoolInput {
  name: string
  password_policy?: PasswordPolicy
  auto_confirm?: boolean
  self_sign_up?: boolean
}

export interface UpdateUserPoolInput {
  password_policy?: PasswordPolicy
  auto_confirm?: boolean
  self_sign_up?: boolean
}

export type CognitoUserStatus = "CONFIRMED" | "UNCONFIRMED" | "FORCE_CHANGE_PASSWORD"

export interface CognitoUser {
  username: string
  sub: string
  attributes: Record<string, string> | null
  status: CognitoUserStatus | string
  enabled: boolean
  groups: string[] | null
  created_at: string
  last_sign_in?: string | null
  /** only in the AdminCreateUser response when HomeCloud generated the password */
  temporary_password?: string
}

export interface CreateCognitoUserInput {
  username: string
  password?: string
  temporary_password?: boolean
  attributes?: Record<string, string>
  groups?: string[]
}

export interface UpdateCognitoUserInput {
  /** an empty value deletes the attribute */
  attributes?: Record<string, string>
  enabled?: boolean
  groups?: string[]
  confirm?: boolean
}

export interface AppClient {
  id: string
  name: string
  has_secret: boolean
  access_token_minutes: number
  refresh_token_days: number
  created_at: string
}

export interface CreateAppClientInput {
  name: string
  generate_secret?: boolean
  access_token_minutes?: number
  refresh_token_days?: number
}

/** POST .../clients response; client_secret is returned only once. */
export interface CreatedAppClient {
  id: string
  name: string
  access_token_minutes: number
  refresh_token_days: number
  client_secret?: string
}
