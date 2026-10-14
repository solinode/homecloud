package awsapi

import (
	"strings"

	"github.com/homecloudhq/homecloud/cli/internal/httpx"
)

// observe records the call on the request's metrics observation. The service
// and operation names come from the registry, never from the request: an
// operation HomeCloud does not implement is reported as "unknown".
func (q *Req) observe() {
	o := httpx.Observed(q.R)
	if o == nil || o.API != "" {
		return
	}
	o.API, o.Service, o.Operation = "aws", "unknown", "unknown"
	if q.Svc != nil {
		o.Service = q.Svc.Name
		switch {
		case q.Protocol != REST && q.Svc.Ops[q.Op] != nil:
			o.Operation = q.Op
		case q.action != "":
			// REST services name their operation when they authorize it (s3:GetObject).
			if _, op, ok := strings.Cut(q.action, ":"); ok {
				o.Operation = op
			}
		}
	}
	if q.info != nil {
		o.ErrorCode = q.info.ErrorCode
	}
	if q.authFailed {
		o.AuthFailure = o.ErrorCode
	}
}
