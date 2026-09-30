import {
  Activity,
  BadgeCheck,
  Container,
  Globe,
  UsersRound,
  FolderOpen,
  LockKeyhole,
  Package,
  SlidersHorizontal,
  Split,
  Workflow,
  Layers,
  Bell,
  Boxes,
  Cable,
  Cpu,
  Database,
  DatabaseZap,
  FunctionSquare,
  HardDrive,
  KeyRound,
  ListOrdered,
  Megaphone,
  Network,
  Route,
  ScrollText,
  ShieldCheck,
  Table2,
  type LucideIcon,
} from "lucide-react"

export interface NavItem {
  label: string
  href: string
  /** extra path prefixes that should highlight this item (detail pages) */
  match?: string[]
}

export interface NavSection {
  title?: string
  items: NavItem[]
}

export interface ServiceDef {
  id: string
  name: string
  /** short AWS-style name shown in the side nav header */
  short: string
  description: string
  category: string
  icon: LucideIcon
  /** tile/icon accent color (tailwind classes) */
  color: string
  href: string
  /** path prefixes owned by this service */
  paths: string[]
  comingSoon?: boolean
  nav?: NavSection[]
}

export const CATEGORIES = [
  "Compute",
  "Containers",
  "Storage",
  "Database",
  "Networking & Content Delivery",
  "Security, Identity & Compliance",
  "Management & Governance",
  "Application Integration",
] as const

