import { conflict, err, notFound, badRequest, type DemoService, type Req, type Router, getState } from "../engine"
import { ACCOUNT, DAY, HOUR, MIN, ago, hex } from "../util"
import { NAMES } from "../ids"
import type {
  AccessKey, IamGroup, IamRole, IamSummary, IamUser, InstanceProfile, Policy, PolicyDocument, PolicyStatement, PolicySummary,
  PolicyVersion, SimulationResult, TempCredentials, TrustPolicyDocument,
} from "@/lib/types"

// ---- state ----

interface StoredPolicy extends Omit<Policy, "versions"> {
  versions: { id: string; document: PolicyDocument; created_at: string }[]
  default_version: string
  next_version: number
}

interface IamState {
  users: IamUser[]
  groups: IamGroup[]
  policies: StoredPolicy[]
  roles: IamRole[]
  profiles: InstanceProfile[]
  keys: AccessKey[]
}

const st = () => getState().iam as IamState

// ---- error helpers (AWS IAM error codes) ----
const noSuch = (msg: string) => err(404, "NoSuchEntity", msg)
const exists = (msg: string) => err(409, "EntityAlreadyExists", msg)
const NAME_RE = /^[\w+=,.@-]{1,64}$/

const userArn = (n: string, path = "/") => `arn:aws:iam::${ACCOUNT}:user${path}${n}`
const groupArn = (n: string) => `arn:aws:iam::${ACCOUNT}:group/${n}`
const roleArn = (n: string, path = "/") => `arn:aws:iam::${ACCOUNT}:role${path}${n}`
const awsPolicyArn = (n: string, path = "/") => `arn:aws:iam::aws:policy${path}${n}`
const customPolicyArn = (n: string) => `arn:aws:iam::${ACCOUNT}:policy/${n}`
const profileArn = (n: string) => `arn:aws:iam::${ACCOUNT}:instance-profile/${n}`

// ---- policy document helpers ----
const allow = (action: string | string[], resource: string | string[] = "*", extra: Partial<PolicyStatement> = {}): PolicyStatement => ({ Effect: "Allow", Action: action, Resource: resource, ...extra })
const pdoc = (...s: PolicyStatement[]): PolicyDocument => ({ Version: "2012-10-17", Statement: s })
const trust = (principal: Record<string, string | string[]>, action = "sts:AssumeRole", condition?: Record<string, unknown>): TrustPolicyDocument => ({
  Version: "2012-10-17",
  Statement: [{ Effect: "Allow", Principal: principal, Action: action, ...(condition ? { Condition: condition } : {}) }],
})
const svcTrust = (svc: string) => trust({ Service: svc })

const LAMBDA_LOGS = allow(["logs:CreateLogGroup", "logs:CreateLogStream", "logs:PutLogEvents"])

interface Builtin { name: string; path?: string; desc: string; doc: PolicyDocument }
const BUILTINS: Builtin[] = [
  { name: "AdministratorAccess", desc: "Provides full access to every service and resource.", doc: pdoc(allow("*")) },
  {
    name: "PowerUserAccess", desc: "Provides full access to every service except user and group management.",
    doc: pdoc({ Effect: "Allow", NotAction: ["iam:*", "organizations:*", "account:*"], Resource: "*" }, allow(["iam:CreateServiceLinkedRole", "iam:DeleteServiceLinkedRole", "iam:ListRoles", "organizations:DescribeOrganization"])),
  },
  { name: "ReadOnlyAccess", desc: "Provides read-only access to every service (secret values excluded).", doc: pdoc(allow(["*:Describe*", "*:List*", "*:Get*"]), { Effect: "Deny", Action: ["secretsmanager:GetSecretValue", "kms:Decrypt"], Resource: "*" }) },
  { name: "IAMFullAccess", desc: "Provides full access to IAM users, groups, roles, policies and access keys.", doc: pdoc(allow(["iam:*", "organizations:DescribeAccount", "organizations:DescribeOrganization"])) },
  { name: "IAMReadOnlyAccess", desc: "Provides read access to IAM users, groups, roles and policies.", doc: pdoc(allow(["iam:GenerateCredentialReport", "iam:Get*", "iam:List*", "iam:SimulatePrincipalPolicy"])) },
  { name: "AmazonEC2FullAccess", desc: "Provides full access to EC2 instances, images, volumes, networking and load balancers.", doc: pdoc(allow(["ec2:*", "elasticloadbalancing:*", "cloudwatch:*", "autoscaling:*"])) },
  { name: "AmazonEC2ReadOnlyAccess", desc: "Provides read-only access to EC2 instances, images, volumes and networking.", doc: pdoc(allow(["ec2:Describe*", "elasticloadbalancing:Describe*", "cloudwatch:Describe*", "cloudwatch:GetMetricStatistics", "autoscaling:Describe*"])) },
  { name: "AmazonS3FullAccess", desc: "Provides full access to all buckets and objects.", doc: pdoc(allow(["s3:*", "s3-object-lambda:*"])) },
  { name: "AmazonS3ReadOnlyAccess", desc: "Provides read-only access to all buckets and objects.", doc: pdoc(allow(["s3:Get*", "s3:List*", "s3:Describe*", "s3-object-lambda:Get*", "s3-object-lambda:List*"])) },
  { name: "AmazonRDSFullAccess", desc: "Provides full access to managed databases and caches.", doc: pdoc(allow(["rds:*", "elasticache:*"])) },
  { name: "AmazonDynamoDBFullAccess", desc: "Provides full access to DynamoDB tables.", doc: pdoc(allow("dynamodb:*")) },
  { name: "AmazonSQSFullAccess", desc: "Provides full access to SQS queues.", doc: pdoc(allow("sqs:*")) },
  { name: "AmazonSNSFullAccess", desc: "Provides full access to SNS topics.", doc: pdoc(allow("sns:*")) },
  { name: "SecretsManagerReadWrite", desc: "Provides full access to Secrets Manager secrets.", doc: pdoc(allow(["secretsmanager:*", "kms:DescribeKey", "kms:ListAliases", "kms:ListKeys"])) },
  { name: "CloudWatchFullAccess", desc: "Provides full access to metrics, logs, alarms and events.", doc: pdoc(allow(["cloudwatch:*", "logs:*", "events:*", "sns:*"])) },
  { name: "CloudWatchLogsFullAccess", desc: "Provides full access to log groups and streams.", doc: pdoc(allow("logs:*")) },
  { name: "AmazonSSMManagedInstanceCore", path: "/", desc: "Lets EC2 instances use Systems Manager and Parameter Store.", doc: pdoc(allow(["ssm:GetParameter", "ssm:GetParameters", "ssm:GetParametersByPath", "ssmmessages:*", "ec2messages:*"])) },
  { name: "AWSLambda_FullAccess", desc: "Provides full access to Lambda functions.", doc: pdoc(allow("lambda:*")) },
  { name: "AWSLambdaBasicExecutionRole", path: "/service-role/", desc: "Lets a Lambda function write its logs.", doc: pdoc(LAMBDA_LOGS) },
  { name: "AWSLambdaSQSQueueExecutionRole", path: "/service-role/", desc: "Lets a Lambda function read from SQS queues and write its logs.", doc: pdoc(allow(["sqs:ReceiveMessage", "sqs:DeleteMessage", "sqs:GetQueueAttributes"]), LAMBDA_LOGS) },
  { name: "AmazonECSTaskExecutionRolePolicy", path: "/service-role/", desc: "Lets ECS pull images from ECR and write container logs.", doc: pdoc(allow(["ecr:GetAuthorizationToken", "ecr:BatchCheckLayerAvailability", "ecr:GetDownloadUrlForLayer", "ecr:BatchGetImage", "logs:CreateLogStream", "logs:PutLogEvents"])) },
  { name: "AmazonAPIGatewayPushToCloudWatchLogs", path: "/service-role/", desc: "Lets API Gateway push logs to CloudWatch.", doc: pdoc(allow(["logs:CreateLogGroup", "logs:CreateLogStream", "logs:DescribeLogGroups", "logs:DescribeLogStreams", "logs:PutLogEvents", "logs:GetLogEvents", "logs:FilterLogEvents"])) },
  { name: "AmazonRDSEnhancedMonitoringRole", path: "/service-role/", desc: "Lets RDS publish enhanced monitoring metrics to CloudWatch Logs.", doc: pdoc(allow(["logs:CreateLogGroup", "logs:PutRetentionPolicy", "logs:CreateLogStream", "logs:PutLogEvents"])) },
  { name: "CloudWatchAgentServerPolicy", desc: "Lets the CloudWatch agent publish metrics and logs.", doc: pdoc(allow(["cloudwatch:PutMetricData", "ec2:DescribeVolumes", "ec2:DescribeTags", "logs:PutLogEvents", "logs:DescribeLogStreams", "logs:CreateLogStream", "logs:CreateLogGroup"])) },
]

