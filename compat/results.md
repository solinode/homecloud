17 of 17 scenarios pass (OpenTofu v1.12.6, 2026-10-07T15:58:59Z).

| Scenario | Modules | Apply | Check | Idempotent | Destroy | Notes |
|---|---|---|---|---|---|---|
| acm | route53@6.5.1<br>acm@6.3.1 | pass | pass | pass | pass |  |
| apigateway-v2 | lambda@8.9.0<br>apigateway-v2@6.1.1 | pass | pass | pass | pass |  |
| cloudwatch | cloudwatch/log-group@5.7.3<br>cloudwatch/log-stream@5.7.3<br>cloudwatch/log-metric-filter@5.7.3<br>cloudwatch/metric-alarm@5.7.3<br>cloudwatch/metric-alarms-by-multiple-dimensions@5.7.3<br>cloudwatch/composite-alarm@5.7.3<br>cloudwatch/query-definition@5.7.3 | pass | pass | pass | pass |  |
| dynamodb-table | dynamodb-table@5.5.2 | pass | pass | pass | pass |  |
| ecs-alb | alb@10.5.1<br>ecs@7.6.1 | pass | pass | pass | pass |  |
| eventbridge | eventbridge@4.3.2 | pass | pass | pass | pass |  |
| iam | iam/iam-policy@6.8.2<br>iam/iam-role@6.8.2<br>iam/iam-user@6.8.2<br>iam/iam-group@6.8.2 | pass | pass | pass | pass |  |
| kms | kms@4.2.2 | pass | pass | pass | pass |  |
| lambda | lambda@8.9.0<br>lambda/alias@8.9.0 | pass | pass | pass | pass |  |
| rds | security-group/postgresql@6.0.0<br>rds@7.2.2 | pass | pass | pass | pass |  |
| route53 | route53@6.5.1 | pass | pass | pass | pass |  |
| s3-bucket | s3-bucket@5.16.1 | pass | pass | pass | pass |  |
| secrets-manager | secrets-manager@2.2.0 | pass | pass | pass | pass |  |
| security-group | security-group/http-80@6.0.0<br>security-group@6.0.0 | pass | pass | pass | pass |  |
| sqs-sns | sqs@5.2.2<br>sns@7.2.0 | pass | pass | pass | pass |  |
| ssm-parameter | ssm-parameter@2.1.2 | pass | pass | pass | pass |  |
| vpc | vpc@6.7.3 | pass | pass | pass | pass |  |

| Service | Scenarios passing |
|---|---|
| acm | 1/1 |
| apigateway | 1/1 |
| cloudwatch | 1/1 |
| cognito | 1/1 |
| dynamodb | 1/1 |
| ec2 | 5/5 |
| ecs | 1/1 |
| elbv2 | 1/1 |
| events | 1/1 |
| iam | 5/5 |
| kms | 3/3 |
| lambda | 2/2 |
| logs | 4/4 |
| rds | 1/1 |
| route53 | 2/2 |
| s3 | 1/1 |
| scheduler | 1/1 |
| secretsmanager | 2/2 |
| sns | 2/2 |
| sqs | 3/3 |
| ssm | 1/1 |
| sts | 1/1 |
