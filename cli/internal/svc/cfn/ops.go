package cfn

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/homecloudhq/homecloud/cli/internal/core"
	"github.com/homecloudhq/homecloud/cli/internal/httpx"
	"github.com/homecloudhq/homecloud/cli/internal/store"
)

func (s *Service) lock(name string) *sync.Mutex {
	m, _ := s.locks.LoadOrStore(name, &sync.Mutex{})
	return m.(*sync.Mutex)
}

func (s *Service) save(st *Stack) {
	st.UpdatedAt = core.Now()
	if err := store.Put(s.env.Store, cStacks, st.Name, st); err != nil {
		log.Printf("cfn: save %s: %v", st.Name, err)
	}
}

func (s *Service) event(st *Stack, id, typ, status, reason string) {
	st.Events = append([]Event{{Time: core.Now(), LogicalID: id, Type: typ, Status: status, Reason: reason}}, st.Events...)
	if len(st.Events) > 500 {
		st.Events = st.Events[:500]
	}
	s.save(st)
}

// params validates supplied parameters against the template.
func params(t *Template, in map[string]any) (map[string]any, error) {
	out := map[string]any{}
	for name, def := range t.Parameters {
		v, ok := in[name]
		if !ok {
			if def.Default == nil {
				return nil, core.BadRequest("parameter %s is required", name)
			}
			v = def.Default
		}
		switch def.Type {
		case "Number":
			if _, err := fmt.Sscan(fmt.Sprint(v), new(float64)); err != nil {
				return nil, core.BadRequest("parameter %s must be a number", name)
			}
		case "CommaDelimitedList":
			parts := []any{}
			for _, p := range strings.Split(fmt.Sprint(v), ",") {
				parts = append(parts, strings.TrimSpace(p))
			}
			v = parts
		}
		if len(def.AllowedValues) > 0 && !slices.ContainsFunc(def.AllowedValues, func(a any) bool { return fmt.Sprint(a) == fmt.Sprint(v) }) {
			return nil, core.BadRequest("parameter %s must be one of %v", name, def.AllowedValues)
		}
		out[name] = v
	}
	for name := range in {
		if _, ok := t.Parameters[name]; !ok {
			return nil, core.BadRequest("template has no parameter %q", name)
		}
	}
	return out, nil
}

