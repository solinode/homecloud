// Seed stacks for the CloudFormation demo: shop-network, shop-app, shop-data
// (all healthy) and shop-sandbox (a failed creation that was rolled back).

import type { Stack, StackEvent, StackResource } from "@/lib/types"
import { AMI, NAMES, SG, SUBNET, VPC_SHOP } from "../ids"
import { DAY, HOUR, MIN, ago, arn, stableId } from "../util"
import { parseTemplate } from "./cfn-template"

const NETWORK_TEMPLATE = `Description: Shop network - VPC, subnets and security groups
Parameters:
  Env:
    Type: String
    Default: production
    AllowedValues: [production, staging]
  VpcCidr:
    Type: String
    Default: 10.20.0.0/16
    Description: CIDR block of the shop VPC
Resources:
  ShopVpc:
    Type: HC::EC2::VPC
    Properties:
      name: shop-vpc
      cidr_block: !Ref VpcCidr
      tags: {Env: !Ref Env, Project: shop}
  PublicSubnetA:
    Type: HC::EC2::Subnet
    Properties: {name: shop-public-a, vpc_id: !Ref ShopVpc, cidr_block: 10.20.1.0/24, availability_zone: us-east-1a}
  PublicSubnetB:
    Type: HC::EC2::Subnet
    Properties: {name: shop-public-b, vpc_id: !Ref ShopVpc, cidr_block: 10.20.2.0/24, availability_zone: us-east-1b}
  PrivateSubnetA:
    Type: HC::EC2::Subnet
    Properties: {name: shop-private-a, vpc_id: !Ref ShopVpc, cidr_block: 10.20.11.0/24, availability_zone: us-east-1a}
  PrivateSubnetB:
    Type: HC::EC2::Subnet
    Properties: {name: shop-private-b, vpc_id: !Ref ShopVpc, cidr_block: 10.20.12.0/24, availability_zone: us-east-1b}
  AlbSecurityGroup:
    Type: HC::EC2::SecurityGroup
    Properties:
      name: shop-alb
      description: Public HTTP and HTTPS to the load balancer
      vpc_id: !Ref ShopVpc
  WebSecurityGroup:
    Type: HC::EC2::SecurityGroup
    Properties:
      name: shop-web
      description: Web tier, reachable from the load balancer only
      vpc_id: !Ref ShopVpc
      source_group: !Ref AlbSecurityGroup
  DbSecurityGroup:
    Type: HC::EC2::SecurityGroup
    Properties:
      name: shop-db
      description: PostgreSQL from the web tier
      vpc_id: !Ref ShopVpc
      source_group: !Ref WebSecurityGroup
Outputs:
  VpcId:
    Description: ID of the shop VPC
    Value: !Ref ShopVpc
  PublicSubnets:
    Value: !Sub "\${PublicSubnetA},\${PublicSubnetB}"
  PrivateSubnets:
    Value: !Sub "\${PrivateSubnetA},\${PrivateSubnetB}"
  WebSecurityGroupId:
    Value: !Ref WebSecurityGroup
`

const APP_TEMPLATE = `Description: Shop application tier - queues, payments function, load balancer and web Auto Scaling group
Parameters:
  Env:
    Type: String
    Default: production
  DesiredCapacity:
    Type: Number
    Default: 2
  WebImageId:
    Type: String
    Default: ${AMI.shopWeb}
Resources:
  OrdersDlq:
    Type: HC::SQS::Queue
    Properties: {name: shop-orders-dlq, retention_seconds: 1209600}
  OrdersQueue:
    Type: HC::SQS::Queue
    Properties:
      name: shop-orders
      visibility_timeout: 60
      redrive_policy: {dead_letter_queue: !Ref OrdersDlq, max_receive_count: 5}
  OrderEventsTopic:
    Type: HC::SNS::Topic
    Properties: {name: shop-order-events}
  AlertsTopic:
    Type: HC::SNS::Topic
    Properties: {name: shop-alerts}
  PaymentsFunction:
    Type: HC::Lambda::Function
    Properties:
      name: shop-payments
      runtime: python3.12
      memory_mb: 256
      timeout_seconds: 10
      environment: {ORDERS_QUEUE: !Ref OrdersQueue, ALERTS_TOPIC: !GetAtt AlertsTopic.arn, ENV: !Ref Env}
  PaymentsTrigger:
    Type: HC::Lambda::EventSourceMapping
    Properties: {function_name: !Ref PaymentsFunction, queue_name: !Ref OrdersQueue, batch_size: 10}
  PaymentsLogGroup:
    Type: HC::Logs::LogGroup
    Properties: {name: /aws/lambda/shop-payments, retention_days: 30}
  WebTargetGroup:
    Type: HC::ELB::TargetGroup
    Properties: {name: shop-web-tg, protocol: HTTP, port: 80, health_check_path: /healthz}
  WebLoadBalancer:
    Type: HC::ELB::LoadBalancer
    Properties: {name: shop-alb, scheme: internet-facing, target_group: !Ref WebTargetGroup}
  WebAsg:
    Type: HC::AutoScaling::AutoScalingGroup
    Properties:
      name: shop-web-asg
      image_id: !Ref WebImageId
      instance_type: t3.small
      min_size: 2
      max_size: 6
      desired_capacity: !Ref DesiredCapacity
      target_group: !Ref WebTargetGroup
  WebCpuAlarm:
    Type: HC::CloudWatch::Alarm
    Properties:
      name: shop-web-cpu-high
      namespace: HC/AutoScaling
      metric: CPUUtilization
      threshold: 70
      alarm_actions: [!GetAtt AlertsTopic.arn]
      group: !Ref WebAsg
Outputs:
  OrdersQueueArn:
    Value: !GetAtt OrdersQueue.arn
  PaymentsFunctionName:
    Value: !Ref PaymentsFunction
  LoadBalancerName:
    Value: !Ref WebLoadBalancer
  AlertsTopicArn:
    Value: !GetAtt AlertsTopic.arn
`

