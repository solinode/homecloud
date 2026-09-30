package server

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/homecloudhq/homecloud/cli/internal/awsapi/awstest"
	"github.com/homecloudhq/homecloud/cli/internal/svc/events"
	"github.com/homecloudhq/homecloud/cli/internal/svc/sns"
	"github.com/homecloudhq/homecloud/cli/internal/svc/sqs"
)

// TestEventBridgeHonorsTargetPolicies checks that a rule delivers to an SQS
// queue or SNS topic only if the target's resource policy allows
// events.amazonaws.com for that rule.
func TestEventBridgeHonorsTargetPolicies(t *testing.T) {
	h := awstest.New(t)
	q := sqs.New(h.Env)
	q.Routes(h.Router)
	q.RegisterAWS()
	sn := sns.New(h.Env, q, nil)
	sn.Routes(h.Router)
	sn.RegisterAWS()
	ev := events.New(h.Env)
	tg := &targets{sqs: q, sns: sn}
	ev.Deliver, ev.Exists = tg.deliver, tg.exists
	ev.Routes(h.Router)
	ev.RegisterAWS()

	acct := h.Env.AccountID
	ruleARN := "arn:aws:events:us-east-1:" + acct + ":rule/orders"
	queue := func(name, policy string) string {
		t.Helper()
		url := h.AWSJSON(t, "sqs", "create-queue", "--queue-name", name)["QueueUrl"].(string)
		if policy != "" {
			policy = strings.NewReplacer("QUEUE", "arn:aws:sqs:us-east-1:"+acct+":"+name, "RULE", ruleARN).Replace(policy)
			attrs, _ := json.Marshal(map[string]string{"Policy": policy})
			h.AWS(t, "sqs", "set-queue-attributes", "--queue-url", url, "--attributes", string(attrs))
		}
		return url
	}
	open := queue("open", "")
	allowed := queue("allowed", `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"Service":"events.amazonaws.com"},`+
		`"Action":"sqs:SendMessage","Resource":"QUEUE","Condition":{"ArnEquals":{"aws:SourceArn":"RULE"}}}]}`)
	wrongRule := queue("wrongrule", `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"Service":"events.amazonaws.com"},`+
		`"Action":"sqs:SendMessage","Resource":"QUEUE","Condition":{"ArnEquals":{"aws:SourceArn":"arn:aws:events:us-east-1:`+acct+`:rule/other"}}}]}`)
	blocked := queue("blocked", `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":"*","Action":"sqs:SendMessage","Resource":"QUEUE"},`+
		`{"Effect":"Deny","Principal":{"Service":"events.amazonaws.com"},"Action":"sqs:SendMessage","Resource":"QUEUE"}]}`)

	topicARN := h.AWSJSON(t, "sns", "create-topic", "--name", "guarded")["TopicArn"].(string)
	h.AWS(t, "sns", "set-topic-attributes", "--topic-arn", topicARN, "--attribute-name", "Policy", "--attribute-value",
		fmt.Sprintf(`{"Version":"2012-10-17","Statement":[{"Effect":"Deny","Principal":{"Service":"events.amazonaws.com"},"Action":"sns:Publish","Resource":"%s"}]}`, topicARN))
	// A subscribed queue shows whether the topic was published to.
	sinkURL := queue("sink", "")
	h.AWS(t, "sns", "subscribe", "--topic-arn", topicARN, "--protocol", "sqs", "--notification-endpoint", "arn:aws:sqs:us-east-1:"+acct+":sink")

	h.AWS(t, "events", "put-rule", "--name", "orders", "--event-pattern", `{"source":["shop"]}`)
	var ts []map[string]any
	for _, n := range []string{"open", "allowed", "wrongrule", "blocked"} {
		ts = append(ts, map[string]any{"Id": n, "Arn": "arn:aws:sqs:us-east-1:" + acct + ":" + n})
	}
	ts = append(ts, map[string]any{"Id": "topic", "Arn": topicARN})
	tj, _ := json.Marshal(ts)
	h.AWS(t, "events", "put-targets", "--rule", "orders", "--targets", string(tj))
	h.AWS(t, "events", "put-events", "--entries", `[{"Source":"shop","DetailType":"x","Detail":"{}"}]`)

	count := func(url string) int {
		t.Helper()
		deadline := time.Now().Add(3 * time.Second)
		n := 0
		for time.Now().Before(deadline) {
			out := h.AWSJSON(t, "sqs", "receive-message", "--queue-url", url, "--max-number-of-messages", "10", "--visibility-timeout", "0")
			if ms, ok := out["Messages"].([]any); ok {
				n = len(ms)
			}
			if n > 0 {
				break
			}
			time.Sleep(100 * time.Millisecond)
		}
		return n
	}
	for url, want := range map[string]int{open: 1, allowed: 1} {
		if got := count(url); got != want {
			t.Errorf("%s: %d messages, want %d", url, got, want)
		}
	}
	// Deliveries the policies refuse never arrive; give the accepted ones time to land first.
	for _, url := range []string{wrongRule, blocked, sinkURL} {
		if got := count(url); got != 0 {
			t.Errorf("%s: %d messages, want none", url, got)
		}
	}
}