// deploy brings the stack's resources in line with template t (create or
// update). prev is the template being replaced (nil on create). On failure
// during create with rollback, everything created is removed again; the
// returned bool reports whether that rollback fully succeeded.
func (s *Service) deploy(ctx context.Context, p *httpx.Principal, st *Stack, t, prev *Template, rollback bool) (error, bool) {
	ord, err := order(t)
	if err != nil {
		return err, true
	}
	r := &resolver{stack: st, params: st.Parameters}
	var created []string
	fail := func(err error) (error, bool) {
		if rollback {
			return err, s.rollback(ctx, p, st, created)
		}
		return err, true
	}
	ids := map[string]bool{}
	for id := range t.Resources {
		ids[id] = true
	}
	// Remove resources that are no longer in the template (reverse order),
	// keeping those the previous template marked DeletionPolicy: Retain.
	for i := len(st.Order) - 1; i >= 0; i-- {
		id := st.Order[i]
		if _, keep := t.Resources[id]; keep {
			continue
		}
		res := st.Resources[id]
		if res == nil {
			continue
		}
		if prev != nil && prev.Resources[id].DeletionPolicy == "Retain" {
			s.event(st, id, res.Type, "DELETE_SKIPPED", "removed from the template; DeletionPolicy: Retain")
			delete(st.Resources, id)
			continue
		}
		s.event(st, id, res.Type, "DELETE_IN_PROGRESS", "removed from the template")
		if err := s.deleteResource(ctx, p, res); err != nil {
			s.event(st, id, res.Type, "DELETE_FAILED", err.Error())
			return fmt.Errorf("%s: %w", id, err), true
		}
		delete(st.Resources, id)
		s.event(st, id, res.Type, "DELETE_COMPLETE", "")
	}
	st.Order = slices.DeleteFunc(st.Order, func(id string) bool { return st.Resources[id] == nil })
	// Resources that were (re)created in this deployment; anything depending on
	// them is replaced as well, since deleting a resource can cascade (e.g. a
	// function's event source mappings go with it).
	fresh := map[string]bool{}
	for _, id := range ord {
		def := t.Resources[id]
		props, err := r.resolve(def.Properties)
		if err != nil {
			s.event(st, id, def.Type, "CREATE_FAILED", err.Error())
			return fail(fmt.Errorf("%s: %w", id, err))
		}
		pm, _ := props.(map[string]any)
		if pm == nil {
			pm = map[string]any{}
		}
		old := st.Resources[id]
		dependsOnFresh := slices.ContainsFunc(deps(def, ids), func(d string) bool { return fresh[d] })
		if old != nil && old.PhysicalID != "" && old.Type == def.Type && !dependsOnFresh && reflect.DeepEqual(normalize(old.Properties), normalize(pm)) {
			continue // unchanged
		}
		fresh[id] = true
		if old != nil && old.PhysicalID != "" {
			// Replacement: remove the old resource first (names are unique in HomeCloud).
			s.event(st, id, old.Type, "UPDATE_IN_PROGRESS", "properties changed; replacing")
			if err := s.deleteResource(ctx, p, old); err != nil {
				s.event(st, id, old.Type, "UPDATE_FAILED", err.Error())
				return fmt.Errorf("%s: %w", id, err), true
			}
			old.PhysicalID = ""
		}
		st.Resources[id] = &Resource{LogicalID: id, Type: def.Type, Status: "CREATE_IN_PROGRESS", Properties: pm, UpdatedAt: core.Now()}
		if !slices.Contains(st.Order, id) {
			st.Order = append(st.Order, id)
		}
		s.event(st, id, def.Type, "CREATE_IN_PROGRESS", "")
		pid, attrs, err := s.createResource(ctx, p, def.Type, pm)
		res := st.Resources[id]
		res.PhysicalID, res.Attributes, res.UpdatedAt = pid, attrs, core.Now()
		if err != nil {
			res.Status, res.Reason = "CREATE_FAILED", err.Error()
			s.event(st, id, def.Type, "CREATE_FAILED", err.Error())
			if pid == "" {
				delete(st.Resources, id)
				st.Order = slices.DeleteFunc(st.Order, func(x string) bool { return x == id })
			} else {
				created = append(created, id)
			}
			return fail(fmt.Errorf("%s: %w", id, err))
		}
		res.Status = "CREATE_COMPLETE"
		created = append(created, id)
		s.event(st, id, def.Type, "CREATE_COMPLETE", "")
	}
	st.Order = ord
	st.Outputs = map[string]any{}
	for name, o := range t.Outputs {
		v, err := r.resolve(o["Value"])
		if err != nil {
			return fail(fmt.Errorf("output %s: %w", name, err))
		}
		st.Outputs[name] = v
	}
	return nil, true
}

func normalize(v any) any {
	b, _ := json.Marshal(v)
	var out any
	_ = json.Unmarshal(b, &out)
	return out
}

// rollback deletes resources created by a failed operation; it reports whether all went.
func (s *Service) rollback(ctx context.Context, p *httpx.Principal, st *Stack, created []string) bool {
	ok := true
	for i := len(created) - 1; i >= 0; i-- {
		res := st.Resources[created[i]]
		if res == nil || res.PhysicalID == "" {
			continue
		}
		s.event(st, res.LogicalID, res.Type, "DELETE_IN_PROGRESS", "rolling back")
		if err := s.deleteResource(ctx, p, res); err != nil {
			s.event(st, res.LogicalID, res.Type, "DELETE_FAILED", err.Error())
			ok = false
			continue
		}
		delete(st.Resources, res.LogicalID)
		st.Order = slices.DeleteFunc(st.Order, func(x string) bool { return x == res.LogicalID })
		s.event(st, res.LogicalID, res.Type, "DELETE_COMPLETE", "rolled back")
	}
	return ok
}

