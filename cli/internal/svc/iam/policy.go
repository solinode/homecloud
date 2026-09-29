package iam

import (
	"encoding/json"

	"github.com/homecloudhq/homecloud/cli/internal/core"
	"path"
	"slices"
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
	Sid       string          `json:"Sid,omitempty"`
	Effect    string          `json:"Effect"`
	Principal *Principals     `json:"Principal,omitempty"`
	Action    StringList      `json:"Action"`
	Resource  StringList      `json:"Resource,omitempty"`
	Condition json.RawMessage `json:"Condition,omitempty"` // kept, not evaluated
}

// Principals is a trust policy's Principal element: "*" or
// {"AWS": ..., "Service": ..., "Federated": ...}.
type Principals struct {
	Any       bool       `json:"-"`
	AWS       StringList `json:"AWS,omitempty"`
	Service   StringList `json:"Service,omitempty"`
	Federated StringList `json:"Federated,omitempty"`
}

func (p *Principals) UnmarshalJSON(b []byte) error {
	var star string
	if json.Unmarshal(b, &star) == nil {
		if star != "*" {
			return errf(`Principal must be "*" or an object`)
		}
		p.Any = true
		return nil
	}
	type plain Principals
	return json.Unmarshal(b, (*plain)(p))
}

func (p Principals) MarshalJSON() ([]byte, error) {
	if p.Any {
		return []byte(`"*"`), nil
	}
	type plain Principals
	return json.Marshal(plain(p))
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
		if st.Principal != nil {
			return errf("Statement[%d].Principal is only allowed in role trust policies", i)
		}
	}
	return nil
}

// ValidateTrust checks a role trust (assume role) policy.
func (d PolicyDocument) ValidateTrust() error {
	if len(d.Statement) == 0 {
		return errf("trust policy needs at least one Statement")
	}
	for i, st := range d.Statement {
		if st.Effect != "Allow" && st.Effect != "Deny" {
			return errf("Statement[%d].Effect must be Allow or Deny", i)
		}
		if st.Principal == nil {
			return errf("Statement[%d].Principal is required in a trust policy", i)
		}
		if len(st.Resource) > 0 {
			return errf("Statement[%d].Resource is not allowed in a trust policy", i)
		}
		for _, a := range st.Action {
			if !strings.HasPrefix(strings.ToLower(a), "sts:") {
				return errf("Statement[%d].Action %q: trust policies only grant sts: actions", i, a)
			}
		}
	}
	return nil
}

// trusts evaluates a trust policy for action (e.g. sts:AssumeRole) by a caller
// identified by ARN (users/roles), service principal ("lambda.amazonaws.com"),
// or account. accountRoot is "arn:aws:iam::<account>:root".
func (d PolicyDocument) trusts(action, callerARN, service, accountID string) decision {
	result := implicitDeny
	for _, st := range d.Statement {
		am := false
		for _, a := range st.Action {
			if match(a, action, true) {
				am = true
				break
			}
		}
		if !am || st.Principal == nil {
			continue
		}
		pm := st.Principal.Any
		if service != "" {
			pm = pm || slices.Contains(st.Principal.Service, service)
		} else {
			root := core.ARN(accountID, "iam", "root")
			for _, a := range st.Principal.AWS {
				a = core.CanonicalARN(a)
				if a == "*" || a == accountID || a == root || match(a, callerARN, false) {
					pm = true
				}
			}
		}
		if !pm {
			continue
		}
		if st.Effect == "Deny" {
			return explicitDeny
		}
		result = allow
	}
	return result
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
	resource = core.CanonicalARN(resource)
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
				if match(core.CanonicalARN(r), resource, false) {
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
	{"ReadOnlyAccess", "Read-only access to every HomeCloud service (secret values excluded).", PolicyDocument{Version: "2012-10-17", Statement: []Statement{
		{Effect: "Allow", Action: StringList{"*:Describe*", "*:List*", "*:Get*"}, Resource: StringList{"*"}},
		{Effect: "Deny", Action: StringList{"secretsmanager:GetSecretValue", "kms:Decrypt"}, Resource: StringList{"*"}},
	}}},
	{"IAMFullAccess", "Full access to users, groups, policies and access keys.", doc("Allow", "iam:*")},
	{"EC2FullAccess", "Full access to compute instances, images, volumes and security groups.", doc("Allow", "ec2:*")},
	{"AutoScalingFullAccess", "Full access to Auto Scaling groups.", doc("Allow", "autoscaling:*")},
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
	{"Route53FullAccess", "Full access to hosted zones and records.", doc("Allow", "route53:*")},
	{"CertificateManagerFullAccess", "Full access to TLS certificates and the private CA.", doc("Allow", "acm:*", "acm-pca:*")},
	{"ElasticLoadBalancingFullAccess", "Full access to load balancers and target groups.", doc("Allow", "elasticloadbalancing:*")},
	{"EFSFullAccess", "Full access to shared file systems.", doc("Allow", "elasticfilesystem:*")},
	{"APIGatewayFullAccess", "Full access to HTTP APIs.", doc("Allow", "apigateway:*")},
	{"CloudFormationFullAccess", "Full access to stacks (resources are created with the caller's own permissions).", doc("Allow", "cloudformation:*")},
	{"CognitoPowerUser", "Full access to user pools, app clients and pool users.", doc("Allow", "cognito-idp:*")},
	{"StepFunctionsFullAccess", "Full access to state machines and executions.", doc("Allow", "states:*")},
}
