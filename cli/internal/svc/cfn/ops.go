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

// deploy brings the stack's resources in line with template t (create or update).
func (s *Service) deploy(ctx context.Context, p *httpx.Principal, st *Stack, t *Template, rollback bool) error {
	ord, err := order(t)
	if err != nil {
		return err
	}
	r := &resolver{stack: st, params: st.Parameters}
	var created []string
	ids := map[string]bool{}
	for id := range t.Resources {
		ids[id] = true
	}
	// Resources that were (re)created in this deployment; anything depending on
	// them is replaced as well, since deleting a resource can cascade (e.g. a
	// function's event source mappings go with it).
	fresh := map[string]bool{}
	// Remove resources that are no longer in the template (reverse order).
	for i := len(st.Order) - 1; i >= 0; i-- {
		id := st.Order[i]
		if _, keep := t.Resources[id]; keep {
			continue
		}
		res := st.Resources[id]
		s.event(st, id, res.Type, "DELETE_IN_PROGRESS", "removed from the template")
		if err := s.deleteResource(ctx, p, res); err != nil {
			s.event(st, id, res.Type, "DELETE_FAILED", err.Error())
			return fmt.Errorf("%s: %w", id, err)
		}
		delete(st.Resources, id)
		s.event(st, id, res.Type, "DELETE_COMPLETE", "")
	}
	for _, id := range ord {
		def := t.Resources[id]
		props, err := r.resolve(def.Properties)
		if err != nil {
			s.event(st, id, def.Type, "CREATE_FAILED", err.Error())
			return fmt.Errorf("%s: %w", id, err)
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
				return fmt.Errorf("%s: %w", id, err)
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
			if rollback {
				s.rollback(ctx, p, st, created)
			}
			return fmt.Errorf("%s: %w", id, err)
		}
		res.Status = "CREATE_COMPLETE"
		created = append(created, id)
		s.event(st, id, def.Type, "CREATE_COMPLETE", "")
	}
	// Order resources as the template dictates, then compute outputs.
	st.Order = ord
	st.Outputs = map[string]any{}
	for name, o := range t.Outputs {
		v, err := r.resolve(o["Value"])
		if err != nil {
			return fmt.Errorf("output %s: %w", name, err)
		}
		st.Outputs[name] = v
	}
	return nil
}

func normalize(v any) any {
	b, _ := json.Marshal(v)
	var out any
	_ = json.Unmarshal(b, &out)
	return out
}

func (s *Service) rollback(ctx context.Context, p *httpx.Principal, st *Stack, created []string) {
	for i := len(created) - 1; i >= 0; i-- {
		res := st.Resources[created[i]]
		if res == nil || res.PhysicalID == "" {
			continue
		}
		s.event(st, res.LogicalID, res.Type, "DELETE_IN_PROGRESS", "rolling back")
		if err := s.deleteResource(ctx, p, res); err != nil {
			s.event(st, res.LogicalID, res.Type, "DELETE_FAILED", err.Error())
			continue
		}
		delete(st.Resources, res.LogicalID)
		st.Order = slices.DeleteFunc(st.Order, func(x string) bool { return x == res.LogicalID })
		s.event(st, res.LogicalID, res.Type, "DELETE_COMPLETE", "rolled back")
	}
}