func (s *Service) destroy(ctx context.Context, p *httpx.Principal, st *Stack) error {
	t, _ := Parse(st.Template)
	for i := len(st.Order) - 1; i >= 0; i-- {
		id := st.Order[i]
		res := st.Resources[id]
		if res == nil || res.PhysicalID == "" {
			delete(st.Resources, id)
			st.Order = slices.Delete(st.Order, i, i+1)
			continue
		}
		if t != nil && t.Resources[id].DeletionPolicy == "Retain" {
			s.event(st, id, res.Type, "DELETE_SKIPPED", "DeletionPolicy: Retain")
			delete(st.Resources, id)
			st.Order = slices.Delete(st.Order, i, i+1)
			continue
		}
		s.event(st, id, res.Type, "DELETE_IN_PROGRESS", "")
		if err := s.deleteResource(ctx, p, res); err != nil {
			s.event(st, id, res.Type, "DELETE_FAILED", err.Error())
			return fmt.Errorf("%s: %w", id, err)
		}
		delete(st.Resources, id)
		st.Order = slices.Delete(st.Order, i, i+1)
		s.event(st, id, res.Type, "DELETE_COMPLETE", "")
	}
	return nil
}

// Recover marks stacks interrupted by a restart as failed so they can be retried or deleted.
func (s *Service) Recover() {
	for _, st := range store.List[Stack](s.env.Store, cStacks) {
		if !strings.HasSuffix(st.Status, "_IN_PROGRESS") {
			continue
		}
		st.Status = strings.TrimSuffix(st.Status, "_IN_PROGRESS") + "_FAILED"
		st.StatusReason = "HomeCloud restarted while the operation was running"
		s.event(&st, st.Name, "HC::CloudFormation::Stack", st.Status, st.StatusReason)
	}
}

// view is the API representation: a deep copy with NoEcho parameter values and
// resolved resource properties (which may contain secrets) removed.
func view(st *Stack) map[string]any {
	b, _ := json.Marshal(st)
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	if t, err := Parse(st.Template); err == nil {
		if ps, ok := m["parameters"].(map[string]any); ok {
			for name, def := range t.Parameters {
				if def.NoEcho {
					if _, set := ps[name]; set {
						ps[name] = "****"
					}
				}
			}
		}
	}
	if rs, ok := m["resources"].(map[string]any); ok {
		for _, r := range rs {
			if rm, ok := r.(map[string]any); ok {
				delete(rm, "properties")
			}
		}
	}
	return m
}

// ---- routes ----

func (s *Service) Routes(r *httpx.Router) {
	res := httpx.Res("arn:hc:cloudformation:local-1:{account}:stack/{name}")
	r.Handle("GET /api/v1/cloudformation/stacks", "cloudformation:ListStacks", s.list)
	r.Handle("POST /api/v1/cloudformation/stacks", "cloudformation:CreateStack", s.create)
	r.Handle("GET /api/v1/cloudformation/stacks/{name}", "cloudformation:DescribeStacks", s.get, res)
	r.Handle("PUT /api/v1/cloudformation/stacks/{name}", "cloudformation:UpdateStack", s.update, res)
	r.Handle("DELETE /api/v1/cloudformation/stacks/{name}", "cloudformation:DeleteStack", s.delete, res)
	r.Handle("POST /api/v1/cloudformation/validate", "cloudformation:ValidateTemplate", s.validate)
	r.Handle("GET /api/v1/cloudformation/resource-types", "cloudformation:ListTypes", s.listTypes)
}