const Q_ARN = `arn:aws:sqs:us-east-1:${ACCOUNT}:${NAMES.ordersQueue}`
const customPolicies = (): { name: string; desc: string; docs: PolicyDocument[]; created: number; updated: number }[] => [
  {
    name: "shop-orders-queue-access", desc: "Send and receive messages on the shop-orders queue.", created: 120 * DAY, updated: 34 * DAY,
    docs: [
      pdoc(allow(["sqs:SendMessage", "sqs:ReceiveMessage", "sqs:DeleteMessage", "sqs:GetQueueAttributes"], Q_ARN)),
      pdoc(allow(["sqs:SendMessage", "sqs:ReceiveMessage", "sqs:DeleteMessage", "sqs:GetQueueAttributes", "sqs:ChangeMessageVisibility"], [Q_ARN, `${Q_ARN}-dlq`])),
    ],
  },
  {
    name: "shop-uploads-readwrite", desc: "Read and write objects in the customer uploads bucket.", created: 98 * DAY, updated: 98 * DAY,
    docs: [pdoc(allow(["s3:GetObject", "s3:PutObject", "s3:DeleteObject"], `arn:aws:s3:::${NAMES.bucketUploads}/*`), allow(["s3:ListBucket"], `arn:aws:s3:::${NAMES.bucketUploads}`))],
  },
  {
    name: "shop-payments-secrets-read", desc: "Read the payments provider credentials and decrypt them.", created: 76 * DAY, updated: 12 * DAY,
    docs: [
      pdoc(allow("secretsmanager:GetSecretValue", `arn:aws:secretsmanager:us-east-1:${ACCOUNT}:secret:shop/prod/*`)),
      pdoc(
        allow(["secretsmanager:GetSecretValue", "secretsmanager:DescribeSecret"], `arn:aws:secretsmanager:us-east-1:${ACCOUNT}:secret:shop/prod/payments-*`),
        allow("kms:Decrypt", `arn:aws:kms:us-east-1:${ACCOUNT}:key/*`, { Condition: { StringEquals: { "kms:ViaService": "secretsmanager.us-east-1.amazonaws.com" } } }),
      ),
    ],
  },
  {
    name: "shop-deny-prod-delete", desc: "Guard rail: deny destructive calls against production resources.", created: 200 * DAY, updated: 200 * DAY,
    docs: [pdoc({ Effect: "Deny", Action: ["rds:DeleteDBInstance", "s3:DeleteBucket", "dynamodb:DeleteTable", "ec2:TerminateInstances"], Resource: "*", Condition: { StringEquals: { "aws:ResourceTag/env": "prod" } } })],
  },
  {
    name: "shop-ci-deploy", desc: "Permissions used by the CI pipeline to deploy the web tier.", created: 150 * DAY, updated: 9 * DAY,
    docs: [
      pdoc(allow(["ecr:*", "ecs:UpdateService", "ecs:DescribeServices", "autoscaling:StartInstanceRefresh"])),
      pdoc(
        allow(["ecr:GetAuthorizationToken", "ecr:BatchCheckLayerAvailability", "ecr:PutImage", "ecr:InitiateLayerUpload", "ecr:UploadLayerPart", "ecr:CompleteLayerUpload"]),
        allow(["ecs:UpdateService", "ecs:DescribeServices", "ecs:RegisterTaskDefinition"], `arn:aws:ecs:us-east-1:${ACCOUNT}:service/${NAMES.ecsCluster}/*`),
        allow("autoscaling:StartInstanceRefresh", `arn:aws:autoscaling:us-east-1:${ACCOUNT}:autoScalingGroup:*:autoScalingGroupName/${NAMES.asg}`),
        allow("iam:PassRole", `arn:aws:iam::${ACCOUNT}:role/shop-*`, { Condition: { StringEquals: { "iam:PassedToService": "ecs-tasks.amazonaws.com" } } }),
      ),
    ],
  },
  {
    name: "shop-devs-boundary", desc: "Permissions boundary for developers: no IAM changes, no billing.", created: 180 * DAY, updated: 180 * DAY,
    docs: [pdoc({ Effect: "Allow", NotAction: ["iam:*", "organizations:*", "aws-portal:*"], Resource: "*" })],
  },
]

