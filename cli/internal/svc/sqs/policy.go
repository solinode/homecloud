package sqs

import (
	"github.com/homecloudhq/homecloud/cli/internal/core"
	"github.com/homecloudhq/homecloud/cli/internal/httpx"
	"github.com/homecloudhq/homecloud/cli/internal/store"
)

// A queue's access policy (the Policy attribute) is evaluated together with
// the caller's identity policies on every queue operation, over the native and
// the AWS API: see httpx.Principal.Permits.

// policyProvider supplies a queue's policy to authorization.
func (s *Service) policyProvider(arn string) (httpx.Access, bool) {
	q, err := store.Get[Queue](s.env.Store, cQueues, NameFromARN(arn))
	if err != nil || q.Policy == "" {
		return httpx.Access{}, false
	}
	return httpx.Access{Policy: q.Policy}, true
}

// AllowDelivery reports whether a service may send to the queue: its policy
// must allow the service principal (with the source's aws:SourceArn and
// aws:SourceAccount) and not deny it. A queue without a policy accepts
// deliveries, which were authorized when the subscription or target was set up.
func (s *Service) AllowDelivery(queue string, src core.Source) bool {
	q, err := store.Get[Queue](s.env.Store, cQueues, NameFromARN(queue))
	if err != nil || q.Policy == "" {
		return true
	}
	return httpx.PermitsService(src.Service, "sqs:SendMessage", q.ARN, src.Keys())
}
