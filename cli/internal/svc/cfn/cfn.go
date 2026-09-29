// Package cfn implements CloudFormation-style stacks: YAML or JSON templates
// declaring resources of any HomeCloud service, with parameters, outputs,
// intrinsic functions, dependency ordering, readiness waits, rollback on
// failure, updates by replacement and ordered deletion. Every resource
// operation is an in-process API call made with the caller's permissions.
package cfn

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/homecloudhq/homecloud/cli/internal/core"
	"github.com/homecloudhq/homecloud/cli/internal/httpx"
	"github.com/homecloudhq/homecloud/cli/internal/svc"
	"gopkg.in/yaml.v3"
)

const cStacks = "cfn_stacks"

type Template struct {
	Description string                    `json:"Description,omitempty"`
	Parameters  map[string]ParamDef       `json:"Parameters,omitempty"`
	Resources   map[string]ResourceDef    `json:"Resources"`
	Outputs     map[string]map[string]any `json:"Outputs,omitempty"`
}

type ParamDef struct {
	Type          string `json:"Type"`
	Default       any    `json:"Default,omitempty"`
	AllowedValues []any  `json:"AllowedValues,omitempty"`
	Description   string `json:"Description,omitempty"`
	NoEcho        bool   `json:"NoEcho,omitempty"`
}

type ResourceDef struct {
	Type           string         `json:"Type"`
	Properties     map[string]any `json:"Properties"`
	DependsOn      any            `json:"DependsOn,omitempty"`
	DeletionPolicy string         `json:"DeletionPolicy,omitempty"`
}

type Resource struct {
	LogicalID  string         `json:"logical_id"`
	Type       string         `json:"type"`
	PhysicalID string         `json:"physical_id"`
	Status     string         `json:"status"`
	Reason     string         `json:"reason,omitempty"`
	Properties map[string]any `json:"properties,omitempty"` // resolved, as sent
	Attributes map[string]any `json:"attributes,omitempty"`
	UpdatedAt  time.Time      `json:"updated_at"`
}

type Event struct {
	Time      time.Time `json:"time"`
	LogicalID string    `json:"logical_id"`
	Type      string    `json:"type"`
	Status    string    `json:"status"`
	Reason    string    `json:"reason,omitempty"`
}

type Stack struct {
	Name         string               `json:"name"`
	ARN          string               `json:"arn"`
	Status       string               `json:"status"`
	StatusReason string               `json:"status_reason,omitempty"`
	Description  string               `json:"description,omitempty"`
	Template     string               `json:"template"`
	Parameters   map[string]any       `json:"parameters"`
	Resources    map[string]*Resource `json:"resources"`
	Order        []string             `json:"order"` // creation order
	Outputs      map[string]any       `json:"outputs"`
	Events       []Event              `json:"events"`
	CreatedAt    time.Time            `json:"created_at"`
	UpdatedAt    time.Time            `json:"updated_at"`
}

type Service struct {
	env *svc.Env
	// Handler is the API (set by the server) that resource operations call into.
	Handler http.Handler
	// Refresh re-reads a principal's current permissions (set by the server from IAM).
	Refresh func(*httpx.Principal) (*httpx.Principal, error)
	locks   sync.Map
	mu      sync.Mutex // guards stack status transitions
}

func New(env *svc.Env) *Service { return &Service{env: env} }

// ---- template parsing ----

// Parse reads a JSON or YAML template; YAML short tags (!Ref, !GetAtt, !Sub, ...) become their long forms.
func Parse(src string) (*Template, error) {
	var node yaml.Node
	if err := yaml.Unmarshal([]byte(src), &node); err != nil {
		return nil, fmt.Errorf("template is not valid YAML or JSON: %w", err)
	}
	if len(src) > 1<<20 {
		return nil, fmt.Errorf("template is larger than 1 MB")
	}
	d := &decoder{active: map[*yaml.Node]bool{}}
	v, err := d.decode(&node)
	if err != nil {
		return nil, err
	}
	b, _ := json.Marshal(v)
	var t Template
	if err := json.Unmarshal(b, &t); err != nil {
		return nil, fmt.Errorf("template structure: %w", err)
	}
	if len(t.Resources) == 0 {
		return nil, fmt.Errorf("template must declare at least one resource")
	}
	for id, r := range t.Resources {
		if !logicalRe.MatchString(id) {
			return nil, fmt.Errorf("logical ID %q must be alphanumeric", id)
		}
		if _, ok := types[r.Type]; !ok {
			return nil, fmt.Errorf("resource %s has unsupported type %q", id, r.Type)
		}
	}
	return &t, nil
}