// ---- seed ----

const key = (user: string, id: string, status: "Active" | "Inactive", created: number, used?: number): AccessKey => ({
  access_key_id: id, user_name: user, status, created_at: ago(created), ...(used !== undefined ? { last_used: ago(used) } : {}),
})

function seed(): IamState {
  const policies: StoredPolicy[] = [
    ...BUILTINS.map((b): StoredPolicy => {
      const path = b.path ?? "/"
      return {
        name: b.name, id: `ANPA${b.name.toUpperCase().replace(/[^A-Z0-9]/g, "").slice(0, 16).padEnd(16, "X")}`, arn: awsPolicyArn(b.name, path), path, description: b.desc,
        managed: true, document: b.doc, created_at: ago(400 * DAY), updated_at: ago(60 * DAY), default_version: "v1", next_version: 2,
        versions: [{ id: "v1", document: b.doc, created_at: ago(400 * DAY) }],
      }
    }),
    ...customPolicies().map((c): StoredPolicy => {
      const versions = c.docs.map((d, i) => ({ id: `v${i + 1}`, document: d, created_at: ago(i === c.docs.length - 1 ? c.updated : c.created) }))
      return {
        name: c.name, id: `ANPA${hex(16).toUpperCase()}`, arn: customPolicyArn(c.name), path: "/", description: c.desc, managed: false,
        document: c.docs[c.docs.length - 1], created_at: ago(c.created), updated_at: ago(c.updated), default_version: `v${c.docs.length}`, next_version: c.docs.length + 1, versions, tags: { team: "platform" },
      }
    }),
  ]
  const A = (n: string, p = "/") => (BUILTINS.some((b) => b.name === n) ? awsPolicyArn(n, BUILTINS.find((b) => b.name === n)!.path ?? p) : customPolicyArn(n))

  const users: IamUser[] = [
    {
      name: "demo-admin", id: "AIDADEMOADMIN000000", arn: userArn("demo-admin"), root: false, created_at: ago(310 * DAY), console_access: true, password_set_at: ago(45 * DAY), last_login: ago(4 * MIN), path: "/",
      groups: ["admins"], attached_policies: [], inline_policies: null, tags: { role: "owner" }, permissions_boundary: "",
    },
    {
      name: "alice", id: "AIDAALICEDEMO0000001", arn: userArn("alice"), root: false, created_at: ago(240 * DAY), console_access: true, password_set_at: ago(30 * DAY), last_login: ago(3 * HOUR), path: "/",
      groups: ["developers"], attached_policies: [A("shop-orders-queue-access")], inline_policies: null, tags: { team: "backend", email: "alice@example.com" }, permissions_boundary: A("shop-devs-boundary"),
    },
    {
      name: "bob", id: "AIDABOBDEMO000000002", arn: userArn("bob"), root: false, created_at: ago(180 * DAY), console_access: true, password_set_at: ago(120 * DAY), last_login: ago(2 * DAY), path: "/",
      groups: ["developers", "read-only"], attached_policies: [], inline_policies: {
        "bob-uploads-debug": pdoc(allow(["s3:GetObject", "s3:ListBucket"], [`arn:aws:s3:::${NAMES.bucketUploads}`, `arn:aws:s3:::${NAMES.bucketUploads}/*`])),
      }, tags: { team: "frontend", email: "bob@example.com" }, permissions_boundary: "",
    },
    {
      name: "carol", id: "AIDACAROLDEMO000003", arn: userArn("carol"), root: false, created_at: ago(70 * DAY), console_access: true, password_set_at: ago(70 * DAY), last_login: ago(19 * DAY), path: "/",
      groups: ["read-only"], attached_policies: [], inline_policies: null, tags: { team: "finance", email: "carol@example.com" }, permissions_boundary: "",
    },
    {
      name: "ci-deploy", id: "AIDACIDEPLOY000000004", arn: userArn("ci-deploy"), root: false, created_at: ago(150 * DAY), console_access: false, password_set_at: null, last_login: null, path: "/",
      groups: [], attached_policies: [A("shop-ci-deploy"), A("AmazonS3ReadOnlyAccess")], inline_policies: null, tags: { purpose: "ci-pipeline" }, permissions_boundary: "",
    },
  ]
  const keys: AccessKey[] = [
    key("demo-admin", "AKIADEMOEXAMPLE00001", "Active", 200 * DAY, 2 * HOUR),
    key("alice", "AKIADEMOEXAMPLE00002", "Active", 95 * DAY, 26 * HOUR),
    key("alice", "AKIADEMOEXAMPLE00003", "Inactive", 240 * DAY, 130 * DAY),
    key("ci-deploy", "AKIADEMOEXAMPLE00004", "Active", 60 * DAY, 25 * MIN),
    key("bob", "AKIADEMOEXAMPLE00005", "Active", 20 * DAY),
  ]
  const groups: IamGroup[] = [
    { name: "admins", id: "AGPADEMOADMINS000001", path: "/", arn: groupArn("admins"), created_at: ago(310 * DAY), attached_policies: [A("AdministratorAccess")], inline_policies: null, members: [] },
    { name: "developers", id: "AGPADEMODEVS0000002", path: "/", arn: groupArn("developers"), created_at: ago(300 * DAY), attached_policies: [A("PowerUserAccess"), A("shop-deny-prod-delete")], inline_policies: null, members: [] },
    { name: "read-only", id: "AGPADEMORO000000003", path: "/", arn: groupArn("read-only"), created_at: ago(250 * DAY), attached_policies: [A("ReadOnlyAccess")], inline_policies: null, members: [] },
    { name: "billing", id: "AGPADEMOBILLING0004", path: "/", arn: groupArn("billing"), created_at: ago(90 * DAY), attached_policies: [], inline_policies: { "billing-view": pdoc(allow(["aws-portal:ViewBilling", "ce:Get*"])) }, members: [] },
  ]
  const R = (name: string, description: string, trustDoc: TrustPolicyDocument, attached: string[], inline: Record<string, PolicyDocument> | null, age: number, used: number | null, extra: Partial<IamRole> = {}): IamRole => ({
    name, id: `AROA${hex(17).toUpperCase()}`, arn: roleArn(name, extra.path ?? "/"), path: "/", description, assume_role_policy: trustDoc,
    attached_policies: attached, inline_policies: inline, max_session_duration: 3600, created_at: ago(age), last_used: used === null ? null : ago(used), tags: { app: "shop" },
    trusted_services: [], permissions_boundary: "", instance_profiles: [], service_linked: "", ...extra,
  })
  const roles: IamRole[] = [
    R("shop-web-ec2-role", "Role assumed by the web tier EC2 instances.", svcTrust("ec2.amazonaws.com"), [A("AmazonSSMManagedInstanceCore"), A("CloudWatchAgentServerPolicy"), A("shop-uploads-readwrite")], null, 200 * DAY, 3 * MIN, { instance_profiles: ["shop-web-profile"] }),
    R("shop-batch-ec2-role", "Role for the nightly batch worker.", svcTrust("ec2.amazonaws.com"), [A("AmazonSSMManagedInstanceCore"), A("AmazonSQSFullAccess")], null, 160 * DAY, 7 * HOUR, { instance_profiles: ["shop-batch-profile"] }),
    R("shop-payments-lambda-role", "Execution role of the shop-payments Lambda function.", svcTrust("lambda.amazonaws.com"), [A("AWSLambdaBasicExecutionRole"), A("shop-payments-secrets-read")], {
      "orders-table-access": pdoc(allow(["dynamodb:PutItem", "dynamodb:GetItem", "dynamodb:UpdateItem"], `arn:aws:dynamodb:us-east-1:${ACCOUNT}:table/shop-orders`)),
    }, 120 * DAY, 11 * MIN),
    R("shop-fulfillment-sfn-role", "Lets the order-fulfillment state machine call Lambda and SQS.", svcTrust("states.amazonaws.com"), [], {
      "fulfillment-invoke": pdoc(allow("lambda:InvokeFunction", `arn:aws:lambda:us-east-1:${ACCOUNT}:function:shop-*`), allow(["sqs:SendMessage"], Q_ARN), allow("sns:Publish", `arn:aws:sns:us-east-1:${ACCOUNT}:${NAMES.ordersTopic}`)),
    }, 110 * DAY, 40 * MIN),
    R("shop-ecs-task-execution-role", "ECS task execution role (pull images, write logs).", svcTrust("ecs-tasks.amazonaws.com"), [A("AmazonECSTaskExecutionRolePolicy")], null, 100 * DAY, 20 * MIN),
    R("shop-ecs-worker-task-role", "Permissions of the worker containers.", svcTrust("ecs-tasks.amazonaws.com"), [A("shop-orders-queue-access")], null, 100 * DAY, 20 * MIN),
    R("shop-apigw-cloudwatch-role", "Lets API Gateway write access logs.", svcTrust("apigateway.amazonaws.com"), [A("AmazonAPIGatewayPushToCloudWatchLogs")], null, 90 * DAY, 5 * HOUR),
    R("shop-rds-monitoring-role", "RDS enhanced monitoring.", svcTrust("monitoring.rds.amazonaws.com"), [A("AmazonRDSEnhancedMonitoringRole")], null, 200 * DAY, 30 * MIN),
    R("ci-deploy-role", "Assumed by the ci-deploy user to run deployments.", trust({ AWS: userArn("ci-deploy") }, "sts:AssumeRole", { StringEquals: { "sts:ExternalId": "shop-ci-demo-external-id" } }), [A("shop-ci-deploy")], null, 90 * DAY, 25 * MIN, { max_session_duration: 7200 }),
    R("AWSServiceRoleForAutoScaling", "Service-linked role for Auto Scaling.", svcTrust("autoscaling.amazonaws.com"), [], null, 280 * DAY, 1 * HOUR, { path: "/aws-service-role/autoscaling.amazonaws.com/", service_linked: "autoscaling.amazonaws.com", tags: null }),
    R("AWSServiceRoleForElasticLoadBalancing", "Service-linked role for Elastic Load Balancing.", svcTrust("elasticloadbalancing.amazonaws.com"), [], null, 280 * DAY, 2 * HOUR, { path: "/aws-service-role/elasticloadbalancing.amazonaws.com/", service_linked: "elasticloadbalancing.amazonaws.com", tags: null }),
  ]
  for (const r of roles) {
    r.path = r.service_linked ? `/aws-service-role/${r.service_linked}/` : "/"
    r.arn = roleArn(r.name, r.path)
    r.trusted_services = trustedServices(r.assume_role_policy)
  }
  const profiles: InstanceProfile[] = [
    { name: "shop-web-profile", id: `AIPA${hex(17).toUpperCase()}`, arn: profileArn("shop-web-profile"), path: "/", roles: ["shop-web-ec2-role"], created_at: ago(200 * DAY), tags: { app: "shop" } },
    { name: "shop-batch-profile", id: `AIPA${hex(17).toUpperCase()}`, arn: profileArn("shop-batch-profile"), path: "/", roles: ["shop-batch-ec2-role"], created_at: ago(160 * DAY), tags: { app: "shop" } },
  ]
  const s: IamState = { users, groups, policies, roles, profiles, keys }
  for (const u of users) for (const g of u.groups) s.groups.find((x) => x.name === g)?.members.push(u.name)
  return s
}

