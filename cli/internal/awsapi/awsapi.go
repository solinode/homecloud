// Package awsapi serves HomeCloud's services over the AWS wire protocols, so
// the AWS CLI, SDKs and tools such as Terraform work against HomeCloud by
// pointing their endpoint at it (AWS_ENDPOINT_URL).
//
// Requests are authenticated with AWS Signature Version 4 using HomeCloud
// access keys (or temporary credentials from STS) and authorized with IAM,
// exactly like the native API. A service registers its operations once:
//
//	awsapi.Register(&awsapi.Service{Name: "sqs", JSONPrefix: "AmazonSQS", Ops: ...})
//
// Protocols:
//   - awsJson1.0/1.1: POST / with X-Amz-Target "<JSONPrefix>.<Op>" (DynamoDB, SQS, Secrets Manager, Logs, ...)
//   - awsQuery / ec2Query: form-encoded Action=<Op> (IAM, STS, SNS, EC2, ...)
//   - REST (restJson1 / restXml): the service's REST handler routes by method and path (S3, Lambda, ...)
package awsapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/homecloudhq/homecloud/cli/internal/core"
	"github.com/homecloudhq/homecloud/cli/internal/httpx"
	"github.com/homecloudhq/homecloud/cli/internal/store"
)

// Op handles one AWS operation. The returned value is encoded for the request's
// protocol: JSON for awsJson, XML (see xml.go) for awsQuery. Returning nil
// sends an empty result.
type Op func(q *Req) (any, error)

// Service describes one AWS service endpoint.
type Service struct {
	// Name is the SigV4 signing name ("sqs", "dynamodb", "monitoring", ...).
	Name string
	// JSONPrefix is the X-Amz-Target prefix of awsJson services ("DynamoDB_20120810").
	JSONPrefix string
	// JSONVersion is "1.0" or "1.1" (awsJson content type); default "1.1".
	JSONVersion string
	// XMLNS is the response namespace of awsQuery services ("https://sns.amazonaws.com/doc/2010-03-31/").
	XMLNS string
	// EC2 selects the ec2Query dialect (no Result wrapper, <item> lists, EC2 error shape).
	EC2 bool
	// Ops are the operations by name (awsJson and awsQuery).
	Ops map[string]Op
	// PublicOps may be called without a signature (e.g. Cognito InitiateAuth).
	PublicOps map[string]bool
	// REST serves REST-protocol requests (restJson1/restXml); it must write the response.
	REST func(q *Req)
	// RESTError writes an error for REST services; default is restJson1 style.
	RESTError func(q *Req, e *Error)
	// ErrorCode maps a HomeCloud error code (e.g. "ResourceNotFound") to this
	// service's AWS error code; unmapped codes use DefaultErrorCode.
	ErrorCode map[string]string
	// StreamBody leaves the request body unread (S3 object uploads); the payload
	// hash is taken from X-Amz-Content-Sha256 and must be verified by the service.
	StreamBody bool
	// Unsigned reports whether an unsigned request belongs to this REST service
	// (anonymous S3 access to public buckets). The service authorizes it itself;
	// Req.P is nil.
	Unsigned func(r *http.Request) bool
}

var (
	regMu    sync.RWMutex
	services = map[string]*Service{} // by signing name
	byTarget = map[string]*Service{} // by JSON target prefix
)

// Register makes a service available over the AWS protocols.
func Register(s *Service) {
	regMu.Lock()
	defer regMu.Unlock()
	if s.JSONVersion == "" {
		s.JSONVersion = "1.1"
	}
	services[s.Name] = s
	if s.JSONPrefix != "" {
		byTarget[s.JSONPrefix] = s
	}
}

func lookup(name string) *Service {
	regMu.RLock()
	defer regMu.RUnlock()
	return services[name]
}

// lookupUnsigned finds the REST service that claims an unsigned request.
func lookupUnsigned(r *http.Request) *Service {
	regMu.RLock()
	defer regMu.RUnlock()
	for _, s := range services {
		if s.Unsigned != nil && s.REST != nil && s.Unsigned(r) {
			return s
		}
	}
	return nil
}

func lookupTarget(prefix string) *Service {
	regMu.RLock()
	defer regMu.RUnlock()
	return byTarget[prefix]
}

