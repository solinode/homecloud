package cfn_test

import (
	"encoding/json"
	"strings"
	"testing"
)

const coreTemplate = `
Resources:
  Table:
    Type: AWS::DynamoDB::Table
    Properties:
      TableName: core-orders
      BillingMode: PAY_PER_REQUEST
      AttributeDefinitions:
        - {AttributeName: pk, AttributeType: S}
        - {AttributeName: sk, AttributeType: N}
        - {AttributeName: gsi1, AttributeType: S}
      KeySchema:
        - {AttributeName: pk, KeyType: HASH}
        - {AttributeName: sk, KeyType: RANGE}
      GlobalSecondaryIndexes:
        - IndexName: by-gsi1
          KeySchema: [{AttributeName: gsi1, KeyType: HASH}]
          Projection: {ProjectionType: ALL}
      StreamSpecification: {StreamViewType: NEW_AND_OLD_IMAGES}
      TimeToLiveSpecification: {AttributeName: expires, Enabled: true}
      Tags: [{Key: app, Value: core}]
  Logs:
    Type: AWS::Logs::LogGroup
    Properties: {LogGroupName: /core/app, RetentionInDays: 14}
  Creds:
    Type: AWS::SecretsManager::Secret
    Properties:
      Name: core/db
      Description: generated
      GenerateSecretString:
        SecretStringTemplate: '{"username":"admin"}'
        GenerateStringKey: password
        PasswordLength: 20
        ExcludePunctuation: true
  Key:
    Type: AWS::KMS::Key
    Properties: {Description: core key, EnableKeyRotation: true}
  KeyAlias:
    Type: AWS::KMS::Alias
    Properties: {AliasName: alias/core-key, TargetKeyId: !Ref Key}
  Queue:
    Type: AWS::SQS::Queue
    Properties: {QueueName: core-queue}
  QueuePolicy:
    Type: AWS::SQS::QueuePolicy
    Properties:
      Queues: [!Ref Queue]
      PolicyDocument:
        Version: 2012-10-17
        Statement:
          - Effect: Allow
            Principal: {Service: events.amazonaws.com}
            Action: sqs:SendMessage
            Resource: !GetAtt Queue.Arn
  Topic:
    Type: AWS::SNS::Topic
    Properties:
      TopicName: core-topic
      DisplayName: core
      Subscription:
        - {Protocol: sqs, Endpoint: !GetAtt Queue.Arn}
  TopicPolicy:
    Type: AWS::SNS::TopicPolicy
    Properties:
      Topics: [!Ref Topic]
      PolicyDocument:
        Version: 2012-10-17
        Statement:
          - Effect: Allow
            Principal: {AWS: "*"}
            Action: sns:Publish
            Resource: !Ref Topic
  Rule:
    Type: AWS::Events::Rule
    Properties:
      Name: core-rule
      Description: every hour
      ScheduleExpression: rate(1 hour)
      State: DISABLED
      Targets:
        - {Id: q, Arn: !GetAtt Queue.Arn, Input: '{"hello":"world"}'}
  MachineRole:
    Type: AWS::IAM::Role
    Properties:
      RoleName: core-machine-role
      Path: /svc/
      Description: state machine role
      MaxSessionDuration: 7200
      AssumeRolePolicyDocument:
        Version: 2012-10-17
        Statement:
          - {Effect: Allow, Principal: {Service: states.amazonaws.com}, Action: "sts:AssumeRole"}
      Policies:
        - PolicyName: inline
          PolicyDocument:
            Version: 2012-10-17
            Statement: [{Effect: Allow, Action: "sqs:SendMessage", Resource: "*"}]
      Tags: [{Key: t, Value: v}]
  Machine:
    Type: AWS::StepFunctions::StateMachine
    Properties:
      StateMachineName: core-machine
      RoleArn: !GetAtt MachineRole.Arn
      DefinitionString: |
        {"StartAt":"Hello","States":{"Hello":{"Type":"Pass","Result":"${Greeting}","End":true}}}
      DefinitionSubstitutions: {Greeting: hi}
  Group:
    Type: AWS::IAM::Group
    Properties: {GroupName: core-group}
  Managed:
    Type: AWS::IAM::ManagedPolicy
    Properties:
      ManagedPolicyName: core-managed
      Groups: [!Ref Group]
      PolicyDocument:
        Version: 2012-10-17
        Statement: [{Effect: Allow, Action: "s3:ListAllMyBuckets", Resource: "*"}]
  Person:
    Type: AWS::IAM::User
    Properties:
      UserName: core-user
      Groups: [!Ref Group]
      Policies:
        - PolicyName: own
          PolicyDocument:
            Version: 2012-10-17
            Statement: [{Effect: Allow, Action: "sqs:ListQueues", Resource: "*"}]
  RolePolicy:
    Type: AWS::IAM::Policy
    Properties:
      PolicyName: attached
      Roles: [!Ref MachineRole]
      PolicyDocument:
        Version: 2012-10-17
        Statement: [{Effect: Allow, Action: "sns:Publish", Resource: !Ref Topic}]
  Bucket:
    Type: AWS::S3::Bucket
    Properties:
      BucketName: core-bucket-cfn
      VersioningConfiguration: {Status: Enabled}
      Tags: [{Key: env, Value: test}]
  BucketPolicy:
    Type: AWS::S3::BucketPolicy
    Properties:
      Bucket: !Ref Bucket
      PolicyDocument:
        Version: 2012-10-17
        Statement:
          - Effect: Allow
            Principal: "*"
            Action: s3:GetObject
            Resource: !Sub "${Bucket.Arn}/*"
Outputs:
  TableArn: {Value: !GetAtt Table.Arn}
  StreamArn: {Value: !GetAtt Table.StreamArn}
  LogsArn: {Value: !GetAtt Logs.Arn}
  SecretArn: {Value: !Ref Creds}
  KeyId: {Value: !Ref Key}
  KeyArn: {Value: !GetAtt Key.Arn}
  RuleArn: {Value: !GetAtt Rule.Arn}
  MachineArn: {Value: !Ref Machine}
  MachineName: {Value: !GetAtt Machine.Name}
  RoleId: {Value: !GetAtt MachineRole.RoleId}
  ManagedArn: {Value: !Ref Managed}
  UserArn: {Value: !GetAtt Person.Arn}
  Domain: {Value: !GetAtt Bucket.DomainName}
`