const DATA_TEMPLATE = `Description: Shop data tier - orders table, PostgreSQL database and buckets
Parameters:
  Env:
    Type: String
    Default: production
  DbInstanceClass:
    Type: String
    Default: db.t3.medium
    AllowedValues: [db.t3.small, db.t3.medium, db.m5.large]
  DbPassword:
    Type: String
    NoEcho: true
    Description: Master password of the database
Resources:
  OrdersTable:
    Type: HC::DynamoDB::Table
    Properties:
      name: shop-orders
      partition_key: {name: customer_id, type: S}
      sort_key: {name: order_id, type: S}
  DbPasswordSecret:
    Type: HC::SecretsManager::Secret
    Properties: {name: shop/db/password, secret_string: !Ref DbPassword}
  Database:
    Type: HC::RDS::DBInstance
    Properties:
      id: shop-db
      engine: postgres
      engine_version: "16.3"
      instance_class: !Ref DbInstanceClass
      allocated_storage: 100
      master_password: !Ref DbPassword
  AssetsBucket:
    Type: HC::S3::Bucket
    Properties: {name: shop-assets-prod}
  UploadsBucket:
    Type: HC::S3::Bucket
    Properties: {name: shop-uploads-prod}
  BackupsBucket:
    Type: HC::S3::Bucket
    Properties: {name: shop-db-backups, versioning: true}
Outputs:
  OrdersTableName:
    Value: !Ref OrdersTable
  DatabaseId:
    Value: !Ref Database
  AssetsBucketName:
    Value: !Ref AssetsBucket
`

const SANDBOX_TEMPLATE = `Description: Scratch stack for trying the image pipeline (failed)
Resources:
  ScratchBucket:
    Type: HC::S3::Bucket
    Properties: {name: shop-scratch-images}
  ThumbnailFunction:
    Type: HC::Lambda::Function
    Properties:
      name: shop-thumbnailer
      runtime: nodejs20.x
      environment: {BUCKET: !Ref ScratchBucket}
`

type Res = [id: string, type: string, physical: string, attrs?: Record<string, unknown>]

function resources(list: Res[], createdMs: number, stepMs = 9000): { map: Record<string, StackResource>; order: string[]; events: StackEvent[] } {
  const map: Record<string, StackResource> = {}
  const order: string[] = []
  const ev: StackEvent[] = [{ time: ago(createdMs), logical_id: "", type: "HC::CloudFormation::Stack", status: "CREATE_IN_PROGRESS", reason: "user initiated" }]
  list.forEach(([id, type, physical, attrs], i) => {
    const t0 = createdMs - 2000 - i * stepMs
    map[id] = { logical_id: id, type, physical_id: physical, status: "CREATE_COMPLETE", attributes: { name: physical, ...(attrs ?? {}) }, updated_at: ago(t0 - 6000) }
    order.push(id)
    ev.push({ time: ago(t0), logical_id: id, type, status: "CREATE_IN_PROGRESS" })
    ev.push({ time: ago(t0 - 6000), logical_id: id, type, status: "CREATE_COMPLETE" })
  })
  ev.push({ time: ago(createdMs - 2000 - list.length * stepMs - 3000), logical_id: "", type: "HC::CloudFormation::Stack", status: "CREATE_COMPLETE" })
  return { map, order, events: ev }
}