var logicalRe = regexp.MustCompile(`^[A-Za-z0-9]{1,255}$`)

// decoder converts YAML nodes to plain values, refusing alias cycles and
// alias "bombs" that would expand to huge documents.
type decoder struct {
	active map[*yaml.Node]bool
	nodes  int
}

const maxNodes = 100000

func (d *decoder) decode(n *yaml.Node) (any, error) {
	d.nodes++
	if d.nodes > maxNodes {
		return nil, fmt.Errorf("template expands to more than %d values", maxNodes)
	}
	switch n.Kind {
	case yaml.DocumentNode:
		if len(n.Content) == 0 {
			return nil, nil
		}
		return d.decode(n.Content[0])
	case yaml.AliasNode:
		if d.active[n.Alias] {
			return nil, fmt.Errorf("template contains a recursive alias")
		}
		d.active[n.Alias] = true
		defer delete(d.active, n.Alias)
		return d.decode(n.Alias)
	}
	var v any
	switch n.Kind {
	case yaml.MappingNode:
		m := map[string]any{}
		for i := 0; i+1 < len(n.Content); i += 2 {
			val, err := d.decode(n.Content[i+1])
			if err != nil {
				return nil, err
			}
			m[n.Content[i].Value] = val
		}
		v = m
	case yaml.SequenceNode:
		arr := []any{}
		for _, c := range n.Content {
			val, err := d.decode(c)
			if err != nil {
				return nil, err
			}
			arr = append(arr, val)
		}
		v = arr
	case yaml.ScalarNode:
		if err := n.Decode(&v); err != nil {
			v = n.Value
		}
		if strings.HasPrefix(n.Tag, "!") && !strings.HasPrefix(n.Tag, "!!") {
			v = n.Value // arguments of short-form intrinsics (!Ref x) are strings
		}
	}
	if strings.HasPrefix(n.Tag, "!") && !strings.HasPrefix(n.Tag, "!!") {
		tag := n.Tag[1:]
		switch tag {
		case "Ref":
			return map[string]any{"Ref": v}, nil
		case "GetAtt":
			if s, ok := v.(string); ok {
				id, attr, _ := strings.Cut(s, ".")
				return map[string]any{"Fn::GetAtt": []any{id, attr}}, nil
			}
			return map[string]any{"Fn::GetAtt": v}, nil
		default:
			return map[string]any{"Fn::" + tag: v}, nil
		}
	}
	return v, nil
}

// ---- intrinsic functions ----

type resolver struct {
	stack  *Stack
	params map[string]any
}

var subRe = regexp.MustCompile(`\$\{([^}]+)\}`)

func (r *resolver) ref(name string) (any, error) {
	switch name {
	case "HC::StackName", "AWS::StackName":
		return r.stack.Name, nil
	case "HC::AccountId", "AWS::AccountId":
		return strings.Split(r.stack.ARN, ":")[4], nil
	case "HC::Region", "AWS::Region":
		return core.DefaultRegion, nil
	}
	if v, ok := r.params[name]; ok {
		return v, nil
	}
	if res := r.stack.Resources[name]; res != nil && res.PhysicalID != "" {
		return res.PhysicalID, nil
	}
	return nil, fmt.Errorf("unresolved reference %q", name)
}

func (r *resolver) getAtt(id, attr string) (any, error) {
	res := r.stack.Resources[id]
	if res == nil || res.PhysicalID == "" {
		return nil, fmt.Errorf("GetAtt: resource %q has not been created", id)
	}
	var cur any = res.Attributes
	for _, p := range strings.Split(attr, ".") {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("GetAtt %s.%s: not found", id, attr)
		}
		v, ok := m[p]
		if !ok {
			// Allow AWS-style PascalCase attribute names (Arn -> arn).
			v, ok = m[snake(p)]
		}
		if !ok {
			return nil, fmt.Errorf("GetAtt %s.%s: attribute not found", id, attr)
		}
		cur = v
	}
	return cur, nil
}

func snake(s string) string {
	var b strings.Builder
	for i, c := range s {
		if c >= 'A' && c <= 'Z' {
			if i > 0 {
				b.WriteByte('_')
			}
			c += 'a' - 'A'
		}
		b.WriteRune(c)
	}
	return b.String()
}

