package cfn

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"sort"
	"strings"

	"github.com/homecloudhq/homecloud/cli/internal/core"
	"github.com/homecloudhq/homecloud/cli/internal/httpx"
)

// Stack updates work like CloudFormation's:
//
//   - resources that changed only in properties the type can modify are updated
//     in place; others are replaced by creating the new resource first;
//   - resources that the template dropped, and the old resources of replaced
//     ones, are deleted only after the whole update succeeded
//     (UPDATE_COMPLETE_CLEANUP_IN_PROGRESS);
//   - a failure rolls the stack back to the previous template and parameters
//     (UPDATE_ROLLBACK_IN_PROGRESS): modified resources get their old
//     properties again, resources the update created are deleted. A rollback
//     that cannot finish ends in UPDATE_ROLLBACK_FAILED and continues with
//     ContinueUpdateRollback.

// Step is one change an update made to a resource; a rollback undoes them in reverse.
type Step struct {
	ID       string         `json:"id"`
	Kind     string         `json:"kind"`            // add | modify | replace
	Tried    map[string]any `json:"tried,omitempty"` // modify: the properties the update applied
	New      *Resource      `json:"new,omitempty"`   // replace: the resource that replaced the old one
	Restored bool           `json:"restored,omitempty"`
	Cleaned  bool           `json:"cleaned,omitempty"`
}

// UpdateState is what a rollback needs: the stack as it was before the update
// and the changes made so far.
type UpdateState struct {
	Template         string                `json:"template"`
	Parameters       map[string]any        `json:"parameters"`
	Description      string                `json:"description,omitempty"`
	Tags             core.Tags             `json:"tags,omitempty"`
	Capabilities     []string              `json:"capabilities,omitempty"`
	NotificationARNs []string              `json:"notification_arns,omitempty"`
	RoleARN          string                `json:"role_arn,omitempty"`
	Outputs          map[string]any        `json:"outputs,omitempty"`
	OutputMeta       map[string]OutputMeta `json:"output_meta,omitempty"`
	Imports          []string              `json:"imports,omitempty"`
	Order            []string              `json:"order"`
	Resources        map[string]*Resource  `json:"resources"`
	Steps            []Step                `json:"steps,omitempty"`
	// Leftovers are old resources of replacements whose update failed without
	// a rollback; the next successful update or the deletion of the stack removes them.
	Leftovers []*Resource `json:"leftovers,omitempty"`
}

// keepLeftovers is what remains of an update state once the update is over.
func keepLeftovers(u *UpdateState, st *Stack, withReplaced bool) *UpdateState {
	if u == nil {
		return nil
	}
	left := u.Leftovers
	if withReplaced {
		for _, step := range u.Steps {
			old, cur := u.Resources[step.ID], st.Resources[step.ID]
			if step.Kind == "replace" && old != nil && old.native() != "" && (cur == nil || cur.native() != old.native()) {
				left = append(left, old)
			}
		}
	}
	if len(left) == 0 {
		return nil
	}
	return &UpdateState{Leftovers: left}
}

// strays lists resources a stack still owns that are not part of it: leftovers
// of failed updates and the replacements a rollback did not remove yet.
func strays(st *Stack) []*Resource {
	u := st.Rollback
	if u == nil {
		return nil
	}
	out := slices.Clone(u.Leftovers)
	for _, step := range u.Steps {
		cur := st.Resources[step.ID]
		if step.Kind == "replace" && !step.Cleaned && step.New != nil && step.New.native() != "" && (cur == nil || cur.native() != step.New.native()) {
			out = append(out, step.New)
		}
	}
	return out
}

// removeLeftovers deletes the old resources earlier failed updates left behind.
func (s *Service) removeLeftovers(ctx context.Context, p *httpx.Principal, st *Stack, left []*Resource) []*Resource {
	var kept []*Resource
	for _, old := range left {
		s.event(st, old.LogicalID, old.Type, "DELETE_IN_PROGRESS", "", old.PhysicalID)
		if err := s.deleteResource(ctx, p, old); err != nil {
			s.event(st, old.LogicalID, old.Type, "DELETE_FAILED", err.Error(), old.PhysicalID)
			kept = append(kept, old)
			continue
		}
		s.event(st, old.LogicalID, old.Type, "DELETE_COMPLETE", "", old.PhysicalID)
	}
	return kept
}

