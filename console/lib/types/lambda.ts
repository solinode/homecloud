import type { Tags } from "./common"

export interface LambdaRuntime {
  name: string
  label: string
  image: string
  default_handler: string
  default_file: string
  /** starter code for default_file */
  template: string
}

export interface FunctionUrlConfig {
  enabled: boolean
  /** NONE | HC_IAM */
  auth_type: "NONE" | "HC_IAM"
  url?: string
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
  last_modified: string
  created_at: string
  tags?: Tags | null
}

export interface FunctionDetail {
  configuration: LambdaFunction
  /** "Warm" when an execution environment is running, else "Idle" */
  environment_state: "Warm" | "Idle" | string
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
}

export interface CreateFunctionInput extends FunctionConfigInput {
  name: string
  code?: { files?: Record<string, string>; zip_base64?: string }
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
}

export interface EventSourceMapping {
  id: string
  function_name: string
  queue_name: string
  event_source_arn: string
  batch_size: number
  enabled: boolean
  last_processing_result: string
  last_invoked_at?: string
  created_at: string
}

export interface ApiRoute {
  id: string
  /** GET, POST, ... or ANY */
  method: string
  /** e.g. /items/{id} or /files/{proxy+} */
  path: string
  function_name: string
}

export interface HttpApi {
  id: string
  name: string
  description: string
  routes: ApiRoute[] | null
  cors: boolean
  /** invoke URL, http://host:port/apigw/<id> */
  endpoint: string
  created_at: string
}