var stackRe = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9-]{0,127}$`)

func (s *Service) list(c *httpx.Ctx) (any, error) {
	out := []map[string]any{}
	for _, st := range store.List[Stack](s.env.Store, cStacks) {
		if st.Status == "DELETE_COMPLETE" {
			_ = store.Delete(s.env.Store, cStacks, st.Name) // left behind by an interrupted delete
			continue
		}
		out = append(out, map[string]any{"name": st.Name, "arn": st.ARN, "status": st.Status, "status_reason": st.StatusReason,
			"description": st.Description, "resources": len(st.Resources), "created_at": st.CreatedAt, "updated_at": st.UpdatedAt})
	}
	return out, nil
}

func (s *Service) get(c *httpx.Ctx) (any, error) {
	st, err := store.Get[Stack](s.env.Store, cStacks, c.Param("name"))
	if err != nil {
		return nil, core.Errf(http.StatusNotFound, "StackNotFound", "stack %q does not exist", c.Param("name"))
	}
	return view(&st), nil
}

func (s *Service) listTypes(c *httpx.Ctx) (any, error) {
	out := []string{}
	for t := range types {
		out = append(out, t)
	}
	slices.Sort(out)
	return out, nil
}

type stackInput struct {
	Name            string         `json:"name"`
	Template        string         `json:"template"`
	Parameters      map[string]any `json:"parameters"`
	DisableRollback bool           `json:"disable_rollback"`
}

func (s *Service) validate(c *httpx.Ctx) (any, error) {
	var in stackInput
	if err := c.Bind(&in); err != nil {
		return nil, err
	}
	t, err := Parse(in.Template)
	if err != nil {
		return map[string]any{"valid": false, "error": err.Error()}, nil
	}
	ord, err := order(t)
	if err != nil {
		return map[string]any{"valid": false, "error": err.Error()}, nil
	}
	return map[string]any{"valid": true, "description": t.Description, "parameters": t.Parameters, "creation_order": ord}, nil
}

// start records the stack in its new state and runs op in the background with
// the caller's (continuously re-checked) permissions. Only one operation runs
// per stack at a time; status checks and transitions happen under s.mu.
func (s *Service) start(st Stack, p *httpx.Principal, op func(ctx context.Context, st *Stack) (status, reason string)) map[string]any {
	s.save(&st)
	resp := view(&st)
	go func() {
		defer core.Recover("cloudformation " + st.Name)
		l := s.lock(st.Name)
		l.Lock()
		defer l.Unlock()
		ctx, cancel := context.WithTimeout(context.Background(), time.Hour)
		defer cancel()
		cur, err := store.Get[Stack](s.env.Store, cStacks, st.Name)
		if err != nil {
			return
		}
		status, reason := op(ctx, &cur)
		cur.Status, cur.StatusReason = status, reason
		if status == "DELETE_COMPLETE" {
			_ = store.Delete(s.env.Store, cStacks, cur.Name)
			return
		}
		s.event(&cur, cur.Name, "HC::CloudFormation::Stack", status, reason)
	}()
	return resp
}

func (s *Service) create(c *httpx.Ctx) (any, error) {
	var in stackInput
	if err := c.Bind(&in); err != nil {
		return nil, err
	}
	if !stackRe.MatchString(in.Name) {
		return nil, core.BadRequest("stack names start with a letter and contain letters, digits and hyphens")
	}
	t, err := Parse(in.Template)
	if err != nil {
		return nil, core.BadRequest("%v", err)
	}
	if _, err := order(t); err != nil {
		return nil, core.BadRequest("%v", err)
	}
	ps, err := params(t, in.Parameters)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if old, err := store.Get[Stack](s.env.Store, cStacks, in.Name); err == nil && old.Status != "DELETE_COMPLETE" {
		return nil, core.Errf(http.StatusConflict, "AlreadyExistsException", "stack %q already exists", in.Name)
	}
	st := Stack{Name: in.Name, ARN: s.env.ARN("cloudformation", "stack/"+in.Name), Status: "CREATE_IN_PROGRESS", Description: t.Description,
		Template: in.Template, Parameters: ps, Resources: map[string]*Resource{}, Order: []string{}, Outputs: map[string]any{},
		CreatedAt: core.Now(), Events: []Event{{Time: core.Now(), LogicalID: in.Name, Type: "HC::CloudFormation::Stack", Status: "CREATE_IN_PROGRESS", Reason: "user initiated"}}}
	p := c.P
	return s.start(st, p, func(ctx context.Context, cur *Stack) (string, string) {
		err, rolledBack := s.deploy(ctx, p, cur, t, nil, !in.DisableRollback)
		switch {
		case err == nil:
			return "CREATE_COMPLETE", ""
		case in.DisableRollback:
			return "CREATE_FAILED", err.Error()
		case !rolledBack:
			return "ROLLBACK_FAILED", err.Error()
		}
		return "ROLLBACK_COMPLETE", err.Error()
	}), nil
}

func (s *Service) update(c *httpx.Ctx) (any, error) {
	var in stackInput
	if err := c.Bind(&in); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	st, err := store.Get[Stack](s.env.Store, cStacks, c.Param("name"))
	if err != nil {
		return nil, core.Errf(http.StatusNotFound, "StackNotFound", "stack %q does not exist", c.Param("name"))
	}
	switch st.Status {
	case "CREATE_COMPLETE", "UPDATE_COMPLETE", "UPDATE_FAILED":
	default:
		return nil, core.Errf(http.StatusConflict, "ValidationError", "a stack in %s cannot be updated", st.Status)
	}
	if in.Template == "" {
		in.Template = st.Template
	}
	t, err := Parse(in.Template)
	if err != nil {
		return nil, core.BadRequest("%v", err)
	}
	if _, err := order(t); err != nil {
		return nil, core.BadRequest("%v", err)
	}
	prev, _ := Parse(st.Template)
	merged := map[string]any{}
	for k, v := range st.Parameters {
		if _, ok := t.Parameters[k]; ok {
			merged[k] = v
		}
	}
	for k, v := range in.Parameters {
		merged[k] = v
	}
	ps, err := params(t, merged)
	if err != nil {
		return nil, err
	}
	st.Template, st.Parameters, st.Description, st.Status = in.Template, ps, t.Description, "UPDATE_IN_PROGRESS"
	st.Events = append([]Event{{Time: core.Now(), LogicalID: st.Name, Type: "HC::CloudFormation::Stack", Status: "UPDATE_IN_PROGRESS", Reason: "user initiated"}}, st.Events...)
	p := c.P
	return s.start(st, p, func(ctx context.Context, cur *Stack) (string, string) {
		if err, _ := s.deploy(ctx, p, cur, t, prev, false); err != nil {
			return "UPDATE_FAILED", err.Error()
		}
		return "UPDATE_COMPLETE", ""
	}), nil
}

func (s *Service) delete(c *httpx.Ctx) (any, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st, err := store.Get[Stack](s.env.Store, cStacks, c.Param("name"))
	if err != nil {
		return nil, core.Errf(http.StatusNotFound, "StackNotFound", "stack %q does not exist", c.Param("name"))
	}
	if strings.HasSuffix(st.Status, "_IN_PROGRESS") {
		return nil, core.Errf(http.StatusConflict, "StackBusy", "stack is %s", st.Status)
	}
	st.Status = "DELETE_IN_PROGRESS"
	st.Events = append([]Event{{Time: core.Now(), LogicalID: st.Name, Type: "HC::CloudFormation::Stack", Status: "DELETE_IN_PROGRESS", Reason: "user initiated"}}, st.Events...)
	p := c.P
	return s.start(st, p, func(ctx context.Context, cur *Stack) (string, string) {
		if err := s.destroy(ctx, p, cur); err != nil {
			return "DELETE_FAILED", err.Error()
		}
		return "DELETE_COMPLETE", ""
	}), nil
}