function outputsOf(template: string, phys: Record<string, string>, attrs: Record<string, Record<string, unknown>>, params: Record<string, unknown>): Record<string, unknown> {
  const out: Record<string, unknown> = {}
  for (const o of parseTemplate(template).outputs) {
    const ref = /!Ref\s+(\w+)/.exec(o.text)
    const ga = /!GetAtt\s+(\w+)\.(\w+)/.exec(o.text)
    const sub = /!Sub\s+"([^"]*)"/.exec(o.text)
    if (ref) out[o.name] = phys[ref[1]]
    else if (ga) out[o.name] = attrs[ga[1]]?.[ga[2]] ?? phys[ga[1]]
    else if (sub) out[o.name] = sub[1].replace(/\$\{(\w+)\}/g, (_, k) => phys[k] ?? String(params[k] ?? k))
  }
  return out
}

export function seedStacks(): Stack[] {
  const stacks: Stack[] = []
  const finish = (name: string, template: string, params: Record<string, unknown>, list: Res[], createdMs: number, extra: Partial<Stack> = {}) => {
    const r = resources(list, createdMs)
    const phys: Record<string, string> = {}
    const attrs: Record<string, Record<string, unknown>> = {}
    for (const [id, , p, a] of list) {
      phys[id] = p
      attrs[id] = { name: p, ...(a ?? {}) }
    }
    const t = parseTemplate(template)
    const events = r.events.map((e) => ({ ...e, logical_id: e.logical_id || name })).reverse()
    stacks.push({
      name,
      arn: arn("cloudformation", `stack/${name}/${stableId("", name, 8)}-${stableId("", name + "x", 4)}`),
      status: "CREATE_COMPLETE",
      status_reason: "",
      description: t.description,
      template,
      parameters: params,
      resources: r.map,
      order: r.order,
      outputs: outputsOf(template, phys, attrs, params),
      events,
      created_at: ago(createdMs),
      updated_at: ago(createdMs - list.length * 9000 - 3000),
      ...extra,
    })
  }

  finish(
    "shop-network",
    NETWORK_TEMPLATE,
    { Env: "production", VpcCidr: "10.20.0.0/16" },
    [
      ["ShopVpc", "HC::EC2::VPC", VPC_SHOP],
      ["PublicSubnetA", "HC::EC2::Subnet", SUBNET.publicA],
      ["PublicSubnetB", "HC::EC2::Subnet", SUBNET.publicB],
      ["PrivateSubnetA", "HC::EC2::Subnet", SUBNET.privateA],
      ["PrivateSubnetB", "HC::EC2::Subnet", SUBNET.privateB],
      ["AlbSecurityGroup", "HC::EC2::SecurityGroup", SG.alb],
      ["WebSecurityGroup", "HC::EC2::SecurityGroup", SG.web],
      ["DbSecurityGroup", "HC::EC2::SecurityGroup", SG.db],
    ],
    74 * DAY,
  )

  finish(
    "shop-app",
    APP_TEMPLATE,
    { Env: "production", DesiredCapacity: 2, WebImageId: AMI.shopWeb },
    [
      ["OrdersDlq", "HC::SQS::Queue", NAMES.ordersDlq, { arn: arn("sqs", NAMES.ordersDlq) }],
      ["OrdersQueue", "HC::SQS::Queue", NAMES.ordersQueue, { arn: arn("sqs", NAMES.ordersQueue) }],
      ["OrderEventsTopic", "HC::SNS::Topic", NAMES.ordersTopic, { arn: arn("sns", NAMES.ordersTopic) }],
      ["AlertsTopic", "HC::SNS::Topic", NAMES.alertsTopic, { arn: arn("sns", NAMES.alertsTopic) }],
      ["PaymentsFunction", "HC::Lambda::Function", NAMES.paymentsFn, { arn: arn("lambda", `function:${NAMES.paymentsFn}`) }],
      ["PaymentsTrigger", "HC::Lambda::EventSourceMapping", "6f1d0c62-5f0e-4a63-9d0b-2b7a1e9c41aa"],
      ["PaymentsLogGroup", "HC::Logs::LogGroup", `/aws/lambda/${NAMES.paymentsFn}`],
      ["WebTargetGroup", "HC::ELB::TargetGroup", NAMES.tgWeb, { arn: arn("elasticloadbalancing", `targetgroup/${NAMES.tgWeb}/3f2a9c1d8e7b6a50`) }],
      ["WebLoadBalancer", "HC::ELB::LoadBalancer", NAMES.alb, { dns_name: `${NAMES.alb}-1284769301.us-east-1.elb.homecloud.local` }],
      ["WebAsg", "HC::AutoScaling::AutoScalingGroup", NAMES.asg],
      ["WebCpuAlarm", "HC::CloudWatch::Alarm", "shop-web-cpu-high"],
    ],
    70 * DAY,
  )
  // shop-app was updated 6 days ago (new web image and capacity)
  const app = stacks[1]
  const u0 = 6 * DAY + 3 * HOUR
  const upd: StackEvent[] = [
    { time: ago(u0 - 2 * MIN - 30_000), logical_id: app.name, type: "HC::CloudFormation::Stack", status: "UPDATE_COMPLETE" },
    { time: ago(u0 - 2 * MIN - 5000), logical_id: "WebAsg", type: "HC::AutoScaling::AutoScalingGroup", status: "UPDATE_COMPLETE" },
    { time: ago(u0 - 2 * MIN), logical_id: "WebAsg", type: "HC::AutoScaling::AutoScalingGroup", status: "UPDATE_IN_PROGRESS", reason: "properties changed; replacing" },
    { time: ago(u0 - 1 * MIN), logical_id: "PaymentsFunction", type: "HC::Lambda::Function", status: "UPDATE_COMPLETE" },
    { time: ago(u0 - 50_000), logical_id: "PaymentsFunction", type: "HC::Lambda::Function", status: "UPDATE_IN_PROGRESS", reason: "properties changed; replacing" },
    { time: ago(u0 - 20_000), logical_id: app.name, type: "HC::CloudFormation::Stack", status: "UPDATE_IN_PROGRESS", reason: "user initiated" },
  ]
  app.events = [...upd, ...(app.events ?? [])]
  app.status = "UPDATE_COMPLETE"
  app.updated_at = ago(u0 - 2 * MIN - 30_000)

  finish(
    "shop-data",
    DATA_TEMPLATE,
    { Env: "production", DbInstanceClass: "db.t3.medium", DbPassword: "****" },
    [
      ["OrdersTable", "HC::DynamoDB::Table", "shop-orders", { arn: arn("dynamodb", "table/shop-orders") }],
      ["DbPasswordSecret", "HC::SecretsManager::Secret", "shop/db/password", { arn: arn("secretsmanager", "secret:shop/db/password-a1B2c3") }],
      ["Database", "HC::RDS::DBInstance", NAMES.db, { endpoint: `${NAMES.db}.c1x2y3z4.us-east-1.rds.homecloud.local` }],
      ["AssetsBucket", "HC::S3::Bucket", NAMES.bucketAssets, { arn: `arn:aws:s3:::${NAMES.bucketAssets}` }],
      ["UploadsBucket", "HC::S3::Bucket", NAMES.bucketUploads, { arn: `arn:aws:s3:::${NAMES.bucketUploads}` }],
      ["BackupsBucket", "HC::S3::Bucket", NAMES.bucketBackups, { arn: `arn:aws:s3:::${NAMES.bucketBackups}` }],
    ],
    70 * DAY - 25 * MIN,
    { updated_at: ago(70 * DAY - 25 * MIN - 90_000) },
  )

  // a creation that failed and was rolled back: only the stack remains
  const sbx = 3 * DAY + 5 * HOUR
  const ev = (mins: number, id: string, type: string, status: string, reason?: string): StackEvent => ({ time: ago(sbx - mins * 60_000), logical_id: id, type, status, ...(reason ? { reason } : {}) })
  const reason = "ThumbnailFunction: function code is required: set code.files or code.zip"
  stacks.push({
    name: "shop-sandbox",
    arn: arn("cloudformation", `stack/shop-sandbox/${stableId("", "shop-sandbox", 8)}-${stableId("", "shop-sandboxx", 4)}`),
    status: "ROLLBACK_COMPLETE",
    status_reason: reason,
    description: "Scratch stack for trying the image pipeline (failed)",
    template: SANDBOX_TEMPLATE,
    parameters: {},
    resources: {},
    order: [],
    outputs: {},
    events: [
      ev(0.9, "shop-sandbox", "HC::CloudFormation::Stack", "ROLLBACK_COMPLETE", reason),
      ev(0.8, "ScratchBucket", "HC::S3::Bucket", "DELETE_COMPLETE", "rolled back"),
      ev(0.6, "ScratchBucket", "HC::S3::Bucket", "DELETE_IN_PROGRESS", "rolling back"),
      ev(0.4, "ThumbnailFunction", "HC::Lambda::Function", "CREATE_FAILED", reason),
      ev(0.3, "ThumbnailFunction", "HC::Lambda::Function", "CREATE_IN_PROGRESS"),
      ev(0.2, "ScratchBucket", "HC::S3::Bucket", "CREATE_COMPLETE"),
      ev(0.1, "ScratchBucket", "HC::S3::Bucket", "CREATE_IN_PROGRESS"),
      ev(0, "shop-sandbox", "HC::CloudFormation::Stack", "CREATE_IN_PROGRESS", "user initiated"),
    ],
    created_at: ago(sbx),
    updated_at: ago(sbx - 0.9 * 60_000),
  })
  return stacks
}