// splitTarget splits an X-Amz-Target at its last dot: prefixes such as
// "com.amazonaws.cloudtrail.v20131101.CloudTrail_20131101" contain dots.
func splitTarget(t string) (prefix, op string) {
	if i := strings.LastIndex(t, "."); i >= 0 {
		return t[:i], t[i+1:]
	}
	return t, ""
}

// Protocol of a request.
type Protocol int

const (
	JSON Protocol = iota
	Query
	REST
)

// Req is one AWS API request.
type Req struct {
	W         http.ResponseWriter
	R         *http.Request
	P         *httpx.Principal
	Svc       *Service
	Protocol  Protocol
	Op        string // operation name (awsJson / awsQuery); REST services set it for auditing
	Region    string // region the client signed for
	Account   string
	RequestID string
	Sig       *Signature // nil for public operations
	Secret    string     // the signing secret (to verify S3 streaming chunks); empty for public ops
	Body      []byte     // the request body (nil when the service streams it)
	Form      url.Values // awsQuery parameters
	// Creds resolves access keys, for services that accept other signature
	// schemes on unsigned-looking requests (S3 SigV2 presigned URLs).
	Creds Credentials

	action, resource string
	status           int
	info             *CallInfo
}

// CallInfo is what the audit trail learns about an AWS API call beyond the
// audit function's arguments: the request ID and, for failures, the error.
// AuditInfo returns it for the request passed to the Audit function.
type CallInfo struct {
	RequestID    string
	ErrorCode    string
	ErrorMessage string
}

type infoKey struct{}

// AuditInfo returns the AWS call details attached to r, or nil when r is not
// an AWS protocol request (native API requests carry none).
func AuditInfo(r *http.Request) *CallInfo {
	i, _ := r.Context().Value(infoKey{}).(*CallInfo)
	return i
}

// Bind decodes the awsJson request body into v.
func (q *Req) Bind(v any) error {
	if len(bytes.TrimSpace(q.Body)) == 0 {
		return nil
	}
	if err := json.Unmarshal(q.Body, v); err != nil {
		return Errorf(http.StatusBadRequest, "SerializationException", "could not parse request body: %v", err)
	}
	return nil
}

// Authorize checks the caller may perform action on resource. The last call is
// what CloudTrail records for the request.
func (q *Req) Authorize(action, resource string) error {
	return q.AuthorizeWith(action, resource, httpx.Access{})
}

// AuthorizeWith is Authorize with the resource's policy and request condition
// keys (s3:prefix, ...). The resource's own service supplies its policy when
// acc has none (see httpx.RegisterPolicyProvider).
func (q *Req) AuthorizeWith(action, resource string, acc httpx.Access) error {
	q.action, q.resource = action, resource
	if q.P == nil {
		return Errorf(http.StatusForbidden, "AccessDenied", "anonymous callers cannot perform %s", action)
	}
	if strings.HasPrefix(action, "sts:") || q.P.Permits(action, resource, acc) {
		return nil
	}
	code := "AccessDeniedException"
	if q.Protocol == Query || q.Svc.Name == "s3" || q.Svc.Name == "route53" {
		code = "AccessDenied"
	}
	return &Error{Status: http.StatusForbidden, Code: code,
		Message: fmt.Sprintf("User: %s is not authorized to perform: %s on resource: %s", q.P.ARN, action, resource)}
}

// ARN builds an ARN in this account for the service.
func (q *Req) ARN(service, resource string) string { return core.ARN(q.Account, service, resource) }

// Error is an AWS API error.
type Error struct {
	Status  int
	Code    string
	Message string
	// QueryCode is the legacy awsQuery code sent in x-amzn-query-error by services
	// that moved from awsQuery to awsJson (SQS), e.g. "AWS.SimpleQueueService.NonExistentQueue".
	QueryCode string
	// Resource is echoed in S3 errors.
	Resource string
	// Fields are extra members of an awsJson error body (e.g. DynamoDB's
	// CancellationReasons or the Item of a failed condition check).
	Fields map[string]any
}

func (e *Error) Error() string { return e.Code + ": " + e.Message }

func Errorf(status int, code, format string, a ...any) *Error {
	return &Error{Status: status, Code: code, Message: fmt.Sprintf(format, a...)}
}