func clone[T any](v T) T {
	var out T
	b, _ := json.Marshal(v)
	_ = json.Unmarshal(b, &out)
	return out
}

// snapshot records the stack before an update changes it.
func snapshot(st *Stack) *UpdateState {
	return &UpdateState{Template: st.Template, Parameters: clone(st.Parameters), Description: st.Description, Tags: clone(st.Tags),
		Capabilities: slices.Clone(st.Capabilities), NotificationARNs: slices.Clone(st.NotificationARNs), RoleARN: st.RoleARN,
		Outputs: clone(st.Outputs), OutputMeta: clone(st.OutputMeta), Imports: slices.Clone(st.Imports), Order: slices.Clone(st.Order),
		Resources: clone(st.Resources)}
}

// ---- what a change needs ----

const (
	repFalse       = "False"
	repTrue        = "True"
	repConditional = "Conditional"
)

// changedKeys lists the properties whose value differs.
func changedKeys(old, in map[string]any) []string {
	var out []string
	seen := map[string]bool{}
	for _, m := range []map[string]any{old, in} {
		for k := range m {
			if seen[k] {
				continue
			}
			seen[k] = true
			if !reflect.DeepEqual(normalize(old[k]), normalize(in[k])) {
				out = append(out, k)
			}
		}
	}
	sort.Strings(out)
	return out
}

// keyReplacement says what changing one property of a resource needs.
func keyReplacement(oldType, newType, key string) string {
	if oldType != newType {
		return repTrue
	}
	a, ok := awsTypes[newType]
	if !ok || a.Update == nil {
		return repTrue
	}
	switch {
	case slices.Contains(a.Mutable, key):
		return repFalse
	case slices.Contains(a.Conditional, key):
		return repConditional
	}
	return repTrue
}

func worse(a, b string) string {
	rank := map[string]int{"": 0, repFalse: 1, repConditional: 2, repTrue: 3}
	if rank[b] > rank[a] {
		return b
	}
	return a
}

// replacement reports whether changing the properties replaces the resource.
func replacement(oldType, newType string, changed []string) string {
	if oldType != newType {
		return repTrue
	}
	out := repFalse
	if a, ok := awsTypes[newType]; !ok || a.Update == nil {
		out = repTrue
	}
	for _, k := range changed {
		out = worse(out, keyReplacement(oldType, newType, k))
	}
	return out
}

// nameProp is the template property that names a resource of the type.
func nameProp(typ string) string {
	if a, ok := awsTypes[typ]; ok {
		return a.Name
	}
	if spec, ok := types[typ]; ok && (spec.IDField == "name" || spec.IDField == "@name") {
		return "name"
	}
	return ""
}

func customNameError(name string) string {
	return "CloudFormation cannot update a stack when a custom-named resource requires replacing. Rename " + name + " and update the stack again."
}

// collides reports that replacing the resource would need a second one with the same custom name.
func collides(typ string, old *Resource, in map[string]any) bool {
	p := nameProp(typ)
	return p != "" && has(in, p) && toStr(old.Properties[p]) == toStr(in[p])
}

// ---- running an update ----

func (s *Service) newResolver(st *Stack, full *Template) *resolver {
	r := &resolver{stack: st, params: st.Parameters, tmpl: full}
	r.imports = func(name string) (any, error) {
		v, err := s.importValue(name)
		if err == nil && !slices.Contains(st.Imports, name) {
			st.Imports = append(st.Imports, name)
		}
		return v, err
	}
	return r
}

func (s *Service) xctxFor(ctx context.Context, p *httpx.Principal, st *Stack, id string) *xctx {
	return &xctx{s: s, ctx: ctx, p: p, Stack: st.Name, Logical: id, Account: accountOf(st.ARN), Endpoint: st.Endpoint}
}

