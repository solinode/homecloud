package httpx

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/homecloudhq/homecloud/cli/internal/core"
	"github.com/homecloudhq/homecloud/cli/internal/store"
)

// Observation describes a request for the server's metrics. The server's
// outermost handler attaches one to the request context; the native router and
// the AWS protocol handler fill in what they learn while serving it. Every
// field must come from a bounded set (route patterns, service and operation
// names the server implements, error codes it defines), never from IDs or
// other caller-chosen text.
type Observation struct {
	API       string // "native" or "aws"; empty: neither claimed the request
	Service   string
	Operation string
	ErrorCode string
	// AuthFailure is the error code of a failed authentication.
	AuthFailure string
}

type observationKey struct{}

// WithObservation attaches o to ctx.
func WithObservation(ctx context.Context, o *Observation) context.Context {
	return context.WithValue(ctx, observationKey{}, o)
}

// Observed returns the request's observation, or nil for requests that do not
// come through the server's HTTP listeners (in-process calls, tests).
func Observed(r *http.Request) *Observation {
	o, _ := r.Context().Value(observationKey{}).(*Observation)
	return o
}

// ErrorCode is the API error code err is reported with.
func ErrorCode(err error) string {
	var ce *core.Error
	switch {
	case errors.As(err, &ce):
		return ce.Code
	case errors.Is(err, store.ErrNotFound):
		return "ResourceNotFound"
	}
	return "InternalError"
}

// claim records a native route on the request's observation, unless another
// handler (an outer route making an in-process call) claimed it first. It
// returns nil when there is nothing to record into.
func claim(r *http.Request, pattern, action string) *Observation {
	o := Observed(r)
	if o == nil || o.API != "" {
		return nil
	}
	o.API, o.Operation = "native", pattern
	o.Service, _, _ = strings.Cut(action, ":")
	return o
}

func (o *Observation) failed(code string) {
	if o != nil {
		o.ErrorCode = code
	}
}

func (o *Observation) authFailed(err error) {
	if o != nil {
		o.ErrorCode = ErrorCode(err)
		o.AuthFailure = o.ErrorCode
	}
}
