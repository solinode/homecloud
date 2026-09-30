// Entry point of the demo backend. Only ever loaded through a dynamic import
// behind NEXT_PUBLIC_DEMO, so normal builds never bundle it.

import { dispatch, getState, load, onDemoChange, register, resetDemo, unavailable, DemoError, type DemoService } from "./engine"
import acm from "./svc/acm"
import apigateway from "./svc/apigateway"
import autoscaling from "./svc/autoscaling"
import cfn from "./svc/cfn"
import cloudwatch from "./svc/cloudwatch"
import cognito from "./svc/cognito"
import dynamodb from "./svc/dynamodb"
import ec2 from "./svc/ec2"
import ecr from "./svc/ecr"
import ecs from "./svc/ecs"
import efs from "./svc/efs"
import elb from "./svc/elb"
import events from "./svc/events"
import iam from "./svc/iam"
import kms from "./svc/kms"
import lambda from "./svc/lambda"
import rds from "./svc/rds"
import route53 from "./svc/route53"
import s3 from "./svc/s3"
import secrets from "./svc/secrets"
import sfn from "./svc/sfn"
import sns from "./svc/sns"
import sqs from "./svc/sqs"
import ssm from "./svc/ssm"
import system from "./svc/system"
import trail from "./svc/trail"
import vpc from "./svc/vpc"

const services: DemoService[] = [
  system, ec2, vpc, efs, autoscaling, elb, lambda, apigateway, ecs, ecr, sfn, events, cfn, s3, dynamodb, rds, sqs, sns,
  cloudwatch, trail, iam, kms, secrets, ssm, route53, acm, cognito,
]

let ready = false
function boot() {
  if (ready) return
  ready = true
  load(services)
  register(services)
}

export interface DemoRequest {
  method: string
  path: string
  query?: Record<string, string>
  body?: unknown
  headers?: Record<string, string>
  raw?: unknown
}

/** demoRequest answers an API call in the browser. Throws DemoError on failure. */
export async function demoRequest<T = unknown>(r: DemoRequest): Promise<T> {
  boot()
  const res = await dispatch(r.method, r.path, r.query ?? {}, r.body, r.headers, r.raw)
  return res.data as T
}

/** demoSyncGet runs a GET synchronously-ish for download links: resolves the JSON/text body. */
export function demoReset() {
  boot()
  resetDemo()
}

export { DemoError, getState, onDemoChange, unavailable }
