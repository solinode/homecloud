package cfn

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"time"

	"github.com/homecloudhq/homecloud/cli/internal/core"
	"github.com/homecloudhq/homecloud/cli/internal/httpx"
)

// ---- API calls ----

type actorKey struct{}

// withActor makes resource operations run as the principal fn returns each time
// (a stack's service role is re-assumed, so its policy edits apply immediately).
func withActor(ctx context.Context, fn func() (*httpx.Principal, error)) context.Context {
	return context.WithValue(ctx, actorKey{}, fn)
}

// actAs is the principal a resource operation runs as right now.
func (s *Service) actAs(ctx context.Context, p *httpx.Principal) (*httpx.Principal, error) {
	if fn, ok := ctx.Value(actorKey{}).(func() (*httpx.Principal, error)); ok {
		fresh, err := fn()
		if err != nil {
			return nil, fmt.Errorf("the stack's service role cannot be used: %w", err)
		}
		return fresh, nil
	}
	if s.Refresh != nil {
		// Act with the caller's current permissions, not those at submission time.
		fresh, err := s.Refresh(p)
		if err != nil {
			return nil, fmt.Errorf("the stack's caller can no longer act: %w", err)
		}
		return fresh, nil
	}
	return p, nil
}

func (s *Service) serve(ctx context.Context, p *httpx.Principal, method, path string, body any) (*httptest.ResponseRecorder, error) {
	p, err := s.actAs(ctx, p)
	if err != nil {
		return nil, err
	}
	var rd io.Reader = http.NoBody
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req := httptest.NewRequest(method, path, rd).WithContext(httpx.WithPrincipal(ctx, p))
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = "127.0.0.1:0"
	w := httptest.NewRecorder()
	s.Handler.ServeHTTP(w, req)
	return w, nil
}

func (s *Service) call(ctx context.Context, p *httpx.Principal, method, path string, body any) (any, error) {
	w, err := s.serve(ctx, p, method, path, body)
	if err != nil {
		return nil, err
	}
	var out any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	if w.Code >= 300 {
		if m, ok := out.(map[string]any); ok {
			if e, ok := m["error"].(map[string]any); ok {
				return nil, fmt.Errorf("%v: %v", e["code"], e["message"])
			}
		}
		return nil, fmt.Errorf("%s %s: HTTP %d", method, path, w.Code)
	}
	return out, nil
}

// fetch reads a raw (non-JSON) response body, as the caller.
func (s *Service) fetch(ctx context.Context, p *httpx.Principal, path string) ([]byte, error) {
	w, err := s.serve(ctx, p, "GET", path, nil)
	if err != nil {
		return nil, err
	}
	if w.Code >= 300 {
		var out struct {
			Error struct{ Code, Message string } `json:"error"`
		}
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		if out.Error.Code != "" {
			return nil, fmt.Errorf("%s: %s", out.Error.Code, out.Error.Message)
		}
		return nil, fmt.Errorf("GET %s: HTTP %d", path, w.Code)
	}
	return w.Body.Bytes(), nil
}

func (x *xctx) Fetch(path string) ([]byte, error) { return x.s.fetch(x.ctx, x.p, path) }

// xctx is what a resource handler knows about the resource being created.
type xctx struct {
	s       *Service
	ctx     context.Context
	p       *httpx.Principal
	Stack   string
	Logical string
	Account string
	// Endpoint is the scheme and host the stack's creator used (queue URLs).
	Endpoint string
}

func (x *xctx) Call(method, path string, body any) (any, error) {
	return x.s.call(x.ctx, x.p, method, path, body)
}

const nameAlphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"

func randID(n int) string {
	h := core.RandHex(n)
	out := make([]byte, n)
	for i := range out {
		out[i] = nameAlphabet[int(h[i])%len(nameAlphabet)]
	}
	return string(out)
}

// GenName is the physical name CloudFormation gives a resource whose template
// leaves the name out: <stack>-<logical id>-<random>, at most max characters.
func (x *xctx) GenName(max int, lower bool) string {
	base := x.Stack + "-" + x.Logical
	if len(base) > max-13 {
		base = base[:max-13]
	}
	n := base + "-" + randID(12)
	if lower {
		n = strings.ToLower(n)
	}
	return n
}

func fill(tmpl, id string, props map[string]any) string {
	out := strings.ReplaceAll(tmpl, "{id}", esc(id))
	if strings.Contains(out, "{name}") {
		out = strings.ReplaceAll(out, "{name}", esc(fmt.Sprint(props["name"])))
	}
	return out
}

type created struct {
	Native string
	HC     string
	Attrs  map[string]any
}

