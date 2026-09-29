import type { Tags } from "./common"

export interface LambdaRuntime {
  name: string
  label: string
  image: string
  default_handler: string
  default_file: string
  /** starter code for default_file; empty when the runtime needs a deployment package */
  template: string
  deprecated?: boolean
}

export interface FunctionUrlConfig {
  enabled: boolean
  /** NONE | HC_IAM */
  auth_type: "NONE" | "HC_IAM"
  url?: string
  qualifier?: string
}

export interface LambdaFunction {
  name: string
  arn: string
  runtime: string
  handler: string
  description: string
  memory_mb: number
  timeout_seconds: number
  environment: Record<string, string> | null
  /** base64 SHA-256 of the code zip */
  code_sha256: string
  code_size: number
  state: string
  function_url: FunctionUrlConfig
  /** CloudWatch log group, /aws/lambda/<name> */
  log_group: string
  subnet_id?: string
  security_group_ids?: string[]
  /** Pending | Active | Failed */
  state_reason?: string
  state_reason_code?: string
  /** InProgress | Successful | Failed */
  last_update_status?: string
  last_update_status_reason?: string
  /** execution role ARN */
  role?: string
  /** Zip (default) | Image */
  package_type?: "Zip" | "Image" | string
  image_uri?: string
  image_config?: { EntryPoint?: string[]; Command?: string[]; WorkingDirectory?: string } | null
  architectures?: string[]
  /** layer version ARNs, in order */
  layers?: string[]
  dead_letter_target?: string
  reserved_concurrency?: number | null
  /** "$LATEST" or a published version number */
  version?: string
  version_description?: string
  last_version?: number
  revision_id?: string
  last_modified: string
  created_at: string
  tags?: Tags | null
}

export interface LambdaAlias {
  function: string
  name: string
  arn: string
  function_version: string
  description: string
  /** version -> weight (0-1) of traffic routed to an additional version */
  additional_version_weights?: Record<string, number> | null
  revision_id: string
}

export interface EventInvokeConfig {
  function: string
  qualifier: string
  maximum_retry_attempts?: number
  maximum_event_age_seconds?: number
  on_success?: string
  on_failure?: string
  last_modified: string
}

export interface FunctionDetail {
  configuration: LambdaFunction
  /** "Warm" when an execution environment is running, else "Idle" */
  environment_state: "Warm" | "Idle" | string
  /** running execution environments */
  environments?: number
  concurrent_executions?: number
  concurrency_limit?: number
  aliases?: LambdaAlias[]
  event_invoke_config?: EventInvokeConfig | null
}

export interface LambdaAccountSettings {
  AccountLimit: {
    TotalCodeSize: number
    CodeSizeUnzipped: number
    CodeSizeZipped: number
    ConcurrentExecutions: number
    UnreservedConcurrentExecutions: number
  }
  AccountUsage: { TotalCodeSize: number; FunctionCount: number }
}

export interface LayerVersion {
  name: string
  version: number
  /** layer version ARN */
  arn: string
  layer_arn: string
  description: string
  compatible_runtimes?: string[] | null
  compatible_architectures?: string[] | null
  license_info?: string
  code_sha256?: string
  code_size: number
  created_at: string
}

export interface FunctionCode {
  /** path -> content, for text files only */
  files: Record<string, string>
  /** false when the package has binaries, large files or >50 files */
  editable: boolean
  file_count: number
  sha256_hex: string
}

export interface FunctionConfigInput {
  runtime?: string
  handler?: string
  description?: string
  memory_mb?: number
  timeout_seconds?: number
  environment?: Record<string, string>
  subnet_id?: string
  tags?: Tags
  role?: string
  layers?: string[]
  architectures?: string[]
  dead_letter_target?: string
}

export interface CreateFunctionInput extends FunctionConfigInput {
  name: string
  code?: { files?: Record<string, string>; zip_base64?: string }
  package_type?: "Zip" | "Image"
  image_uri?: string
  publish?: boolean
}

export interface InvokeResult {
  request_id: string
  status_code: number
  payload: unknown
  /** "Unhandled" when the handler threw or timed out */
  function_error?: string
  logs: string
  duration_ms: number
  billed_duration_ms: number
  cold_start: boolean
  /** the version that ran ("$LATEST" or a number), resolved from an alias */
  executed_version?: string
}

export interface EventSourceMapping {
  id: string
  function_name: string
  queue_name: string
  event_source_arn: string
  batch_size: number
  batching_window_seconds?: number
  function_response_types?: string[] | null
  enabled: boolean
  last_processing_result: string
  last_invoked_at?: string
  created_at: string
  /** DynamoDB streams: TRIM_HORIZON | LATEST */
  starting_position?: string
  checkpoint?: string
  maximum_retry_attempts?: number
  bisect_batch_on_function_error?: boolean
  on_failure?: string
}

export interface ApiRoute {
  id: string
  /** GET, POST, ... or ANY */
  method: string
  /** e.g. /items/{id} or /files/{proxy+} */
  path: string
  function_name: string
  /** NONE (public) or JWT (requires a token from the API's authorizer user pool) */
  authorization?: "NONE" | "JWT" | string
}

/** Validates Cognito user pool tokens on routes whose authorization is JWT. */
export interface ApiAuthorizer {
  user_pool_id: string
  /** app client ID; empty accepts any client of the pool */
  audience?: string
}

export interface HttpApi {
  id: string
  name: string
  description: string
  routes: ApiRoute[] | null
  cors: boolean
  authorizer?: ApiAuthorizer | null
  /** invoke URL, http://host:port/apigw/<id> */
  endpoint: string
  created_at: string
}
