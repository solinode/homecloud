package sns

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestTopicPolicyEnforcement(t *testing.T) {
	h, _, _ := harness(t)
	acct := h.Env.AccountID
	arn := h.AWSJSON(t, "sns", "create-topic", "--name", "alerts")["TopicArn"].(string)
	bk, bs := h.User(t, "bob")
	ck, cs := h.User(t, "carol", "AmazonSNSFullAccess")
	dk, ds := h.User(t, "dave")

	setPolicy := func(policy string) {
		t.Helper()
		h.AWS(t, "sns", "set-topic-attributes", "--topic-arn", arn, "--attribute-name", "Policy", "--attribute-value", policy)
	}
	publish := []string{"sns", "publish", "--topic-arn", arn, "--message", "hi"}
	denied := func(ak, sk string, args ...string) {
		t.Helper()
		out, err := h.AWSAs(t, ak, sk, "", args...)
		if err == nil || !strings.Contains(out, "AccessDenied") && !strings.Contains(out, "AuthorizationError") {
			t.Fatalf("aws %s: want a denial, got %v %s", strings.Join(args, " "), err, out)
		}
	}
	ok := func(ak, sk string, args ...string) {
		t.Helper()
		if out, err := h.AWSAs(t, ak, sk, "", args...); err != nil {
			t.Fatalf("aws %s: %v %s", strings.Join(args, " "), err, out)
		}
	}

	denied(bk, bs, publish...)
	setPolicy(fmt.Sprintf(`{"Version":"2012-10-17","Statement":[
		{"Effect":"Allow","Principal":{"AWS":"arn:aws:iam::%s:user/bob"},"Action":"sns:Publish","Resource":"%s"}]}`, acct, arn))
	ok(bk, bs, publish...)
	denied(dk, ds, publish...)
	denied(bk, bs, "sns", "delete-topic", "--topic-arn", arn)

	// Native API.
	if st, b := h.NativeAs(t, bk, bs, "POST", "/api/v1/sns/topics/alerts/publish", map[string]any{"message": "native"}); st != 200 {
		t.Fatalf("native publish by bob: %d %s", st, b)
	}
	if st, _ := h.NativeAs(t, dk, ds, "POST", "/api/v1/sns/topics/alerts/publish", map[string]any{"message": "native"}); st != 403 {
		t.Fatalf("native publish by dave: %d", st)
	}

	// Explicit Deny beats carol's full access; a condition scopes it.
	setPolicy(fmt.Sprintf(`{"Version":"2012-10-17","Statement":[
		{"Effect":"Deny","Principal":"*","Action":"sns:Publish","Resource":"%s","Condition":{"Bool":{"aws:SecureTransport":"false"}}}]}`, arn))
	denied(ck, cs, publish...)
	ok(ck, cs, "sns", "get-topic-attributes", "--topic-arn", arn)
	ok(h.AccessKeyID, h.SecretKey, publish...)

	setPolicy(fmt.Sprintf(`{"Version":"2012-10-17","Statement":[
		{"Effect":"Deny","Principal":{"AWS":"arn:aws:iam::%s:user/carol"},"Action":"sns:*","Resource":"%s"}]}`, acct, arn))
	denied(ck, cs, publish...)
	denied(ck, cs, "sns", "get-topic-attributes", "--topic-arn", arn)

	// Allow only from a network range.
	setPolicy(fmt.Sprintf(`{"Version":"2012-10-17","Statement":[
		{"Effect":"Allow","Principal":"*","Action":"sns:Publish","Resource":"%s","Condition":{"NotIpAddress":{"aws:SourceIp":"10.0.0.0/8"}}}]}`, arn))
	ok(dk, ds, publish...)
	setPolicy(fmt.Sprintf(`{"Version":"2012-10-17","Statement":[
		{"Effect":"Allow","Principal":"*","Action":"sns:Publish","Resource":"%s","Condition":{"IpAddress":{"aws:SourceIp":"10.0.0.0/8"}}}]}`, arn))
	denied(dk, ds, publish...)
}

// TestDeliveryHonorsQueuePolicy checks that a queue's policy decides which
// topics can deliver to it, with aws:SourceArn.
func TestDeliveryHonorsQueuePolicy(t *testing.T) {
	h, _, _ := harness(t)
	acct := h.Env.AccountID
	topicA := h.AWSJSON(t, "sns", "create-topic", "--name", "a")["TopicArn"].(string)
	topicB := h.AWSJSON(t, "sns", "create-topic", "--name", "b")["TopicArn"].(string)

	newQueue := func(name, policy string) (url, arn string) {
		t.Helper()
		url = h.AWSJSON(t, "sqs", "create-queue", "--queue-name", name)["QueueUrl"].(string)
		arn = "arn:aws:sqs:us-east-1:" + acct + ":" + name
		if policy != "" {
			attrs, _ := json.Marshal(map[string]string{"Policy": fmt.Sprintf(policy, arn, topicA)})
			h.AWS(t, "sqs", "set-queue-attributes", "--queue-url", url, "--attributes", string(attrs))
		}
		for _, topic := range []string{topicA, topicB} {
			h.AWS(t, "sns", "subscribe", "--topic-arn", topic, "--protocol", "sqs", "--notification-endpoint", arn)
		}
		return url, arn
	}
	bodies := func(url string) string {
		t.Helper()
		out := h.AWSJSON(t, "sqs", "receive-message", "--queue-url", url, "--max-number-of-messages", "10")
		var all []string
		for _, m := range out["Messages"].([]any) {
			var env struct{ Message, TopicArn string }
			_ = json.Unmarshal([]byte(m.(map[string]any)["Body"].(string)), &env)
			all = append(all, env.Message)
		}
		return strings.Join(all, ",")
	}
	publishBoth := func() {
		h.AWS(t, "sns", "publish", "--topic-arn", topicA, "--message", "from-a")
		h.AWS(t, "sns", "publish", "--topic-arn", topicB, "--message", "from-b")
	}

	// No queue policy: both topics deliver (they were authorized when subscribing).
	openURL, _ := newQueue("open", "")
	// The policy names topic A only.
	guardedURL, _ := newQueue("guarded", `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"Service":"sns.amazonaws.com"},`+
		`"Action":"sqs:SendMessage","Resource":"%s","Condition":{"ArnEquals":{"aws:SourceArn":"%s"}}}]}`)
	// An explicit Deny for the service beats an Allow for everyone.
	deniedURL, _ := newQueue("blocked", `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":"*","Action":"sqs:SendMessage","Resource":"%s"},`+
		`{"Effect":"Deny","Principal":{"Service":"sns.amazonaws.com"},"Action":"sqs:SendMessage","Resource":"%[1]s","Condition":{"ArnEquals":{"aws:SourceArn":"%[2]s"}}}]}`)

	publishBoth()
	if got := bodies(openURL); !strings.Contains(got, "from-a") || !strings.Contains(got, "from-b") {
		t.Fatalf("queue without a policy: %q", got)
	}
	if got := bodies(guardedURL); got != "from-a" {
		t.Fatalf("guarded queue received %q, want only from-a", got)
	}
	if got := bodies(deniedURL); got != "from-b" {
		t.Fatalf("queue denying topic a received %q, want only from-b", got)
	}
}