// DefaultErrorCode maps HomeCloud error codes to the most common AWS equivalents.
var DefaultErrorCode = map[string]string{
	"ResourceNotFound":   "ResourceNotFoundException",
	"ValidationError":    "ValidationException",
	"BadRequest":         "ValidationException",
	"Conflict":           "ResourceInUseException",
	"AlreadyExists":      "ResourceAlreadyExistsException",
	"AccessDenied":       "AccessDeniedException",
	"TooManyRequests":    "ThrottlingException",
	"InternalError":      "InternalFailure",
	"LimitExceeded":      "LimitExceededException",
	"ServiceUnavailable": "ServiceUnavailable",
}

// toError converts any error to an AWS error for the service.
func toError(s *Service, err error) *Error {
	var ae *Error
	if errors.As(err, &ae) {
		return ae
	}
	var ce *core.Error
	switch {
	case errors.As(err, &ce):
	case errors.Is(err, store.ErrNotFound):
		ce = core.Errf(http.StatusNotFound, "ResourceNotFound", "resource not found")
	case errors.Is(err, store.ErrConflict):
		ce = core.Errf(http.StatusConflict, "Conflict", "the resource was modified concurrently; retry")
	default:
		log.Printf("aws %s: internal error: %v", s.Name, err)
		ce = core.Errf(http.StatusInternalServerError, "InternalError", "%s", core.InternalErrorMessage)
	}
	code := ""
	if s != nil && s.ErrorCode != nil {
		code = s.ErrorCode[ce.Code]
	}
	if code == "" {
		code = DefaultErrorCode[ce.Code]
	}
	if code == "" {
		code = ce.Code
	}
	status := ce.Status
	if status == http.StatusNotFound && s != nil && s.REST == nil {
		status = http.StatusBadRequest // awsJson/awsQuery report missing resources as 400
	}
	if status == http.StatusConflict && s != nil && s.REST == nil {
		status = http.StatusBadRequest
	}
	return &Error{Status: status, Code: code, Message: ce.Message}
}

// Credentials resolves an access key to its secret and principal. The session
// token must match for temporary credentials.
type Credentials interface {
	SigningSecret(accessKeyID, sessionToken string) (string, *httpx.Principal, error)
}

// Handler serves AWS protocol requests.
type Handler struct {
	Creds   Credentials
	Account string
	Audit   httpx.AuditFunc
	Now     func() time.Time
}

// Match reports whether r is an AWS protocol request rather than a native one.
func Match(r *http.Request) bool {
	if IsSigned(r) {
		return true
	}
	// Unsigned awsJson calls (public operations such as Cognito sign-in).
	if t := r.Header.Get("X-Amz-Target"); t != "" && r.Method == http.MethodPost {
		prefix, _ := splitTarget(t)
		return lookupTarget(prefix) != nil
	}
	return lookupUnsigned(r) != nil
}

