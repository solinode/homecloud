// Types for the CloudFormation (stacks) API: cli/internal/svc/cfn.

/** Stack and resource statuses written by the stack engine. */
export type StackStatus =
  | "CREATE_IN_PROGRESS"
  | "CREATE_COMPLETE"
  | "CREATE_FAILED"
  | "ROLLBACK_COMPLETE"
  | "UPDATE_IN_PROGRESS"
  | "UPDATE_COMPLETE"
  | "UPDATE_FAILED"
  | "DELETE_IN_PROGRESS"
  | "DELETE_FAILED"
  | "DELETE_COMPLETE"

/** A template parameter declaration (Parameters.<Name>), as returned by validate. */
export interface CfnParamDef {
  Type: string
  Default?: unknown
  AllowedValues?: unknown[]
  Description?: string
  NoEcho?: boolean
}

/** GET /api/v1/cloudformation/stacks item. */
export interface StackSummary {
  name: string
  arn: string
  status: StackStatus | string
  status_reason: string
  description: string
  /** number of resources */
  resources: number
  created_at: string
  updated_at: string
}

export interface StackResource {
  logical_id: string
  type: string
  physical_id: string
  status: string
  reason?: string
  /** resolved properties, as sent to the service API */
  properties?: Record<string, unknown>
  /** the service's create/describe response (GetAtt source) */
  attributes?: Record<string, unknown>
  updated_at: string
}

export interface StackEvent {
  time: string
  logical_id: string
  type: string
  status: string
  reason?: string
}

/** GET /api/v1/cloudformation/stacks/{name} (also returned by create/update/delete). */
export interface Stack {
  name: string
  arn: string
  status: StackStatus | string
  status_reason?: string
  description?: string
  template: string
  parameters: Record<string, unknown> | null
  resources: Record<string, StackResource> | null
  /** creation order of logical IDs */
  order: string[] | null
  outputs: Record<string, unknown> | null
  /** newest first */
  events: StackEvent[] | null
  created_at: string
  updated_at: string
}

/** POST /api/v1/cloudformation/stacks and PUT .../stacks/{name} body. */
export interface StackInput {
  name?: string
  template: string
  parameters?: Record<string, unknown>
  disable_rollback?: boolean
}

/** POST /api/v1/cloudformation/validate response. */
export type ValidateTemplateResult =
  | { valid: false; error: string }
  | { valid: true; description?: string; parameters?: Record<string, CfnParamDef> | null; creation_order: string[] }