// createResource makes one resource. AWS::* types translate their properties
// (see awstypes.go); every call is made with the caller's permissions.
func (s *Service) createResource(x *xctx, typ string, props map[string]any) (created, error) {
	ctx, p := x.ctx, x.p
	out := created{HC: typ}
	orig, translated := props, map[string]any{}
	if !supported(typ) {
		return out, fmt.Errorf("Resource type %s is not supported by HomeCloud", typ)
	}
	if a, ok := awsTypes[typ]; ok {
		out.HC = a.HC
		if a.Create != nil {
			id, attrs, err := a.Create(x, props)
			out.Native, out.Attrs = id, attrs
			return out, err
		}
		if a.HC == "" {
			out.Native, out.Attrs = x.Stack+"-"+x.Logical+"-"+randID(12), map[string]any{}
			return out, nil
		}
		if a.Props != nil {
			np, err := a.Props(x, props)
			if err != nil {
				return out, err
			}
			props, translated = np, np // Post sees the template properties too (translated keys win)
		}
	}
	spec := types[out.HC]
	method, path, _ := strings.Cut(spec.Create, " ")
	path = fill(path, "", props)
	if spec.Get != "" && (method == http.MethodPut || spec.IDField == "name") {
		// Upsert-style and idempotent create APIs would silently take over an
		// existing resource (and a rollback would then delete it).
		if _, err := s.call(ctx, p, "GET", fill(spec.Get, fmt.Sprint(props["name"]), props), nil); err == nil {
			return out, fmt.Errorf("%s %v already exists", typ, props["name"])
		}
	}
	body := map[string]any{}
	for k, v := range props {
		body[k] = v
	}
	if out.HC == "HC::SSM::Parameter" {
		body["overwrite"] = false
	}
	resp, err := s.call(ctx, p, method, path, body)
	if err != nil {
		return out, err
	}
	if spec.ArrayFirst {
		arr, _ := resp.([]any)
		if len(arr) == 0 {
			return out, fmt.Errorf("empty create response")
		}
		resp = arr[0]
	}
	attrs, _ := resp.(map[string]any)
	if attrs == nil {
		attrs = map[string]any{}
	}
	var id string
	switch {
	case spec.IDField == "@name":
		id = fmt.Sprint(props["name"])
	case spec.IDField == "@family:revision":
		id = fmt.Sprintf("%v:%v", attrs["family"], attrs["revision"])
	default:
		id = fmt.Sprint(attrs[spec.IDField])
	}
	if id == "" || id == "<nil>" {
		return out, fmt.Errorf("could not determine the physical ID of the new %s", typ)
	}
	out.Native, out.Attrs = id, attrs
	// Wait for asynchronous resources to become ready.
	if spec.WaitField != "" {
		deadline := time.Now().Add(20 * time.Minute)
		for {
			cur, err := s.call(ctx, p, "GET", fill(spec.Get, id, props), nil)
			if err != nil {
				return out, err
			}
			m, _ := cur.(map[string]any)
			st := fmt.Sprint(m[spec.WaitField])
			if slices.Contains(spec.Ready, st) {
				out.Attrs = merge(out.Attrs, m)
				break
			}
			if slices.Contains(spec.Failed, st) {
				return out, fmt.Errorf("resource became %s: %v", st, firstNonNil(m["state_reason"], m["status_reason"]))
			}
			if time.Now().After(deadline) {
				return out, fmt.Errorf("timed out waiting for %s to become ready", typ)
			}
			select {
			case <-ctx.Done():
				return out, ctx.Err()
			case <-time.After(2 * time.Second):
			}
		}
	} else if spec.Get != "" {
		if cur, err := s.call(ctx, p, "GET", fill(spec.Get, id, props), nil); err == nil {
			if m, ok := cur.(map[string]any); ok {
				out.Attrs = merge(out.Attrs, m)
			}
		}
	}
	if a, ok := awsTypes[typ]; ok && a.Post != nil {
		if err := a.Post(x, id, merge(orig, translated), out.Attrs); err != nil {
			return out, err
		}
	}
	return out, nil
}

func firstNonNil(vs ...any) any {
	for _, v := range vs {
		if v != nil && v != "" {
			return v
		}
	}
	return "unknown reason"
}

func merge(a, b map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range a {
		out[k] = v
	}
	for k, v := range b {
		out[k] = v
	}
	return out
}

func (s *Service) deleteResource(ctx context.Context, p *httpx.Principal, r *Resource) error {
	if a, ok := awsTypes[r.Type]; ok && a.Delete != nil {
		x := &xctx{s: s, ctx: ctx, p: p, Logical: r.LogicalID}
		err := a.Delete(x, r)
		if err != nil && gone(err) {
			return nil
		}
		return err
	}
	if r.nativeType() == "" || types[r.nativeType()].Delete == "" {
		return nil // nothing behind it (AWS::CDK::Metadata)
	}
	spec := types[r.nativeType()]
	method, path, _ := strings.Cut(spec.Delete, " ")
	_, err := s.call(ctx, p, method, fill(path, r.native(), r.Properties), nil)
	if err != nil && gone(err) {
		return nil // already gone
	}
	return err
}

func gone(err error) bool {
	m := err.Error()
	return strings.Contains(m, "NotFound") || strings.Contains(m, "DoesNotExist") || strings.Contains(m, "does not exist")
}
