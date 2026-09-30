package sns

import (
	"github.com/homecloudhq/homecloud/cli/internal/core"
	"github.com/homecloudhq/homecloud/cli/internal/httpx"
	"github.com/homecloudhq/homecloud/cli/internal/store"
)

// A topic's Policy attribute is evaluated together with the caller's identity
// policies on every topic operation (publish, subscribe, attribute changes),
// over the native and the AWS API: see httpx.Principal.Permits.

// policyProvider supplies a topic's policy to authorization.
func (s *Service) policyProvider(arn string) (httpx.Access, bool) {
	t, err := store.Get[Topic](s.env.Store, cTopics, nameFromARN(arn))
	if err != nil || t.attr("Policy") == "" {
		return httpx.Access{}, false
	}
	return httpx.Access{Policy: t.attr("Policy")}, true
}

// AllowDelivery reports whether a service (EventBridge, CloudWatch alarms, S3)
// may publish to the topic: its policy must allow the service principal and not
// deny it. A topic without a policy accepts deliveries, which were authorized
// when the target was set up.
func (s *Service) AllowDelivery(topic string, src core.Source) bool {
	t, err := store.Get[Topic](s.env.Store, cTopics, nameFromARN(topic))
	if err != nil || t.attr("Policy") == "" {
		return true
	}
	return httpx.PermitsService(src.Service, "sns:Publish", t.ARN, src.Keys())
}
