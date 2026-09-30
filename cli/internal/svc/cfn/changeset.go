package cfn

import (
	"context"
	"net/http"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/homecloudhq/homecloud/cli/internal/core"
	"github.com/homecloudhq/homecloud/cli/internal/httpx"
	"github.com/homecloudhq/homecloud/cli/internal/store"
)

const cChangeSets = "cfn_changesets"

// Change is one resource change of a change set.
type Change struct {
	Action      string `json:"action"` // Add | Modify | Remove
	LogicalID   string `json:"logical_id"`
	PhysicalID  string `json:"physical_id,omitempty"`
	Type        string `json:"type"`
	Replacement string `json:"replacement,omitempty"` // True | False | Conditional
	// Details lists the changed properties and whether each recreates the resource.
	Details []ChangeDetail `json:"details,omitempty"`
}

// ChangeDetail is one changed property of a resource.
type ChangeDetail struct {
	Name       string `json:"name"`
	Recreation string `json:"recreation"` // Never | Always | Conditionally
}

// ChangeSet is a preview of a stack change that can be executed.
type ChangeSet struct {
	ID              string         `json:"id"` // ARN
	Name            string         `json:"name"`
	StackID         string         `json:"stack_id"`
	StackName       string         `json:"stack_name"`
	Description     string         `json:"description,omitempty"`
	Template        string         `json:"template"`
	Params          map[string]any `json:"params"`
	ParamsIn        map[string]any `json:"params_in,omitempty"` // as supplied, for DescribeChangeSet
	Tags            core.Tags      `json:"tags,omitempty"`
	Capabilities    []string       `json:"capabilities,omitempty"`
	RoleARN         string         `json:"role_arn,omitempty"`
	Type            string         `json:"type"` // CREATE | UPDATE
	Status          string         `json:"status"`
	StatusReason    string         `json:"status_reason,omitempty"`
	ExecutionStatus string         `json:"execution_status"`
	Changes         []Change       `json:"changes"`
	CreatedAt       time.Time      `json:"created_at"`
}

const noChangesReason = "The submitted information didn't contain changes. Submit different information to create a change set."

func csKey(cs *ChangeSet) string { return uuidOf(cs.ID) }

// findChangeSet resolves a change set by ARN, or by name within a stack.
func (s *Service) findChangeSet(ref, stack string) (ChangeSet, error) {
	notFound := core.Errf(http.StatusNotFound, "ChangeSetNotFound", "ChangeSet [%s] does not exist", ref)
	if strings.HasPrefix(ref, "arn:") {
		if id := uuidOf(ref); id != "" {
			if cs, err := store.Get[ChangeSet](s.env.Store, cChangeSets, id); err == nil && cs.ID == ref {
				return cs, nil
			}
		}
		return ChangeSet{}, notFound
	}
	st, err := s.find(stack)
	if err != nil {
		return ChangeSet{}, err
	}
	for _, cs := range store.List[ChangeSet](s.env.Store, cChangeSets) {
		if cs.Name == ref && cs.StackID == st.ARN {
			return cs, nil
		}
	}
	return ChangeSet{}, notFound
}