func (s *Service) destroy(ctx context.Context, p *httpx.Principal, st *Stack) error {
	for i := len(st.Order) - 1; i >= 0; i-- {
		id := st.Order[i]
		res := st.Resources[id]
		if res == nil {
			continue
		}
		if res.PhysicalID == "" {
			delete(st.Resources, id)
			continue
		}
		var def ResourceDef
		if t, err := Parse(st.Template); err == nil {
			def = t.Resources[id]
		}
		if def.DeletionPolicy == "Retain" {
			s.event(st, id, res.Type, "DELETE_SKIPPED", "DeletionPolicy: Retain")
			delete(st.Resources, id)
			continue
		}
		s.event(st, id, res.Type, "DELETE_IN_PROGRESS", "")
		if err := s.deleteResource(ctx, p, res); err != nil {
			s.event(st, id, res.Type, "DELETE_FAILED", err.Error())
			return fmt.Errorf("%s: %w", id, err)
		}
		delete(st.Resources, id)
		s.event(st, id, res.Type, "DELETE_COMPLETE", "")
	}
	return nil
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
	return st, nil
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

// run executes a stack operation in the background with the caller's permissions.
func (s *Service) run(st *Stack, p *httpx.Principal, op string, fn func(ctx context.Context) error, done func(err error)) {
	go func() {
		l := s.lock(st.Name)
		l.Lock()
		defer l.Unlock()
		ctx, cancel := context.WithTimeout(context.Background(), time.Hour)
		defer cancel()
		err := fn(ctx)
		done(err)
		if st.Status != "DELETE_COMPLETE" {
			s.save(st)
		}
		if err != nil {
			log.Printf("cfn: %s %s: %v", op, st.Name, err)
		}
	}()
}

func (s *Service) create(c *httpx.Ctx) (any, error) {
	var in stackInput
	if err := c.Bind(&in); err != nil {
		return nil, err
	}
	if !stackRe.MatchString(in.Name) {
		return nil, core.BadRequest("stack names start with a letter and contain letters, digits and hyphens")
	}
	if old, err := store.Get[Stack](s.env.Store, cStacks, in.Name); err == nil && old.Status != "DELETE_COMPLETE" {
		return nil, core.Errf(http.StatusConflict, "AlreadyExistsException", "stack %q already exists", in.Name)
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
	st := &Stack{Name: in.Name, ARN: s.env.ARN("cloudformation", "stack/"+in.Name), Status: "CREATE_IN_PROGRESS", Description: t.Description,
		Template: in.Template, Parameters: ps, Resources: map[string]*Resource{}, Order: []string{}, Outputs: map[string]any{},
		CreatedAt: core.Now(), Events: []Event{}}
	s.event(st, in.Name, "HC::CloudFormation::Stack", "CREATE_IN_PROGRESS", "user initiated")
	p := c.P
	s.run(st, p, "create", func(ctx context.Context) error { return s.deploy(ctx, p, st, t, !in.DisableRollback) }, func(err error) {
		switch {
		case err == nil:
			st.Status, st.StatusReason = "CREATE_COMPLETE", ""
		case in.DisableRollback:
			st.Status, st.StatusReason = "CREATE_FAILED", err.Error()
		default:
			st.Status, st.StatusReason = "ROLLBACK_COMPLETE", err.Error()
		}
		s.event(st, st.Name, "HC::CloudFormation::Stack", st.Status, st.StatusReason)
	})
	return st, nil
}

func (s *Service) update(c *httpx.Ctx) (any, error) {
	var in stackInput
	if err := c.Bind(&in); err != nil {
		return nil, err
	}
	st, err := store.Get[Stack](s.env.Store, cStacks, c.Param("name"))
	if err != nil {
		return nil, core.Errf(http.StatusNotFound, "StackNotFound", "stack %q does not exist", c.Param("name"))
	}
	if strings.HasSuffix(st.Status, "_IN_PROGRESS") {
		return nil, core.Errf(http.StatusConflict, "StackBusy", "stack is %s", st.Status)
	}
	if st.Status == "ROLLBACK_COMPLETE" {
		return nil, core.Errf(http.StatusConflict, "ValidationError", "a stack in ROLLBACK_COMPLETE can only be deleted")
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
	cur := st
	cur.Template, cur.Parameters, cur.Description, cur.Status = in.Template, ps, t.Description, "UPDATE_IN_PROGRESS"
	s.event(&cur, cur.Name, "HC::CloudFormation::Stack", "UPDATE_IN_PROGRESS", "user initiated")
	p := c.P
	s.run(&cur, p, "update", func(ctx context.Context) error { return s.deploy(ctx, p, &cur, t, false) }, func(err error) {
		if err != nil {
			cur.Status, cur.StatusReason = "UPDATE_FAILED", err.Error()
		} else {
			cur.Status, cur.StatusReason = "UPDATE_COMPLETE", ""
		}
		s.event(&cur, cur.Name, "HC::CloudFormation::Stack", cur.Status, cur.StatusReason)
	})
	return cur, nil
}

func (s *Service) delete(c *httpx.Ctx) (any, error) {
	st, err := store.Get[Stack](s.env.Store, cStacks, c.Param("name"))
	if err != nil {
		return nil, core.Errf(http.StatusNotFound, "StackNotFound", "stack %q does not exist", c.Param("name"))
	}
	if strings.HasSuffix(st.Status, "_IN_PROGRESS") && st.Status != "DELETE_IN_PROGRESS" {
		return nil, core.Errf(http.StatusConflict, "StackBusy", "stack is %s", st.Status)
	}
	cur := st
	cur.Status = "DELETE_IN_PROGRESS"
	s.event(&cur, cur.Name, "HC::CloudFormation::Stack", "DELETE_IN_PROGRESS", "user initiated")
	p := c.P
	s.run(&cur, p, "delete", func(ctx context.Context) error { return s.destroy(ctx, p, &cur) }, func(err error) {
		if err != nil {
			cur.Status, cur.StatusReason = "DELETE_FAILED", err.Error()
			s.event(&cur, cur.Name, "HC::CloudFormation::Stack", cur.Status, cur.StatusReason)
			return
		}
		cur.Status = "DELETE_COMPLETE"
		_ = store.Delete(s.env.Store, cStacks, cur.Name)
	})
	return cur, nil
}