function trustedServices(d: TrustPolicyDocument): string[] {
  const out: string[] = []
  for (const s of Array.isArray(d.Statement) ? d.Statement : [d.Statement]) {
    if (s.Effect !== "Allow" || !s.Principal || s.Principal === "*") continue
    const svc = s.Principal.Service
    if (svc) out.push(...(Array.isArray(svc) ? svc : [svc]))
  }
  return out
}

// ---- lookups ----

const policyRefName = (ref: string) => ref.slice(ref.lastIndexOf("/") + 1)
function findPolicy(ref: string): StoredPolicy {
  const p = st().policies.find((x) => x.name === policyRefName(ref))
  if (!p) throw noSuch(`Policy ${ref} does not exist or is not attachable.`)
  return p
}
const getUser = (n: string) => st().users.find((u) => u.name === n) ?? (() => { throw noSuch(`The user with name ${n} cannot be found.`) })()
const getGroup = (n: string) => st().groups.find((g) => g.name === n) ?? (() => { throw noSuch(`The group with name ${n} cannot be found.`) })()
const roleNm = (n: string) => policyRefName(n)
const getRole = (n: string) => st().roles.find((r) => r.name === roleNm(n)) ?? (() => { throw noSuch(`The role with name ${roleNm(n)} cannot be found.`) })()
const getProfile = (n: string) => st().profiles.find((p) => p.name === policyRefName(n)) ?? (() => { throw noSuch(`Instance Profile ${n} cannot be found.`) })()

