package lambda_test

import (
	"strings"
	"testing"
	"time"

	"github.com/homecloudhq/homecloud/cli/internal/awsapi/awstest"
	"github.com/homecloudhq/homecloud/cli/internal/svc/cloudwatch"
	"github.com/homecloudhq/homecloud/cli/internal/svc/lambda"
	"github.com/homecloudhq/homecloud/cli/internal/svc/vpc"
)

type fakeQueues struct{}

func (fakeQueues) Receive(string, int, time.Duration) ([]lambda.QueueMessage, error) { return nil, nil }
func (fakeQueues) Delete(string, string) error                                       { return nil }
func (fakeQueues) QueueARN(q string) (string, bool) {
	return "arn:aws:sqs:us-east-1:000000000000:" + q, true
}

// TestEventSourceMappingTags covers what Terraform's aws_lambda_event_source_mapping does: create with
// tags, then read them back with ListTags on the mapping's ARN.
func TestEventSourceMappingTags(t *testing.T) {
	h := awstest.New(t)
	cw, err := cloudwatch.New(h.Env)
	if err != nil {
		t.Fatal(err)
	}
	l := lambda.New(h.Env, cw, vpc.New(h.Env))
	l.Roles = roles{h.IAM}
	l.Queues = fakeQueues{}
	l.RegisterAWS()
	l.Routes(h.Router)
	acct := h.Env.AccountID
	h.Native(t, "POST", "/api/v1/iam/roles", map[string]any{"name": "fn-role", "assume_role_policy": map[string]any{"Version": "2012-10-17",
		"Statement": []any{map[string]any{"Effect": "Allow", "Principal": map[string]any{"Service": "lambda.amazonaws.com"}, "Action": "sts:AssumeRole"}}}})
	code := zipFile(t, "app.py", "def handler(e, c):\n    return 1\n")
	h.AWS(t, "lambda", "create-function", "--function-name", "f1", "--runtime", "python3.12", "--handler", "app.handler",
		"--role", "arn:aws:iam::"+acct+":role/fn-role", "--zip-file", "fileb://"+code)

	m := h.AWSJSON(t, "lambda", "create-event-source-mapping", "--function-name", "f1",
		"--event-source-arn", "arn:aws:sqs:us-east-1:"+acct+":jobs", "--tags", "team=x")
	arn := m["EventSourceMappingArn"].(string)
	if tags := h.AWSJSON(t, "lambda", "list-tags", "--resource", arn)["Tags"].(map[string]any); tags["team"] != "x" {
		t.Fatalf("tags at create: %v", tags)
	}
	h.AWS(t, "lambda", "tag-resource", "--resource", arn, "--tags", "env=dev")
	h.AWS(t, "lambda", "untag-resource", "--resource", arn, "--tag-keys", "team")
	if tags := h.AWSJSON(t, "lambda", "list-tags", "--resource", arn)["Tags"].(map[string]any); len(tags) != 1 || tags["env"] != "dev" {
		t.Fatalf("tags after edits: %v", tags)
	}
	h.AWS(t, "lambda", "delete-event-source-mapping", "--uuid", m["UUID"].(string))
	if out, err := h.AWSErr(t, "lambda", "list-tags", "--resource", arn); err == nil || !strings.Contains(out, "ResourceNotFoundException") {
		t.Fatalf("tags of a deleted mapping: %s", out)
	}
}