// build creates a resource; the result is set even when the creation failed
// (with a native ID if something was made that has to be removed again).
func (s *Service) build(x *xctx, def ResourceDef, pm map[string]any) (*Resource, error) {
	res := &Resource{LogicalID: x.Logical, Type: def.Type, Status: "CREATE_IN_PROGRESS", Properties: pm, UpdatedAt: core.Now()}
	c, err := s.createResource(x, def.Type, pm)
	res.HCType, res.NativeID, res.Attributes, res.UpdatedAt = c.HC, c.Native, c.Attrs, core.Now()
	res.PhysicalID = physicalID(x, def.Type, c, pm)
	if err != nil {
		res.Status, res.Reason = "CREATE_FAILED", err.Error()
	}
	return res, err
}

// resolveOutputs evaluates the template's outputs into the stack.
func (s *Service) resolveOutputs(r *resolver, t *Template, st *Stack) error {
	st.Outputs, st.OutputMeta = map[string]any{}, map[string]OutputMeta{}
	for name, o := range t.Outputs {
		v, err := r.resolve(o["Value"])
		if err != nil {
			return fmt.Errorf("output %s: %w", name, err)
		}
		st.Outputs[name] = v
		meta := OutputMeta{}
		meta.Description, _ = o["Description"].(string)
		if ex, ok := o["Export"].(map[string]any); ok {
			n, err := r.resolve(ex["Name"])
			if err != nil {
				return fmt.Errorf("output %s export: %w", name, err)
			}
			meta.Export = toStr(n)
			for _, e := range s.exports() {
				if e.Name == meta.Export && e.StackName != st.Name {
					return fmt.Errorf("Export with name %s is already exported by stack %s", meta.Export, e.StackName)
				}
			}
		}
		st.OutputMeta[name] = meta
	}
	return nil
}

func upErr(id string, err error, verb string) error { return &resErr{id: id, err: err, verb: verb} }

