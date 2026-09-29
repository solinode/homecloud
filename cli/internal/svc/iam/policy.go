package iam

import (
	"encoding/json"
	"path"
	"strings"
)

// StringList accepts either a JSON string or an array of strings, as AWS policy documents do.
type StringList []string

func (s *StringList) UnmarshalJSON(b []byte) error {
	var one string
	if json.Unmarshal(b, &one) == nil {
		*s = StringList{one}
		return nil
	}
	var many []string
	if err := json.Unmarshal(b, &many); err != nil {
		return err
	}
	*s = many
	return nil
}

type Statement struct {
	Sid      string     `json:"Sid,omitempty"`
	Effect   string     `json:"Effect"`
	Action   StringList `json:"Action"`
	Resource StringList `json:"Resource"`
}

type PolicyDocument struct {
	Version   string      `json:"Version"`
	Statement []Statement `json:"Statement"`
}

func (d PolicyDocument) Validate() error {
	if len(d.Statement) == 0 {
		return errf("policy document needs at least one Statement")
	}
	for i, st := range d.Statement {
		if st.Effect != "Allow" && st.Effect != "Deny" {
			return errf("Statement[%d].Effect must be Allow or Deny", i)
		}
		if len(st.Action) == 0 {
			return errf("Statement[%d].Action is required", i)
		}
		if len(st.Resource) == 0 {
			return errf("Statement[%d].Resource is required", i)
		}
	}
	return nil
}

// match reports whether value matches an IAM glob pattern (* and ?).
// Action names compare case-insensitively, as in AWS.
func match(pattern, value string, fold bool) bool {
	if fold {
		pattern, value = strings.ToLower(pattern), strings.ToLower(value)
	}
	if pattern == "*" {
		return true
	}
	// path.Match treats '/' specially; ARNs contain slashes, so escape them.
	p := strings.ReplaceAll(pattern, "/", "\x00")
	v := strings.ReplaceAll(value, "/", "\x00")
	p = strings.ReplaceAll(p, "[", "\\[")
	ok, err := path.Match(p, v)
	return err == nil && ok
}

type decision int

const (
	implicitDeny decision = iota
	allow
	explicitDeny
)

func evaluate(docs []PolicyDocument, action, resource string) decision {
	result := implicitDeny
	for _, d := range docs {
		for _, st := range d.Statement {
			am := false
			for _, a := range st.Action {
				if match(a, action, true) {
					am = true
					break
				}
			}
			if !am {
				continue
			}
			rm := false
			for _, r := range st.Resource {
				if match(r, resource, false) {
					rm = true
					break
				}
			}
			if !rm {
				continue
			}
			if st.Effect == "Deny" {
				return explicitDeny
			}
			result = allow
		}
	}
	return result
}

func doc(effect string, actions ...string) PolicyDocument {
	return PolicyDocument{Version: "2012-10-17", Statement: []Statement{{Effect: effect, Action: actions, Resource: StringList{"*"}}}}
}

// builtinPolicies are the managed policies every account starts with.
var builtinPolicies = []struct {
	Name, Description string
	Doc               PolicyDocument
}{
	{"AdministratorAccess", "Full access to every HomeCloud service and resource.", doc("Allow", "*")},
	{"ReadOnlyAccess", "Read-only access to every HomeCloud service.", doc("Allow", "*:Describe*", "*:List*", "*:Get*")},
	{"IAMFullAccess", "Full access to users, groups, policies and access keys.", doc("Allow", "iam:*")},
	{"EC2FullAccess", "Full access to compute instances, images, volumes and security groups.", doc("Allow", "ec2:*")},
	{"S3FullAccess", "Full access to all buckets and objects.", doc("Allow", "s3:*")},
	{"S3ReadOnlyAccess", "Read access to all buckets and objects.", doc("Allow", "s3:Get*", "s3:List*")},
	{"RDSFullAccess", "Full access to managed database instances.", doc("Allow", "rds:*", "elasticache:*")},
	{"LambdaFullAccess", "Full access to serverless functions.", doc("Allow", "lambda:*")},
	{"VPCFullAccess", "Full access to VPCs, subnets and security groups.", doc("Allow", "ec2:*Vpc*", "ec2:*Subnet*", "ec2:*SecurityGroup*")},
	{"CloudWatchFullAccess", "Full access to metrics, logs and alarms.", doc("Allow", "cloudwatch:*", "logs:*")},
	{"SQSFullAccess", "Full access to message queues.", doc("Allow", "sqs:*")},
	{"SNSFullAccess", "Full access to notification topics.", doc("Allow", "sns:*")},
	{"SecretsManagerReadWrite", "Full access to secrets.", doc("Allow", "secretsmanager:*")},
	{"DynamoDBFullAccess", "Full access to key-value tables.", doc("Allow", "dynamodb:*")},
	{"EventBridgeFullAccess", "Full access to scheduled rules.", doc("Allow", "events:*")},
	{"KMSFullAccess", "Full access to encryption keys.", doc("Allow", "kms:*")},
	{"SSMFullAccess", "Full access to Parameter Store.", doc("Allow", "ssm:*")},
	{"SSMReadOnlyAccess", "Read access to Parameter Store.", doc("Allow", "ssm:Get*", "ssm:Describe*")},
	{"ECRFullAccess", "Full access to container repositories.", doc("Allow", "ecr:*")},
	{"ECSFullAccess", "Full access to container services and tasks.", doc("Allow", "ecs:*", "elasticloadbalancing:*")},
	{"ElasticLoadBalancingFullAccess", "Full access to load balancers and target groups.", doc("Allow", "elasticloadbalancing:*")},
	{"EFSFullAccess", "Full access to shared file systems.", doc("Allow", "elasticfilesystem:*")},
	{"APIGatewayFullAccess", "Full access to HTTP APIs.", doc("Allow", "apigateway:*")},
	{"CloudFormationFullAccess", "Full access to stacks (resources are created with the caller's own permissions).", doc("Allow", "cloudformation:*")},
	{"CognitoPowerUser", "Full access to user pools, app clients and pool users.", doc("Allow", "cognito-idp:*")},
	{"StepFunctionsFullAccess", "Full access to state machines and executions.", doc("Allow", "states:*")},
}