export const SERVICES: ServiceDef[] = [
  {
    id: "ec2",
    name: "EC2",
    short: "EC2",
    description: "Virtual servers in the cloud",
    category: "Compute",
    icon: Cpu,
    color: "bg-orange-500/10 text-orange-600 dark:text-orange-400",
    href: "/ec2/",
    paths: ["/ec2"],
    nav: [
      {
        title: "Instances",
        items: [
          { label: "Instances", href: "/ec2/", match: ["/ec2/instance/", "/ec2/launch/"] },
          { label: "Instance types", href: "/ec2/instance-types/" },
        ],
      },
      {
        title: "Images",
        items: [{ label: "AMIs", href: "/ec2/images/" }],
      },
      {
        title: "Auto Scaling",
        items: [{ label: "Auto Scaling groups", href: "/ec2/autoscaling/", match: ["/ec2/autoscaling/group/", "/ec2/autoscaling/create/"] }],
      },
      {
        title: "Elastic Block Store",
        items: [
          { label: "Volumes", href: "/ec2/volumes/" },
          { label: "Snapshots", href: "/ec2/snapshots/" },
        ],
      },
      {
        title: "Load Balancing",
        items: [
          { label: "Load balancers", href: "/elb/" },
          { label: "Target groups", href: "/elb/target-groups/" },
        ],
      },
      {
        title: "Network & Security",
        items: [
          { label: "Security groups", href: "/vpc/security-groups/" },
          { label: "Key pairs", href: "/ec2/key-pairs/" },
          { label: "Elastic IPs", href: "/ec2/elastic-ips/" },
          { label: "VPCs", href: "/vpc/" },
        ],
      },
    ],
  },
  {
    id: "lambda",
    name: "Lambda",
    short: "Lambda",
    description: "Run code without thinking about servers",
    category: "Compute",
    icon: FunctionSquare,
    color: "bg-orange-500/10 text-orange-600 dark:text-orange-400",
    href: "/lambda/",
    paths: ["/lambda"],
    nav: [
      {
        title: "Lambda",
        items: [
          { label: "Functions", href: "/lambda/", match: ["/lambda/function/", "/lambda/create/"] },
          { label: "Event source mappings", href: "/lambda/triggers/" },
        ],
      },
      {
        title: "Additional resources",
        items: [{ label: "Layers", href: "/lambda/layers/" }],
      },
      { title: "Related", items: [{ label: "API Gateway", href: "/apigateway/" }] },
    ],
  },
  {
    id: "s3",
    name: "S3",
    short: "Amazon S3",
    description: "Scalable object storage",
    category: "Storage",
    icon: HardDrive,
    color: "bg-emerald-500/10 text-emerald-600 dark:text-emerald-400",
    href: "/s3/",
    paths: ["/s3"],
    nav: [{ items: [{ label: "Buckets", href: "/s3/", match: ["/s3/bucket/"] }] }],
  },
  {
    id: "rds",
    name: "RDS",
    short: "RDS",
    description: "Managed relational and document databases",
    category: "Database",
    icon: Database,
    color: "bg-blue-500/10 text-blue-600 dark:text-blue-400",
    href: "/rds/",
    paths: ["/rds"],
    nav: [
      {
        items: [
          { label: "Databases", href: "/rds/", match: ["/rds/instance/", "/rds/create/"] },
          { label: "Snapshots", href: "/rds/snapshots/" },
        ],
      },
      { title: "Related", items: [{ label: "ElastiCache", href: "/elasticache/" }] },
    ],
  },
  {
    id: "elasticache",
    name: "ElastiCache",
    short: "ElastiCache",
    description: "In-memory caching (Redis, Valkey, Memcached)",
    category: "Database",
    icon: DatabaseZap,
    color: "bg-blue-500/10 text-blue-600 dark:text-blue-400",
    href: "/elasticache/",
    paths: ["/elasticache"],
    nav: [
      {
        title: "Resources",
        items: [
          { label: "Clusters", href: "/elasticache/", match: ["/elasticache/cluster/", "/elasticache/create/"] },
          { label: "Backups", href: "/elasticache/backups/" },
        ],
      },
    ],
  },
  {
    id: "dynamodb",
    name: "DynamoDB",
    short: "DynamoDB",
    description: "Managed NoSQL key-value database",
    category: "Database",
    icon: Table2,
    color: "bg-blue-500/10 text-blue-600 dark:text-blue-400",
    href: "/dynamodb/",
    paths: ["/dynamodb"],
    nav: [{ items: [{ label: "Tables", href: "/dynamodb/", match: ["/dynamodb/table/", "/dynamodb/create/"] }] }],
  },
  {
    id: "vpc",
    name: "VPC",
    short: "VPC",
    description: "Isolated virtual networks",
    category: "Networking & Content Delivery",
    icon: Network,
    color: "bg-violet-500/10 text-violet-600 dark:text-violet-400",
    href: "/vpc/",
    paths: ["/vpc"],
    nav: [
      {
        title: "Virtual private cloud",
        items: [
          { label: "Your VPCs", href: "/vpc/" },
          { label: "Subnets", href: "/vpc/subnets/" },
          { label: "Route tables", href: "/vpc/route-tables/" },
          { label: "Internet gateways", href: "/vpc/internet-gateways/" },
        ],
      },
      {
        title: "Security",
        items: [{ label: "Security groups", href: "/vpc/security-groups/", match: ["/vpc/security-group/"] }],
      },
    ],
  },
  {
    id: "apigateway",
    name: "API Gateway",
    short: "API Gateway",
    description: "Create, publish and secure APIs",
    category: "Networking & Content Delivery",
    icon: Route,
    color: "bg-violet-500/10 text-violet-600 dark:text-violet-400",
    href: "/apigateway/",
    paths: ["/apigateway"],
    nav: [
      { items: [{ label: "APIs", href: "/apigateway/", match: ["/apigateway/api/"] }] },
      { title: "Related", items: [{ label: "Cognito user pools", href: "/cognito/" }] },
    ],
  },
  {
    id: "iam",
    name: "IAM",
    short: "IAM",
    description: "Manage access to HomeCloud resources",
    category: "Security, Identity & Compliance",
    icon: ShieldCheck,
    color: "bg-red-500/10 text-red-600 dark:text-red-400",
    href: "/iam/",
    paths: ["/iam"],
    nav: [
      { items: [{ label: "Dashboard", href: "/iam/" }] },
      {
        title: "Access management",
        items: [
          { label: "Users", href: "/iam/users/", match: ["/iam/user/"] },
          { label: "User groups", href: "/iam/groups/", match: ["/iam/group/"] },
          { label: "Roles", href: "/iam/roles/", match: ["/iam/role/", "/iam/roles/"] },
          { label: "Policies", href: "/iam/policies/", match: ["/iam/policy/"] },
          { label: "Instance profiles", href: "/iam/instance-profiles/", match: ["/iam/instance-profile/"] },
        ],
      },
      { title: "Tools", items: [{ label: "Policy simulator", href: "/iam/simulator/" }] },
    ],
  },
  {
    id: "secrets",
    name: "Secrets Manager",
    short: "Secrets Manager",
    description: "Store and rotate credentials securely",
    category: "Security, Identity & Compliance",
    icon: KeyRound,
    color: "bg-red-500/10 text-red-600 dark:text-red-400",
    href: "/secrets/",
    paths: ["/secrets"],
    nav: [{ items: [{ label: "Secrets", href: "/secrets/", match: ["/secrets/secret/", "/secrets/create/"] }] }],
  },
  {
    id: "cloudwatch",
    name: "CloudWatch",
    short: "CloudWatch",
    description: "Metrics, logs and alarms",
    category: "Management & Governance",
    icon: Activity,
    color: "bg-pink-500/10 text-pink-600 dark:text-pink-400",
    href: "/cloudwatch/",
    paths: ["/cloudwatch"],
    nav: [
      { items: [{ label: "Overview", href: "/cloudwatch/" }] },
      { title: "Alarms", items: [{ label: "All alarms", href: "/cloudwatch/alarms/" }] },
      {
        title: "Logs",
        items: [
          { label: "Log groups", href: "/cloudwatch/logs/", match: ["/cloudwatch/logs/group/"] },
          { label: "Logs Insights", href: "/cloudwatch/insights/" },
        ],
      },
      { title: "Metrics", items: [{ label: "All metrics", href: "/cloudwatch/metrics/" }] },
    ],
  },
  {
    id: "cloudtrail",
    name: "CloudTrail",
    short: "CloudTrail",
    description: "Audit log of every API call",
    category: "Management & Governance",
    icon: ScrollText,
    color: "bg-pink-500/10 text-pink-600 dark:text-pink-400",
    href: "/cloudtrail/",
    paths: ["/cloudtrail"],
    nav: [{ items: [{ label: "Event history", href: "/cloudtrail/" }] }],
  },
  {
    id: "sqs",
    name: "SQS",
    short: "SQS",
    description: "Managed message queues",
    category: "Application Integration",
    icon: ListOrdered,
    color: "bg-fuchsia-500/10 text-fuchsia-600 dark:text-fuchsia-400",
    href: "/sqs/",
    paths: ["/sqs"],
    nav: [{ items: [{ label: "Queues", href: "/sqs/", match: ["/sqs/queue/", "/sqs/create/"] }] }],
  },
  {
    id: "sns",
    name: "SNS",
    short: "SNS",
    description: "Pub/sub notifications",
    category: "Application Integration",
    icon: Megaphone,
    color: "bg-fuchsia-500/10 text-fuchsia-600 dark:text-fuchsia-400",
    href: "/sns/",
    paths: ["/sns"],
    nav: [
      {
        items: [
          { label: "Topics", href: "/sns/", match: ["/sns/topic/"] },
          { label: "Subscriptions", href: "/sns/subscriptions/" },
        ],
      },
    ],
  },
  {
    id: "eventbridge",
    name: "EventBridge",
    short: "EventBridge",
    description: "Serverless event bus and schedules",
    category: "Application Integration",
    icon: Cable,
    color: "bg-fuchsia-500/10 text-fuchsia-600 dark:text-fuchsia-400",
    href: "/events/",
    paths: ["/events"],
    nav: [
      {
        title: "Buses",
        items: [
          { label: "Rules", href: "/events/", match: ["/events/rule/", "/events/create/"] },
          { label: "Event buses", href: "/events/buses/" },
          { label: "Send events", href: "/events/send/" },
        ],
      },
      { title: "Scheduler", items: [{ label: "Schedules", href: "/events/scheduler/" }] },
    ],
  },
  {
    id: "ecs",
    name: "ECS",
    short: "Elastic Container Service",
    description: "Run containerized services and tasks",
    category: "Containers",
    icon: Container,
    color: "bg-orange-500/10 text-orange-600 dark:text-orange-400",
    href: "/ecs/",
    paths: ["/ecs"],
    nav: [
      {
        items: [
          { label: "Services", href: "/ecs/", match: ["/ecs/service/", "/ecs/create/"] },
          { label: "Task definitions", href: "/ecs/task-definitions/", match: ["/ecs/task-definition/"] },
          { label: "Tasks", href: "/ecs/tasks/", match: ["/ecs/task/"] },
        ],
      },
      { title: "Related", items: [{ label: "ECR repositories", href: "/ecr/" }, { label: "Load balancers", href: "/elb/" }] },
    ],
  },
  {
    id: "ecr",
    name: "ECR",
    short: "Elastic Container Registry",
    description: "Private Docker image registry",
    category: "Containers",
    icon: Package,
    color: "bg-orange-500/10 text-orange-600 dark:text-orange-400",
    href: "/ecr/",
    paths: ["/ecr"],
    nav: [{ items: [{ label: "Repositories", href: "/ecr/", match: ["/ecr/repository/"] }] }],
  },
  {
    id: "efs",
    name: "EFS",
    short: "Elastic File System",
    description: "Shared file systems for instances",
    category: "Storage",
    icon: FolderOpen,
    color: "bg-emerald-500/10 text-emerald-600 dark:text-emerald-400",
    href: "/efs/",
    paths: ["/efs"],
    nav: [{ items: [{ label: "File systems", href: "/efs/", match: ["/efs/file-system/"] }] }],
  },
  {
    id: "elb",
    name: "ELB",
    short: "Elastic Load Balancing",
    description: "Load balancers and target groups",
    category: "Networking & Content Delivery",
    icon: Split,
    color: "bg-violet-500/10 text-violet-600 dark:text-violet-400",
    href: "/elb/",
    paths: ["/elb"],
    nav: [
      {
        title: "Load balancing",
        items: [
          { label: "Load balancers", href: "/elb/", match: ["/elb/load-balancer/", "/elb/create/"] },
          { label: "Target groups", href: "/elb/target-groups/", match: ["/elb/target-group/"] },
        ],
      },
      { title: "Related", items: [{ label: "Certificates (ACM)", href: "/acm/" }, { label: "Hosted zones (Route 53)", href: "/route53/" }] },
    ],
  },
  {
    id: "kms",
    name: "KMS",
    short: "Key Management Service",
    description: "Create and control encryption keys",
    category: "Security, Identity & Compliance",
    icon: LockKeyhole,
    color: "bg-red-500/10 text-red-600 dark:text-red-400",
    href: "/kms/",
    paths: ["/kms"],
    nav: [{ items: [{ label: "Customer managed keys", href: "/kms/", match: ["/kms/key/"] }, { label: "Encrypt / decrypt", href: "/kms/crypto/" }] }],
  },
  {
    id: "ssm",
    name: "Systems Manager",
    short: "Systems Manager",
    description: "Parameter Store for configuration and secrets",
    category: "Management & Governance",
    icon: SlidersHorizontal,
    color: "bg-pink-500/10 text-pink-600 dark:text-pink-400",
    href: "/ssm/",
    paths: ["/ssm"],
    nav: [{ title: "Application management", items: [{ label: "Parameter Store", href: "/ssm/", match: ["/ssm/parameter/", "/ssm/create/"] }] }],
  },
  {
    id: "sfn",
    name: "Step Functions",
    short: "Step Functions",
    description: "Visual workflows for distributed applications",
    category: "Application Integration",
    icon: Workflow,
    color: "bg-fuchsia-500/10 text-fuchsia-600 dark:text-fuchsia-400",
    href: "/sfn/",
    paths: ["/sfn"],
    nav: [
      {
        items: [
          { label: "State machines", href: "/sfn/", match: ["/sfn/state-machine/", "/sfn/create/", "/sfn/execution/"] },
        ],
      },
    ],
  },
  {
    id: "cloudformation",
    name: "CloudFormation",
    short: "CloudFormation",
    description: "Model and provision resources with templates",
    category: "Management & Governance",
    icon: Layers,
    color: "bg-pink-500/10 text-pink-600 dark:text-pink-400",
    href: "/cloudformation/",
    paths: ["/cloudformation"],
    nav: [
      {
        items: [
          { label: "Stacks", href: "/cloudformation/", match: ["/cloudformation/stack/", "/cloudformation/create/"] },
          { label: "Resource types", href: "/cloudformation/resource-types/" },
        ],
      },
    ],
  },
  {
    id: "route53",
    name: "Route 53",
    short: "Route 53",
    description: "DNS hosted zones for your VPCs and LAN",
    category: "Networking & Content Delivery",
    icon: Globe,
    color: "bg-violet-500/10 text-violet-600 dark:text-violet-400",
    href: "/route53/",
    paths: ["/route53"],
    nav: [
      { title: "DNS management", items: [{ label: "Hosted zones", href: "/route53/", match: ["/route53/zone/"] }] },
      { title: "Related", items: [{ label: "Load balancers", href: "/elb/" }, { label: "VPCs", href: "/vpc/" }] },
    ],
  },
  {
    id: "acm",
    name: "Certificate Manager",
    short: "Certificate Manager",
    description: "TLS certificates from a private CA or imported",
    category: "Security, Identity & Compliance",
    icon: BadgeCheck,
    color: "bg-red-500/10 text-red-600 dark:text-red-400",
    href: "/acm/",
    paths: ["/acm"],
    nav: [
      { items: [{ label: "Certificates", href: "/acm/", match: ["/acm/certificate/"] }] },
      { title: "Related", items: [{ label: "Load balancers", href: "/elb/" }] },
    ],
  },
  {
    id: "cognito",
    name: "Cognito",
    short: "Cognito",
    description: "User sign-up, sign-in and JWTs for your apps",
    category: "Security, Identity & Compliance",
    icon: UsersRound,
    color: "bg-red-500/10 text-red-600 dark:text-red-400",
    href: "/cognito/",
    paths: ["/cognito"],
    nav: [
      { items: [{ label: "User pools", href: "/cognito/", match: ["/cognito/pool/"] }] },
      { title: "Related", items: [{ label: "API Gateway", href: "/apigateway/" }] },
    ],
  },
]

export const ALARM_ICON = Bell
export const GENERIC_ICON = Boxes

/** serviceForPath returns the service owning a pathname. VPC pages linked
 * from EC2 keep the VPC nav. */
export function serviceForPath(pathname: string): ServiceDef | undefined {
  const p = pathname.endsWith("/") ? pathname : `${pathname}/`
  return SERVICES.find((s) => s.paths.some((x) => p === `${x}/` || p.startsWith(`${x}/`)))
}

export function servicesByCategory(list: ServiceDef[] = SERVICES) {
  return CATEGORIES.map((c) => ({ category: c, services: list.filter((s) => s.category === c) })).filter((g) => g.services.length > 0)
}