// runUpdate applies template full to the stack (st.Rollback holds the way back)
// and returns the stack's final status.
func (s *Service) runUpdate(ctx context.Context, p *httpx.Principal, st *Stack, full, prev *Template, disable bool) (string, string) {
	u := st.Rollback
	fail := func(err error) (string, string) { return s.failUpdate(ctx, p, st, err, disable) }
	t, err := s.prune(full, st)
	if err != nil {
		return fail(err)
	}
	ord, err := order(t)
	if err != nil {
		return fail(err)
	}
	r := s.newResolver(st, full)
	for _, id := range ord {
		def := t.Resources[id]
		old := st.Resources[id]
		props, err := r.resolve(def.Properties)
		if err != nil {
			status, verb := "CREATE_FAILED", "create"
			if old != nil {
				status, verb = "UPDATE_FAILED", "update"
			}
			s.event(st, id, def.Type, status, err.Error(), "")
			return fail(upErr(id, err, verb))
		}
		pm, _ := props.(map[string]any)
		if pm == nil {
			pm = map[string]any{}
		}
		x := s.xctxFor(ctx, p, st, id)
		if old != nil && old.Status == "CREATE_FAILED" && old.native() != "" {
			// left behind by an earlier update that kept its resources
			s.event(st, id, old.Type, "DELETE_IN_PROGRESS", "")
			if err := s.deleteResource(ctx, p, old); err != nil {
				s.event(st, id, old.Type, "DELETE_FAILED", err.Error())
				return fail(upErr(id, err, "delete"))
			}
			old = nil
		}
		exists := old != nil && old.native() != ""
		var kind string
		switch {
		case !exists:
			kind = "add"
		case old.Type == def.Type && reflect.DeepEqual(normalize(old.Properties), normalize(pm)):
			continue // unchanged
		case replacement(old.Type, def.Type, changedKeys(old.Properties, pm)) == repFalse:
			kind = "modify"
		default:
			kind = "replace"
		}
		switch kind {
		case "add":
			st.Resources[id] = &Resource{LogicalID: id, Type: def.Type, Status: "CREATE_IN_PROGRESS", Properties: pm, UpdatedAt: core.Now()}
			if !slices.Contains(st.Order, id) {
				st.Order = append(st.Order, id)
			}
			s.event(st, id, def.Type, "CREATE_IN_PROGRESS", "", "")
			res, err := s.build(x, def, pm)
			if err != nil {
				s.event(st, id, def.Type, "CREATE_FAILED", err.Error(), res.PhysicalID)
				if res.NativeID == "" && res.PhysicalID == "" {
					delete(st.Resources, id)
					st.Order = slices.DeleteFunc(st.Order, func(x string) bool { return x == id })
				} else {
					st.Resources[id] = res
					u.Steps = append(u.Steps, Step{ID: id, Kind: "add"})
				}
				return fail(upErr(id, err, "create"))
			}
			res.Status = "CREATE_COMPLETE"
			st.Resources[id] = res
			u.Steps = append(u.Steps, Step{ID: id, Kind: "add"})
			s.event(st, id, def.Type, "CREATE_COMPLETE", "")
		case "modify":
			u.Steps = append(u.Steps, Step{ID: id, Kind: "modify", Tried: pm})
			old.Status = "UPDATE_IN_PROGRESS"
			s.event(st, id, def.Type, "UPDATE_IN_PROGRESS", "")
			attrs, err := s.updateResource(x, old, old.Properties, pm)
			if err != nil {
				old.Status, old.Reason = "UPDATE_FAILED", err.Error()
				s.event(st, id, def.Type, "UPDATE_FAILED", err.Error())
				return fail(upErr(id, err, "update"))
			}
			old.Properties, old.Attributes = pm, merge(old.Attributes, attrs)
			old.Status, old.Reason, old.UpdatedAt = "UPDATE_COMPLETE", "", core.Now()
			s.event(st, id, def.Type, "UPDATE_COMPLETE", "")
		case "replace":
			if collides(def.Type, old, pm) {
				msg := customNameError(toStr(old.Properties[nameProp(def.Type)]))
				old.Status, old.Reason = "UPDATE_FAILED", msg
				s.event(st, id, old.Type, "UPDATE_FAILED", msg)
				return fail(upErr(id, errors.New(msg), "update"))
			}
			s.event(st, id, old.Type, "UPDATE_IN_PROGRESS", "Requested update requires the creation of a new physical resource; hence creating one.")
			res, err := s.build(x, def, pm)
			if err != nil {
				msg := err.Error()
				if strings.Contains(strings.ToLower(msg), "already exist") {
					msg = customNameError(firstNonEmpty(toStr(old.Properties[nameProp(def.Type)]), old.PhysicalID))
					err = errors.New(msg)
				}
				res.Reason = msg
				s.event(st, id, def.Type, "UPDATE_FAILED", msg, res.PhysicalID)
				if res.NativeID != "" || res.PhysicalID != "" {
					res.Status = "UPDATE_FAILED"
					st.Resources[id] = res
					u.Steps = append(u.Steps, Step{ID: id, Kind: "replace", New: res})
				} else {
					old.Status, old.Reason = "UPDATE_FAILED", msg
				}
				return fail(upErr(id, err, "update"))
			}
			res.Status = "UPDATE_COMPLETE"
			st.Resources[id] = res
			u.Steps = append(u.Steps, Step{ID: id, Kind: "replace", New: res})
			s.event(st, id, def.Type, "UPDATE_COMPLETE", "")
		}
	}
	if err := s.resolveOutputs(r, t, st); err != nil {
		return fail(err)
	}
	return s.cleanupUpdate(ctx, p, st, t, prev, ord)
}