function attachRef(list: string[], ref: string): string[] {
  const p = findPolicy(ref)
  if (list.includes(p.arn)) throw conflict(`Policy ${p.name} is already attached.`)
  return [...list, p.arn]
}
function detachRef(list: string[], ref: string, who: string): string[] {
  const n = policyRefName(ref)
  if (!list.some((a) => policyRefName(a) === n)) throw noSuch(`Policy ${ref} was not found attached to ${who}.`)
  return list.filter((a) => policyRefName(a) !== n)
}
const checkName = (kind: string, n: unknown): string => {
  if (typeof n !== "string" || !n) throw badRequest(`${kind} name is required`)
  if (!NAME_RE.test(n)) throw badRequest(`${kind} name must be 1-64 characters: letters, digits and + = , . @ _ -`)
  return n
}
const asDoc = (body: unknown): PolicyDocument => {
  let d = body as PolicyDocument | string
  if (typeof d === "string") {
    try {
      d = JSON.parse(d) as PolicyDocument
    } catch {
      throw err(400, "MalformedPolicyDocument", "Syntax errors in policy.")
    }
  }
  if (!d || typeof d !== "object" || !d.Statement) throw err(400, "MalformedPolicyDocument", "Syntax errors in policy: Statement is required.")
  return { ...d, Version: d.Version || "2012-10-17" }
}
const userView = (u: IamUser, withKeys = false): IamUser => ({
  ...u, ...(withKeys ? { access_keys: st().keys.filter((k) => k.user_name === u.name) } : {}),
})
const roleView = (r: IamRole): IamRole => ({
  ...r, trusted_services: trustedServices(r.assume_role_policy), instance_profiles: st().profiles.filter((p) => p.roles.includes(r.name)).map((p) => p.name),
})
const groupView = (g: IamGroup): IamGroup => ({ ...g, members: st().users.filter((u) => u.groups.includes(g.name)).map((u) => u.name) })

function attachments(p: StoredPolicy) {
  const has = (l: string[]) => l.some((a) => a === p.arn || a === p.name)
  return {
    users: st().users.filter((u) => has(u.attached_policies)).map((u) => u.name),
    groups: st().groups.filter((g) => has(g.attached_policies)).map((g) => g.name),
    roles: st().roles.filter((r) => has(r.attached_policies)).map((r) => r.name),
  }
}
const policySummary = (p: StoredPolicy): PolicySummary => {
  const a = attachments(p)
  return { name: p.name, arn: p.arn, path: p.path, description: p.description, managed: p.managed, created_at: p.created_at, updated_at: p.updated_at, attachment_count: a.users.length + a.groups.length + a.roles.length, default_version: p.default_version }
}
const versionView = (p: StoredPolicy, v: StoredPolicy["versions"][number]): PolicyVersion => ({ version_id: v.id, is_default: v.id === p.default_version, created_at: v.created_at, document: v.document })
const fullPolicy = (p: StoredPolicy): Policy => ({ ...p, versions: p.versions })

// ---- simulator ----

const globRe = (g: string, ci: boolean) => new RegExp("^" + g.replace(/[.+^${}()|[\]\\]/g, "\\$&").replace(/\*/g, ".*").replace(/\?/g, ".") + "$", ci ? "i" : "")
const asList = (x: string | string[] | undefined) => (x === undefined ? [] : Array.isArray(x) ? x : [x])
function stmtMatches(s: PolicyStatement, action: string, resource: string): boolean {
  const actOk = s.Action ? asList(s.Action).some((a) => globRe(a, true).test(action)) : s.NotAction ? !asList(s.NotAction).some((a) => globRe(a, true).test(action)) : false
  const resOk = s.Resource ? asList(s.Resource).some((r) => globRe(r, false).test(resource)) : s.NotResource ? !asList(s.NotResource).some((r) => globRe(r, false).test(resource)) : true
  return actOk && resOk && !s.Condition
}
function docsFor(entity: { attached_policies: string[]; inline_policies?: Record<string, PolicyDocument> | null }): PolicyDocument[] {
  const out: PolicyDocument[] = []
  for (const ref of entity.attached_policies) {
    const p = st().policies.find((x) => x.name === policyRefName(ref))
    if (p) out.push(p.document)
  }
  out.push(...Object.values(entity.inline_policies ?? {}))
  return out
}
function simulate(target: string, actions: string[], resource: string): SimulationResult[] {
  const s = st()
  const u = s.users.find((x) => x.name === target || x.arn === target)
  const r = s.roles.find((x) => x.name === target || x.arn === target)
  if (!u && !r) throw noSuch(`The user with name ${target} cannot be found.`)
  const ent = (u ?? r)!
  const docs = docsFor(ent)
  if (u) for (const g of u.groups) docs.push(...docsFor(getGroup(g)))
  const stmts = docs.flatMap((d) => (Array.isArray(d.Statement) ? d.Statement : [d.Statement]))
  const boundary = ent.permissions_boundary ? s.policies.find((p) => p.name === policyRefName(ent.permissions_boundary!)) : undefined
  const bStmts = boundary ? (Array.isArray(boundary.document.Statement) ? boundary.document.Statement : [boundary.document.Statement]) : null
  return actions.map((action) => {
    let decision: SimulationResult["decision"] = "implicitDeny"
    if (stmts.some((x) => x.Effect === "Deny" && stmtMatches(x, action, resource))) decision = "explicitDeny"
    else if (stmts.some((x) => x.Effect === "Allow" && stmtMatches(x, action, resource))) {
      if (!bStmts || bStmts.some((x) => x.Effect === "Allow" && stmtMatches(x, action, resource))) decision = "allowed"
    }
    return { action, resource, decision }
  })
}

// ---- routes ----

const tagsBody = (b: { tags?: Record<string, string> } | undefined) => b?.tags ?? {}

