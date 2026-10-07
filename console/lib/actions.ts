import {
  BadgeCheck,
  Bell,
  Cpu,
  Database,
  FunctionSquare,
  Globe,
  HardDrive,
  KeyRound,
  Layers,
  ListOrdered,
  Scaling,
  Terminal,
  UserPlus,
  UsersRound,
  type LucideIcon,
} from "lucide-react"

export interface QuickAction {
  href: string
  label: string
  icon: LucideIcon
  /** shown as the primary quick actions on Console Home */
  featured?: boolean
  hint?: string
}

/** Create flows reachable from Console Home and the command palette. */
export const QUICK_ACTIONS: QuickAction[] = [
  { href: "/ec2/launch/", label: "Launch an instance", icon: Cpu, featured: true, hint: "Container or VM in a VPC subnet" },
  { href: "/s3/?create=1", label: "Create a bucket", icon: HardDrive, featured: true, hint: "S3-compatible object storage" },
  { href: "/lambda/create/", label: "Deploy a function", icon: FunctionSquare, featured: true, hint: "Python, Node.js, Java, Ruby or custom runtimes" },
  { href: "/rds/create/", label: "Create a database", icon: Database, featured: true, hint: "Postgres, MySQL, MariaDB or MongoDB" },
  { href: "/cloudformation/create/", label: "Create a stack", icon: Layers },
  { href: "/ec2/autoscaling/create/", label: "Create an Auto Scaling group", icon: Scaling },
  { href: "/sqs/create/", label: "Create a queue", icon: ListOrdered },
  { href: "/iam/users/?create=1", label: "Add an IAM user", icon: UserPlus },
  { href: "/secrets/create/", label: "Store a secret", icon: KeyRound },
  { href: "/route53/?create=1", label: "Create a hosted zone", icon: Globe },
  { href: "/acm/?request=1", label: "Request a certificate", icon: BadgeCheck },
  { href: "/cognito/?create=1", label: "Create a user pool", icon: UsersRound },
  { href: "/cloudwatch/alarms/?create=1", label: "Create an alarm", icon: Bell },
  { href: "/cloudwatch/logs/", label: "View logs", icon: Terminal },
]

export const DOCS_URL = "https://github.com/solinode/homecloud/tree/main/docs"
export const REPO_URL = "https://github.com/solinode/homecloud"