func TestCoreResourceTypes(t *testing.T) {
	e := newEnv(t, true)
	acct := e.Env.AccountID
	tf := write(t, "core.yaml", coreTemplate)
	e.AWS(t, "cloudformation", "create-stack", "--stack-name", "core", "--template-body", "file://"+tf, "--capabilities", "CAPABILITY_NAMED_IAM")
	st := e.waitFor(t, "core", "CREATE_COMPLETE")
	out := outputs(st)
	want := map[string]string{
		"TableArn":    "arn:aws:dynamodb:us-east-1:" + acct + ":table/core-orders",
		"LogsArn":     "arn:aws:logs:us-east-1:" + acct + ":log-group:/core/app:*",
		"MachineName": "core-machine",
		"UserArn":     "arn:aws:iam::" + acct + ":user/core-user",
		"ManagedArn":  "arn:aws:iam::" + acct + ":policy/core-managed",
		"Domain":      "core-bucket-cfn.s3.amazonaws.com",
		"MachineArn":  "arn:aws:states:us-east-1:" + acct + ":stateMachine:core-machine",
	}
	for k, v := range want {
		if out[k] != v {
			t.Errorf("output %s = %q, want %q", k, out[k], v)
		}
	}
	if !strings.Contains(out["StreamArn"], "core-orders") || !strings.HasPrefix(out["SecretArn"], "arn:aws:secretsmanager:us-east-1:"+acct+":secret:core/db") ||
		!strings.HasPrefix(out["KeyArn"], "arn:aws:kms:us-east-1:"+acct+":key/") || out["KeyId"] == "" || !strings.HasSuffix(out["RuleArn"], ":rule/core-rule") || out["RoleId"] == "" {
		t.Fatalf("outputs: %v", out)
	}
	if t.Failed() {
		t.FailNow()
	}

	tbl := e.AWSJSON(t, "dynamodb", "describe-table", "--table-name", "core-orders")["Table"].(map[string]any)
	if len(tbl["KeySchema"].([]any)) != 2 || len(tbl["GlobalSecondaryIndexes"].([]any)) != 1 || tbl["StreamSpecification"] == nil {
		t.Fatalf("table: %v", tbl)
	}
	if lg := e.AWSJSON(t, "logs", "describe-log-groups", "--log-group-name-prefix", "/core")["logGroups"].([]any); len(lg) != 1 || lg[0].(map[string]any)["retentionInDays"] != float64(14) {
		t.Fatalf("log groups: %v", lg)
	}
	sec := e.AWSJSON(t, "secretsmanager", "get-secret-value", "--secret-id", "core/db")
	var val map[string]string
	if err := json.Unmarshal([]byte(str(sec, "SecretString")), &val); err != nil || val["username"] != "admin" || len(val["password"]) != 20 || strings.ContainsAny(val["password"], "!#$%&*()-_=+[]{}<>:?") {
		t.Fatalf("generated secret: %v %v", sec["SecretString"], err)
	}
	if k := e.AWSJSON(t, "kms", "describe-key", "--key-id", "alias/core-key")["KeyMetadata"].(map[string]any); str(k, "KeyId") != out["KeyId"] {
		t.Fatalf("alias target: %v", k)
	}
	if r := e.AWSJSON(t, "kms", "get-key-rotation-status", "--key-id", out["KeyId"]); r["KeyRotationEnabled"] != true {
		t.Fatalf("rotation: %v", r)
	}
	rule := e.AWSJSON(t, "events", "describe-rule", "--name", "core-rule")
	if str(rule, "ScheduleExpression") != "rate(1 hour)" || str(rule, "State") != "DISABLED" {
		t.Fatalf("rule: %v", rule)
	}
	if tg := e.AWSJSON(t, "events", "list-targets-by-rule", "--rule", "core-rule")["Targets"].([]any); len(tg) != 1 || str(tg[0].(map[string]any), "Input") != `{"hello":"world"}` {
		t.Fatalf("targets: %v", tg)
	}
	if sm := e.AWSJSON(t, "stepfunctions", "describe-state-machine", "--state-machine-arn", out["MachineArn"]); !strings.Contains(str(sm, "definition"), `"hi"`) || !strings.HasSuffix(str(sm, "roleArn"), ":role/svc/core-machine-role") {
		t.Fatalf("state machine: %v", sm)
	}
	role := e.AWSJSON(t, "iam", "get-role", "--role-name", "core-machine-role")["Role"].(map[string]any)
	if role["MaxSessionDuration"] != float64(7200) || str(role, "Path") != "/svc/" {
		t.Fatalf("role: %v", role)
	}
	if ip := e.AWSJSON(t, "iam", "list-role-policies", "--role-name", "core-machine-role")["PolicyNames"].([]any); len(ip) != 2 {
		t.Fatalf("role inline policies (own + attached): %v", ip)
	}
	if gp := e.AWSJSON(t, "iam", "list-attached-group-policies", "--group-name", "core-group")["AttachedPolicies"].([]any); len(gp) != 1 {
		t.Fatalf("group policies: %v", gp)
	}
	if gr := e.AWSJSON(t, "iam", "list-groups-for-user", "--user-name", "core-user")["Groups"].([]any); len(gr) != 1 {
		t.Fatalf("user groups: %v", gr)
	}
	if up := e.AWSJSON(t, "iam", "list-user-policies", "--user-name", "core-user")["PolicyNames"].([]any); len(up) != 1 || up[0] != "own" {
		t.Fatalf("user policies: %v", up)
	}
	if pol := e.AWS(t, "sqs", "get-queue-attributes", "--queue-url", e.AWSJSON(t, "sqs", "get-queue-url", "--queue-name", "core-queue")["QueueUrl"].(string), "--attribute-names", "Policy"); !strings.Contains(pol, "events.amazonaws.com") {
		t.Fatalf("queue policy: %s", pol)
	}
	if pol := e.AWS(t, "sns", "get-topic-attributes", "--topic-arn", "arn:aws:sns:us-east-1:"+acct+":core-topic"); !strings.Contains(pol, "sns:Publish") || !strings.Contains(pol, `\"DisplayName\"`) && !strings.Contains(pol, "DisplayName") {
		t.Fatalf("topic: %s", pol)
	}
	if subs := e.AWSJSON(t, "sns", "list-subscriptions-by-topic", "--topic-arn", "arn:aws:sns:us-east-1:"+acct+":core-topic")["Subscriptions"].([]any); len(subs) != 1 {
		t.Fatalf("inline topic subscription (Post sees the template properties): %v", subs)
	}
	if pol := e.AWS(t, "s3api", "get-bucket-policy", "--bucket", "core-bucket-cfn"); !strings.Contains(pol, "s3:GetObject") {
		t.Fatalf("bucket policy: %s", pol)
	}
	if v := e.AWSJSON(t, "s3api", "get-bucket-versioning", "--bucket", "core-bucket-cfn"); v["Status"] != "Enabled" {
		t.Fatalf("versioning: %v", v)
	}
	res := e.AWSJSON(t, "cloudformation", "list-stack-resources", "--stack-name", "core")["StackResourceSummaries"].([]any)
	if len(res) != 18 {
		t.Fatalf("%d resources", len(res))
	}

	// Removing resources from the template deletes them, in reverse dependency order.
	small := write(t, "small.yaml", `
Resources:
  Queue:
    Type: AWS::SQS::Queue
    Properties: {QueueName: core-queue}
`)
	e.AWS(t, "cloudformation", "update-stack", "--stack-name", "core", "--template-body", "file://"+small, "--capabilities", "CAPABILITY_NAMED_IAM")
	e.waitFor(t, "core", "UPDATE_COMPLETE")
	for _, c := range [][]string{
		{"dynamodb", "describe-table", "--table-name", "core-orders"},
		{"iam", "get-role", "--role-name", "core-machine-role"},
		{"iam", "get-user", "--user-name", "core-user"},
		{"iam", "get-group", "--group-name", "core-group"},
		{"kms", "describe-key", "--key-id", "alias/core-key"},
		{"s3api", "head-bucket", "--bucket", "core-bucket-cfn"},
		{"secretsmanager", "describe-secret", "--secret-id", "core/db"},
		{"events", "describe-rule", "--name", "core-rule"},
	} {
		if o, err := e.AWSErr(t, c...); err == nil {
			t.Errorf("aws %v still works after removal from the template: %s", c, o)
		}
	}
	if o, err := e.AWSErr(t, "iam", "get-policy", "--policy-arn", out["ManagedArn"]); err == nil {
		t.Errorf("managed policy survived: %s", o)
	}
	e.AWS(t, "cloudformation", "delete-stack", "--stack-name", "core")
	e.waitGone(t, "core")
}
