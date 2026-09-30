package iam

import (
	"bytes"
	"encoding/json"
	"net/http"
	"path"
	"regexp"
	"slices"
	"strings"

	"github.com/homecloudhq/homecloud/cli/internal/core"
)

// StringList accepts either a JSON string or an array of strings, as AWS policy documents do.
type StringList []string

func (s *StringList) UnmarshalJSON(b []byte) error {
	if bytes.Equal(bytes.TrimSpace(b), []byte("null")) {
		*s = nil
		return nil
	}
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
	Sid       string      `json:"Sid,omitempty"`
	Effect    string      `json:"Effect"`
	Principal *Principals `json:"Principal,omitempty"`
	// NotPrincipal only appears in resource-based policies (bucket, queue, topic
	// and key policies); role trust and identity policies reject it.
	NotPrincipal *Principals     `json:"NotPrincipal,omitempty"`
	Action       StringList      `json:"Action,omitempty"`
	NotAction    StringList      `json:"NotAction,omitempty"`
	Resource     StringList      `json:"Resource,omitempty"`
	NotResource  StringList      `json:"NotResource,omitempty"`
	Condition    json.RawMessage `json:"Condition,omitempty"`
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

// Statements accepts a single statement object or an array, as AWS does.
type Statements []Statement

func (s *Statements) UnmarshalJSON(b []byte) error {
	b = bytes.TrimSpace(b)
	if len(b) > 0 && b[0] == '{' {
		var one Statement
		if err := json.Unmarshal(b, &one); err != nil {
			return err
		}
		*s = Statements{one}
		return nil
	}
	var many []Statement
	if err := json.Unmarshal(b, &many); err != nil {
		return err
	}
	*s = many
	return nil
}

type PolicyDocument struct {
	Version   string     `json:"Version,omitempty"`
	ID        string     `json:"Id,omitempty"`
	Statement Statements `json:"Statement"`
	// raw is the document as it was written (compacted); it is what the
	// document marshals to, so policies come back exactly as submitted.
	raw json.RawMessage
}

func (d *PolicyDocument) UnmarshalJSON(b []byte) error {
	type plain PolicyDocument
	var p plain
	if err := json.Unmarshal(b, &p); err != nil {
		return err
	}
	*d = PolicyDocument(p)
	var buf bytes.Buffer
	if json.Compact(&buf, b) == nil {
		d.raw = buf.Bytes()
	}
	return nil
}

func (d PolicyDocument) MarshalJSON() ([]byte, error) {
	if len(d.raw) > 0 {
		return d.raw, nil
	}
	type plain PolicyDocument
	return json.Marshal(plain(d))
}

// defaultVersion sets Version to 2012-10-17 when the document has none.
func (d *PolicyDocument) defaultVersion() {
	if d.Version == "" {
		d.Version, d.raw = policyVersion, nil
	}
}

const policyVersion = "2012-10-17"

func malformed(format string, a ...any) error {
	return core.Errf(http.StatusBadRequest, "MalformedPolicyDocument", format, a...)
}

var (
	docKeys  = []string{"Version", "Id", "Statement"}
	stmtKeys = []string{"Sid", "Effect", "Principal", "NotPrincipal", "Action", "NotAction", "Resource", "NotResource", "Condition"}
	sidRe    = regexp.MustCompile(`^[A-Za-z0-9 ]*$`)
)

// ParsePolicy parses a policy document as sent to the AWS API (a JSON string),
// rejecting elements IAM does not know. Errors are MalformedPolicyDocument.
func ParsePolicy(text string) (PolicyDocument, error) {
	var d PolicyDocument
	var top map[string]json.RawMessage
	if err := json.Unmarshal([]byte(text), &top); err != nil {
		return d, malformed("Syntax errors in policy: %v", err)
	}
	for k := range top {
		if !slices.Contains(docKeys, k) {
			return d, malformed("Syntax errors in policy: unknown element %q", k)
		}
	}
	var stmts []map[string]json.RawMessage
	if raw := bytes.TrimSpace(top["Statement"]); len(raw) > 0 && raw[0] == '{' {
		var one map[string]json.RawMessage
		if json.Unmarshal(raw, &one) == nil {
			stmts = append(stmts, one)
		}
	} else if len(raw) > 0 {
		_ = json.Unmarshal(raw, &stmts)
	}
	for _, st := range stmts {
		for k := range st {
			if !slices.Contains(stmtKeys, k) {
				return d, malformed("Syntax errors in policy: unknown element %q in Statement", k)
			}
			if k == "NotPrincipal" {
				return d, malformed("NotPrincipal is not supported")
			}
		}
	}
	if err := json.Unmarshal([]byte(text), &d); err != nil {
		return d, malformed("Syntax errors in policy: %v", err)
	}
	return d, nil
}

// Validate checks an identity-based (permissions) policy.
func (d PolicyDocument) Validate() error {
	if d.Version != "" && d.Version != policyVersion && d.Version != "2008-10-17" {
		return malformed("The policy failed legacy parsing: Version must be 2012-10-17 or 2008-10-17")
	}
	if len(d.Statement) == 0 {
		return malformed("policy document needs at least one Statement")
	}
	for i, st := range d.Statement {
		if err := st.validateCommon(i); err != nil {
			return err
		}
		if len(st.Resource) == 0 && len(st.NotResource) == 0 {
			return malformed("Statement[%d] needs a Resource or NotResource", i)
		}
		if len(st.Resource) > 0 && len(st.NotResource) > 0 {
			return malformed("Statement[%d] cannot have both Resource and NotResource", i)
		}
		if st.Principal != nil {
			return malformed("Statement[%d]: Principal is only allowed in role trust policies", i)
		}
	}
	return nil
}

func (st Statement) validateCommon(i int) error {
	if st.Effect != "Allow" && st.Effect != "Deny" {
		return malformed("Statement[%d].Effect must be Allow or Deny", i)
	}
	if len(st.Action) == 0 && len(st.NotAction) == 0 {
		return malformed("Statement[%d] needs an Action or NotAction", i)
	}
	if len(st.Action) > 0 && len(st.NotAction) > 0 {
		return malformed("Statement[%d] cannot have both Action and NotAction", i)
	}
	for _, a := range append(slices.Clone(st.Action), st.NotAction...) {
		if a != "*" && !strings.Contains(a, ":") {
			return malformed("Statement[%d]: action %q must be <service>:<action>", i, a)
		}
	}
	if !sidRe.MatchString(st.Sid) {
		return malformed("Statement[%d].Sid may contain only letters, digits and spaces", i)
	}
	if len(st.Condition) > 0 {
		if _, err := parseConditions(st.Condition); err != nil {
			return malformed("Statement[%d].Condition: %v", i, err)
		}
	}
	return nil
}

// ValidateTrust checks a role trust (assume role) policy.
func (d PolicyDocument) ValidateTrust() error {
	if len(d.Statement) == 0 {
		return malformed("trust policy needs at least one Statement")
	}
	for i, st := range d.Statement {
		if err := st.validateCommon(i); err != nil {
			return err
		}
		if st.Principal == nil {
			return malformed("Statement[%d].Principal is required in a trust policy", i)
		}
		if p := st.Principal; !p.Any && len(p.AWS)+len(p.Service)+len(p.Federated) == 0 {
			return malformed("Statement[%d].Principal names no principal", i)
		}
		if len(st.Resource) > 0 || len(st.NotResource) > 0 {
			return malformed("Statement[%d]: Resource is not allowed in a trust policy", i)
		}
		for _, a := range append(slices.Clone(st.Action), st.NotAction...) {
			if !strings.HasPrefix(strings.ToLower(a), "sts:") {
				return malformed("Statement[%d].Action %q: trust policies only grant sts: actions", i, a)
			}
		}
	}
	return nil
}

// trusts evaluates a trust policy for action (e.g. sts:AssumeRole) by a caller
// identified by ARN (users/roles), service principal ("lambda.amazonaws.com"),
// or account. Conditions are evaluated against ctx for AWS principals; service
// principals (HomeCloud's own services) are not given a request context.
func (d PolicyDocument) trusts(action, callerARN, service, accountID string, ctx CondContext) decision {
	result := implicitDeny
	for _, st := range d.Statement {
		if !st.actionMatches(action) || st.Principal == nil {
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
		if service == "" && len(st.Condition) > 0 {
			ok, err := evalConditions(st.Condition, ctx, d.Version == policyVersion)
			if err != nil {
				ok = st.Effect == "Deny"
			}
			if !ok {
				continue
			}
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

func (d decision) String() string {
	switch d {
	case allow:
		return "allowed"
	case explicitDeny:
		return "explicitDeny"
	}
	return "implicitDeny"
}

func (st Statement) actionMatches(action string) bool {
	if len(st.NotAction) > 0 {
		for _, a := range st.NotAction {
			if match(a, action, true) {
				return false
			}
		}
		return true
	}
	for _, a := range st.Action {
		if match(a, action, true) {
			return true
		}
	}
	return false
}

func (st Statement) resourceMatches(resource string, ctx CondContext, vars bool) bool {
	in := func(list StringList) bool {
		for _, r := range list {
			p := core.CanonicalARN(r)
			if vars {
				var ok bool
				if p, ok = substitute(p, ctx, true); !ok {
					continue // an unresolvable policy variable matches nothing
				}
			}
			if match(p, resource, false) {
				return true
			}
		}
		return false
	}
	if len(st.NotResource) > 0 {
		return !in(st.NotResource)
	}
	return in(st.Resource)
}

// applies reports whether the statement covers the request.
func (st Statement) applies(version, action, resource string, ctx CondContext) bool {
	vars := version == policyVersion
	if !st.actionMatches(action) || !st.resourceMatches(resource, ctx, vars) {
		return false
	}
	if len(st.Condition) == 0 {
		return true
	}
	ok, err := evalConditions(st.Condition, ctx, vars)
	if err != nil {
		// A condition HomeCloud cannot evaluate fails closed: Deny applies, Allow does not.
		return st.Effect == "Deny"
	}
	return ok
}

// evaluate decides a request against identity policies: an explicit Deny wins,
// then any Allow; otherwise the request is implicitly denied.
func evaluate(docs []PolicyDocument, action, resource string, ctx CondContext) decision {
	resource = core.CanonicalARN(resource)
	result := implicitDeny
	for _, d := range docs {
		for _, st := range d.Statement {
			if !st.applies(d.Version, action, resource, ctx) {
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

// decide combines identity policies with an optional permissions boundary:
// the boundary limits what the identity policies can grant.
func decide(docs []PolicyDocument, boundary *PolicyDocument, action, resource string, ctx CondContext) decision {
	d := evaluate(docs, action, resource, ctx)
	if d != allow || boundary == nil {
		return d
	}
	switch evaluate([]PolicyDocument{*boundary}, action, resource, ctx) {
	case allow:
		return allow
	case explicitDeny:
		return explicitDeny
	}
	return implicitDeny
}

func doc(effect string, actions ...string) PolicyDocument {
	return PolicyDocument{Version: policyVersion, Statement: Statements{{Effect: effect, Action: actions, Resource: StringList{"*"}}}}
}

func docs(stmts ...Statement) PolicyDocument {
	return PolicyDocument{Version: policyVersion, Statement: stmts}
}

func allowAll(actions ...string) Statement {
	return Statement{Effect: "Allow", Action: actions, Resource: StringList{"*"}}
}

var lambdaLogs = allowAll("logs:CreateLogGroup", "logs:CreateLogStream", "logs:PutLogEvents")

// builtinPolicy is an AWS managed policy every account starts with. They live
// at arn:aws:iam::aws:policy<Path><Name>, as in AWS.
type builtinPolicy struct {
	Name, Path, Description string
	Doc                     PolicyDocument
}

var builtinPolicies = []builtinPolicy{
	{"AdministratorAccess", "/", "Full access to every service and resource.", doc("Allow", "*")},
	{"PowerUserAccess", "/", "Full access to every service except user and group management.", docs(
		Statement{Effect: "Allow", NotAction: StringList{"iam:*", "organizations:*", "account:*"}, Resource: StringList{"*"}},
		allowAll("iam:CreateServiceLinkedRole", "iam:DeleteServiceLinkedRole", "iam:ListRoles", "organizations:DescribeOrganization", "account:ListRegions", "account:GetAccountInformation"),
	)},
	{"ReadOnlyAccess", "/", "Read-only access to every service (secret values excluded).", docs(
		allowAll("*:Describe*", "*:List*", "*:Get*", "cloudtrail:LookupEvents"),
		Statement{Effect: "Deny", Action: StringList{"secretsmanager:GetSecretValue", "kms:Decrypt"}, Resource: StringList{"*"}},
	)},
	{"IAMFullAccess", "/", "Full access to users, groups, roles, policies and access keys.", doc("Allow", "iam:*", "organizations:DescribeAccount", "organizations:DescribeOrganization", "organizations:DescribeOrganizationalUnit", "organizations:DescribePolicy", "organizations:ListChildren", "organizations:ListParents", "organizations:ListPoliciesForTarget", "organizations:ListRoots", "organizations:ListPolicies", "organizations:ListTargetsForPolicy")},
	{"IAMReadOnlyAccess", "/", "Read access to users, groups, roles and policies.", doc("Allow", "iam:GenerateCredentialReport", "iam:GenerateServiceLastAccessedDetails", "iam:Get*", "iam:List*", "iam:SimulateCustomPolicy", "iam:SimulatePrincipalPolicy")},
	{"AmazonEC2FullAccess", "/", "Full access to compute instances, images, volumes, networking and load balancers.", doc("Allow", "ec2:*", "elasticloadbalancing:*", "cloudwatch:*", "autoscaling:*")},
	{"AmazonEC2ReadOnlyAccess", "/", "Read access to compute instances, images, volumes and networking.", doc("Allow", "ec2:Describe*", "elasticloadbalancing:Describe*", "cloudwatch:ListMetrics", "cloudwatch:GetMetricStatistics", "cloudwatch:Describe*", "autoscaling:Describe*")},
	{"AmazonVPCFullAccess", "/", "Full access to VPCs, subnets and security groups.", doc("Allow", "ec2:*Vpc*", "ec2:*Subnet*", "ec2:*SecurityGroup*", "ec2:*Route*", "ec2:*Gateway*", "ec2:*Address*", "ec2:*NetworkAcl*", "ec2:DescribeAvailabilityZones", "ec2:DescribeTags", "ec2:CreateTags", "ec2:DeleteTags")},
	{"AutoScalingFullAccess", "/", "Full access to Auto Scaling groups.", doc("Allow", "autoscaling:*", "cloudwatch:PutMetricAlarm")},
	{"AmazonS3FullAccess", "/", "Full access to all buckets and objects.", doc("Allow", "s3:*", "s3-object-lambda:*")},
	{"AmazonS3ReadOnlyAccess", "/", "Read access to all buckets and objects.", doc("Allow", "s3:Get*", "s3:List*", "s3:Describe*", "s3-object-lambda:Get*", "s3-object-lambda:List*")},
	{"AmazonRDSFullAccess", "/", "Full access to managed database instances and caches.", doc("Allow", "rds:*", "elasticache:*")},
	{"AWSLambda_FullAccess", "/", "Full access to serverless functions.", doc("Allow", "lambda:*")},
	{"AWSLambda_ReadOnlyAccess", "/", "Read access to serverless functions.", doc("Allow", "lambda:Get*", "lambda:List*")},
	{"AWSLambdaBasicExecutionRole", "/service-role/", "Lets a Lambda function write its logs.", docs(lambdaLogs)},
	{"AWSLambdaSQSQueueExecutionRole", "/service-role/", "Lets a Lambda function read from SQS queues and write its logs.", docs(
		allowAll("sqs:ReceiveMessage", "sqs:DeleteMessage", "sqs:GetQueueAttributes"), lambdaLogs)},
	{"AWSLambdaDynamoDBExecutionRole", "/service-role/", "Lets a Lambda function read DynamoDB streams and write its logs.", docs(
		allowAll("dynamodb:DescribeStream", "dynamodb:GetRecords", "dynamodb:GetShardIterator", "dynamodb:ListStreams"), lambdaLogs)},
	{"AWSLambdaVPCAccessExecutionRole", "/service-role/", "Lets a Lambda function attach to a VPC and write its logs.", docs(
		allowAll("ec2:CreateNetworkInterface", "ec2:DescribeNetworkInterfaces", "ec2:DescribeSubnets", "ec2:DeleteNetworkInterface", "ec2:AssignPrivateIpAddresses", "ec2:UnassignPrivateIpAddresses"), lambdaLogs)},
	{"AWSLambdaRole", "/service-role/", "Lets a service invoke Lambda functions.", doc("Allow", "lambda:InvokeFunction")},
	{"CloudWatchFullAccess", "/", "Full access to metrics, logs, alarms and events.", doc("Allow", "cloudwatch:*", "logs:*", "events:*", "autoscaling:Describe*", "sns:*")},
	{"CloudWatchReadOnlyAccess", "/", "Read access to metrics, logs and alarms.", doc("Allow", "cloudwatch:Describe*", "cloudwatch:Get*", "cloudwatch:List*", "logs:Get*", "logs:List*", "logs:Describe*", "logs:FilterLogEvents", "logs:StartQuery", "logs:StopQuery", "logs:TestMetricFilter")},
	{"CloudWatchLogsFullAccess", "/", "Full access to log groups and streams.", doc("Allow", "logs:*")},
	{"CloudWatchAgentServerPolicy", "/", "Lets the CloudWatch agent publish metrics and logs.", doc("Allow", "cloudwatch:PutMetricData", "ec2:DescribeVolumes", "ec2:DescribeTags", "logs:PutLogEvents", "logs:DescribeLogStreams", "logs:DescribeLogGroups", "logs:CreateLogStream", "logs:CreateLogGroup", "ssm:GetParameter")},
	{"AmazonSQSFullAccess", "/", "Full access to message queues.", doc("Allow", "sqs:*")},
	{"AmazonSQSReadOnlyAccess", "/", "Read access to message queues.", doc("Allow", "sqs:GetQueueAttributes", "sqs:GetQueueUrl", "sqs:ListDeadLetterSourceQueues", "sqs:ListQueues", "sqs:ListMessageMoveTasks", "sqs:ListQueueTags")},
	{"AmazonSNSFullAccess", "/", "Full access to notification topics.", doc("Allow", "sns:*")},
	{"AmazonSNSReadOnlyAccess", "/", "Read access to notification topics.", doc("Allow", "sns:GetTopicAttributes", "sns:List*", "sns:CheckIfPhoneNumberIsOptedOut", "sns:GetEndpointAttributes", "sns:GetPlatformApplicationAttributes", "sns:GetSMSAttributes", "sns:GetSubscriptionAttributes")},
	{"SecretsManagerReadWrite", "/", "Full access to secrets.", doc("Allow", "secretsmanager:*", "kms:DescribeKey", "kms:ListAliases", "kms:ListKeys")},
	{"AmazonDynamoDBFullAccess", "/", "Full access to key-value tables.", doc("Allow", "dynamodb:*")},
	{"AmazonDynamoDBReadOnlyAccess", "/", "Read access to key-value tables.", doc("Allow", "dynamodb:BatchGetItem", "dynamodb:Describe*", "dynamodb:List*", "dynamodb:GetItem", "dynamodb:GetRecords", "dynamodb:GetShardIterator", "dynamodb:Query", "dynamodb:Scan", "dynamodb:PartiQLSelect")},
	{"AmazonEventBridgeFullAccess", "/", "Full access to event buses, rules and schedules.", doc("Allow", "events:*", "scheduler:*")},
	{"KMSFullAccess", "/", "Full access to encryption keys (HomeCloud policy; AWS has no equivalent).", doc("Allow", "kms:*")},
	{"AWSKeyManagementServicePowerUser", "/", "Manage encryption keys without using them to encrypt or decrypt.", doc("Allow",
		"kms:CreateAlias", "kms:CreateKey", "kms:DeleteAlias", "kms:Describe*", "kms:GenerateRandom", "kms:Get*", "kms:List*", "kms:TagResource", "kms:UntagResource")},
	{"AmazonSSMFullAccess", "/", "Full access to Parameter Store.", doc("Allow", "ssm:*")},
	{"AmazonSSMReadOnlyAccess", "/", "Read access to Parameter Store.", doc("Allow", "ssm:Describe*", "ssm:Get*", "ssm:List*")},
	{"AmazonSSMManagedInstanceCore", "/", "Lets an instance read its parameters and report to Systems Manager.", doc("Allow", "ssm:DescribeAssociation", "ssm:GetDocument", "ssm:DescribeDocument", "ssm:GetParameter", "ssm:GetParameters", "ssm:ListAssociations", "ssm:ListInstanceAssociations", "ssm:PutInventory", "ssm:UpdateInstanceInformation", "ssmmessages:*", "ec2messages:*")},
	{"AmazonEC2ContainerRegistryFullAccess", "/", "Full access to container repositories.", doc("Allow", "ecr:*")},
	{"AmazonEC2ContainerRegistryReadOnly", "/", "Pull images from container repositories.", doc("Allow", "ecr:GetAuthorizationToken", "ecr:BatchCheckLayerAvailability", "ecr:GetDownloadUrlForLayer", "ecr:GetRepositoryPolicy", "ecr:DescribeRepositories", "ecr:ListImages", "ecr:DescribeImages", "ecr:BatchGetImage")},
	{"AmazonECS_FullAccess", "/", "Full access to container services, tasks and their load balancers.", doc("Allow", "ecs:*", "elasticloadbalancing:*", "application-autoscaling:*", "logs:*")},
	{"AmazonECSTaskExecutionRolePolicy", "/service-role/", "Lets ECS pull task images and write task logs.", doc("Allow", "ecr:GetAuthorizationToken", "ecr:BatchCheckLayerAvailability", "ecr:GetDownloadUrlForLayer", "ecr:BatchGetImage", "logs:CreateLogStream", "logs:PutLogEvents")},
	{"AmazonRoute53FullAccess", "/", "Full access to hosted zones and records.", doc("Allow", "route53:*", "route53domains:*")},
	{"AWSCertificateManagerFullAccess", "/", "Full access to TLS certificates and the private CA.", doc("Allow", "acm:*", "acm-pca:*")},
	{"ElasticLoadBalancingFullAccess", "/", "Full access to load balancers and target groups.", doc("Allow", "elasticloadbalancing:*")},
	{"AmazonElasticFileSystemFullAccess", "/", "Full access to shared file systems.", doc("Allow", "elasticfilesystem:*")},
	{"AmazonAPIGatewayAdministrator", "/", "Full access to HTTP APIs.", doc("Allow", "apigateway:*")},
	{"AWSCloudFormationFullAccess", "/", "Full access to stacks (resources are created with the caller's own permissions).", doc("Allow", "cloudformation:*")},
	{"AmazonCognitoPowerUser", "/", "Full access to user pools, app clients and pool users.", doc("Allow", "cognito-idp:*", "cognito-identity:*", "cognito-sync:*")},
	{"AWSStepFunctionsFullAccess", "/", "Full access to state machines and executions.", doc("Allow", "states:*")},
	{"AWSCloudTrail_ReadOnlyAccess", "/", "Read access to the event history.", doc("Allow", "cloudtrail:Get*", "cloudtrail:Describe*", "cloudtrail:List*", "cloudtrail:LookupEvents")},
}

// legacyPolicyNames maps the names HomeCloud's managed policies had before
// they took their AWS names. Old names keep working wherever a policy is named,
// and stored attachments are migrated at startup.
var legacyPolicyNames = map[string]string{
	"EC2FullAccess":                "AmazonEC2FullAccess",
	"S3FullAccess":                 "AmazonS3FullAccess",
	"S3ReadOnlyAccess":             "AmazonS3ReadOnlyAccess",
	"RDSFullAccess":                "AmazonRDSFullAccess",
	"LambdaFullAccess":             "AWSLambda_FullAccess",
	"VPCFullAccess":                "AmazonVPCFullAccess",
	"SQSFullAccess":                "AmazonSQSFullAccess",
	"SNSFullAccess":                "AmazonSNSFullAccess",
	"DynamoDBFullAccess":           "AmazonDynamoDBFullAccess",
	"EventBridgeFullAccess":        "AmazonEventBridgeFullAccess",
	"SSMFullAccess":                "AmazonSSMFullAccess",
	"SSMReadOnlyAccess":            "AmazonSSMReadOnlyAccess",
	"ECRFullAccess":                "AmazonEC2ContainerRegistryFullAccess",
	"ECSFullAccess":                "AmazonECS_FullAccess",
	"Route53FullAccess":            "AmazonRoute53FullAccess",
	"CertificateManagerFullAccess": "AWSCertificateManagerFullAccess",
	"EFSFullAccess":                "AmazonElasticFileSystemFullAccess",
	"APIGatewayFullAccess":         "AmazonAPIGatewayAdministrator",
	"CloudFormationFullAccess":     "AWSCloudFormationFullAccess",
	"CognitoPowerUser":             "AmazonCognitoPowerUser",
	"StepFunctionsFullAccess":      "AWSStepFunctionsFullAccess",
	// AWS's deprecated name for the Lambda full-access policy.
	"AWSLambdaFullAccess": "AWSLambda_FullAccess",
}

func builtin(name string) (builtinPolicy, bool) {
	for _, bp := range builtinPolicies {
		if bp.Name == name {
			return bp, true
		}
	}
	return builtinPolicy{}, false
}