const (
	maxBody = 100 << 20
	// maxPublicBody bounds what an unsigned (public operation) request may send.
	maxPublicBody = 1 << 20
)

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	q := &Req{W: w, R: r, Account: h.Account, RequestID: RequestID(), status: 200, Creds: h.Creds}
	q.info = &CallInfo{RequestID: q.RequestID}
	w.Header().Set("x-amzn-RequestId", q.RequestID)
	w.Header().Set("x-amz-request-id", q.RequestID)
	sw := &statusWriter{ResponseWriter: w, q: q}
	q.W = sw
	defer func() {
		if v := recover(); v != nil {
			log.Printf("aws: panic serving %s %s: %v", r.Method, r.URL.Path, v)
			q.fail(Errorf(http.StatusInternalServerError, "InternalFailure", "internal error"))
		}
		if h.Audit != nil && q.P != nil {
			action := q.action
			if action == "" && q.Svc != nil && q.Op != "" {
				action = q.Svc.Name + ":" + q.Op
			}
			if action != "" {
				res := q.resource
				if res == "" {
					res = "*"
				}
				h.Audit(q.P, action, res, r.WithContext(context.WithValue(r.Context(), infoKey{}, q.info)), q.status, time.Since(start))
			}
		}
	}()

	// Identify the service and operation.
	target := r.Header.Get("X-Amz-Target")
	var sig *Signature
	if IsSigned(r) {
		var err error
		if sig, err = ParseSignature(r); err != nil {
			q.fail(err)
			return
		}
		q.Region = sig.Region
		q.Svc = lookup(sig.Service)
		if target != "" {
			// Services sharing a signing name (DynamoDB and DynamoDB Streams both
			// sign as "dynamodb") are told apart by their target prefix.
			prefix, _ := splitTarget(target)
			if ts := lookupTarget(prefix); ts != nil {
				q.Svc = ts
			}
		}
		if q.Svc == nil {
			q.fail(Errorf(http.StatusBadRequest, "UnknownService", "HomeCloud does not implement the AWS %q API yet", sig.Service))
			return
		}
	} else if target != "" {
		prefix, _ := splitTarget(target)
		q.Svc = lookupTarget(prefix)
	} else {
		q.Svc = lookupUnsigned(r)
	}
	if q.Svc == nil {
		q.fail(Errorf(http.StatusForbidden, "MissingAuthenticationToken", "request is not signed"))
		return
	}
	switch {
	case target != "":
		prefix, op := splitTarget(target)
		if prefix != q.Svc.JSONPrefix {
			q.fail(Errorf(http.StatusBadRequest, "UnknownOperationException", "unknown target %q", target))
			return
		}
		q.Protocol, q.Op = JSON, op
	case q.Svc.REST != nil && !q.isQuery():
		q.Protocol = REST
	default:
		q.Protocol = Query
	}

	// Before buffering a body, make sure the caller is worth it: the signature
	// must be fresh and name a live access key, and when the client declared the
	// payload hash the signature is verified before the body is read. Otherwise
	// anyone could make the server buffer maxBody bytes per connection.
	payloadHash := ""
	var secret string
	var principal *httpx.Principal
	verified := false
	now := time.Now()
	if h.Now != nil {
		now = h.Now()
	}
	if sig != nil {
		payloadHash = sig.PayloadHash
		if err := sig.checkTime(now); err != nil {
			q.fail(err)
			return
		}
		var err error
		if secret, principal, err = h.Creds.SigningSecret(sig.AccessKeyID, sig.SessionToken); err != nil {
			q.fail(err)
			return
		}
		if payloadHash != "" && !strings.HasPrefix(payloadHash, "STREAMING-") || sig.Presigned {
			if err := sig.Verify(r, secret, payloadHash, now); err != nil {
				q.fail(err)
				return
			}
			verified = true
		}
	}
	if !(q.Svc.StreamBody && q.Protocol == REST) {
		limit := int64(maxBody)
		if sig == nil {
			limit = maxPublicBody // only public operations are reachable unsigned
		}
		b, err := io.ReadAll(io.LimitReader(r.Body, limit+1))
		if err != nil {
			q.fail(Errorf(http.StatusBadRequest, "IncompleteBody", "read body: %v", err))
			return
		}
		if int64(len(b)) > limit {
			q.fail(Errorf(http.StatusRequestEntityTooLarge, "RequestEntityTooLarge", "request body exceeds %d MB", limit>>20))
			return
		}
		q.Body = b
		r.Body = io.NopCloser(bytes.NewReader(b))
		actual := HashHex(b)
		if payloadHash == "" {
			payloadHash = actual
		} else if payloadHash != UnsignedPayload && !strings.HasPrefix(payloadHash, "STREAMING-") && payloadHash != actual {
			q.fail(Errorf(http.StatusBadRequest, "XAmzContentSHA256Mismatch", "the provided x-amz-content-sha256 header does not match what was computed"))
			return
		}
	}
	if payloadHash == "" {
		payloadHash = UnsignedPayload
	}
	if q.Protocol == Query {
		form := r.URL.Query()
		if r.Method == http.MethodPost {
			if f, err := url.ParseQuery(string(q.Body)); err == nil {
				for k, v := range f {
					form[k] = v
				}
			}
		}
		q.Form = form
		q.Op = form.Get("Action")
	}

	public := (q.Svc.PublicOps[q.Op] && q.Protocol != REST) || (q.Protocol == REST && q.Svc.Unsigned != nil)
	if sig != nil {
		p := principal
		if !verified {
			if err := sig.Verify(r, secret, payloadHash, now); err != nil {
				q.fail(err)
				return
			}
		}
		q.Sig, q.Secret, q.P = sig, secret, p
		p.AddRequestContext(r)
		p.Context["aws:requestedregion"] = []string{sig.Region}
	} else if !public {
		q.fail(Errorf(http.StatusForbidden, "MissingAuthenticationToken", "request is not signed"))
		return
	}

	if q.Protocol == REST {
		q.Svc.REST(q)
		return
	}
	op := q.Svc.Ops[q.Op]
	if op == nil {
		code := "UnknownOperationException"
		if q.Protocol == Query {
			code = "InvalidAction"
		}
		q.fail(Errorf(http.StatusBadRequest, code, "HomeCloud does not implement %s.%s yet", q.Svc.Name, q.Op))
		return
	}
	out, err := op(q)
	if err != nil {
		q.fail(err)
		return
	}
	if q.Protocol == JSON {
		q.writeJSON(http.StatusOK, out)
	} else {
		q.writeQuery(out)
	}
}

