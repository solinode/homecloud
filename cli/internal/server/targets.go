package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/homecloudhq/homecloud/cli/internal/core"
	"github.com/homecloudhq/homecloud/cli/internal/svc/events"
	"github.com/homecloudhq/homecloud/cli/internal/svc/lambda"
	"github.com/homecloudhq/homecloud/cli/internal/svc/sfn"
	"github.com/homecloudhq/homecloud/cli/internal/svc/sns"
	"github.com/homecloudhq/homecloud/cli/internal/svc/sqs"
)

// targets delivers payloads to resources named by ARN, for EventBridge rules.
type targets struct {
	lambda *lambda.Service
	sqs    *sqs.Service
	sns    *sns.Service
	sfn    *sfn.Service
	// account, when set, restricts deliveries to this account's own ARNs.
	account string
}

// Invoke, SendMessage and Publish implement sfn.Tasks.
func (t *targets) Invoke(ctx context.Context, fn string, payload []byte) (json.RawMessage, string, string, error) {
	r, err := t.lambda.Invoke(ctx, fn, payload)
	if err != nil {
		return nil, "", "", err
	}
	if r.FunctionError != "" {
		var e struct {
			Type string `json:"errorType"`
			Msg  string `json:"errorMessage"`
		}
		_ = json.Unmarshal(r.Payload, &e)
		if e.Type == "" {
			e.Type = "Lambda.Unknown"
		}
		return nil, e.Type, e.Msg, nil
	}
	return r.Payload, "", "", nil
}

func (t *targets) SendMessage(queue, body string) (map[string]any, error) {
	in := sqs.SendInput{Body: body}
	if strings.HasSuffix(queue, ".fifo") {
		in.GroupID, in.DedupID = "states", core.RandHex(32)
	}
	r, err := t.sqs.Send(sqs.NameFromARN(queue), in)
	if err != nil {
		return nil, err
	}
	return map[string]any{"MessageId": r.MessageID, "MD5OfMessageBody": r.MD5OfBody}, nil
}

func (t *targets) Publish(topic, subject, message string) (map[string]any, error) {
	id, err := t.sns.Publish(topic, subject, message, nil)
	if err != nil {
		return nil, err
	}
	return map[string]any{"MessageId": id}, nil
}

// parse splits "arn:aws:<service>:<region>:<account>:<resource>".
func (t *targets) parse(arn string) (service, resource string, ok bool) {
	if t.account != "" && !core.IsLocalARN(arn, t.account) {
		return "", "", false // another account or region: not a resource this server can deliver to
	}
	parts := strings.SplitN(core.CanonicalARN(arn), ":", 6)
	if len(parts) != 6 || parts[0] != "arn" || parts[1] != core.Partition {
		return "", "", false
	}
	return parts[2], parts[5], true
}

func (t *targets) exists(arn string) bool {
	svc, res, ok := t.parse(arn)
	if !ok {
		return false
	}
	switch svc {
	case "lambda":
		return t.lambda.Exists(strings.TrimPrefix(res, "function:"))
	case "sqs":
		_, ok := t.sqs.QueueARN(res)
		return ok
	case "sns":
		return t.sns.TopicExists(res)
	case "states":
		return t.sfn.Exists(strings.TrimPrefix(res, "stateMachine:"))
	}
	return false
}

// denied is the error for a delivery the target's resource policy refuses; it is
// a 403, so EventBridge does not retry it.
func denied(src core.Source, target string) error {
	return core.Errf(http.StatusForbidden, "AccessDenied", "the resource policy of %s does not allow %s to deliver from %s", target, src.Service, src.ARN)
}

func (t *targets) deliver(ctx context.Context, arn string, payload []byte) error {
	svc, res, ok := t.parse(arn)
	if !ok {
		return fmt.Errorf("malformed ARN %q", arn)
	}
	switch svc {
	case "lambda":
		r, err := t.lambda.Invoke(ctx, strings.TrimPrefix(res, "function:"), payload)
		if err != nil {
			return err
		}
		if r.FunctionError != "" {
			return fmt.Errorf("%w: function error: %s", events.ErrNoRetry, r.Payload)
		}
		return nil
	case "sqs":
		in := sqs.SendInput{Body: string(payload)}
		if strings.HasSuffix(res, ".fifo") {
			in.GroupID, in.DedupID = "events", core.RandHex(32)
		}
		if src, ok := core.SourceFrom(ctx); ok && !t.sqs.AllowDelivery(res, src) {
			return denied(src, arn)
		}
		_, err := t.sqs.Send(res, in)
		return err
	case "sns":
		if src, ok := core.SourceFrom(ctx); ok && !t.sns.AllowDelivery(res, src) {
			return denied(src, arn)
		}
		_, err := t.sns.Publish(res, "", string(payload), nil)
		return err
	case "states":
		var input any
		_ = json.Unmarshal(payload, &input)
		_, err := t.sfn.Start(strings.TrimPrefix(res, "stateMachine:"), "", input)
		return err
	}
	return fmt.Errorf("unsupported target service %q", svc)
}
