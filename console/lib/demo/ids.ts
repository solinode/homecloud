// Fixed identifiers for the "shop" demo company, shared across services so
// cross-references (instance -> subnet -> VPC -> security group ...) line up.
//
// The story: "Shop", a small web store. A web Auto Scaling group behind an
// Application Load Balancer, an orders SQS queue, a payments Lambda, a Postgres
// RDS database and a Redis ElastiCache cluster, all inside shop-vpc.

import { stableId } from "./util"

export const VPC_DEFAULT = stableId("vpc-", "default")
export const VPC_SHOP = stableId("vpc-", "shop-vpc")

export const SUBNET = {
  defaultA: stableId("subnet-", "default-a"),
  defaultB: stableId("subnet-", "default-b"),
  publicA: stableId("subnet-", "shop-public-a"),
  publicB: stableId("subnet-", "shop-public-b"),
  privateA: stableId("subnet-", "shop-private-a"),
  privateB: stableId("subnet-", "shop-private-b"),
}

export const IGW_DEFAULT = stableId("igw-", "default")
export const IGW_SHOP = stableId("igw-", "shop")

export const SG = {
  defaultVpc: stableId("sg-", "default-default"),
  shopDefault: stableId("sg-", "shop-default"),
  alb: stableId("sg-", "shop-alb"),
  web: stableId("sg-", "shop-web"),
  db: stableId("sg-", "shop-db"),
  cache: stableId("sg-", "shop-cache"),
  bastion: stableId("sg-", "shop-bastion"),
}

export const INSTANCE = {
  web1: stableId("i-", "shop-web-1"),
  web2: stableId("i-", "shop-web-2"),
  bastion: stableId("i-", "shop-bastion"),
  batch: stableId("i-", "shop-batch-worker"),
  dev: stableId("i-", "dev-sandbox"),
}

export const AMI = {
  ubuntu: stableId("ami-", "ubuntu-24.04"),
  debian: stableId("ami-", "debian-12"),
  alpine: stableId("ami-", "alpine-3.20"),
  amazonLinux: stableId("ami-", "amazon-linux-2023"),
  shopWeb: stableId("ami-", "shop-web-v14"),
}

export const KEY_PAIRS = ["shop-prod", "shop-dev", "alice-laptop"]

export const EIP = {
  bastion: stableId("eipalloc-", "bastion"),
  nat: stableId("eipalloc-", "nat"),
}

export const VOLUME = {
  web1: stableId("vol-", "web-1-root"),
  web2: stableId("vol-", "web-2-root"),
  bastion: stableId("vol-", "bastion-root"),
  data: stableId("vol-", "batch-data"),
  scratch: stableId("vol-", "unattached-scratch"),
}

/** Names */
export const NAMES = {
  alb: "shop-alb",
  tgWeb: "shop-web-tg",
  asg: "shop-web-asg",
  launchTemplate: "shop-web-lt",
  ecsCluster: "shop-cluster",
  ecsWorker: "shop-worker",
  db: "shop-db",
  cache: "shop-cache",
  ordersQueue: "shop-orders",
  ordersDlq: "shop-orders-dlq",
  paymentsFn: "shop-payments",
  ordersTopic: "shop-order-events",
  alertsTopic: "shop-alerts",
  stateMachine: "shop-order-fulfillment",
  api: "shop-api",
  zone: "shop.example.com",
  userPool: "shop-customers",
  efs: "shop-media",
  bucketAssets: "shop-assets-prod",
  bucketUploads: "shop-uploads-prod",
  bucketBackups: "shop-db-backups",
  bucketLogs: "shop-alb-logs",
  bucketTfState: "shop-terraform-state",
}

/** EFS file systems (owned by the efs demo service). */
export const FS = {
  media: stableId("fs-", "shop-media"),
  backups: stableId("fs-", "shop-backups"),
  scratch: stableId("fs-", "dev-scratch"),
}

/** Cognito user pool "shop-customers" (see svc/cognito.ts). */
export const COGNITO_POOL_ID = "us-east-1_8F2A1C9D3"