// cleanupUpdate deletes what the update made obsolete: resources the template
// dropped and the old resources of replaced ones.
func (s *Service) cleanupUpdate(ctx context.Context, p *httpx.Principal, st *Stack, t, prev *Template, ord []string) (string, string) {
	u := st.Rollback
	s.stackEvent(st, "UPDATE_COMPLETE_CLEANUP_IN_PROGRESS", "")
	replaced := map[string]*Step{}
	for i := range u.Steps {
		if u.Steps[i].Kind == "replace" {
			replaced[u.Steps[i].ID] = &u.Steps[i]
		}
	}
	leftover := []string{}
	for i := len(u.Order) - 1; i >= 0; i-- {
		id := u.Order[i]
		if _, keep := t.Resources[id]; keep {
			step := replaced[id]
			if step == nil {
				continue
			}
			old, cur := u.Resources[id], st.Resources[id]
			switch {
			case old == nil || old.native() == "":
			case cur != nil && cur.native() == old.native() && cur.Type == old.Type:
				// the replacement took over the same physical resource: nothing to remove
			case t.Resources[id].UpdateReplacePolicy == "Retain":
				s.event(st, id, old.Type, "DELETE_SKIPPED", "UpdateReplacePolicy: Retain", old.PhysicalID)
			default:
				s.event(st, id, old.Type, "DELETE_IN_PROGRESS", "", old.PhysicalID)
				if err := s.deleteResource(ctx, p, old); err != nil {
					s.event(st, id, old.Type, "DELETE_FAILED", err.Error(), old.PhysicalID)
				} else {
					s.event(st, id, old.Type, "DELETE_COMPLETE", "", old.PhysicalID)
				}
			}
			continue
		}
		res := st.Resources[id]
		if res == nil {
			continue
		}
		if prev != nil && prev.Resources[id].DeletionPolicy == "Retain" {
			s.event(st, id, res.Type, "DELETE_SKIPPED", "DeletionPolicy: Retain")
			delete(st.Resources, id)
			continue
		}
		s.event(st, id, res.Type, "DELETE_IN_PROGRESS", "")
		pid := res.PhysicalID
		if err := s.deleteResource(ctx, p, res); err != nil {
			// The update itself succeeded; the resource stays in the stack for the next update or delete.
			res.Status, res.Reason = "DELETE_FAILED", err.Error()
			s.event(st, id, res.Type, "DELETE_FAILED", err.Error())
			leftover = append(leftover, id)
			continue
		}
		delete(st.Resources, id)
		s.event(st, id, res.Type, "DELETE_COMPLETE", "", pid)
	}
	st.Order = append(slices.Clone(ord), leftover...)
	st.Rollback = keepLeftovers(&UpdateState{Leftovers: s.removeLeftovers(ctx, p, st, u.Leftovers)}, st, false)
	return "UPDATE_COMPLETE", ""
}

// failUpdate ends a failed update: it rolls back unless the request disabled that.
func (s *Service) failUpdate(ctx context.Context, p *httpx.Principal, st *Stack, err error, disable bool) (string, string) {
	reason := failedReason(err, "update")
	if disable {
		st.Rollback = keepLeftovers(st.Rollback, st, true)
		return "UPDATE_FAILED", reason
	}
	s.stackEvent(st, "UPDATE_ROLLBACK_IN_PROGRESS", reason)
	return s.rollbackUpdate(ctx, p, st, nil)
}

