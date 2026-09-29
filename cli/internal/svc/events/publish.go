package events

import (
	"context"
	"encoding/json"
)

// PublishEvent routes an event emitted by another service (for example a
// Lambda asynchronous invocation record) to every matching rule.
func (s *Service) PublishEvent(ctx context.Context, source, detailType string, resources []string, detail json.RawMessage) string {
	id, _ := s.Publish(ctx, entry{Source: source, DetailType: detailType, Detail: detail, Resources: resources})
	return id
}