func (s *Service) changeSetsOf(stackARN string) []ChangeSet {
	var out []ChangeSet
	for _, cs := range store.List[ChangeSet](s.env.Store, cChangeSets) {
		if cs.StackID == stackARN {
			out = append(out, cs)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out
}

// dropChangeSets returns a callback that removes a stack's change sets once it is deleted.
func (s *Service) dropChangeSets(st Stack) func(string) {
	return func(status string) {
		if status != "DELETE_COMPLETE" {
			return
		}
		for _, cs := range s.changeSetsOf(st.ARN) {
			_ = store.Delete(s.env.Store, cChangeSets, csKey(&cs))
		}
	}
}

// ChangeSetReq is CreateChangeSet's request.
type ChangeSetReq struct {
	StackReq
	ChangeSetName string
	Description   string
	Type          string
	// Provided lists the parameters the request set (the rest keep their default,
	// or their previous value when UsePrevious names them).
	UsePreviousTemplate bool
}

// CreateChangeSet computes what applying the request would change.
func (s *Service) CreateChangeSet(ctx context.Context, p *httpx.Principal, in ChangeSetReq) (ChangeSet, error) {
	if in.ChangeSetName == "" || len(in.ChangeSetName) > 128 || !stackRe.MatchString(in.ChangeSetName) {
		return ChangeSet{}, validation("1 validation error detected: Value '%s' at 'changeSetName' failed to satisfy constraint: Member must satisfy regular expression pattern: [a-zA-Z][-a-zA-Z0-9]*", in.ChangeSetName)
	}
	typ := in.Type
	if typ == "" {
		typ = "UPDATE"
	}
	if typ != "CREATE" && typ != "UPDATE" && typ != "IMPORT" {
		return ChangeSet{}, validation("Unsupported ChangeSetType %s", typ)
	}
	if typ == "IMPORT" {
		return ChangeSet{}, validation("ChangeSetType IMPORT is not supported")
	}
	if err := s.checkRole(in.RoleARN); err != nil {
		return ChangeSet{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	st, err := s.find(in.Name)
	exists := err == nil
	if typ == "CREATE" {
		if exists && st.Status != "REVIEW_IN_PROGRESS" {
			return ChangeSet{}, core.Errf(http.StatusConflict, "AlreadyExistsException", "Stack [%s] already exists and cannot be created again with the changeSet [%s].", st.Name, in.ChangeSetName)
		}
		if !stackRe.MatchString(in.Name) {
			return ChangeSet{}, validation("stack names start with a letter and contain letters, digits and hyphens")
		}
	} else {
		if !exists {
			return ChangeSet{}, validation("Stack [%s] does not exist", in.Name)
		}
		if !slicesContains(updatable, st.Status) {
			return ChangeSet{}, validation("Stack:%s is in %s state and can not be updated.", st.ARN, st.Status)
		}
	}
	if in.UsePreviousTemplate && exists {
		in.Template = st.Template
	}
	t, err := s.parseTemplate(in.Template)
	if err != nil {
		return ChangeSet{}, err
	}
	ps, err := params(t, in.Params)
	if err != nil {
		return ChangeSet{}, err
	}
	if err := s.resolveSSM(ctx, p, t, ps); err != nil {
		return ChangeSet{}, err
	}
	if exists {
		for _, o := range s.changeSetsOf(st.ARN) {
			if o.Name == in.ChangeSetName {
				return ChangeSet{}, core.Errf(http.StatusConflict, "AlreadyExistsException", "ChangeSet [%s] already exists in stack [%s]", in.ChangeSetName, st.Name)
			}
		}
	}
	if !exists {
		st = s.newStack(in.StackReq, t, ps)
		st.Template, st.Parameters = "", map[string]any{}
		st.Status = "REVIEW_IN_PROGRESS"
		st.Events = []Event{{ID: newUUID(), Time: core.Now(), LogicalID: in.Name, PhysicalID: st.ARN, Type: stackType, Status: "REVIEW_IN_PROGRESS", Reason: "User Initiated"}}
		s.save(&st)
	}
	cs := ChangeSet{ID: s.env.ARN("cloudformation", "changeSet/"+in.ChangeSetName+"/"+newUUID()), Name: in.ChangeSetName, StackID: st.ARN, StackName: st.Name,
		Description: in.Description, Template: in.Template, Params: ps, ParamsIn: in.Params, Tags: in.Tags, Capabilities: in.Capabilities, RoleARN: in.RoleARN,
		Type: typ, Status: "CREATE_COMPLETE", ExecutionStatus: "AVAILABLE", CreatedAt: core.Now()}
	if typ == "UPDATE" {
		prev, _ := Parse(st.Template)
		cs.Changes = s.diff(prev, &st, t, ps)
		if canonical(in.Template) == canonical(st.Template) && reflect.DeepEqual(normalize(ps), normalize(st.Parameters)) && (in.Tags == nil || reflect.DeepEqual(in.Tags, st.Tags)) {
			cs.Status, cs.StatusReason, cs.ExecutionStatus = "FAILED", noChangesReason, "UNAVAILABLE"
		}
	} else {
		full, _ := s.prune(t, &Stack{Name: st.Name, ARN: st.ARN, Parameters: ps})
		for _, id := range sortedKeys(full.Resources) {
			cs.Changes = append(cs.Changes, Change{Action: "Add", LogicalID: id, Type: full.Resources[id].Type})
		}
	}
	if err := store.Put(s.env.Store, cChangeSets, csKey(&cs), cs); err != nil {
		return ChangeSet{}, err
	}
	return cs, nil
}

func slicesContains(l []string, s string) bool {
	for _, x := range l {
		if x == s {
			return true
		}
	}
	return false
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// diff lists the resource changes between the stack's current template and t.
func (s *Service) diff(prev *Template, st *Stack, t *Template, ps map[string]any) []Change {
	var out []Change
	if prev == nil {
		prev = &Template{Resources: map[string]ResourceDef{}}
	}
	oldT, err := s.prune(prev, st)
	if err != nil {
		oldT = prev
	}
	newT, err := s.prune(t, &Stack{Name: st.Name, ARN: st.ARN, Parameters: ps})
	if err != nil {
		newT = t
	}
	ord, _ := order(newT)
	changed, reps := map[string]bool{}, map[string]string{}
	for _, id := range ord {
		nd := newT.Resources[id]
		od, existed := oldT.Resources[id]
		res := st.Resources[id]
		physical := ""
		if res != nil {
			physical = res.PhysicalID
		}
		switch {
		case !existed || res == nil:
			out = append(out, Change{Action: "Add", LogicalID: id, Type: nd.Type})
			changed[id] = true
			continue
		}
		// What changes each property: its own value, a parameter it reads, or a
		// resource it refers to that is being replaced.
		keys := map[string]string{}
		ids := idset(newT)
		names := map[string]bool{}
		for k := range od.Properties {
			names[k] = true
		}
		for k := range nd.Properties {
			names[k] = true
		}
		for k := range names {
			one := ResourceDef{Properties: map[string]any{k: nd.Properties[k]}}
			direct := !reflect.DeepEqual(normalize(od.Properties[k]), normalize(nd.Properties[k]))
			for _, pn := range paramRefs(one, t.Parameters) {
				if !reflect.DeepEqual(normalize(st.Parameters[pn]), normalize(ps[pn])) {
					direct = true
				}
			}
			if direct {
				keys[k] = keyReplacement(od.Type, nd.Type, k)
				continue
			}
			for _, d := range deps(one, ids) {
				if reps[d] == repTrue || reps[d] == repConditional {
					r := keyReplacement(od.Type, nd.Type, k)
					if r == repTrue {
						r = repConditional
					}
					keys[k] = r
				}
			}
		}
		if od.Type != nd.Type || len(keys) > 0 {
			rep := repFalse
			if od.Type != nd.Type {
				rep = repTrue
			}
			var details []ChangeDetail
			for _, k := range sortedKeys(keys) {
				rep = worse(rep, keys[k])
				recreate := map[string]string{repFalse: "Never", repTrue: "Always", repConditional: "Conditionally"}[keys[k]]
				details = append(details, ChangeDetail{Name: k, Recreation: recreate})
			}
			out = append(out, Change{Action: "Modify", LogicalID: id, PhysicalID: physical, Type: nd.Type, Replacement: rep, Details: details})
			changed[id] = true
			reps[id] = rep
		}
	}
	for _, id := range sortedKeys(oldT.Resources) {
		if _, ok := newT.Resources[id]; !ok {
			physical := ""
			if r := st.Resources[id]; r != nil {
				physical = r.PhysicalID
			}
			out = append(out, Change{Action: "Remove", LogicalID: id, PhysicalID: physical, Type: oldT.Resources[id].Type})
		}
	}
	return out
}

func idset(t *Template) map[string]bool {
	out := map[string]bool{}
	for id := range t.Resources {
		out[id] = true
	}
	return out
}

// ExecuteChangeSet applies a change set: it creates the stack (CREATE) or updates it.
func (s *Service) ExecuteChangeSet(p *httpx.Principal, ref, stack string, disableRollback ...bool) (Stack, error) {
	cs, err := s.findChangeSet(ref, stack)
	if err != nil {
		return Stack{}, err
	}
	if cs.ExecutionStatus != "AVAILABLE" {
		return Stack{}, core.Errf(http.StatusBadRequest, "InvalidChangeSetStatus", "ChangeSet [%s] cannot be executed in its current execution status of [%s]", cs.ID, cs.ExecutionStatus)
	}
	if err := s.checkRole(cs.RoleARN); err != nil {
		return Stack{}, err
	}
	t, err := s.parseTemplate(cs.Template)
	if err != nil {
		return Stack{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	st, err := s.find(cs.StackID)
	if err != nil {
		return Stack{}, err
	}
	finish := func(status string) {
		ok := status == "CREATE_COMPLETE" || status == "UPDATE_COMPLETE"
		if c, err := store.Get[ChangeSet](s.env.Store, cChangeSets, csKey(&cs)); err == nil {
			c.ExecutionStatus = "EXECUTE_COMPLETE"
			if !ok {
				c.ExecutionStatus = "EXECUTE_FAILED"
			}
			_ = store.Put(s.env.Store, cChangeSets, csKey(&c), c)
		}
		for _, o := range s.changeSetsOf(cs.StackID) {
			if o.ID != cs.ID && o.ExecutionStatus == "AVAILABLE" {
				o.ExecutionStatus = "OBSOLETE"
				_ = store.Put(s.env.Store, cChangeSets, csKey(&o), o)
			}
		}
	}
	if err := s.markExecuting(cs); err != nil {
		return Stack{}, err
	}
	req := StackReq{Name: st.Name, Template: cs.Template, Params: cs.ParamsIn, Tags: cs.Tags, Capabilities: cs.Capabilities, RoleARN: cs.RoleARN, KeepTags: true,
		DisableRollback: len(disableRollback) > 0 && disableRollback[0]}
	switch {
	case cs.Type == "CREATE" && st.Status == "REVIEW_IN_PROGRESS":
		st.Template, st.Parameters, st.Description = cs.Template, cs.Params, t.Description
		st.Tags, st.Capabilities, st.RoleARN = cs.Tags, cs.Capabilities, cs.RoleARN
		st.Status, st.StatusReason = "CREATE_IN_PROGRESS", ""
		st.CreatedAt = core.Now()
		st.Events = append([]Event{{ID: newUUID(), Time: core.Now(), LogicalID: st.Name, PhysicalID: st.ARN, Type: stackType, Status: "CREATE_IN_PROGRESS", Reason: "User Initiated"}}, st.Events...)
		return s.launchCreate(p, st, t, "", req.DisableRollback, finish), nil
	case slicesContains(updatable, st.Status):
		return s.launchUpdate(p, st, req, t, cs.Params, finish), nil
	}
	return Stack{}, validation("Stack:%s is in %s state and can not be updated.", st.ARN, st.Status)
}

func (s *Service) markExecuting(cs ChangeSet) error {
	_, err := store.Update(s.env.Store, cChangeSets, csKey(&cs), func(c *ChangeSet) error {
		c.ExecutionStatus = "EXECUTE_IN_PROGRESS"
		return nil
	})
	return err
}

// DeleteChangeSet removes a change set.
func (s *Service) DeleteChangeSet(ref, stack string) error {
	cs, err := s.findChangeSet(ref, stack)
	if err != nil {
		return err
	}
	if cs.ExecutionStatus == "EXECUTE_IN_PROGRESS" {
		return validation("ChangeSet [%s] cannot be deleted while it is executing", cs.ID)
	}
	return store.Delete(s.env.Store, cChangeSets, csKey(&cs))
}