// rollbackUpdate returns the stack to what it was before the update. skip names
// resources whose rollback is given up (ContinueUpdateRollback's ResourcesToSkip).
func (s *Service) rollbackUpdate(ctx context.Context, p *httpx.Principal, st *Stack, skip []string) (string, string) {
	u := st.Rollback
	if u == nil {
		return "UPDATE_ROLLBACK_COMPLETE", ""
	}
	st.Template, st.Parameters, st.Description = u.Template, clone(u.Parameters), u.Description
	st.Tags, st.Capabilities, st.NotificationARNs, st.RoleARN = clone(u.Tags), slices.Clone(u.Capabilities), slices.Clone(u.NotificationARNs), u.RoleARN
	st.Outputs, st.OutputMeta, st.Imports = clone(u.Outputs), clone(u.OutputMeta), slices.Clone(u.Imports)
	var failedUpdate, failedDelete []string
	// Give modified resources their old properties back, newest change first.
	for i := len(u.Steps) - 1; i >= 0; i-- {
		step := &u.Steps[i]
		if step.Restored || step.Kind == "add" {
			continue
		}
		id := step.ID
		old, cur := clone(u.Resources[id]), st.Resources[id]
		typ := old.Type
		if slices.Contains(skip, id) {
			step.Restored = true
			s.event(st, id, typ, "UPDATE_COMPLETE", "Rollback of this resource was skipped by request")
			continue
		}
		if step.Kind == "modify" && cur != nil {
			cur.Status = "UPDATE_IN_PROGRESS"
			s.event(st, id, typ, "UPDATE_IN_PROGRESS", "")
			x := s.xctxFor(ctx, p, st, id)
			attrs, err := s.updateResource(x, cur, step.Tried, old.Properties)
			if err != nil {
				cur.Status, cur.Reason = "UPDATE_FAILED", err.Error()
				s.event(st, id, typ, "UPDATE_FAILED", err.Error())
				failedUpdate = append(failedUpdate, id)
				continue
			}
			old.Attributes = merge(old.Attributes, attrs)
		}
		old.Status, old.Reason, old.UpdatedAt = "UPDATE_COMPLETE", "", core.Now()
		st.Resources[id] = old
		step.Restored = true
		s.event(st, id, typ, "UPDATE_COMPLETE", "")
	}
	if len(failedUpdate) > 0 {
		return s.rollbackFailed(st, failedUpdate, "update")
	}
	// Remove what the update created, newest first.
	s.stackEvent(st, "UPDATE_ROLLBACK_COMPLETE_CLEANUP_IN_PROGRESS", "")
	for i := len(u.Steps) - 1; i >= 0; i-- {
		step := &u.Steps[i]
		if step.Cleaned || step.Kind == "modify" {
			continue
		}
		id := step.ID
		var res *Resource
		if step.Kind == "add" {
			res = st.Resources[id]
		} else {
			res = step.New
		}
		if res == nil || res.native() == "" || slices.Contains(skip, id) {
			if step.Kind == "add" {
				delete(st.Resources, id)
			}
			step.Cleaned = true
			continue
		}
		if old := u.Resources[id]; step.Kind == "replace" && old != nil && old.native() == res.native() {
			step.Cleaned = true // the replacement is the old resource
			continue
		}
		s.event(st, id, res.Type, "DELETE_IN_PROGRESS", "", res.PhysicalID)
		if err := s.deleteResource(ctx, p, res); err != nil {
			res.Status, res.Reason = "DELETE_FAILED", err.Error()
			s.event(st, id, res.Type, "DELETE_FAILED", err.Error(), res.PhysicalID)
			failedDelete = append(failedDelete, id)
			continue
		}
		if step.Kind == "add" {
			delete(st.Resources, id)
		}
		step.Cleaned = true
		s.event(st, id, res.Type, "DELETE_COMPLETE", "", res.PhysicalID)
	}
	if len(failedDelete) > 0 {
		return s.rollbackFailed(st, failedDelete, "delete")
	}
	for id, res := range st.Resources {
		// a resource whose update failed before it changed anything is as it was
		if o := u.Resources[id]; o != nil && res.native() == o.native() && strings.HasSuffix(res.Status, "_FAILED") {
			res.Status, res.Reason = o.Status, ""
		}
	}
	st.Order = slices.Clone(u.Order)
	st.Rollback = keepLeftovers(u, st, false)
	return "UPDATE_ROLLBACK_COMPLETE", ""
}

func (s *Service) rollbackFailed(st *Stack, ids []string, verb string) (string, string) {
	sort.Strings(ids)
	return "UPDATE_ROLLBACK_FAILED", fmt.Sprintf("The following resource(s) failed to %s: [%s]. ", verb, strings.Join(ids, ", "))
}

// ContinueUpdateRollback resumes a rollback that ended in UPDATE_ROLLBACK_FAILED.
func (s *Service) ContinueUpdateRollback(p *httpx.Principal, ref, roleARN string, skip []string) (Stack, error) {
	if err := s.checkRole(roleARN); err != nil {
		return Stack{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	st, err := s.find(ref)
	if err != nil {
		return Stack{}, err
	}
	if st.Status != "UPDATE_ROLLBACK_FAILED" {
		return Stack{}, validation("Stack:%s is in %s state and can not be continued.", st.ARN, st.Status)
	}
	if roleARN != "" {
		st.RoleARN = roleARN
	}
	st.Status, st.StatusReason = "UPDATE_ROLLBACK_IN_PROGRESS", ""
	st.Events = append([]Event{{ID: newUUID(), Time: core.Now(), LogicalID: st.Name, PhysicalID: st.ARN, Type: stackType, Status: "UPDATE_ROLLBACK_IN_PROGRESS", Reason: "User Initiated"}}, st.Events...)
	return s.start(st, p, func(ctx context.Context, cur *Stack) (string, string) {
		return s.rollbackUpdate(ctx, p, cur, skip)
	}, nil), nil
}
