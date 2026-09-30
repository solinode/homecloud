package core

import (
	"context"
	"strings"
)

// Source identifies the AWS service and resource delivering a payload to
// another service's resource (an SNS topic to a queue, an EventBridge rule to
// a topic). The target's resource policy is evaluated for it as a service
// principal, with aws:SourceArn and aws:SourceAccount set.
type Source struct {
	Service string // "sns.amazonaws.com"
	ARN     string // the delivering resource
	Account string
}

// Keys are the condition keys a target's policy sees for this source.
func (s Source) Keys() map[string][]string {
	k := map[string][]string{}
	if s.ARN != "" {
		k["aws:sourcearn"] = []string{s.ARN}
		if s.Account == "" {
			if parts := strings.Split(s.ARN, ":"); len(parts) > 4 {
				s.Account = parts[4]
			}
		}
	}
	if s.Account != "" {
		k["aws:sourceaccount"] = []string{s.Account}
	}
	return k
}

type sourceKey struct{}

// WithSource marks a delivery as made by src.
func WithSource(ctx context.Context, src Source) context.Context {
	return context.WithValue(ctx, sourceKey{}, src)
}

// SourceFrom returns the delivering source of ctx, if any.
func SourceFrom(ctx context.Context) (Source, bool) {
	s, ok := ctx.Value(sourceKey{}).(Source)
	return s, ok
}
