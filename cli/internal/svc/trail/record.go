package trail

import (
	"encoding/json"
	"strings"

	"github.com/homecloudhq/homecloud/cli/internal/core"
)

// Resource is a resource an event acted on, in CloudTrail's terms.
type Resource struct {
	ARN, Name, Type string
}

// eventSources maps an IAM action prefix to the CloudTrail event source when
// it is not "<prefix>.amazonaws.com".
var eventSources = map[string]string{"cloudwatch": "monitoring.amazonaws.com", "states": "states.amazonaws.com"}

// Source is the event source ("s3.amazonaws.com") of the call.
func (e Event) Source() string {
	svc, _, _ := strings.Cut(e.Action, ":")
	if s := eventSources[svc]; s != "" {
		return s
	}
	return svc + ".amazonaws.com"
}

// Name is the API operation ("CreateBucket").
func (e Event) Name() string {
	_, name, ok := strings.Cut(e.Action, ":")
	if !ok {
		return e.Action
	}
	return name
}

// ReadOnly reports whether the operation only reads.
func (e Event) ReadOnly() bool {
	n := e.Name()
	for _, p := range []string{"Get", "List", "Describe", "Lookup", "Head", "BatchGet", "Query", "Scan", "Search", "Check", "Filter", "Test", "Simulate"} {
		if strings.HasPrefix(n, p) {
			return true
		}
	}
	return false
}

func lastSegment(arn string) string {
	if i := strings.LastIndexAny(arn, "/:"); i >= 0 {
		return arn[i+1:]
	}
	return arn
}

var serviceNames = map[string]string{"s3": "S3", "iam": "IAM", "ec2": "EC2", "sqs": "SQS", "sns": "SNS", "kms": "KMS", "ssm": "SSM", "rds": "RDS",
	"ecs": "ECS", "ecr": "ECR", "dynamodb": "DynamoDB", "lambda": "Lambda", "cognito-idp": "Cognito", "logs": "Logs", "events": "Events",
	"states": "StepFunctions", "cloudformation": "CloudFormation", "secretsmanager": "SecretsManager", "cloudtrail": "CloudTrail",
	"acm": "CertificateManager", "route53": "Route53", "cloudwatch": "CloudWatch", "elasticloadbalancing": "ElasticLoadBalancingV2",
	"autoscaling": "AutoScaling", "apigateway": "ApiGateway", "sts": "STS"}

func titled(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// Resources parses the event's resource ARN.
func (e Event) Resources() []Resource {
	arn := e.Resource
	if !strings.HasPrefix(arn, "arn:") {
		return nil
	}
	parts := strings.SplitN(arn, ":", 6)
	if len(parts) < 6 || parts[5] == "" || parts[5] == "*" || strings.HasSuffix(parts[5], "/") || strings.HasSuffix(parts[5], "/*") {
		return nil
	}
	svc, res := parts[2], parts[5]
	name := serviceNames[svc]
	if name == "" {
		name = titled(svc)
	}
	kind := ""
	switch {
	case svc == "s3" && strings.Contains(res, "/"):
		kind = "Object"
	case svc == "s3":
		kind = "Bucket"
	default:
		k, _, ok := strings.Cut(res, "/")
		if !ok {
			k, _, _ = strings.Cut(res, ":")
		}
		kind = strings.ReplaceAll(titled(k), "-", "")
	}
	return []Resource{{ARN: arn, Name: arn, Type: "AWS::" + name + "::" + kind}}
}

// identityType is CloudTrail's userIdentity.type for a principal ARN.
func identityType(arn string) string {
	switch {
	case strings.HasSuffix(arn, ":root"):
		return "Root"
	case strings.Contains(arn, ":assumed-role/"):
		return "AssumedRole"
	}
	return "IAMUser"
}

// Record renders the event as a CloudTrail event record (the JSON that fills
// LookupEvents' CloudTrailEvent and the Records of delivered log files).
func (e Event) Record(account string) map[string]any {
	acct := account
	if p := strings.Split(e.UserARN, ":"); len(p) > 4 && p[4] != "" {
		acct = p[4]
	}
	ident := map[string]any{"type": identityType(e.UserARN), "principalId": e.User, "arn": e.UserARN, "accountId": acct}
	if e.AccessKey != "" {
		ident["accessKeyId"] = e.AccessKey
	}
	if ident["type"] == "IAMUser" {
		ident["userName"] = e.User
	}
	rid := e.RequestID
	if rid == "" {
		rid = e.ID
	}
	rec := map[string]any{"eventVersion": "1.08", "userIdentity": ident, "eventTime": e.Time.UTC().Format("2006-01-02T15:04:05Z"),
		"eventSource": e.Source(), "eventName": e.Name(), "awsRegion": core.Region, "sourceIPAddress": e.SourceIP, "userAgent": e.UserAgent,
		"requestParameters": nil, "responseElements": nil, "requestID": rid, "eventID": e.ID, "readOnly": e.ReadOnly(),
		"eventType": "AwsApiCall", "managementEvent": true, "recipientAccountId": account, "eventCategory": "Management"}
	code := e.ErrorCode
	if code == "" && e.Status == 403 {
		code = "AccessDenied"
	}
	if code != "" {
		rec["errorCode"] = code
		if e.ErrorMessage != "" {
			rec["errorMessage"] = e.ErrorMessage
		}
	}
	if rs := e.Resources(); len(rs) > 0 {
		list := []map[string]string{}
		for _, r := range rs {
			list = append(list, map[string]string{"accountId": account, "type": r.Type, "ARN": r.ARN})
		}
		rec["resources"] = list
	}
	return rec
}

// RecordJSON is Record encoded as the CloudTrailEvent string.
func (e Event) RecordJSON(account string) string {
	b, _ := json.Marshal(e.Record(account))
	return string(b)
}