func (q *Req) isQuery() bool {
	ct := q.R.Header.Get("Content-Type")
	if q.R.Method == http.MethodPost && strings.HasPrefix(ct, "application/x-www-form-urlencoded") && q.Svc.Ops != nil {
		return true
	}
	return q.R.Method == http.MethodGet && q.R.URL.Query().Get("Action") != "" && q.Svc.Ops != nil
}

// Fail writes err in the request's protocol.
func (q *Req) Fail(err error) { q.fail(err) }

func (q *Req) fail(err error) {
	e := toError(q.Svc, err)
	if q.info != nil {
		q.info.ErrorCode, q.info.ErrorMessage = e.Code, e.Message
	}
	if q.Svc == nil {
		q.writeJSONError(e)
		return
	}
	switch q.Protocol {
	case JSON:
		q.writeJSONError(e)
	case Query:
		q.writeQueryError(e)
	default:
		if q.Svc.RESTError != nil {
			q.Svc.RESTError(q, e)
		} else {
			q.writeRESTJSONError(e)
		}
	}
}

func (q *Req) writeJSON(status int, v any) {
	q.W.Header().Set("Content-Type", "application/x-amz-json-"+q.Svc.JSONVersion)
	q.W.WriteHeader(status)
	if v == nil {
		v = struct{}{}
	}
	_ = json.NewEncoder(q.W).Encode(v)
}

func (q *Req) writeJSONError(e *Error) {
	q.W.Header().Set("X-Amzn-ErrorType", e.Code)
	if e.QueryCode != "" {
		fault := "Sender"
		if e.Status >= 500 {
			fault = "Receiver"
		}
		q.W.Header().Set("x-amzn-query-error", e.QueryCode+";"+fault)
	}
	ver := "1.1"
	if q.Svc != nil {
		ver = q.Svc.JSONVersion
	}
	q.W.Header().Set("Content-Type", "application/x-amz-json-"+ver)
	q.W.WriteHeader(e.Status)
	body := map[string]any{"__type": e.Code, "message": e.Message}
	for k, v := range e.Fields {
		body[k] = v
	}
	_ = json.NewEncoder(q.W).Encode(body)
}

func (q *Req) writeRESTJSONError(e *Error) {
	q.W.Header().Set("X-Amzn-ErrorType", e.Code)
	q.W.Header().Set("Content-Type", "application/json")
	q.W.WriteHeader(e.Status)
	_ = json.NewEncoder(q.W).Encode(map[string]string{"Type": "User", "message": e.Message, "__type": e.Code})
}

// WriteJSON writes a restJson1 response.
func (q *Req) WriteJSON(status int, v any) {
	q.W.Header().Set("Content-Type", "application/json")
	q.W.WriteHeader(status)
	if v != nil {
		_ = json.NewEncoder(q.W).Encode(v)
	}
}

type statusWriter struct {
	http.ResponseWriter
	q *Req
}

func (s *statusWriter) WriteHeader(code int) { s.q.status = code; s.ResponseWriter.WriteHeader(code) }
func (s *statusWriter) Flush() {
	if f, ok := s.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// RequestID returns an AWS-style request ID.
func RequestID() string {
	h := core.RandHex(32)
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32]
}

// Epoch returns t as AWS JSON timestamps (seconds since the epoch, with fraction).
func Epoch(t time.Time) float64 {
	if t.IsZero() {
		return 0
	}
	return float64(t.UnixMilli()) / 1000
}