func (r *resolver) resolve(v any) (any, error) {
	switch x := v.(type) {
	case map[string]any:
		if len(x) == 1 {
			for k, arg := range x {
				switch k {
				case "Ref":
					return r.ref(fmt.Sprint(arg))
				case "Fn::GetAtt":
					parts, _ := arg.([]any)
					if len(parts) != 2 {
						return nil, fmt.Errorf("Fn::GetAtt takes [LogicalId, Attribute]")
					}
					return r.getAtt(fmt.Sprint(parts[0]), fmt.Sprint(parts[1]))
				case "Fn::Sub":
					return r.sub(arg)
				case "Fn::Join":
					parts, _ := arg.([]any)
					if len(parts) != 2 {
						return nil, fmt.Errorf("Fn::Join takes [delimiter, list]")
					}
					list, err := r.resolve(parts[1])
					if err != nil {
						return nil, err
					}
					items, _ := list.([]any)
					strs := make([]string, len(items))
					for i, it := range items {
						strs[i] = fmt.Sprint(it)
					}
					return strings.Join(strs, fmt.Sprint(parts[0])), nil
				case "Fn::Select":
					parts, _ := arg.([]any)
					if len(parts) != 2 {
						return nil, fmt.Errorf("Fn::Select takes [index, list]")
					}
					list, err := r.resolve(parts[1])
					if err != nil {
						return nil, err
					}
					items, _ := list.([]any)
					i, _ := strconv.Atoi(fmt.Sprint(parts[0]))
					if i < 0 || i >= len(items) {
						return nil, fmt.Errorf("Fn::Select index %d out of range", i)
					}
					return items[i], nil
				case "Fn::Split":
					parts, _ := arg.([]any)
					if len(parts) != 2 {
						return nil, fmt.Errorf("Fn::Split takes [delimiter, string]")
					}
					s, err := r.resolve(parts[1])
					if err != nil {
						return nil, err
					}
					out := []any{}
					for _, p := range strings.Split(fmt.Sprint(s), fmt.Sprint(parts[0])) {
						out = append(out, p)
					}
					return out, nil
				}
			}
		}
		out := map[string]any{}
		for k, val := range x {
			rv, err := r.resolve(val)
			if err != nil {
				return nil, err
			}
			out[k] = rv
		}
		return out, nil
	case []any:
		out := make([]any, len(x))
		for i, val := range x {
			rv, err := r.resolve(val)
			if err != nil {
				return nil, err
			}
			out[i] = rv
		}
		return out, nil
	}
	return v, nil
}

func (r *resolver) sub(arg any) (any, error) {
	tmpl, vars := "", map[string]any{}
	switch a := arg.(type) {
	case string:
		tmpl = a
	case []any:
		if len(a) != 2 {
			return nil, fmt.Errorf("Fn::Sub takes a string or [string, variables]")
		}
		tmpl = fmt.Sprint(a[0])
		m, _ := a[1].(map[string]any)
		for k, v := range m {
			rv, err := r.resolve(v)
			if err != nil {
				return nil, err
			}
			vars[k] = rv
		}
	}
	var firstErr error
	out := subRe.ReplaceAllStringFunc(tmpl, func(m string) string {
		name := m[2 : len(m)-1]
		if v, ok := vars[name]; ok {
			return fmt.Sprint(v)
		}
		var v any
		var err error
		if id, attr, ok := strings.Cut(name, "."); ok && r.stack.Resources[id] != nil {
			v, err = r.getAtt(id, attr)
		} else {
			v, err = r.ref(name)
		}
		if err != nil && firstErr == nil {
			firstErr = err
		}
		return fmt.Sprint(v)
	})
	return out, firstErr
}

