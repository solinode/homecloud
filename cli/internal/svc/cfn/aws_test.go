package cfn_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/homecloudhq/homecloud/cli/internal/awsapi/awstest"
	"github.com/homecloudhq/homecloud/cli/internal/core"
	"github.com/homecloudhq/homecloud/cli/internal/httpx"
	"github.com/homecloudhq/homecloud/cli/internal/svc/cfn"
	"github.com/homecloudhq/homecloud/cli/internal/svc/kms"
	"github.com/homecloudhq/homecloud/cli/internal/svc/s3"
	"github.com/homecloudhq/homecloud/cli/internal/svc/sns"
	"github.com/homecloudhq/homecloud/cli/internal/svc/sqs"
	"github.com/homecloudhq/homecloud/cli/internal/svc/ssm"
)

// These tests drive the AWS CloudFormation endpoint with the real AWS CLI and
// boto3, on top of the services stacks create. S3 needs a throwaway MinIO
// container (tests that use it are skipped without Docker).

var (
	minioOnce sync.Once
	minioAddr string
	minioName string
	minioErr  string
)

const minioUser, minioPass = "cfntestroot", "cfntest-secret-key-123"

func TestMain(m *testing.M) {
	code := m.Run()
	if minioName != "" {
		_ = exec.Command("docker", "rm", "-f", "-v", minioName).Run()
	}
	os.Exit(code)
}

func startMinIO(t *testing.T) string {
	t.Helper()
	minioOnce.Do(func() {
		if exec.Command("docker", "info").Run() != nil {
			minioErr = "docker not available"
			return
		}
		name := "cfntest-" + strings.ToLower(core.RandHex(6))
		out, err := exec.Command("docker", "run", "-d", "--name", name, "--user", "0", "-p", "127.0.0.1::9000",
			"-e", "MINIO_ROOT_USER="+minioUser, "-e", "MINIO_ROOT_PASSWORD="+minioPass,
			"cgr.dev/chainguard/minio:latest", "server", "/data").CombinedOutput()
		if err != nil {
			minioErr = "start minio: " + string(out)
			return
		}
		minioName = name
		out, err = exec.Command("docker", "port", name, "9000/tcp").Output()
		if err != nil {
			minioErr = "docker port: " + err.Error()
			return
		}
		minioAddr = strings.TrimSpace(strings.Split(strings.TrimSpace(string(out)), "\n")[0])
	})
	if minioErr != "" {
		t.Skip(minioErr)
	}
	return minioAddr
}

type env struct {
	*awstest.Harness
	CFN *cfn.Service
}

// newEnv starts an AWS endpoint with CloudFormation and the services stacks use.
func newEnv(t *testing.T, withS3 bool) *env {
	t.Helper()
	h := awstest.New(t)
	q := sqs.New(h.Env)
	q.Routes(h.Router)
	q.RegisterAWS()
	sn := sns.New(h.Env, q, nil)
	sn.Routes(h.Router)
	sn.RegisterAWS()
	k := kms.New(h.Env, h.Secrets)
	k.Routes(h.Router)
	k.RegisterAWS()
	p := ssm.New(h.Env, k)
	p.Routes(h.Router)
	p.RegisterAWS()
	if withS3 {
		addr := startMinIO(t)
		b := s3.New(h.Env, h.Secrets)
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		if err := b.UseMinIO(ctx, addr, minioUser, minioPass); err != nil {
			t.Fatal(err)
		}
		b.Routes(h.Router)
		b.RegisterAWS()
	}
	c := cfn.New(h.Env)
	c.Handler = h.Mux
	c.Refresh = h.IAM.Refresh
	c.RolePrincipal = func(arn string) (*httpx.Principal, error) {
		return h.IAM.ServiceRolePrincipal(arn, "cloudformation.amazonaws.com", "HomeCloudCloudFormation")
	}
	c.Routes(h.Router)
	c.RegisterAWS()
	return &env{Harness: h, CFN: c}
}