function routes(r: Router) {
  const P = "/api/v1/iam"

  r.get(`${P}/summary`, (): IamSummary => {
    const s = st()
    const custom = s.policies.filter((p) => !p.managed).length
    return {
      account_id: ACCOUNT, users: s.users.length, groups: s.groups.length, policies: s.policies.length, customer_policies: custom, managed_policies: s.policies.length - custom,
      access_keys: s.keys.length, mfa_devices: 2, roles: s.roles.length, instance_profiles: s.profiles.length,
    }
  })

  // users
  r.get(`${P}/users`, () =>
    st().users.map((u) => {
      const ks = st().keys.filter((k) => k.user_name === u.name)
      const used = ks.map((k) => k.last_used).filter(Boolean).sort().pop() ?? null
      return { ...userView(u), access_key_count: ks.length, active_access_keys: ks.filter((k) => k.status === "Active").length, access_key_last_used: used }
    }),
  )
  r.post(`${P}/users`, ({ body }) => {
    const name = checkName("User", body?.name)
    if (st().users.some((u) => u.name === name)) throw exists(`User with name ${name} already exists.`)
    const u: IamUser = {
      name, id: `AIDA${hex(17).toUpperCase()}`, arn: userArn(name, body.path || "/"), root: false, created_at: new Date().toISOString(), console_access: !!body.password,
      password_set_at: body.password ? new Date().toISOString() : null, last_login: null, path: body.path || "/", groups: [], attached_policies: [], inline_policies: null,
      tags: tagsBody(body), permissions_boundary: body.permissions_boundary ? findPolicy(body.permissions_boundary).arn : "",
    }
    for (const g of (body.groups ?? []) as string[]) {
      getGroup(g)
      u.groups.push(g)
    }
    for (const p of (body.policies ?? []) as string[]) u.attached_policies = attachRef(u.attached_policies, p)
    st().users.push(u)
    return userView(u)
  })
  r.get(`${P}/users/:name`, ({ params }) => userView(getUser(params.name), true))
  r.del(`${P}/users/:name`, ({ params }) => {
    const u = getUser(params.name)
    st().users = st().users.filter((x) => x !== u)
    st().keys = st().keys.filter((k) => k.user_name !== u.name)
  })
  r.put(`${P}/users/:name/tags`, ({ params, body }) => {
    const u = getUser(params.name)
    u.tags = tagsBody(body)
    return userView(u)
  })
  r.put(`${P}/users/:name/password`, ({ params, body }) => {
    const u = getUser(params.name)
    if (!body?.password || String(body.password).length < 8) throw badRequest("password must be at least 8 characters")
    u.console_access = true
    u.password_set_at = new Date().toISOString()
    return userView(u)
  })
  r.del(`${P}/users/:name/password`, ({ params }) => {
    const u = getUser(params.name)
    u.console_access = false
    u.password_set_at = null
    return userView(u)
  })
  r.post(`${P}/users/:name/policies`, ({ params, body }) => {
    const u = getUser(params.name)
    u.attached_policies = attachRef(u.attached_policies, body?.policy)
    return userView(u)
  })
  r.del(`${P}/users/:name/policies/:policy`, ({ params }) => {
    const u = getUser(params.name)
    u.attached_policies = detachRef(u.attached_policies, params.policy, `user ${u.name}`)
    return userView(u)
  })
  r.put(`${P}/users/:name/inline-policies/:policy`, ({ params, body }) => {
    const u = getUser(params.name)
    checkName("Policy", params.policy)
    u.inline_policies = { ...(u.inline_policies ?? {}), [params.policy]: asDoc(body) }
    return userView(u)
  })
  r.del(`${P}/users/:name/inline-policies/:policy`, ({ params }) => {
    const u = getUser(params.name)
    if (!u.inline_policies?.[params.policy]) throw noSuch(`The user policy with name ${params.policy} cannot be found.`)
    const { [params.policy]: _gone, ...rest } = u.inline_policies
    u.inline_policies = Object.keys(rest).length ? rest : null
    return userView(u)
  })
  r.put(`${P}/users/:name/permissions-boundary`, ({ params, body }) => {
    const u = getUser(params.name)
    u.permissions_boundary = findPolicy(body?.policy ?? "").arn
    return userView(u)
  })
  r.del(`${P}/users/:name/permissions-boundary`, ({ params }) => {
    const u = getUser(params.name)
    u.permissions_boundary = ""
    return userView(u)
  })
  r.get(`${P}/users/:name/access-keys`, ({ params }) => {
    getUser(params.name)
    return st().keys.filter((k) => k.user_name === params.name)
  })
  r.post(`${P}/users/:name/access-keys`, ({ params }) => {
    const u = getUser(params.name)
    if (st().keys.filter((k) => k.user_name === u.name).length >= 2) throw err(409, "LimitExceeded", "Cannot exceed quota for AccessKeysPerUser: 2")
    const k: AccessKey = { access_key_id: `AKIADEMO${hex(12).toUpperCase()}`, user_name: u.name, status: "Active", created_at: new Date().toISOString() }
    st().keys.push(k)
    return { access_key_id: k.access_key_id, secret_access_key: "demo-not-a-real-secret-" + hex(8), user_name: u.name, status: k.status, created_at: k.created_at }
  })
  r.patch(`${P}/users/:name/access-keys/:key`, ({ params, body }) => {
    const k = st().keys.find((x) => x.user_name === params.name && x.access_key_id === params.key)
    if (!k) throw noSuch(`The Access Key with id ${params.key} cannot be found.`)
    if (body?.status !== "Active" && body?.status !== "Inactive") throw badRequest("status must be Active or Inactive")
    k.status = body.status
    return k
  })
  r.del(`${P}/users/:name/access-keys/:key`, ({ params }) => {
    const before = st().keys.length
    st().keys = st().keys.filter((x) => !(x.user_name === params.name && x.access_key_id === params.key))
    if (st().keys.length === before) throw noSuch(`The Access Key with id ${params.key} cannot be found.`)
  })

  // groups
  r.get(`${P}/groups`, () => st().groups.map(groupView))
  r.post(`${P}/groups`, ({ body }) => {
    const name = checkName("Group", body?.name)
    if (st().groups.some((g) => g.name === name)) throw exists(`Group with name ${name} already exists.`)
    const g: IamGroup = { name, id: `AGPA${hex(17).toUpperCase()}`, path: "/", arn: groupArn(name), created_at: new Date().toISOString(), attached_policies: [], inline_policies: null, members: [] }
    for (const p of (body.policies ?? []) as string[]) g.attached_policies = attachRef(g.attached_policies, p)
    st().groups.push(g)
    return groupView(g)
  })
  r.get(`${P}/groups/:name`, ({ params }) => groupView(getGroup(params.name)))
  r.del(`${P}/groups/:name`, ({ params }) => {
    const g = getGroup(params.name)
    for (const u of st().users) u.groups = u.groups.filter((x) => x !== g.name)
    st().groups = st().groups.filter((x) => x !== g)
  })
  r.post(`${P}/groups/:name/members`, ({ params, body }) => {
    const g = getGroup(params.name)
    const u = getUser(body?.user)
    if (u.groups.includes(g.name)) throw conflict(`User ${u.name} is already a member of ${g.name}.`)
    u.groups.push(g.name)
    return userView(u)
  })
  r.del(`${P}/groups/:name/members/:user`, ({ params }) => {
    const g = getGroup(params.name)
    const u = getUser(params.user)
    if (!u.groups.includes(g.name)) throw noSuch(`User ${u.name} is not a member of ${g.name}.`)
    u.groups = u.groups.filter((x) => x !== g.name)
    return userView(u)
  })
  r.post(`${P}/groups/:name/policies`, ({ params, body }) => {
    const g = getGroup(params.name)
    g.attached_policies = attachRef(g.attached_policies, body?.policy)
    return groupView(g)
  })
  r.del(`${P}/groups/:name/policies/:policy`, ({ params }) => {
    const g = getGroup(params.name)
    g.attached_policies = detachRef(g.attached_policies, params.policy, `group ${g.name}`)
    return groupView(g)
  })
  r.put(`${P}/groups/:name/inline-policies/:policy`, ({ params, body }) => {
    const g = getGroup(params.name)
    checkName("Policy", params.policy)
    g.inline_policies = { ...(g.inline_policies ?? {}), [params.policy]: asDoc(body) }
    return groupView(g)
  })
  r.del(`${P}/groups/:name/inline-policies/:policy`, ({ params }) => {
    const g = getGroup(params.name)
    if (!g.inline_policies?.[params.policy]) throw noSuch(`The group policy with name ${params.policy} cannot be found.`)
    const { [params.policy]: _gone, ...rest } = g.inline_policies
    g.inline_policies = Object.keys(rest).length ? rest : null
    return groupView(g)
  })

  // policies
  r.get(`${P}/policies`, ({ query }) =>
    st().policies.filter((p) => !(query.scope === "managed" && !p.managed) && !(query.scope === "local" && p.managed)).map(policySummary),
  )
  r.post(`${P}/policies`, ({ body }) => {
    const name = checkName("Policy", body?.name)
    if (st().policies.some((p) => p.name === name)) throw exists(`A policy called ${name} already exists. Duplicate names are not allowed.`)
    const doc = asDoc(body.document)
    const now = new Date().toISOString()
    const p: StoredPolicy = {
      name, id: `ANPA${hex(16).toUpperCase()}`, arn: customPolicyArn(name), path: "/", description: body.description ?? "", managed: false, document: doc, created_at: now, updated_at: now,
      default_version: "v1", next_version: 2, versions: [{ id: "v1", document: doc, created_at: now }], tags: tagsBody(body),
    }
    st().policies.push(p)
    return fullPolicy(p)
  })
  r.get(`${P}/policies/:name`, ({ params }) => {
    const p = findPolicy(params.name)
    return { policy: fullPolicy(p), attachments: attachments(p) }
  })
  const newVersion = (p: StoredPolicy, doc: PolicyDocument, makeDefault: boolean) => {
    if (p.managed) throw err(400, "UnmodifiableEntity", "AWS managed policies cannot be modified.")
    if (p.versions.length >= 5) {
      const oldest = p.versions.find((v) => v.id !== p.default_version)
      if (!oldest) throw err(409, "LimitExceeded", "A managed policy can have up to 5 versions.")
      p.versions = p.versions.filter((v) => v !== oldest)
    }
    const v = { id: `v${p.next_version++}`, document: doc, created_at: new Date().toISOString() }
    p.versions.push(v)
    if (makeDefault) {
      p.default_version = v.id
      p.document = doc
      p.updated_at = v.created_at
    }
    return v
  }
  r.put(`${P}/policies/:name`, ({ params, body }) => {
    const p = findPolicy(params.name)
    newVersion(p, asDoc(body?.document), true)
    if (typeof body?.description === "string") p.description = body.description
    return fullPolicy(p)
  })
  r.del(`${P}/policies/:name`, ({ params }) => {
    const p = findPolicy(params.name)
    if (p.managed) throw err(400, "UnmodifiableEntity", "AWS managed policies cannot be deleted.")
    const a = attachments(p)
    if (a.users.length + a.groups.length + a.roles.length) throw err(409, "DeleteConflict", "Cannot delete a policy attached to entities.")
    st().policies = st().policies.filter((x) => x !== p)
  })
  r.get(`${P}/policies/:name/versions`, ({ params }) => {
    const p = findPolicy(params.name)
    return [...p.versions].reverse().map((v) => versionView(p, v))
  })
  r.post(`${P}/policies/:name/versions`, ({ params, body }) => {
    const p = findPolicy(params.name)
    return versionView(p, newVersion(p, asDoc(body?.document), !!body?.set_as_default))
  })
  r.put(`${P}/policies/:name/default-version`, ({ params, body }) => {
    const p = findPolicy(params.name)
    const v = p.versions.find((x) => x.id === body?.version)
    if (!v) throw noSuch(`Policy version ${body?.version} not found.`)
    p.default_version = v.id
    p.document = v.document
    return fullPolicy(p)
  })
  r.del(`${P}/policies/:name/versions/:version`, ({ params }) => {
    const p = findPolicy(params.name)
    if (params.version === p.default_version) throw err(409, "DeleteConflict", "Cannot delete the default version of a policy.")
    if (!p.versions.some((v) => v.id === params.version)) throw noSuch(`Policy version ${params.version} not found.`)
    p.versions = p.versions.filter((v) => v.id !== params.version)
  })
  r.post(`${P}/simulate`, ({ body }) => {
    const actions = (body?.actions ?? []) as string[]
    if (!actions.length) throw badRequest("actions is required")
    return simulate(body.source_arn || body.user, actions, body.resource || "*")
  })

  // roles
  r.get(`${P}/roles`, () => st().roles.map(roleView))
  r.post(`${P}/roles`, ({ body }) => {
    const name = checkName("Role", body?.name)
    if (st().roles.some((x) => x.name === name)) throw exists(`Role with name ${name} already exists.`)
    let td = body.assume_role_policy
    if (typeof td === "string") {
      try {
        td = JSON.parse(td)
      } catch {
        throw err(400, "MalformedPolicyDocument", "Syntax errors in the trust policy.")
      }
    }
    if (!td?.Statement) throw err(400, "MalformedPolicyDocument", "assume_role_policy is required.")
    const dur = body.max_session_duration || 3600
    if (dur < 3600 || dur > 43200) throw badRequest("max_session_duration must be between 3600 and 43200 seconds")
    const path = body.path || "/"
    const role: IamRole = {
      name, id: `AROA${hex(17).toUpperCase()}`, arn: roleArn(name, path), path, description: body.description ?? "", assume_role_policy: td, attached_policies: [], inline_policies: null,
      max_session_duration: dur, created_at: new Date().toISOString(), last_used: null, tags: tagsBody(body), trusted_services: [], permissions_boundary: body.permissions_boundary ? findPolicy(body.permissions_boundary).arn : "",
      instance_profiles: [], service_linked: "",
    }
    for (const p of (body.policies ?? []) as string[]) role.attached_policies = attachRef(role.attached_policies, p)
    st().roles.push(role)
    return roleView(role)
  })
  r.get(`${P}/roles/:name`, ({ params }) => roleView(getRole(params.name)))
  r.del(`${P}/roles/:name`, ({ params, query }) => {
    const role = getRole(params.name)
    const inUse = st().profiles.some((p) => p.roles.includes(role.name))
    if (query.force !== "true" && (role.attached_policies.length || inUse || role.inline_policies)) {
      throw err(409, "DeleteConflict", `Cannot delete entity, must detach all policies first${inUse ? " and remove the role from its instance profiles" : ""}.`)
    }
    for (const p of st().profiles) p.roles = p.roles.filter((x) => x !== role.name)
    st().roles = st().roles.filter((x) => x !== role)
  })
  r.patch(`${P}/roles/:name`, ({ params, body }) => {
    const role = getRole(params.name)
    if (typeof body?.description === "string") role.description = body.description
    if (typeof body?.max_session_duration === "number") {
      if (body.max_session_duration < 3600 || body.max_session_duration > 43200) throw badRequest("max_session_duration must be between 3600 and 43200 seconds")
      role.max_session_duration = body.max_session_duration
    }
    if (body?.tags) role.tags = body.tags
    return roleView(role)
  })
  r.put(`${P}/roles/:name/trust-policy`, ({ params, body }) => {
    const role = getRole(params.name)
    role.assume_role_policy = asDoc(body) as unknown as TrustPolicyDocument
    return roleView(role)
  })
  r.post(`${P}/roles/:name/policies`, ({ params, body }) => {
    const role = getRole(params.name)
    role.attached_policies = attachRef(role.attached_policies, body?.policy)
    return roleView(role)
  })
  r.del(`${P}/roles/:name/policies/:policy`, ({ params }) => {
    const role = getRole(params.name)
    role.attached_policies = detachRef(role.attached_policies, params.policy, `role ${role.name}`)
    return roleView(role)
  })
  r.put(`${P}/roles/:name/inline-policies/:policy`, ({ params, body }) => {
    const role = getRole(params.name)
    checkName("Policy", params.policy)
    role.inline_policies = { ...(role.inline_policies ?? {}), [params.policy]: asDoc(body) }
    return roleView(role)
  })
  r.del(`${P}/roles/:name/inline-policies/:policy`, ({ params }) => {
    const role = getRole(params.name)
    if (!role.inline_policies?.[params.policy]) throw noSuch(`The role policy with name ${params.policy} cannot be found.`)
    const { [params.policy]: _gone, ...rest } = role.inline_policies
    role.inline_policies = Object.keys(rest).length ? rest : null
    return roleView(role)
  })
  r.post(`${P}/roles/:name/revoke-sessions`, ({ params }) => {
    getRole(params.name)
  })
  r.put(`${P}/roles/:name/permissions-boundary`, ({ params, body }) => {
    const role = getRole(params.name)
    role.permissions_boundary = findPolicy(body?.policy ?? "").arn
    return roleView(role)
  })
  r.del(`${P}/roles/:name/permissions-boundary`, ({ params }) => {
    const role = getRole(params.name)
    role.permissions_boundary = ""
    return roleView(role)
  })
  r.put(`${P}/roles/:name/tags`, ({ params, body }) => {
    const role = getRole(params.name)
    role.tags = { ...(role.tags ?? {}), ...tagsBody(body) }
    return roleView(role)
  })
  r.del(`${P}/roles/:name/tags`, ({ params, query }: Req) => {
    const role = getRole(params.name)
    const keys = (query.keys ?? "").split(",").map((k) => k.trim()).filter(Boolean)
    if (!keys.length) throw badRequest("name the tags to remove with ?keys=")
    role.tags = Object.fromEntries(Object.entries(role.tags ?? {}).filter(([k]) => !keys.includes(k)))
    return roleView(role)
  })
  r.post("/api/v1/sts/assume-role", ({ body }): TempCredentials => {
    const role = getRole(body?.role ?? "")
    const dur = Number(body?.duration_seconds) || 3600
    if (dur > role.max_session_duration) throw err(400, "ValidationError", `The requested DurationSeconds exceeds the MaxSessionDuration set for this role (${role.max_session_duration}).`)
    const session = body?.session_name || "homecloud-" + hex(8)
    return {
      access_key_id: `ASIADEMO${hex(12).toUpperCase()}`, secret_access_key: "demo-not-a-real-secret-" + hex(8), session_token: "demo-not-a-real-session-token-" + hex(24),
      expiration: new Date(Date.now() + dur * 1000).toISOString(), assumed_role_arn: `arn:aws:sts::${ACCOUNT}:assumed-role/${role.name}/${session}`, assumed_role_id: `${role.id}:${session}`,
    }
  })

  // instance profiles
  r.get(`${P}/instance-profiles`, () => st().profiles)
  r.post(`${P}/instance-profiles`, ({ body }) => {
    const name = checkName("Instance profile", body?.name)
    if (st().profiles.some((p) => p.name === name)) throw exists(`Instance Profile ${name} already exists.`)
    const p: InstanceProfile = { name, id: `AIPA${hex(17).toUpperCase()}`, arn: profileArn(name), path: body.path || "/", roles: [], created_at: new Date().toISOString(), tags: {} }
    if (body.role) p.roles.push(getRole(body.role).name)
    st().profiles.push(p)
    return p
  })
  r.get(`${P}/instance-profiles/:name`, ({ params }) => getProfile(params.name))
  r.del(`${P}/instance-profiles/:name`, ({ params }) => {
    const p = getProfile(params.name)
    if (p.roles.length) throw err(409, "DeleteConflict", "Cannot delete entity, must remove roles from instance profile first.")
    st().profiles = st().profiles.filter((x) => x !== p)
  })
  r.post(`${P}/instance-profiles/:name/roles`, ({ params, body }) => {
    const p = getProfile(params.name)
    const role = getRole(body?.role ?? "")
    if (p.roles.length) throw err(409, "LimitExceeded", "Cannot exceed quota for InstanceSessionsPerInstanceProfile: 1")
    p.roles.push(role.name)
    return p
  })
  r.del(`${P}/instance-profiles/:name/roles/:role`, ({ params }) => {
    const p = getProfile(params.name)
    if (!p.roles.includes(params.role)) throw noSuch(`Role ${params.role} is not in instance profile ${p.name}.`)
    p.roles = p.roles.filter((x) => x !== params.role)
    return p
  })
}

const service: DemoService = { name: "iam", seed, routes }
export default service