// deps lists the logical IDs a resource definition refers to.
func deps(def ResourceDef, ids map[string]bool) []string {
	found := map[string]bool{}
	var walk func(v any)
	walk = func(v any) {
		switch x := v.(type) {
		case map[string]any:
			if ref, ok := x["Ref"].(string); ok && ids[ref] {
				found[ref] = true
			}
			if ga, ok := x["Fn::GetAtt"].([]any); ok && len(ga) > 0 {
				if id := fmt.Sprint(ga[0]); ids[id] {
					found[id] = true
				}
			}
			if s, ok := x["Fn::Sub"]; ok {
				str := fmt.Sprint(s)
				if arr, ok := s.([]any); ok && len(arr) > 0 {
					str = fmt.Sprint(arr[0])
				}
				for _, m := range subRe.FindAllStringSubmatch(str, -1) {
					id, _, _ := strings.Cut(m[1], ".")
					if ids[id] {
						found[id] = true
					}
				}
			}
			for _, val := range x {
				walk(val)
			}
		case []any:
			for _, val := range x {
				walk(val)
			}
		}
	}
	walk(def.Properties)
	switch d := def.DependsOn.(type) {
	case string:
		found[d] = true
	case []any:
		for _, x := range d {
			found[fmt.Sprint(x)] = true
		}
	}
	out := make([]string, 0, len(found))
	for id := range found {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

// order returns resources in dependency order.
func order(t *Template) ([]string, error) {
	ids := map[string]bool{}
	for id := range t.Resources {
		ids[id] = true
	}
	var out []string
	state := map[string]int{} // 1 = visiting, 2 = done
	var visit func(id string, path []string) error
	visit = func(id string, path []string) error {
		switch state[id] {
		case 1:
			return fmt.Errorf("circular dependency: %s", strings.Join(append(path, id), " -> "))
		case 2:
			return nil
		}
		if !ids[id] {
			return fmt.Errorf("%s depends on unknown resource %q", path[len(path)-1], id)
		}
		state[id] = 1
		for _, d := range deps(t.Resources[id], ids) {
			if err := visit(d, append(path, id)); err != nil {
				return err
			}
		}
		state[id] = 2
		out = append(out, id)
		return nil
	}
	names := make([]string, 0, len(ids))
	for id := range ids {
		names = append(names, id)
	}
	sort.Strings(names)
	for _, id := range names {
		if err := visit(id, nil); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// ---- API calls ----

func (s *Service) call(ctx context.Context, p *httpx.Principal, method, path string, body any) (any, error) {
	if s.Refresh != nil {
		// Act with the caller's current permissions, not those at submission time.
		fresh, err := s.Refresh(p)
		if err != nil {
			return nil, fmt.Errorf("the stack's caller can no longer act: %w", err)
		}
		p = fresh
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

func fill(tmpl, id string, props map[string]any) string {
	out := strings.ReplaceAll(tmpl, "{id}", esc(id))
	if strings.Contains(out, "{name}") {
		out = strings.ReplaceAll(out, "{name}", esc(fmt.Sprint(props["name"])))
	}
	return out
}

func (s *Service) createResource(ctx context.Context, p *httpx.Principal, typ string, props map[string]any) (string, map[string]any, error) {
	spec := types[typ]
	method, path, _ := strings.Cut(spec.Create, " ")
	path = fill(path, "", props)
	if method == http.MethodPut && spec.Get != "" {
		// Upsert-style APIs would silently take over an existing resource.
		if _, err := s.call(ctx, p, "GET", fill(spec.Get, fmt.Sprint(props["name"]), props), nil); err == nil {
			return "", nil, fmt.Errorf("%s %v already exists", typ, props["name"])
		}
	}
	body := map[string]any{}
	for k, v := range props {
		body[k] = v
	}
	if typ == "HC::SSM::Parameter" {
		body["overwrite"] = false
	}
	out, err := s.call(ctx, p, method, path, body)
	if err != nil {
		return "", nil, err
	}
	if spec.ArrayFirst {
		arr, _ := out.([]any)
		if len(arr) == 0 {
			return "", nil, fmt.Errorf("empty create response")
		}
		out = arr[0]
	}
	attrs, _ := out.(map[string]any)
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
		return "", nil, fmt.Errorf("could not determine the physical ID of the new %s", typ)
	}
	// Wait for asynchronous resources to become ready.
	if spec.WaitField != "" {
		deadline := time.Now().Add(20 * time.Minute)
		for {
			cur, err := s.call(ctx, p, "GET", fill(spec.Get, id, props), nil)
			if err != nil {
				return id, attrs, err
			}
			m, _ := cur.(map[string]any)
			st := fmt.Sprint(m[spec.WaitField])
			if slices.Contains(spec.Ready, st) {
				attrs = merge(attrs, m)
				break
			}
			if slices.Contains(spec.Failed, st) {
				return id, attrs, fmt.Errorf("resource became %s: %v", st, firstNonNil(m["state_reason"], m["status_reason"]))
			}
			if time.Now().After(deadline) {
				return id, attrs, fmt.Errorf("timed out waiting for %s to become ready", typ)
			}
			select {
			case <-ctx.Done():
				return id, attrs, ctx.Err()
			case <-time.After(2 * time.Second):
			}
		}
	} else if spec.Get != "" {
		if cur, err := s.call(ctx, p, "GET", fill(spec.Get, id, props), nil); err == nil {
			if m, ok := cur.(map[string]any); ok {
				attrs = merge(attrs, m)
			}
		}
	}
	return id, attrs, nil
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
	spec := types[r.Type]
	method, path, _ := strings.Cut(spec.Delete, " ")
	_, err := s.call(ctx, p, method, fill(path, r.PhysicalID, r.Properties), nil)
	if err != nil && (strings.Contains(err.Error(), "NotFound") || strings.Contains(err.Error(), "DoesNotExist") || strings.Contains(err.Error(), "does not exist")) {
		return nil // already gone
	}
	return err
}