func write(t *testing.T, name, body string) string {
	t.Helper()
	f := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(f, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return f
}

func str(m map[string]any, k string) string { s, _ := m[k].(string); return s }

func (e *env) stack(t *testing.T, name string) map[string]any {
	t.Helper()
	out := e.AWSJSON(t, "cloudformation", "describe-stacks", "--stack-name", name)
	return out["Stacks"].([]any)[0].(map[string]any)
}

// waitFor polls a stack until its status is one of want.
func (e *env) waitFor(t *testing.T, name string, want ...string) map[string]any {
	t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	var last string
	for time.Now().Before(deadline) {
		out, err := e.AWSErr(t, "cloudformation", "describe-stacks", "--stack-name", name)
		if err == nil {
			var m map[string]any
			_ = json.Unmarshal([]byte(out), &m)
			st := m["Stacks"].([]any)[0].(map[string]any)
			last = str(st, "StackStatus")
			for _, w := range want {
				if last == w {
					return st
				}
			}
			if strings.HasSuffix(last, "_COMPLETE") || strings.HasSuffix(last, "_FAILED") {
				t.Fatalf("stack %s reached %s (%s), want %v", name, last, str(st, "StackStatusReason"), want)
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("stack %s is %s, want %v", name, last, want)
	return nil
}

func outputs(st map[string]any) map[string]string {
	out := map[string]string{}
	for _, o := range st["Outputs"].([]any) {
		om := o.(map[string]any)
		out[str(om, "OutputKey")] = str(om, "OutputValue")
	}
	return out
}

func (e *env) events(t *testing.T, name string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, ev := range e.AWSJSON(t, "cloudformation", "describe-stack-events", "--stack-name", name)["StackEvents"].([]any) {
		out = append(out, ev.(map[string]any))
	}
	return out
}

func hasEvent(evs []map[string]any, logical, status string) (map[string]any, bool) {
	for _, ev := range evs {
		if str(ev, "LogicalResourceId") == logical && str(ev, "ResourceStatus") == status {
			return ev, true
		}
	}
	return nil, false
}

const appTemplate = `
AWSTemplateFormatVersion: "2010-09-09"
Description: queue, topic, subscription, bucket and parameter
Parameters:
  Env:
    Type: String
    Default: dev
    AllowedValues: [dev, prod]
  Timeout:
    Type: Number
    Default: 30
  DbPassword:
    Type: String
    NoEcho: true
    Default: hunter2
Resources:
  Jobs:
    Type: AWS::SQS::Queue
    Properties:
      QueueName: !Sub "jobs-${Env}"
      VisibilityTimeout: !Ref Timeout
      Tags:
        - Key: team
          Value: core
  Events:
    Type: AWS::SNS::Topic
    Properties:
      TopicName: !Join ["-", [events, !Ref Env]]
  Sub:
    Type: AWS::SNS::Subscription
    Properties:
      TopicArn: !Ref Events
      Protocol: sqs
      Endpoint: !GetAtt Jobs.Arn
  Uploads:
    Type: AWS::S3::Bucket
    Properties:
      BucketName: !Sub "uploads-${AWS::StackName}-${AWS::AccountId}"
  Config:
    Type: AWS::SSM::Parameter
    Properties:
      Name: !Sub "/${AWS::StackName}/queue-url"
      Type: String
      Value: !Ref Jobs
      Description: !Sub "queue of ${AWS::Region} in ${AWS::Partition}"
Outputs:
  QueueUrl:
    Description: the queue
    Value: !Ref Jobs
    Export:
      Name: !Sub "${AWS::StackName}-QueueUrl"
  QueueArn:
    Value: !GetAtt Jobs.Arn
  TopicArn:
    Value: !Ref Events
  Bucket:
    Value: !Ref Uploads
  BucketArn:
    Value: !GetAtt Uploads.Arn
  StackId:
    Value: !Ref AWS::StackId
`

func TestStackLifecycleCLI(t *testing.T) {
	e := newEnv(t, true)
	tf := write(t, "app.yaml", appTemplate)
	acct := e.Env.AccountID

	if o, err := e.AWSErr(t, "cloudformation", "validate-template", "--template-body", "file://"+tf); err != nil || !strings.Contains(o, "DbPassword") {
		t.Fatalf("validate: %v %s", err, o)
	}
	sum := e.AWSJSON(t, "cloudformation", "get-template-summary", "--template-body", "file://"+tf)
	if len(sum["ResourceTypes"].([]any)) != 5 || str(sum, "Description") == "" {
		t.Fatalf("summary: %v", sum)
	}

	created := e.AWSJSON(t, "cloudformation", "create-stack", "--stack-name", "app", "--template-body", "file://"+tf,
		"--parameters", "ParameterKey=Timeout,ParameterValue=45", "--tags", "Key=owner,Value=qa")
	id := str(created, "StackId")
	if !strings.HasPrefix(id, "arn:aws:cloudformation:us-east-1:"+acct+":stack/app/") || len(strings.Split(id, "/")[2]) != 36 {
		t.Fatalf("stack id %q", id)
	}
	if o, err := e.AWSErr(t, "cloudformation", "create-stack", "--stack-name", "app", "--template-body", "file://"+tf); err == nil || !strings.Contains(o, "AlreadyExistsException") {
		t.Fatalf("duplicate stack: %v %s", err, o)
	}
	st := e.waitFor(t, "app", "CREATE_COMPLETE")
	out := outputs(st)
	if !strings.HasSuffix(out["QueueUrl"], "/"+acct+"/jobs-dev") || out["QueueArn"] != "arn:aws:sqs:us-east-1:"+acct+":jobs-dev" ||
		out["TopicArn"] != "arn:aws:sns:us-east-1:"+acct+":events-dev" || out["Bucket"] != "uploads-app-"+acct || out["StackId"] != id ||
		out["BucketArn"] != "arn:aws:s3:::uploads-app-"+acct {
		t.Fatalf("outputs: %v", out)
	}
	if str(st, "StackId") != id || str(st, "Description") == "" {
		t.Fatalf("describe: %v", st)
	}
	params := map[string]string{}
	for _, p := range st["Parameters"].([]any) {
		pm := p.(map[string]any)
		params[str(pm, "ParameterKey")] = str(pm, "ParameterValue")
	}
	if params["Timeout"] != "45" || params["Env"] != "dev" || params["DbPassword"] != "****" {
		t.Fatalf("parameters (NoEcho must be masked): %v", params)
	}
	if tags := st["Tags"].([]any); len(tags) != 1 || str(tags[0].(map[string]any), "Key") != "owner" {
		t.Fatalf("tags: %v", st["Tags"])
	}

	// The resources are real, and reachable through their own AWS APIs by the Ref values.
	qa := e.AWSJSON(t, "sqs", "get-queue-attributes", "--queue-url", out["QueueUrl"], "--attribute-names", "VisibilityTimeout")["Attributes"].(map[string]any)
	if qa["VisibilityTimeout"] != "45" {
		t.Fatalf("queue attributes: %v", qa)
	}
	subs := e.AWSJSON(t, "sns", "list-subscriptions-by-topic", "--topic-arn", out["TopicArn"])["Subscriptions"].([]any)
	if len(subs) != 1 || str(subs[0].(map[string]any), "Endpoint") != out["QueueArn"] {
		t.Fatalf("subscriptions: %v", subs)
	}
	e.AWS(t, "s3api", "head-bucket", "--bucket", out["Bucket"])
	param := e.AWSJSON(t, "ssm", "get-parameter", "--name", "/app/queue-url")["Parameter"].(map[string]any)
	if param["Value"] != out["QueueUrl"] {
		t.Fatalf("ssm parameter: %v", param)
	}

	// Resources, events, template.
	res := e.AWSJSON(t, "cloudformation", "list-stack-resources", "--stack-name", "app")["StackResourceSummaries"].([]any)
	if len(res) != 5 {
		t.Fatalf("list-stack-resources: %v", res)
	}
	byLogical := map[string]map[string]any{}
	for _, r := range res {
		rm := r.(map[string]any)
		byLogical[str(rm, "LogicalResourceId")] = rm
		if str(rm, "ResourceStatus") != "CREATE_COMPLETE" {
			t.Fatalf("resource status: %v", rm)
		}
	}
	if byLogical["Jobs"]["ResourceType"] != "AWS::SQS::Queue" || byLogical["Jobs"]["PhysicalResourceId"] != out["QueueUrl"] ||
		byLogical["Uploads"]["PhysicalResourceId"] != out["Bucket"] || byLogical["Events"]["PhysicalResourceId"] != out["TopicArn"] {
		t.Fatalf("resources: %v", byLogical)
	}
	one := e.AWSJSON(t, "cloudformation", "describe-stack-resource", "--stack-name", "app", "--logical-resource-id", "Config")["StackResourceDetail"].(map[string]any)
	if one["PhysicalResourceId"] != "/app/queue-url" || one["ResourceType"] != "AWS::SSM::Parameter" {
		t.Fatalf("describe-stack-resource: %v", one)
	}
	if o, err := e.AWSErr(t, "cloudformation", "describe-stack-resource", "--stack-name", "app", "--logical-resource-id", "Nope"); err == nil || !strings.Contains(o, "ValidationError") {
		t.Fatalf("missing resource: %v %s", err, o)
	}
	many := e.AWSJSON(t, "cloudformation", "describe-stack-resources", "--stack-name", "app")["StackResources"].([]any)
	if len(many) != 5 {
		t.Fatalf("describe-stack-resources: %v", many)
	}
	evs := e.events(t, "app")
	if ev, ok := hasEvent(evs, "app", "CREATE_IN_PROGRESS"); !ok || str(ev, "ResourceType") != "AWS::CloudFormation::Stack" || str(ev, "ResourceStatusReason") != "User Initiated" || str(ev, "PhysicalResourceId") != id {
		t.Fatalf("stack event: %v", ev)
	}
	if _, ok := hasEvent(evs, "app", "CREATE_COMPLETE"); !ok {
		t.Fatalf("no CREATE_COMPLETE: %v", evs)
	}
	if ev, ok := hasEvent(evs, "Jobs", "CREATE_COMPLETE"); !ok || str(ev, "PhysicalResourceId") != out["QueueUrl"] || str(ev, "EventId") == "" {
		t.Fatalf("resource event: %v", ev)
	}
	tb := e.AWS(t, "cloudformation", "get-template", "--stack-name", "app")
	if !strings.Contains(tb, "DbPassword") {
		t.Fatalf("get-template: %s", tb)
	}
	exp := e.AWSJSON(t, "cloudformation", "list-exports")["Exports"].([]any)
	if len(exp) != 1 || str(exp[0].(map[string]any), "Name") != "app-QueueUrl" || str(exp[0].(map[string]any), "Value") != out["QueueUrl"] {
		t.Fatalf("exports: %v", exp)
	}
	ls := e.AWSJSON(t, "cloudformation", "list-stacks", "--stack-status-filter", "CREATE_COMPLETE")["StackSummaries"].([]any)
	if len(ls) != 1 || str(ls[0].(map[string]any), "StackName") != "app" {
		t.Fatalf("list-stacks: %v", ls)
	}

	// Update: a new parameter value changes the queue; the topic is untouched.
	if o, err := e.AWSErr(t, "cloudformation", "update-stack", "--stack-name", "app", "--use-previous-template",
		"--parameters", "ParameterKey=Timeout,UsePreviousValue=true", "ParameterKey=Env,UsePreviousValue=true", "ParameterKey=DbPassword,UsePreviousValue=true"); err == nil || !strings.Contains(o, "No updates are to be performed") {
		t.Fatalf("no-op update: %v %s", err, o)
	}
	e.AWS(t, "cloudformation", "update-stack", "--stack-name", "app", "--use-previous-template",
		"--parameters", "ParameterKey=Timeout,ParameterValue=90", "ParameterKey=Env,UsePreviousValue=true", "ParameterKey=DbPassword,UsePreviousValue=true")
	st = e.waitFor(t, "app", "UPDATE_COMPLETE")
	if str(st, "LastUpdatedTime") == "" {
		t.Fatalf("no LastUpdatedTime: %v", st)
	}
	qa = e.AWSJSON(t, "sqs", "get-queue-attributes", "--queue-url", out["QueueUrl"], "--attribute-names", "VisibilityTimeout")["Attributes"].(map[string]any)
	if qa["VisibilityTimeout"] != "90" {
		t.Fatalf("after update: %v", qa)
	}
	if _, ok := hasEvent(e.events(t, "app"), "Events", "DELETE_IN_PROGRESS"); ok {
		t.Fatal("the topic did not change but was replaced")
	}

	// Delete.
	e.AWS(t, "cloudformation", "delete-stack", "--stack-name", "app")
	e.waitGone(t, "app")
	if o, err := e.AWSErr(t, "sqs", "get-queue-url", "--queue-name", "jobs-dev"); err == nil {
		t.Fatalf("queue survived the stack: %s", o)
	}
	if o, err := e.AWSErr(t, "s3api", "head-bucket", "--bucket", out["Bucket"]); err == nil {
		t.Fatalf("bucket survived the stack: %s", o)
	}
	if o, err := e.AWSErr(t, "ssm", "get-parameter", "--name", "/app/queue-url"); err == nil {
		t.Fatalf("parameter survived the stack: %s", o)
	}
	// A deleted stack is still readable by its ID.
	del := e.AWSJSON(t, "cloudformation", "describe-stacks", "--stack-name", id)["Stacks"].([]any)[0].(map[string]any)
	if str(del, "StackStatus") != "DELETE_COMPLETE" || str(del, "DeletionTime") == "" {
		t.Fatalf("deleted stack by id: %v", del)
	}
	if o, err := e.AWSErr(t, "cloudformation", "describe-stacks", "--stack-name", "app"); err == nil || !strings.Contains(o, "does not exist") {
		t.Fatalf("deleted stack by name: %v %s", err, o)
	}
	dl := e.AWSJSON(t, "cloudformation", "list-stacks", "--stack-status-filter", "DELETE_COMPLETE")["StackSummaries"].([]any)
	if len(dl) != 1 {
		t.Fatalf("list-stacks deleted: %v", dl)
	}
	// The name can be reused.
	e.AWS(t, "cloudformation", "create-stack", "--stack-name", "app", "--template-body", "file://"+tf)
	e.waitFor(t, "app", "CREATE_COMPLETE")
}

func (e *env) waitGone(t *testing.T, name string) {
	t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		out, err := e.AWSErr(t, "cloudformation", "describe-stacks", "--stack-name", name)
		if err != nil && strings.Contains(out, "does not exist") {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("stack %s was not deleted", name)
}

func TestDeployChangeSetPath(t *testing.T) {
	e := newEnv(t, false)
	tf := write(t, "d.yaml", `
Parameters:
  Retention: {Type: Number, Default: 60}
Resources:
  Q:
    Type: AWS::SQS::Queue
    Properties: {QueueName: deployed, MessageRetentionPeriod: !Ref Retention}
Outputs:
  Url: {Value: !Ref Q}
`)
	first, err := e.AWSAs(t, e.AccessKeyID, e.SecretKey, "", "cloudformation", "deploy", "--template-file", tf, "--stack-name", "dep")
	if err != nil || !strings.Contains(first, "Successfully created/updated stack - dep") {
		t.Fatalf("first deploy: %v\n%s", err, first)
	}
	st := e.stack(t, "dep")
	if str(st, "StackStatus") != "CREATE_COMPLETE" {
		t.Fatalf("status %v", st)
	}
	time.Sleep(1100 * time.Millisecond) // the CLI names change sets by the second
	second, err := e.AWSAs(t, e.AccessKeyID, e.SecretKey, "", "cloudformation", "deploy", "--template-file", tf, "--stack-name", "dep")
	if err != nil || !strings.Contains(second, "No changes to deploy") {
		t.Fatalf("second deploy must be a no-op: %v\n%s", err, second)
	}
	// A change goes through an UPDATE change set.
	time.Sleep(1100 * time.Millisecond)
	third, err := e.AWSAs(t, e.AccessKeyID, e.SecretKey, "", "cloudformation", "deploy", "--template-file", tf, "--stack-name", "dep",
		"--parameter-overrides", "Retention=120")
	if err != nil || !strings.Contains(third, "Successfully created/updated stack - dep") {
		t.Fatalf("third deploy: %v\n%s", err, third)
	}
	st = e.waitFor(t, "dep", "UPDATE_COMPLETE")
	qa := e.AWSJSON(t, "sqs", "get-queue-attributes", "--queue-url", outputs(st)["Url"], "--attribute-names", "MessageRetentionPeriod")["Attributes"].(map[string]any)
	if qa["MessageRetentionPeriod"] != "120" {
		t.Fatalf("queue after deploy: %v", qa)
	}

	// Change set API details.
	cs := e.AWSJSON(t, "cloudformation", "create-change-set", "--stack-name", "dep", "--change-set-name", "preview", "--use-previous-template",
		"--parameters", "ParameterKey=Retention,ParameterValue=300")
	if !strings.Contains(str(cs, "Id"), ":changeSet/preview/") {
		t.Fatalf("change set id: %v", cs)
	}
	d := e.AWSJSON(t, "cloudformation", "describe-change-set", "--stack-name", "dep", "--change-set-name", "preview")
	changes := d["Changes"].([]any)
	if str(d, "Status") != "CREATE_COMPLETE" || str(d, "ExecutionStatus") != "AVAILABLE" || len(changes) != 1 {
		t.Fatalf("describe-change-set: %v", d)
	}
	rc := changes[0].(map[string]any)["ResourceChange"].(map[string]any)
	if rc["Action"] != "Modify" || rc["LogicalResourceId"] != "Q" || rc["ResourceType"] != "AWS::SQS::Queue" {
		t.Fatalf("resource change: %v", rc)
	}
	ls := e.AWSJSON(t, "cloudformation", "list-change-sets", "--stack-name", "dep")["Summaries"].([]any)
	seen := map[string]string{}
	for _, c := range ls {
		cm := c.(map[string]any)
		seen[str(cm, "ChangeSetName")] = str(cm, "ExecutionStatus")
	}
	if len(ls) != 4 || seen["preview"] != "AVAILABLE" {
		t.Fatalf("list-change-sets: %v", seen)
	}
	if o, err := e.AWSErr(t, "cloudformation", "create-change-set", "--stack-name", "dep", "--change-set-name", "preview", "--use-previous-template"); err == nil || !strings.Contains(o, "AlreadyExistsException") {
		t.Fatalf("duplicate change set: %v %s", err, o)
	}
	e.AWS(t, "cloudformation", "delete-change-set", "--stack-name", "dep", "--change-set-name", "preview")
	if o, err := e.AWSErr(t, "cloudformation", "describe-change-set", "--stack-name", "dep", "--change-set-name", "preview"); err == nil || !strings.Contains(o, "ChangeSetNotFound") {
		t.Fatalf("deleted change set: %v %s", err, o)
	}
	// An identical template makes a FAILED change set that cannot be executed.
	e.AWS(t, "cloudformation", "create-change-set", "--stack-name", "dep", "--change-set-name", "same", "--template-body", "file://"+tf, "--parameters", "ParameterKey=Retention,ParameterValue=120")
	d = e.AWSJSON(t, "cloudformation", "describe-change-set", "--stack-name", "dep", "--change-set-name", "same")
	if str(d, "Status") != "FAILED" || !strings.Contains(str(d, "StatusReason"), "didn't contain changes") {
		t.Fatalf("no-op change set: %v", d)
	}
	if o, err := e.AWSErr(t, "cloudformation", "execute-change-set", "--stack-name", "dep", "--change-set-name", "same"); err == nil || !strings.Contains(o, "InvalidChangeSetStatus") {
		t.Fatalf("execute failed change set: %v %s", err, o)
	}
	// CREATE change sets leave the stack in REVIEW_IN_PROGRESS until executed.
	tf = write(t, "fresh.yaml", "Resources:\n  Q:\n    Type: AWS::SQS::Queue\n    Properties: {QueueName: fresh-queue}\n")
	e.AWS(t, "cloudformation", "create-change-set", "--stack-name", "fresh", "--change-set-name", "init", "--change-set-type", "CREATE", "--template-body", "file://"+tf)
	if s := e.stack(t, "fresh"); str(s, "StackStatus") != "REVIEW_IN_PROGRESS" {
		t.Fatalf("review stack: %v", s)
	}
	e.AWS(t, "cloudformation", "execute-change-set", "--stack-name", "fresh", "--change-set-name", "init")
	e.waitFor(t, "fresh", "CREATE_COMPLETE")
	if o, err := e.AWSErr(t, "cloudformation", "create-change-set", "--stack-name", "fresh", "--change-set-name", "again", "--change-set-type", "CREATE", "--template-body", "file://"+tf); err == nil || !strings.Contains(o, "AlreadyExistsException") {
		t.Fatalf("CREATE on existing stack: %v %s", err, o)
	}
	if o, err := e.AWSErr(t, "cloudformation", "create-change-set", "--stack-name", "ghost", "--change-set-name", "x", "--template-body", "file://"+tf); err == nil || !strings.Contains(o, "does not exist") {
		t.Fatalf("UPDATE on missing stack: %v %s", err, o)
	}
}

func TestFailureRollbackAndUnsupported(t *testing.T) {
	e := newEnv(t, false)
	bad := write(t, "bad.yaml", `
Resources:
  Q:
    Type: AWS::SQS::Queue
    Properties: {QueueName: rollback-q}
  Topic:
    Type: AWS::SNS::Topic
    Properties: {TopicName: rollback-t}
  Sub:
    Type: AWS::SNS::Subscription
    Properties:
      TopicArn: !Ref Topic
      Protocol: carrier-pigeon
      Endpoint: !GetAtt Q.Arn
`)
	e.AWS(t, "cloudformation", "create-stack", "--stack-name", "bad", "--template-body", "file://"+bad)
	st := e.waitFor(t, "bad", "ROLLBACK_COMPLETE")
	if !strings.Contains(str(st, "StackStatusReason"), "[Sub]") {
		t.Fatalf("status reason: %v", st["StackStatusReason"])
	}
	evs := e.events(t, "bad")
	ev, ok := hasEvent(evs, "Sub", "CREATE_FAILED")
	if !ok || str(ev, "ResourceStatusReason") == "" {
		t.Fatalf("no CREATE_FAILED with a reason: %v", evs)
	}
	for _, want := range [][2]string{{"Q", "DELETE_COMPLETE"}, {"Topic", "DELETE_COMPLETE"}, {"bad", "ROLLBACK_IN_PROGRESS"}, {"bad", "ROLLBACK_COMPLETE"}} {
		if _, ok := hasEvent(evs, want[0], want[1]); !ok {
			t.Fatalf("no %v event: %v", want, evs)
		}
	}
	if o, err := e.AWSErr(t, "sqs", "get-queue-url", "--queue-name", "rollback-q"); err == nil {
		t.Fatalf("rolled-back queue survived: %s", o)
	}
	// ROLLBACK_COMPLETE stacks cannot be updated, only deleted.
	if o, err := e.AWSErr(t, "cloudformation", "update-stack", "--stack-name", "bad", "--template-body", "file://"+bad); err == nil || !strings.Contains(o, "ROLLBACK_COMPLETE") {
		t.Fatalf("update of a rolled-back stack: %v %s", err, o)
	}
	e.AWS(t, "cloudformation", "delete-stack", "--stack-name", "bad")
	e.waitGone(t, "bad")

	// Unsupported resource types fail the stack with a clear reason.
	uns := write(t, "uns.yaml", `
Resources:
  Q:
    Type: AWS::SQS::Queue
    Properties: {QueueName: uns-q}
  Thing:
    Type: AWS::Frobnicator::Widget
    Properties: {Size: 3}
`)
	e.AWS(t, "cloudformation", "create-stack", "--stack-name", "uns", "--template-body", "file://"+uns)
	st = e.waitFor(t, "uns", "ROLLBACK_COMPLETE")
	ev, ok = hasEvent(e.events(t, "uns"), "Thing", "CREATE_FAILED")
	if !ok || !strings.Contains(str(ev, "ResourceStatusReason"), "AWS::Frobnicator::Widget is not supported") {
		t.Fatalf("unsupported type event: %v", ev)
	}
	// Malformed types are rejected before anything is created.
	if o, err := e.AWSErr(t, "cloudformation", "create-stack", "--stack-name", "mal", "--template-body", `{"Resources":{"X":{"Type":"nonsense"}}}`); err == nil || !strings.Contains(o, "Unrecognized resource types") {
		t.Fatalf("malformed type: %v %s", err, o)
	}
	if o, err := e.AWSErr(t, "cloudformation", "create-stack", "--stack-name", "mal", "--template-body", `{"Resources":{"X":{"Type":"AWS::SQS::Queue","Properties":{"QueueName":{"Ref":"Missing"}}}}}`); err == nil || !strings.Contains(o, "Unresolved resource dependencies [Missing]") {
		t.Fatalf("dangling ref: %v %s", err, o)
	}
	if o, err := e.AWSErr(t, "cloudformation", "create-stack", "--stack-name", "mal", "--template-body", `{not json`); err == nil || !strings.Contains(o, "ValidationError") {
		t.Fatalf("garbage: %v %s", err, o)
	}

	// DisableRollback keeps what was made; OnFailure=DELETE removes the stack.
	e.AWS(t, "cloudformation", "create-stack", "--stack-name", "keep", "--template-body", "file://"+bad, "--disable-rollback")
	e.waitFor(t, "keep", "CREATE_FAILED")
	if o, err := e.AWSErr(t, "sqs", "get-queue-url", "--queue-name", "rollback-q"); err != nil {
		t.Fatalf("with rollback disabled the queue must stay: %v %s", err, o)
	}
	e.AWS(t, "cloudformation", "delete-stack", "--stack-name", "keep")
	e.waitGone(t, "keep")
	e.AWS(t, "cloudformation", "create-stack", "--stack-name", "gone", "--template-body", "file://"+bad, "--on-failure", "DELETE")
	e.waitGone(t, "gone")
	if o, err := e.AWSErr(t, "sqs", "get-queue-url", "--queue-name", "rollback-q"); err == nil {
		t.Fatalf("OnFailure=DELETE left the queue: %s", o)
	}
}

func TestCapabilities(t *testing.T) {
	e := newEnv(t, false)
	tf := write(t, "iam.yaml", `
Resources:
  Role:
    Type: AWS::IAM::Role
    Properties:
      AssumeRolePolicyDocument:
        Version: 2012-10-17
        Statement:
          - Effect: Allow
            Principal: {Service: lambda.amazonaws.com}
            Action: sts:AssumeRole
Outputs:
  RoleName: {Value: !Ref Role}
  RoleArn: {Value: !GetAtt Role.Arn}
`)
	o, err := e.AWSErr(t, "cloudformation", "create-stack", "--stack-name", "iam", "--template-body", "file://"+tf)
	if err == nil || !strings.Contains(o, "InsufficientCapabilitiesException") || !strings.Contains(o, "CAPABILITY_IAM") {
		t.Fatalf("no capabilities: %v %s", err, o)
	}
	sum := e.AWSJSON(t, "cloudformation", "get-template-summary", "--template-body", "file://"+tf)
	if caps := sum["Capabilities"].([]any); len(caps) != 1 || caps[0] != "CAPABILITY_IAM" {
		t.Fatalf("summary capabilities: %v", sum)
	}
	e.AWS(t, "cloudformation", "create-stack", "--stack-name", "iam", "--template-body", "file://"+tf, "--capabilities", "CAPABILITY_IAM")
	st := e.waitFor(t, "iam", "CREATE_COMPLETE")
	out := outputs(st)
	if !strings.HasPrefix(out["RoleName"], "iam-Role-") || out["RoleArn"] != "arn:aws:iam::"+e.Env.AccountID+":role/"+out["RoleName"] {
		t.Fatalf("role outputs: %v", out)
	}
	e.AWS(t, "iam", "get-role", "--role-name", out["RoleName"])
	if caps := st["Capabilities"].([]any); len(caps) != 1 {
		t.Fatalf("stack capabilities: %v", st["Capabilities"])
	}
	// Named IAM resources need CAPABILITY_NAMED_IAM.
	named := write(t, "named.yaml", `
Resources:
  Role:
    Type: AWS::IAM::Role
    Properties:
      RoleName: my-fixed-role
      AssumeRolePolicyDocument: {Version: "2012-10-17", Statement: [{Effect: Allow, Principal: {Service: ec2.amazonaws.com}, Action: "sts:AssumeRole"}]}
`)
	o, err = e.AWSErr(t, "cloudformation", "create-stack", "--stack-name", "named", "--template-body", "file://"+named, "--capabilities", "CAPABILITY_IAM")
	if err == nil || !strings.Contains(o, "CAPABILITY_NAMED_IAM") {
		t.Fatalf("named without NAMED_IAM: %v %s", err, o)
	}
	// The change set path checks capabilities too.
	o, err = e.AWSErr(t, "cloudformation", "create-change-set", "--stack-name", "cs", "--change-set-name", "c", "--change-set-type", "CREATE", "--template-body", "file://"+tf)
	if err == nil || !strings.Contains(o, "InsufficientCapabilitiesException") {
		t.Fatalf("change set capabilities: %v %s", err, o)
	}
	e.AWS(t, "cloudformation", "delete-stack", "--stack-name", "iam")
	e.waitGone(t, "iam")
	if _, err := e.AWSErr(t, "iam", "get-role", "--role-name", out["RoleName"]); err == nil {
		t.Fatal("role survived the stack")
	}
}

func TestStackAuthorization(t *testing.T) {
	e := newEnv(t, false)
	pol := func(name, doc string) {
		var d any
		if err := json.Unmarshal([]byte(doc), &d); err != nil {
			t.Fatal(err)
		}
		e.Native(t, "POST", "/api/v1/iam/policies", map[string]any{"name": name, "document": d})
	}
	pol("cfn-only", `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"cloudformation:*","Resource":"*"},{"Effect":"Allow","Action":["sns:*"],"Resource":"*"}]}`)
	akid, secret := e.User(t, "deployer", "cfn-only")
	tf := write(t, "q.yaml", `
Resources:
  Topic:
    Type: AWS::SNS::Topic
    Properties: {TopicName: allowed-topic}
  Q:
    Type: AWS::SQS::Queue
    Properties: {QueueName: forbidden-queue}
`)
	if o, err := e.AWSAs(t, akid, secret, "", "cloudformation", "create-stack", "--stack-name", "denied", "--template-body", "file://"+tf); err != nil {
		t.Fatalf("create-stack itself is allowed: %v %s", err, o)
	}
	deadline := time.Now().Add(30 * time.Second)
	var st map[string]any
	for time.Now().Before(deadline) {
		out, err := e.AWSAs(t, akid, secret, "", "cloudformation", "describe-stacks", "--stack-name", "denied")
		if err != nil {
			t.Fatalf("describe: %v %s", err, out)
		}
		var m map[string]any
		_ = json.Unmarshal([]byte(out), &m)
		st = m["Stacks"].([]any)[0].(map[string]any)
		if str(st, "StackStatus") == "ROLLBACK_COMPLETE" {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if str(st, "StackStatus") != "ROLLBACK_COMPLETE" {
		t.Fatalf("a user without sqs:CreateQueue must not create a queue through a stack: %v", st)
	}
	out, _ := e.AWSAs(t, akid, secret, "", "cloudformation", "describe-stack-events", "--stack-name", "denied")
	if !strings.Contains(out, "CREATE_FAILED") || !strings.Contains(out, "sqs:CreateQueue") {
		t.Fatalf("events must name the denied action: %s", out)
	}
	if o, err := e.AWSErr(t, "sqs", "get-queue-url", "--queue-name", "forbidden-queue"); err == nil {
		t.Fatalf("forbidden queue exists: %s", o)
	}
	if o, err := e.AWSErr(t, "sns", "get-topic-attributes", "--topic-arn", "arn:aws:sns:us-east-1:"+e.Env.AccountID+":allowed-topic"); err == nil {
		t.Fatalf("the topic was created before the failure and must have been rolled back: %s", o)
	}

	// Actions on the stack itself are authorized too.
	pol("read-only", `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":["cloudformation:Describe*","cloudformation:List*"],"Resource":"*"}]}`)
	rk, rs := e.User(t, "reader", "read-only")
	if o, err := e.AWSAs(t, rk, rs, "", "cloudformation", "describe-stacks", "--stack-name", "denied"); err != nil {
		t.Fatalf("reader describe: %v %s", err, o)
	}
	if o, err := e.AWSAs(t, rk, rs, "", "cloudformation", "delete-stack", "--stack-name", "denied"); err == nil || !strings.Contains(o, "AccessDenied") || !strings.Contains(o, "cloudformation:DeleteStack") {
		t.Fatalf("reader delete: %v %s", err, o)
	}
	if o, err := e.AWSAs(t, rk, rs, "", "cloudformation", "create-stack", "--stack-name", "nope", "--template-body", "file://"+tf); err == nil || !strings.Contains(o, "AccessDenied") {
		t.Fatalf("reader create: %v %s", err, o)
	}
	if o, err := e.AWSAs(t, rk, rs, "", "cloudformation", "create-change-set", "--stack-name", "denied", "--change-set-name", "x", "--template-body", "file://"+tf); err == nil || !strings.Contains(o, "AccessDenied") {
		t.Fatalf("reader change set: %v %s", err, o)
	}

	// A service role supplies the permissions instead: the deployer may pass it.
	e.Native(t, "POST", "/api/v1/iam/roles", map[string]any{"name": "cfn-exec", "assume_role_policy": map[string]any{"Version": "2012-10-17",
		"Statement": []any{map[string]any{"Effect": "Allow", "Principal": map[string]any{"Service": "cloudformation.amazonaws.com"}, "Action": "sts:AssumeRole"}}}})
	e.Native(t, "POST", "/api/v1/iam/policies", map[string]any{"name": "exec-perms", "document": map[string]any{"Version": "2012-10-17",
		"Statement": []any{map[string]any{"Effect": "Allow", "Action": []string{"sqs:*", "sns:*"}, "Resource": "*"}}}})
	e.Native(t, "POST", "/api/v1/iam/roles/cfn-exec/policies", map[string]any{"policy": "exec-perms"})
	roleARN := "arn:aws:iam::" + e.Env.AccountID + ":role/cfn-exec"
	// Without iam:PassRole the deployer cannot hand the role to CloudFormation.
	if o, err := e.AWSAs(t, akid, secret, "", "cloudformation", "create-stack", "--stack-name", "viarole", "--template-body", "file://"+tf, "--role-arn", roleARN); err == nil || !strings.Contains(o, "iam:PassRole") {
		t.Fatalf("PassRole must be required: %v %s", err, o)
	}
	pol("pass-role", `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"iam:PassRole","Resource":"*"}]}`)
	e.Native(t, "POST", "/api/v1/iam/users/deployer/policies", map[string]any{"policy": "pass-role"})
	if o, err := e.AWSAs(t, akid, secret, "", "cloudformation", "create-stack", "--stack-name", "viarole", "--template-body", "file://"+tf, "--role-arn", roleARN); err != nil {
		t.Fatalf("create with role: %v %s", err, o)
	}
	deadline = time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		out, _ := e.AWSAs(t, akid, secret, "", "cloudformation", "describe-stacks", "--stack-name", "viarole")
		var m map[string]any
		_ = json.Unmarshal([]byte(out), &m)
		if list, ok := m["Stacks"].([]any); ok {
			st = list[0].(map[string]any)
			if str(st, "StackStatus") == "CREATE_COMPLETE" {
				break
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	if str(st, "StackStatus") != "CREATE_COMPLETE" || str(st, "RoleARN") != roleARN {
		t.Fatalf("stack with a service role: %v", st)
	}
	e.AWS(t, "sqs", "get-queue-url", "--queue-name", "forbidden-queue")
	// A role that does not trust CloudFormation is refused.
	e.Native(t, "POST", "/api/v1/iam/roles", map[string]any{"name": "wrong-trust", "assume_role_policy": map[string]any{"Version": "2012-10-17",
		"Statement": []any{map[string]any{"Effect": "Allow", "Principal": map[string]any{"Service": "lambda.amazonaws.com"}, "Action": "sts:AssumeRole"}}}})
	if o, err := e.AWSAs(t, akid, secret, "", "cloudformation", "create-stack", "--stack-name", "badrole", "--template-body", "file://"+tf,
		"--role-arn", "arn:aws:iam::"+e.Env.AccountID+":role/wrong-trust"); err == nil || !strings.Contains(o, "cannot be assumed") {
		t.Fatalf("untrusted role: %v %s", err, o)
	}
	e.AWS(t, "cloudformation", "delete-stack", "--stack-name", "viarole")
	e.waitGone(t, "viarole")
	if _, err := e.AWSErr(t, "sqs", "get-queue-url", "--queue-name", "forbidden-queue"); err == nil {
		t.Fatal("queue survived the stack")
	}
}

func TestBoto3Waiters(t *testing.T) {
	e := newEnv(t, false)
	tf := write(t, "w.json", `{"Resources":{"Q":{"Type":"AWS::SQS::Queue","Properties":{"QueueName":"waited"}},"P":{"Type":"AWS::SSM::Parameter","Properties":{"Type":"String","Value":"v","Name":"/waited/p"}}},
"Outputs":{"Q":{"Value":{"Ref":"Q"}}}}`)
	script := fmt.Sprintf(`
cf = boto3.client("cloudformation")
body = open(%q).read()
r = cf.create_stack(StackName="waiter", TemplateBody=body, Tags=[{"Key": "k", "Value": "v"}])
assert r["StackId"].startswith("arn:aws:cloudformation:us-east-1:"), r
cf.get_waiter("stack_create_complete").wait(StackName="waiter", WaiterConfig={"Delay": 1, "MaxAttempts": 60})
st = cf.describe_stacks(StackName="waiter")["Stacks"][0]
assert st["StackStatus"] == "CREATE_COMPLETE", st
assert st["Outputs"][0]["OutputValue"].endswith("/waited"), st
assert cf.list_stack_resources(StackName="waiter")["StackResourceSummaries"][0]["ResourceStatus"] == "CREATE_COMPLETE"
tmpl = cf.get_template(StackName="waiter")["TemplateBody"]
assert "waited" in (tmpl if isinstance(tmpl, str) else json.dumps(tmpl))
cf.update_stack(StackName="waiter", TemplateBody=body.replace('"v"', '"v2"'))
cf.get_waiter("stack_update_complete").wait(StackName="waiter", WaiterConfig={"Delay": 1, "MaxAttempts": 60})
assert boto3.client("ssm").get_parameter(Name="/waited/p")["Parameter"]["Value"] == "v2"
try:
    cf.update_stack(StackName="waiter", TemplateBody=body.replace('"v"', '"v2"'))
    raise SystemExit("expected no-op update to fail")
except botocore.exceptions.ClientError as err:
    assert err.response["Error"]["Code"] == "ValidationError" and "No updates" in err.response["Error"]["Message"], err
pager = cf.get_paginator("describe_stack_events")
n = sum(len(p["StackEvents"]) for p in pager.paginate(StackName="waiter"))
assert n >= 10, n
cf.delete_stack(StackName="waiter")
cf.get_waiter("stack_delete_complete").wait(StackName="waiter", WaiterConfig={"Delay": 1, "MaxAttempts": 60})
try:
    cf.describe_stacks(StackName="waiter")
    raise SystemExit("stack still there")
except botocore.exceptions.ClientError as err:
    assert err.response["Error"]["Code"] == "ValidationError", err
# a failing stack makes the create waiter raise
try:
    cf.create_stack(StackName="fails", TemplateBody='{"Resources":{"X":{"Type":"AWS::Bogus::Thing"}}}')
    cf.get_waiter("stack_create_complete").wait(StackName="fails", WaiterConfig={"Delay": 1, "MaxAttempts": 60})
    raise SystemExit("waiter did not fail")
except botocore.exceptions.WaiterError as err:
    assert "ROLLBACK_COMPLETE" in str(err), err
print("ok")
`, tf)
	if out := e.Python(t, script); !strings.Contains(out, "ok") {
		t.Fatal(out)
	}
}

func TestTemplateFeatures(t *testing.T) {
	e := newEnv(t, false)
	tf := write(t, "f.yaml", `
Parameters:
  Env: {Type: String, Default: dev}
  Extra: {Type: String, Default: ""}
  Names: {Type: CommaDelimitedList, Default: "a,b,c"}
  Size: {Type: Number, Default: 5, MinValue: 1, MaxValue: 10}
  Pin: {Type: String, Default: abc, AllowedPattern: "[a-z]+", MinLength: 2}
Mappings:
  Tier:
    dev: {Retention: "60"}
    prod: {Retention: "600"}
Conditions:
  IsProd: !Equals [!Ref Env, prod]
  HasExtra: !Not [!Equals [!Ref Extra, ""]]
  Either: !Or [!Condition IsProd, !Condition HasExtra]
Resources:
  Q:
    Type: AWS::SQS::Queue
    DeletionPolicy: Retain
    Properties:
      QueueName: !Join ["-", ["feat", !Select [1, !Ref Names], !If [IsProd, big, small]]]
      MessageRetentionPeriod: !FindInMap [Tier, !Ref Env, Retention]
      DelaySeconds: !If [IsProd, 5, !Ref "AWS::NoValue"]
  Prod:
    Type: AWS::SSM::Parameter
    Condition: IsProd
    Properties: {Name: /feat/prod, Type: String, Value: yes}
  Dev:
    Type: AWS::SSM::Parameter
    Condition: Either
    Properties: {Name: /feat/either, Type: String, Value: !Sub ["${A}-${Env}-${!Literal}", {A: x}]}
  Meta:
    Type: AWS::CDK::Metadata
    Properties: {Analytics: v2:deflate64:abc}
Outputs:
  OnlyProd:
    Condition: IsProd
    Value: !Ref Prod
  Delay: {Value: !Select [0, !GetAZs ""]}
  B64: {Value: !Base64 hello}
  Len: {Value: !Ref Size}
`)
	e.AWS(t, "cloudformation", "create-stack", "--stack-name", "feat", "--template-body", "file://"+tf)
	st := e.waitFor(t, "feat", "CREATE_COMPLETE")
	out := outputs(st)
	if _, ok := out["OnlyProd"]; ok || out["Delay"] != "us-east-1a" || out["B64"] != "aGVsbG8=" || out["Len"] != "5" {
		t.Fatalf("outputs: %v", out)
	}
	qa := e.AWSJSON(t, "sqs", "get-queue-attributes", "--queue-url", e.AWSJSON(t, "sqs", "get-queue-url", "--queue-name", "feat-b-small")["QueueUrl"].(string), "--attribute-names", "All")["Attributes"].(map[string]any)
	if qa["MessageRetentionPeriod"] != "60" || qa["DelaySeconds"] != "0" {
		t.Fatalf("queue: %v", qa)
	}
	if _, err := e.AWSErr(t, "ssm", "get-parameter", "--name", "/feat/prod"); err == nil {
		t.Fatal("conditional resource was created")
	}
	if _, err := e.AWSErr(t, "ssm", "get-parameter", "--name", "/feat/either"); err == nil {
		t.Fatal("Either is false and must not create the parameter")
	}
	res := e.AWSJSON(t, "cloudformation", "list-stack-resources", "--stack-name", "feat")["StackResourceSummaries"].([]any)
	if len(res) != 2 {
		t.Fatalf("resources: %v", res)
	}
	// Prod turns the conditions on; Retain keeps the queue after the stack goes.
	e.AWS(t, "cloudformation", "update-stack", "--stack-name", "feat", "--use-previous-template", "--parameters", "ParameterKey=Env,ParameterValue=prod")
	st = e.waitFor(t, "feat", "UPDATE_COMPLETE")
	if outputs(st)["OnlyProd"] != "/feat/prod" {
		t.Fatalf("outputs after prod: %v", outputs(st))
	}
	if v := e.AWSJSON(t, "ssm", "get-parameter", "--name", "/feat/either")["Parameter"].(map[string]any)["Value"]; v != "x-prod-${Literal}" {
		t.Fatalf("Fn::Sub with a variable map: %v", v)
	}
	e.AWS(t, "sqs", "get-queue-url", "--queue-name", "feat-b-big")
	e.AWS(t, "cloudformation", "delete-stack", "--stack-name", "feat")
	e.waitGone(t, "feat")
	e.AWS(t, "sqs", "get-queue-url", "--queue-name", "feat-b-big") // DeletionPolicy: Retain
	if _, err := e.AWSErr(t, "ssm", "get-parameter", "--name", "/feat/prod"); err == nil {
		t.Fatal("parameter survived")
	}
	// Parameter constraints.
	for _, p := range []string{"Size=11", "Size=x", "Pin=A1", "Pin=a", "Env=prod,Bogus=1"} {
		args := []string{"cloudformation", "create-stack", "--stack-name", "constraint", "--template-body", "file://" + tf, "--parameters"}
		for _, kv := range strings.Split(p, ",") {
			k, v, _ := strings.Cut(kv, "=")
			args = append(args, "ParameterKey="+k+",ParameterValue="+v)
		}
		if o, err := e.AWSErr(t, args...); err == nil || !strings.Contains(o, "ValidationError") {
			t.Fatalf("%s accepted: %v %s", p, err, o)
		}
	}
	if o, err := e.AWSErr(t, "cloudformation", "create-stack", "--stack-name", "req", "--template-body", `{"Parameters":{"Must":{"Type":"String"}},"Resources":{"Q":{"Type":"AWS::SQS::Queue"}}}`); err == nil || !strings.Contains(o, "Parameters: [Must] must have values") {
		t.Fatalf("missing parameter: %v %s", err, o)
	}
}

func TestExportsAndImports(t *testing.T) {
	e := newEnv(t, false)
	base := write(t, "base.yaml", `
Resources:
  Topic:
    Type: AWS::SNS::Topic
    Properties: {TopicName: shared-topic}
Outputs:
  TopicArn:
    Value: !Ref Topic
    Export: {Name: shared-topic-arn}
`)
	user := write(t, "user.yaml", `
Resources:
  Param:
    Type: AWS::SSM::Parameter
    Properties:
      Name: /imports/topic
      Type: String
      Value: !ImportValue shared-topic-arn
`)
	e.AWS(t, "cloudformation", "create-stack", "--stack-name", "base", "--template-body", "file://"+base)
	e.waitFor(t, "base", "CREATE_COMPLETE")
	// Export names are unique.
	e.AWS(t, "cloudformation", "create-stack", "--stack-name", "base2", "--template-body", "file://"+base)
	e.waitFor(t, "base2", "ROLLBACK_COMPLETE")
	if _, ok := hasEvent(e.events(t, "base2"), "Topic", "CREATE_FAILED"); !ok {
		t.Fatalf("duplicate topic name did not fail: %v", e.events(t, "base2"))
	}
	e.AWS(t, "cloudformation", "create-stack", "--stack-name", "user", "--template-body", "file://"+user)
	e.waitFor(t, "user", "CREATE_COMPLETE")
	if v := e.AWSJSON(t, "ssm", "get-parameter", "--name", "/imports/topic")["Parameter"].(map[string]any)["Value"]; v != "arn:aws:sns:us-east-1:"+e.Env.AccountID+":shared-topic" {
		t.Fatalf("import: %v", v)
	}
	imp := e.AWSJSON(t, "cloudformation", "list-imports", "--export-name", "shared-topic-arn")["Imports"].([]any)
	if len(imp) != 1 || imp[0] != "user" {
		t.Fatalf("imports: %v", imp)
	}
	if o, err := e.AWSErr(t, "cloudformation", "delete-stack", "--stack-name", "base"); err == nil || !strings.Contains(o, "in use") {
		t.Fatalf("deleting an imported export: %v %s", err, o)
	}
	e.AWS(t, "cloudformation", "delete-stack", "--stack-name", "user")
	e.waitGone(t, "user")
	e.AWS(t, "cloudformation", "delete-stack", "--stack-name", "base")
	e.waitGone(t, "base")
	// Importing a missing export fails the stack.
	e.AWS(t, "cloudformation", "create-stack", "--stack-name", "user", "--template-body", "file://"+user)
	e.waitFor(t, "user", "ROLLBACK_COMPLETE")
	if ev, ok := hasEvent(e.events(t, "user"), "Param", "CREATE_FAILED"); !ok || !strings.Contains(str(ev, "ResourceStatusReason"), "No export named shared-topic-arn") {
		t.Fatalf("missing export: %v", ev)
	}
}

func TestTemplateURLAndTerminationProtection(t *testing.T) {
	e := newEnv(t, true)
	e.AWS(t, "s3api", "create-bucket", "--bucket", "templates")
	tf := write(t, "url.yaml", "Resources:\n  P:\n    Type: AWS::SSM::Parameter\n    Properties: {Name: /url/p, Type: String, Value: from-s3}\n")
	e.AWS(t, "s3api", "put-object", "--bucket", "templates", "--key", "stacks/url.yaml", "--body", tf)
	for _, u := range []string{e.URL + "/templates/stacks/url.yaml", "https://templates.s3.amazonaws.com/stacks/url.yaml", "https://s3.us-east-1.amazonaws.com/templates/stacks/url.yaml"} {
		e.AWS(t, "cloudformation", "create-stack", "--stack-name", "byurl", "--template-url", u, "--enable-termination-protection")
		e.waitFor(t, "byurl", "CREATE_COMPLETE")
		if v := e.AWSJSON(t, "ssm", "get-parameter", "--name", "/url/p")["Parameter"].(map[string]any)["Value"]; v != "from-s3" {
			t.Fatalf("%s: %v", u, v)
		}
		if o, err := e.AWSErr(t, "cloudformation", "delete-stack", "--stack-name", "byurl"); err == nil || !strings.Contains(o, "TerminationProtection") {
			t.Fatalf("termination protection: %v %s", err, o)
		}
		e.AWS(t, "cloudformation", "update-termination-protection", "--stack-name", "byurl", "--no-enable-termination-protection")
		e.AWS(t, "cloudformation", "delete-stack", "--stack-name", "byurl")
		e.waitGone(t, "byurl")
	}
	if o, err := e.AWSErr(t, "cloudformation", "create-stack", "--stack-name", "nourl", "--template-url", e.URL+"/templates/missing.yaml"); err == nil || !strings.Contains(o, "TemplateURL must reference a valid S3 object") {
		t.Fatalf("missing template object: %v %s", err, o)
	}
	if o, err := e.AWSErr(t, "cloudformation", "create-stack", "--stack-name", "nourl", "--template-url", "https://example.com/t.yaml"); err == nil || !strings.Contains(o, "TemplateURL must reference a valid S3 object") {
		t.Fatalf("foreign template url: %v %s", err, o)
	}
}
