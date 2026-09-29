package server

import (
	"context"
	"fmt"
	"strings"

	"github.com/homecloudhq/homecloud/cli/internal/core"
	"github.com/homecloudhq/homecloud/cli/internal/svc/lambda"
	"github.com/homecloudhq/homecloud/cli/internal/svc/sns"
	"github.com/homecloudhq/homecloud/cli/internal/svc/sqs"
)

// targets delivers payloads to resources named by ARN, for EventBridge rules.
type targets struct {
	lambda *lambda.Service
	sqs    *sqs.Service
	sns    *sns.Service
}

// parse splits "arn:hc:<service>:<region>:<account>:<resource>".
func parse(arn string) (service, resource string, ok bool) {
	parts := strings.SplitN(arn, ":", 6)
	if len(parts) != 6 || parts[0] != "arn" || parts[1] != "hc" {
		return "", "", false
	}
	return parts[2], parts[5], true
}

func (t *targets) exists(arn string) bool {
	svc, res, ok := parse(arn)
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
		return strings.HasPrefix(arn, "arn:hc:sns:")
	}
	return false
}

func (t *targets) deliver(ctx context.Context, arn string, payload []byte) error {
	svc, res, ok := parse(arn)
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
			return fmt.Errorf("function error: %s", r.Payload)
		}
		return nil
	case "sqs":
		in := sqs.SendInput{Body: string(payload)}
		if strings.HasSuffix(res, ".fifo") {
			in.GroupID, in.DedupID = "events", core.RandHex(32)
		}
		_, err := t.sqs.Send(res, in)
		return err
	case "sns":
		_, err := t.sns.Publish(res, "", string(payload), nil)
		return err
	}
	return fmt.Errorf("unsupported target service %q", svc)
}
