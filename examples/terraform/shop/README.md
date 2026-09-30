# A full app on HomeCloud with Terraform

This is a small "shop" stack written with the stock `hashicorp/aws` provider. Nothing in it is
HomeCloud specific except the provider's `endpoints {}` block, so the same files deploy to AWS if you
delete that block. It works with Terraform and with OpenTofu.

What it builds:

| Piece | Resources |
| --- | --- |
| Network | VPC, two public subnets, internet gateway, route table, ALB / web / database security groups |
| Edge | Application load balancer, target group, HTTP and HTTPS listeners, a public Route 53 zone, an ACM certificate validated by a Route 53 CNAME, a private zone with an alias record to the ALB |
| Compute | ECS cluster, Fargate task definition (nginx, `awslogs` to a CloudWatch log group) and a two-task service behind the ALB |
| Data | RDS Postgres in a DB subnet group with `manage_master_user_password` (the password lives in Secrets Manager), versioned S3 bucket, DynamoDB table |
| Messaging | SNS topic subscribed to an SQS queue (with a dead-letter queue) that a Lambda consumes through an event source mapping |
| Serverless | Python Lambda functions zipped inline with `archive_file`, an IAM role, an API Gateway HTTP API in front of one of them |

## Run it

You need a running HomeCloud (`homecloud serve`, Docker or OrbStack) and Terraform 1.5+ or OpenTofu.

```bash
eval "$(homecloud aws-env)"                       # AWS_ACCESS_KEY_ID, AWS_SECRET_ACCESS_KEY, AWS_ENDPOINT_URL
export TF_VAR_endpoint="$AWS_ENDPOINT_URL"        # the provider block points every service here

cd examples/terraform/shop
terraform init
terraform apply -auto-approve                     # about four minutes, most of it the database and load balancer
```

`endpoint` defaults to `http://localhost:8080`. Use a host name such as `localhost`, not an IP address: the
S3 client puts the account and bucket name in the host name.

### Provider settings, and why

* `endpoints {}` points each service the stack uses at HomeCloud.
* `s3_use_path_style = true`: HomeCloud serves buckets by path, so the bucket name need not resolve as a
  host name.
* No `skip_credentials_validation`, `skip_requesting_account_id` or `skip_metadata_api_check` is needed:
  HomeCloud implements STS, so the provider's startup checks pass unchanged.
* Lambda log groups are declared in the stack. Lambda creates them on first invoke and, on AWS as well as
  on HomeCloud, leaves them behind when the function is deleted, so `destroy` would otherwise not
  leave the account empty.

## Check that it works

The load balancer's name (`shop-alb.elb.internal`) resolves inside the VPC. From your machine use the
port HomeCloud published for it:

```bash
docker port hc-elb-shop-alb                       # e.g. 80/tcp -> 0.0.0.0:32770 and 443/tcp -> 0.0.0.0:32771
curl http://localhost:32770/                      # nginx welcome page, served by the ECS tasks
curl -k --resolve www.shop.example.test:32771:127.0.0.1 https://www.shop.example.test:32771/
                                                  # the same over TLS (the ACM certificate is from HomeCloud's private CA)

curl "$(terraform output -raw api_url)"           # {"service": "shop-api", "path": "/hello"}

aws sqs send-message --queue-url "$(terraform output -raw queue_url)" --message-body '{"order":1}'
aws sns publish --topic-arn "$(terraform output -raw topic_arn)" --message '{"order":2}'
aws logs tail "$(terraform output -raw consumer_log_group)" --since 5m     # "order received: ..."
```

Connect to the database with the password Terraform never saw. From a container on the VPC's network:

```bash
SECRET=$(aws secretsmanager get-secret-value --secret-id "$(terraform output -raw db_secret_arn)" \
  --query SecretString --output text)
PGPASSWORD=$(echo "$SECRET" | python3 -c 'import sys,json; print(json.load(sys.stdin)["password"])')
NET=$(docker inspect hc-rds-shop-db --format '{{range $k,$v := .NetworkSettings.Networks}}{{$k}}{{end}}')
docker run --rm --network "$NET" -e PGPASSWORD="$PGPASSWORD" postgres:16-alpine \
  psql -h "$(terraform output -raw db_endpoint)" -U shopadmin -d shop -c 'select version()'
```

A second `terraform plan` reports no changes, and `terraform destroy` removes everything: the containers,
networks and volumes the stack created go with it.

## Notes

* The ACM certificate is issued at once by HomeCloud's private CA and the Route 53 validation record is
  reported but not checked. Trust the CA (`GET /api/v1/acm/ca?format=pem`) to verify HTTPS without `-k`.
* The private zone record `app.shop.internal` is answered by the VPC's DNS resolver (the `.2` address of the
  VPC, here `10.20.0.2`). A container started by hand with `docker run` uses Docker's own resolver instead
  and does not see it.
* Fargate tasks are Docker containers, so the image must be pullable by Docker (`nginx:1.27-alpine` here).
