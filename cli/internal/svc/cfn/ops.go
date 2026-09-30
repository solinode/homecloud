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
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/homecloudhq/homecloud/cli/internal/core"
	"github.com/homecloudhq/homecloud/cli/internal/httpx"
	"github.com/homecloudhq/homecloud/cli/internal/store"
)

const (
	cDeleted  = "cfn_deleted"
	stackType = "AWS::CloudFormation::Stack"
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

func newUUID() string {
	h := core.RandHex(32)
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32]
}

func uuidOf(arn string) string {
	parts := strings.Split(arn, "/")
	if len(parts) >= 3 {
		return parts[len(parts)-1]
	}
	return ""
}

// event records a stack or resource event; the physical ID is looked up unless given.
func (s *Service) event(st *Stack, id, typ, status, reason string, physical ...string) {
	pid := ""
	switch {
	case len(physical) > 0:
		pid = physical[0]
	case id == st.Name:
		pid = st.ARN
	case st.Resources[id] != nil:
		pid = st.Resources[id].PhysicalID
	}
	st.Events = append([]Event{{ID: newUUID(), Time: core.Now(), LogicalID: id, PhysicalID: pid, Type: typ, Status: status, Reason: reason}}, st.Events...)
	if len(st.Events) > 500 {
		st.Events = st.Events[:500]
	}
	s.save(st)
}

func (s *Service) stackEvent(st *Stack, status, reason string) {
	st.Status, st.StatusReason = status, reason
	s.event(st, st.Name, stackType, status, reason)
}

// ---- parameters ----

func num(v any) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, true
	case int:
		return float64(x), true
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(x), 64)
		return f, err == nil
	}
	return 0, false
}

func isListType(t string) bool {
	return t == "CommaDelimitedList" || strings.HasPrefix(t, "List<") || strings.HasPrefix(t, "AWS::SSM::Parameter::Value<List<") || strings.HasPrefix(t, "AWS::SSM::Parameter::Value<CommaDelimitedList")
}

// params validates supplied parameters against the template and applies defaults.
func params(t *Template, in map[string]any) (map[string]any, error) {
	out := map[string]any{}
	var missing []string
	names := make([]string, 0, len(t.Parameters))
	for name := range t.Parameters {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		def := t.Parameters[name]
		v, ok := in[name]
		if !ok {
			if def.Default == nil {
				missing = append(missing, name)
				continue
			}
			v = def.Default
		}
		str := toStr(v)
		switch {
		case def.Type == "Number":
			if _, ok := num(v); !ok {
				return nil, core.BadRequest("Parameter '%s' must be a number.", name)
			}
			v = str
		case def.Type == "List<Number>":
			for _, p := range strings.Split(str, ",") {
				if _, ok := num(strings.TrimSpace(p)); !ok {
					return nil, core.BadRequest("Parameter '%s' must contain only numbers.", name)
				}
			}
		}
		if isListType(def.Type) {
			if l, isList := v.([]any); isList {
				v = l
			} else {
				parts := []any{}
				for _, p := range strings.Split(str, ",") {
					parts = append(parts, strings.TrimSpace(p))
				}
				v = parts
			}
		}
		if len(def.AllowedValues) > 0 && !slices.ContainsFunc(def.AllowedValues, func(a any) bool { return toStr(a) == str }) {
			return nil, core.BadRequest("Parameter %s must be one of AllowedValues", name)
		}
		if def.AllowedPattern != "" && !def.checkPattern(str) {
			return nil, core.BadRequest("Parameter %s must match pattern %s", name, def.AllowedPattern)
		}
		if n, ok := num(def.MinLength); ok && float64(len(str)) < n {
			return nil, core.BadRequest("Parameter %s must be at least %s characters long", name, toStr(def.MinLength))
		}
		if n, ok := num(def.MaxLength); ok && float64(len(str)) > n {
			return nil, core.BadRequest("Parameter %s must be at most %s characters long", name, toStr(def.MaxLength))
		}
		if f, ok := num(v); ok && def.Type == "Number" {
			if n, ok := num(def.MinValue); ok && f < n {
				return nil, core.BadRequest("Parameter %s must be at least %s", name, toStr(def.MinValue))
			}
			if n, ok := num(def.MaxValue); ok && f > n {
				return nil, core.BadRequest("Parameter %s must be at most %s", name, toStr(def.MaxValue))
			}
		}
		out[name] = v
	}
	if len(missing) > 0 {
		return nil, core.BadRequest("Parameters: [%s] must have values", strings.Join(missing, ", "))
	}
	for name := range in {
		if _, ok := t.Parameters[name]; !ok {
			return nil, core.BadRequest("Parameters: [%s] do not exist in the template", name)
		}
	}
	return out, nil
}

func (d ParamDef) checkPattern(v string) bool {
	re, err := regexp.Compile("^(?:" + d.AllowedPattern + ")$")
	return err != nil || re.MatchString(v)
}

const ssmParamPrefix = "AWS::SSM::Parameter::Value<"

// resolveSSM replaces parameters of type AWS::SSM::Parameter::Value<...> by the
// value of the Parameter Store parameter they name, read as the caller.
func (s *Service) resolveSSM(ctx context.Context, p *httpx.Principal, t *Template, ps map[string]any) error {
	for name, def := range t.Parameters {
		if !strings.HasPrefix(def.Type, ssmParamPrefix) {
			continue
		}
		key := toStr(ps[name])
		if l, ok := ps[name].([]any); ok && len(l) > 0 {
			key = toStr(l[0])
		}
		out, err := s.call(ctx, p, "GET", "/api/v1/ssm/parameter?with_decryption=true&name="+esc(key), nil)
		if err != nil {
			return core.BadRequest("Parameters: [%s] parameter type refers to SSM parameter %q that cannot be read: %v", name, key, err)
		}
		m, _ := out.(map[string]any)
		val := toStr(m["value"])
		if isListType(def.Type) {
			parts := []any{}
			for _, x := range strings.Split(val, ",") {
				parts = append(parts, strings.TrimSpace(x))
			}
			ps[name] = parts
		} else {
			ps[name] = val
		}
	}
	return nil
}

// ---- conditions ----

// prune returns the template without the resources and outputs whose
// Condition is false for these parameters.
func (s *Service) prune(t *Template, st *Stack) (*Template, error) {
	r := &resolver{stack: st, params: st.Parameters, tmpl: t}
	out := *t
	out.Resources = map[string]ResourceDef{}
	for id, def := range t.Resources {
		if def.Condition != "" {
			ok, err := r.cond(def.Condition)
			if err != nil {
				return nil, err
			}
			if !ok {
				continue
			}
		}
		out.Resources[id] = def
	}
	out.Outputs = map[string]map[string]any{}
	for name, o := range t.Outputs {
		if c, ok := o["Condition"].(string); ok && c != "" {
			ok, err := r.cond(c)
			if err != nil {
				return nil, err
			}
			if !ok {
				continue
			}
		}
		out.Outputs[name] = o
	}
	return &out, nil
}

// ---- exports ----

// Export is one exported stack output.
type Export struct {
	Name, Value, StackID, StackName string
}

func (s *Service) exports() []Export {
	var out []Export
	for _, st := range store.List[Stack](s.env.Store, cStacks) {
		if st.Status == "DELETE_COMPLETE" || strings.HasPrefix(st.Status, "ROLLBACK") || st.Status == "CREATE_FAILED" || st.Status == "REVIEW_IN_PROGRESS" {
			continue
		}
		for name, m := range st.OutputMeta {
			if m.Export != "" {
				out = append(out, Export{Name: m.Export, Value: toStr(st.Outputs[name]), StackID: st.ARN, StackName: st.Name})
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func (s *Service) importValue(name string) (any, error) {
	for _, e := range s.exports() {
		if e.Name == name {
			return e.Value, nil
		}
	}
	return nil, fmt.Errorf("No export named %s found!", name)
}

// importers lists the stacks that import any of the stack's exports.
func (s *Service) importers(st *Stack) map[string][]string {
	out := map[string][]string{}
	mine := map[string]bool{}
	for _, m := range st.OutputMeta {
		if m.Export != "" {
			mine[m.Export] = true
		}
	}
	for _, o := range store.List[Stack](s.env.Store, cStacks) {
		if o.Name == st.Name {
			continue
		}
		for _, imp := range o.Imports {
			if mine[imp] {
				out[imp] = append(out[imp], o.Name)
			}
		}
	}
	return out
}

// ---- deploy ----

// resErr is a failure while creating or updating one resource.
type resErr struct {
	id  string
	err error
}

func (e *resErr) Error() string { return fmt.Sprintf("%s: %v", e.id, e.err) }
func (e *resErr) Unwrap() error { return e.err }

func failedReason(err error, verb string) string {
	if re, ok := err.(*resErr); ok {
		return fmt.Sprintf("The following resource(s) failed to %s: [%s]. ", verb, re.id)
	}
	return err.Error()
}

// deploy brings the stack's resources in line with template t (create or
// update). prev is the template being replaced (nil on create). On failure
// during create with rollback, everything created is removed again; the
// returned bool reports whether that rollback fully succeeded.
func (s *Service) deploy(ctx context.Context, p *httpx.Principal, st *Stack, full, prev *Template, rollback bool) (error, bool) {
	t, err := s.prune(full, st)
	if err != nil {
		return err, true
	}
	ord, err := order(t)
	if err != nil {
		return err, true
	}
	r := &resolver{stack: st, params: st.Parameters, tmpl: full}
	r.imports = func(name string) (any, error) {
		v, err := s.importValue(name)
		if err == nil && !slices.Contains(st.Imports, name) {
			st.Imports = append(st.Imports, name)
		}
		return v, err
	}
	var created []string
	fail := func(err error) (error, bool) {
		if rollback {
			st.Status = "ROLLBACK_IN_PROGRESS"
			s.event(st, st.Name, stackType, "ROLLBACK_IN_PROGRESS", failedReason(err, "create")+"Rollback requested by user.")
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
			s.event(st, id, res.Type, "DELETE_SKIPPED", "DeletionPolicy: Retain")
			delete(st.Resources, id)
			continue
		}
		s.event(st, id, res.Type, "DELETE_IN_PROGRESS", "")
		pid := res.PhysicalID
		if err := s.deleteResource(ctx, p, res); err != nil {
			s.event(st, id, res.Type, "DELETE_FAILED", err.Error())
			return &resErr{id, err}, true
		}
		delete(st.Resources, id)
		s.event(st, id, res.Type, "DELETE_COMPLETE", "", pid)
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
			s.event(st, id, def.Type, "CREATE_FAILED", err.Error(), "")
			return fail(&resErr{id, err})
		}
		pm, _ := props.(map[string]any)
		if pm == nil {
			pm = map[string]any{}
		}
		old := st.Resources[id]
		dependsOnFresh := slices.ContainsFunc(deps(def, ids), func(d string) bool { return fresh[d] })
		if old != nil && old.native() != "" && old.Type == def.Type && !dependsOnFresh && reflect.DeepEqual(normalize(old.Properties), normalize(pm)) {
			continue // unchanged
		}
		fresh[id] = true
		if old != nil && old.native() != "" {
			// Replacement: remove the old resource first (names are unique in HomeCloud).
			s.event(st, id, old.Type, "UPDATE_IN_PROGRESS", "Requested update requires the creation of a new physical resource; replacing.")
			if def.UpdateReplacePolicy == "Retain" {
				s.event(st, id, old.Type, "DELETE_SKIPPED", "UpdateReplacePolicy: Retain")
			} else if err := s.deleteResource(ctx, p, old); err != nil {
				s.event(st, id, old.Type, "UPDATE_FAILED", err.Error())
				return &resErr{id, err}, true
			}
			old.PhysicalID, old.NativeID = "", ""
		}
		st.Resources[id] = &Resource{LogicalID: id, Type: def.Type, Status: "CREATE_IN_PROGRESS", Properties: pm, UpdatedAt: core.Now()}
		if !slices.Contains(st.Order, id) {
			st.Order = append(st.Order, id)
		}
		s.event(st, id, def.Type, "CREATE_IN_PROGRESS", "", "")
		x := &xctx{s: s, ctx: ctx, p: p, Stack: st.Name, Logical: id, Account: accountOf(st.ARN), Endpoint: st.Endpoint}
		c, err := s.createResource(x, def.Type, pm)
		res := st.Resources[id]
		res.HCType, res.NativeID, res.Attributes, res.UpdatedAt = c.HC, c.Native, c.Attrs, core.Now()
		res.PhysicalID = physicalID(x, def.Type, c, pm)
		if err != nil {
			res.Status, res.Reason = "CREATE_FAILED", err.Error()
			s.event(st, id, def.Type, "CREATE_FAILED", err.Error())
			if c.Native == "" {
				delete(st.Resources, id)
				st.Order = slices.DeleteFunc(st.Order, func(x string) bool { return x == id })
			} else {
				created = append(created, id)
			}
			return fail(&resErr{id, err})
		}
		res.Status = "CREATE_COMPLETE"
		created = append(created, id)
		s.event(st, id, def.Type, "CREATE_COMPLETE", "")
	}
	st.Order = ord
	st.Outputs, st.OutputMeta = map[string]any{}, map[string]OutputMeta{}
	for name, o := range t.Outputs {
		v, err := r.resolve(o["Value"])
		if err != nil {
			return fail(fmt.Errorf("output %s: %w", name, err))
		}
		st.Outputs[name] = v
		meta := OutputMeta{}
		meta.Description, _ = o["Description"].(string)
		if ex, ok := o["Export"].(map[string]any); ok {
			n, err := r.resolve(ex["Name"])
			if err != nil {
				return fail(fmt.Errorf("output %s export: %w", name, err))
			}
			meta.Export = toStr(n)
			for _, e := range s.exports() {
				if e.Name == meta.Export && e.StackName != st.Name {
					return fail(fmt.Errorf("Export with name %s is already exported by stack %s", meta.Export, e.StackName))
				}
			}
		}
		st.OutputMeta[name] = meta
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
		if res == nil || res.native() == "" {
			continue
		}
		s.event(st, res.LogicalID, res.Type, "DELETE_IN_PROGRESS", "")
		pid := res.PhysicalID
		if err := s.deleteResource(ctx, p, res); err != nil {
			s.event(st, res.LogicalID, res.Type, "DELETE_FAILED", err.Error())
			ok = false
			continue
		}
		delete(st.Resources, res.LogicalID)
		st.Order = slices.DeleteFunc(st.Order, func(x string) bool { return x == res.LogicalID })
		s.event(st, res.LogicalID, res.Type, "DELETE_COMPLETE", "", pid)
	}
	return ok
}

func (s *Service) destroy(ctx context.Context, p *httpx.Principal, st *Stack) error {
	t, _ := Parse(st.Template)
	for i := len(st.Order) - 1; i >= 0; i-- {
		id := st.Order[i]
		res := st.Resources[id]
		if res == nil || res.native() == "" {
			delete(st.Resources, id)
			st.Order = slices.Delete(st.Order, i, i+1)
			continue
		}
		if t != nil && t.Resources[id].DeletionPolicy == "Retain" {
			s.event(st, id, res.Type, "DELETE_SKIPPED", "")
			delete(st.Resources, id)
			st.Order = slices.Delete(st.Order, i, i+1)
			continue
		}
		s.event(st, id, res.Type, "DELETE_IN_PROGRESS", "")
		pid := res.PhysicalID
		if err := s.deleteResource(ctx, p, res); err != nil {
			s.event(st, id, res.Type, "DELETE_FAILED", err.Error())
			return &resErr{id, err}
		}
		delete(st.Resources, id)
		st.Order = slices.Delete(st.Order, i, i+1)
		s.event(st, id, res.Type, "DELETE_COMPLETE", "", pid)
	}
	return nil
}

// Recover marks stacks interrupted by a restart as failed so they can be retried or deleted.
func (s *Service) Recover() {
	for _, st := range store.List[Stack](s.env.Store, cStacks) {
		if !strings.HasSuffix(st.Status, "_IN_PROGRESS") || st.Status == "REVIEW_IN_PROGRESS" {
			continue
		}
		st.Status = strings.TrimSuffix(st.Status, "_IN_PROGRESS") + "_FAILED"
		st.StatusReason = "HomeCloud restarted while the operation was running"
		s.event(&st, st.Name, stackType, st.Status, st.StatusReason)
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

// ---- finding stacks ----

func stackNotFound(ref string) error {
	return core.Errf(http.StatusNotFound, "StackNotFound", "Stack with id %s does not exist", ref)
}

// find looks a stack up by name or ID (ARN). Deleted stacks are found by ID only.
func (s *Service) find(ref string) (Stack, error) {
	if ref == "" {
		return Stack{}, stackNotFound(ref)
	}
	if !strings.HasPrefix(ref, "arn:") {
		if st, err := store.Get[Stack](s.env.Store, cStacks, ref); err == nil {
			return st, nil
		}
		return Stack{}, stackNotFound(ref)
	}
	parts := strings.Split(ref, "/")
	if len(parts) >= 2 {
		if st, err := store.Get[Stack](s.env.Store, cStacks, parts[1]); err == nil && (st.ARN == ref || len(parts) == 2) {
			return st, nil
		}
	}
	if id := uuidOf(ref); id != "" {
		if st, err := store.Get[Stack](s.env.Store, cDeleted, id); err == nil && st.ARN == ref {
			return st, nil
		}
	}
	return Stack{}, stackNotFound(ref)
}

// allStacks lists live stacks and, when deleted is set, the archived deleted ones.
func (s *Service) allStacks(deleted bool) []Stack {
	out := []Stack{}
	for _, st := range store.List[Stack](s.env.Store, cStacks) {
		if st.Status == "DELETE_COMPLETE" {
			_ = store.Delete(s.env.Store, cStacks, st.Name) // left behind by an interrupted delete
			continue
		}
		out = append(out, st)
	}
	if deleted {
		out = append(out, store.List[Stack](s.env.Store, cDeleted)...)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out
}

// archive keeps a deleted stack readable by its ID (CloudFormation keeps them for 90 days).
func (s *Service) archive(st Stack) {
	now := core.Now()
	st.DeletedAt = &now
	st.Status = "DELETE_COMPLETE"
	key := uuidOf(st.ARN)
	if key == "" {
		return
	}
	_ = store.Put(s.env.Store, cDeleted, key, st)
	old := store.List[Stack](s.env.Store, cDeleted)
	if len(old) > 200 {
		sort.Slice(old, func(i, j int) bool { return old[i].DeletedAt.Before(*old[j].DeletedAt) })
		for _, o := range old[:len(old)-200] {
			_ = store.Delete(s.env.Store, cDeleted, uuidOf(o.ARN))
		}
	}
}

// ---- stack operations shared by the native and AWS APIs ----

// StackReq is a request to create or update a stack.
type StackReq struct {
	Name             string
	Template         string
	Params           map[string]any
	Tags             core.Tags
	Capabilities     []string
	RoleARN          string
	NotificationARNs []string
	DisableRollback  bool
	OnFailure        string // ROLLBACK, DO_NOTHING or DELETE
	// AWS reports an update that changes nothing as an error.
	ErrIfNoChanges        bool
	TerminationProtection bool
	// Endpoint is the scheme and host of the request (queue URLs use it).
	Endpoint string
	// KeepTags leaves the stack's tags alone when the request sets none (UpdateStack).
	KeepTags bool
}

var stackRe = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9-]{0,127}$`)

func validation(format string, a ...any) *core.Error {
	return core.Errf(http.StatusBadRequest, "ValidationError", format, a...)
}

// check assumes the stack's role now, so a bad role fails the request.
func (s *Service) checkRole(roleARN string) error {
	if roleARN == "" {
		return nil
	}
	if s.RolePrincipal == nil {
		return validation("Role %s cannot be used: service roles are not available", roleARN)
	}
	if _, err := s.RolePrincipal(roleARN); err != nil {
		return validation("Role %s is invalid or cannot be assumed", roleARN)
	}
	return nil
}

func (s *Service) actor(ctx context.Context, roleARN string) context.Context {
	if roleARN == "" || s.RolePrincipal == nil {
		return ctx
	}
	return withActor(ctx, func() (*httpx.Principal, error) { return s.RolePrincipal(roleARN) })
}

// start records the stack in its new state and runs op in the background with
// the caller's (continuously re-checked) permissions. Only one operation runs
// per stack at a time; status checks and transitions happen under s.mu.
func (s *Service) start(st Stack, p *httpx.Principal, op func(ctx context.Context, st *Stack) (status, reason string), after func(status string)) Stack {
	s.save(&st)
	done := make(chan struct{})
	st.done = done
	go func() {
		defer close(done)
		defer core.Recover("cloudformation " + st.Name)
		l := s.lock(st.Name)
		l.Lock()
		defer l.Unlock()
		ctx, cancel := context.WithTimeout(s.actor(context.Background(), st.RoleARN), time.Hour)
		defer cancel()
		cur, err := store.Get[Stack](s.env.Store, cStacks, st.Name)
		if err != nil {
			return
		}
		status, reason := op(ctx, &cur)
		cur.Status, cur.StatusReason = status, reason
		if status == "DELETE_COMPLETE" {
			s.event(&cur, cur.Name, stackType, status, reason)
			s.archive(cur)
			_ = store.Delete(s.env.Store, cStacks, cur.Name)
		} else {
			if status == "UPDATE_COMPLETE" {
				now := core.Now()
				cur.LastUpdated = &now
			}
			s.event(&cur, cur.Name, stackType, status, reason)
		}
		if after != nil {
			after(status)
		}
	}()
	return st
}

// settleWait is how long the AWS API holds a create, update or delete request
// open for the operation to finish. Clients poll with long delays (the AWS CLI
// waits 30 seconds between checks), so a stack that takes milliseconds to make
// is reported complete on the first poll instead of the second.
var settleWait = 3 * time.Second

func settle(st Stack) {
	if st.done == nil {
		return
	}
	select {
	case <-st.done:
	case <-time.After(settleWait):
	}
}

func (s *Service) parseTemplate(body string) (*Template, error) {
	t, err := Parse(body)
	if err != nil {
		return nil, validation("%v", err)
	}
	if _, err := order(t); err != nil {
		return nil, validation("Template format error: %v", err)
	}
	return t, nil
}

// CreateStack starts creating a stack and returns it in CREATE_IN_PROGRESS.
func (s *Service) CreateStack(ctx context.Context, p *httpx.Principal, req StackReq) (Stack, error) {
	if !stackRe.MatchString(req.Name) {
		return Stack{}, validation("stack names start with a letter and contain letters, digits and hyphens")
	}
	t, err := s.parseTemplate(req.Template)
	if err != nil {
		return Stack{}, err
	}
	ps, err := params(t, req.Params)
	if err != nil {
		return Stack{}, err
	}
	if err := s.resolveSSM(ctx, p, t, ps); err != nil {
		return Stack{}, err
	}
	if err := s.checkRole(req.RoleARN); err != nil {
		return Stack{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := store.Get[Stack](s.env.Store, cStacks, req.Name); err == nil {
		return Stack{}, core.Errf(http.StatusConflict, "AlreadyExistsException", "Stack [%s] already exists", req.Name)
	}
	st := s.newStack(req, t, ps)
	st.Status = "CREATE_IN_PROGRESS"
	st.Events = []Event{{ID: newUUID(), Time: core.Now(), LogicalID: req.Name, PhysicalID: st.ARN, Type: stackType, Status: "CREATE_IN_PROGRESS", Reason: "User Initiated"}}
	return s.launchCreate(p, st, t, req.OnFailure, req.DisableRollback, nil), nil
}

func (s *Service) newStack(req StackReq, t *Template, ps map[string]any) Stack {
	return Stack{Name: req.Name, ARN: s.env.ARN("cloudformation", "stack/"+req.Name+"/"+newUUID()), Description: t.Description,
		Template: req.Template, Parameters: ps, Resources: map[string]*Resource{}, Order: []string{}, Outputs: map[string]any{},
		Tags: req.Tags, Capabilities: req.Capabilities, RoleARN: req.RoleARN, NotificationARNs: req.NotificationARNs,
		DisableRollback: req.DisableRollback || req.OnFailure == "DO_NOTHING", Endpoint: req.Endpoint, TerminationProtection: req.TerminationProtection, CreatedAt: core.Now()}
}

// launchCreate runs the creation of st (already saved states are rewritten) in the background.
func (s *Service) launchCreate(p *httpx.Principal, st Stack, t *Template, onFailure string, disable bool, after func(string)) Stack {
	disable = disable || onFailure == "DO_NOTHING"
	return s.start(st, p, func(ctx context.Context, cur *Stack) (string, string) {
		err, rolledBack := s.deploy(ctx, p, cur, t, nil, !disable)
		switch {
		case err == nil:
			return "CREATE_COMPLETE", ""
		case disable:
			return "CREATE_FAILED", failedReason(err, "create")
		case !rolledBack:
			return "ROLLBACK_FAILED", failedReason(err, "create")
		case onFailure == "DELETE":
			s.stackEvent(cur, "ROLLBACK_COMPLETE", failedReason(err, "create"))
			s.stackEvent(cur, "DELETE_IN_PROGRESS", "")
			return "DELETE_COMPLETE", ""
		}
		return "ROLLBACK_COMPLETE", failedReason(err, "create")
	}, after)
}

var updatable = []string{"CREATE_COMPLETE", "UPDATE_COMPLETE", "UPDATE_ROLLBACK_COMPLETE", "UPDATE_FAILED"}

func canonical(src string) string {
	t, err := Parse(src)
	if err != nil {
		return src
	}
	b, _ := json.Marshal(t)
	return string(b)
}

// UpdateStack starts updating a stack and returns it in UPDATE_IN_PROGRESS.
func (s *Service) UpdateStack(ctx context.Context, p *httpx.Principal, ref string, req StackReq) (Stack, error) {
	st, err := s.find(ref)
	if err != nil {
		return Stack{}, err
	}
	if req.Template == "" {
		req.Template = st.Template
	}
	t, err := s.parseTemplate(req.Template)
	if err != nil {
		return Stack{}, err
	}
	ps, err := params(t, req.Params)
	if err != nil {
		return Stack{}, err
	}
	if err := s.resolveSSM(ctx, p, t, ps); err != nil {
		return Stack{}, err
	}
	if req.RoleARN == "" {
		req.RoleARN = st.RoleARN
	}
	if err := s.checkRole(req.RoleARN); err != nil {
		return Stack{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	st, err = s.find(ref)
	if err != nil {
		return Stack{}, err
	}
	if !slices.Contains(updatable, st.Status) {
		return Stack{}, validation("Stack:%s is in %s state and can not be updated.", st.ARN, st.Status)
	}
	if req.ErrIfNoChanges && canonical(req.Template) == canonical(st.Template) && reflect.DeepEqual(normalize(ps), normalize(st.Parameters)) &&
		(req.Tags == nil || reflect.DeepEqual(req.Tags, st.Tags)) && req.RoleARN == st.RoleARN {
		return Stack{}, validation("No updates are to be performed.")
	}
	return s.launchUpdate(p, st, req, t, ps, nil), nil
}

// launchUpdate applies req to the stored stack st (which the caller holds s.mu for).
func (s *Service) launchUpdate(p *httpx.Principal, st Stack, req StackReq, t *Template, ps map[string]any, after func(string)) Stack {
	prev, _ := Parse(st.Template)
	st.Template, st.Parameters, st.Description = req.Template, ps, t.Description
	if req.Tags != nil || !req.KeepTags {
		st.Tags = req.Tags
	}
	if req.Capabilities != nil {
		st.Capabilities = req.Capabilities
	}
	if req.NotificationARNs != nil {
		st.NotificationARNs = req.NotificationARNs
	}
	st.RoleARN = req.RoleARN
	if req.Endpoint != "" {
		st.Endpoint = req.Endpoint
	}
	st.Status, st.StatusReason = "UPDATE_IN_PROGRESS", ""
	st.Events = append([]Event{{ID: newUUID(), Time: core.Now(), LogicalID: st.Name, PhysicalID: st.ARN, Type: stackType, Status: "UPDATE_IN_PROGRESS", Reason: "User Initiated"}}, st.Events...)
	return s.start(st, p, func(ctx context.Context, cur *Stack) (string, string) {
		if err, _ := s.deploy(ctx, p, cur, t, prev, false); err != nil {
			return "UPDATE_FAILED", failedReason(err, "update")
		}
		return "UPDATE_COMPLETE", ""
	}, after)
}

// DeleteStack starts deleting a stack.
func (s *Service) DeleteStack(p *httpx.Principal, ref, roleARN string) (Stack, error) {
	if err := s.checkRole(roleARN); err != nil {
		return Stack{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	st, err := s.find(ref)
	if err != nil {
		return Stack{}, err
	}
	if strings.HasSuffix(st.Status, "_IN_PROGRESS") && st.Status != "REVIEW_IN_PROGRESS" {
		return Stack{}, core.Errf(http.StatusConflict, "StackBusy", "Stack:%s is in %s state and can not be deleted.", st.ARN, st.Status)
	}
	if st.TerminationProtection {
		return Stack{}, validation("Stack [%s] cannot be deleted while TerminationProtection is enabled", st.Name)
	}
	for exp, by := range s.importers(&st) {
		return Stack{}, validation("Cannot delete export %s as it is in use by %s.", exp, strings.Join(by, ", "))
	}
	if roleARN != "" {
		st.RoleARN = roleARN
	}
	st.Status, st.StatusReason = "DELETE_IN_PROGRESS", ""
	st.Events = append([]Event{{ID: newUUID(), Time: core.Now(), LogicalID: st.Name, PhysicalID: st.ARN, Type: stackType, Status: "DELETE_IN_PROGRESS", Reason: "User Initiated"}}, st.Events...)
	return s.start(st, p, func(ctx context.Context, cur *Stack) (string, string) {
		if err := s.destroy(ctx, p, cur); err != nil {
			return "DELETE_FAILED", failedReason(err, "delete")
		}
		return "DELETE_COMPLETE", ""
	}, s.dropChangeSets(st)), nil
}

// ---- routes ----

func (s *Service) Routes(r *httpx.Router) {
	res := httpx.Res("arn:aws:cloudformation:{region}:{account}:stack/{name}")
	r.Handle("GET /api/v1/cloudformation/stacks", "cloudformation:ListStacks", s.list)
	r.Handle("POST /api/v1/cloudformation/stacks", "cloudformation:CreateStack", s.create)
	r.Handle("GET /api/v1/cloudformation/stacks/{name}", "cloudformation:DescribeStacks", s.get, res)
	r.Handle("PUT /api/v1/cloudformation/stacks/{name}", "cloudformation:UpdateStack", s.update, res)
	r.Handle("DELETE /api/v1/cloudformation/stacks/{name}", "cloudformation:DeleteStack", s.delete, res)
	r.Handle("POST /api/v1/cloudformation/validate", "cloudformation:ValidateTemplate", s.validate)
	r.Handle("GET /api/v1/cloudformation/resource-types", "cloudformation:ListTypes", s.listTypes)
}

func (s *Service) list(c *httpx.Ctx) (any, error) {
	out := []map[string]any{}
	for _, st := range s.allStacks(false) {
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
	for t := range awsTypes {
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

func (s *Service) create(c *httpx.Ctx) (any, error) {
	var in stackInput
	if err := c.Bind(&in); err != nil {
		return nil, err
	}
	st, err := s.CreateStack(c.R.Context(), c.P, StackReq{Name: in.Name, Template: in.Template, Params: in.Parameters, DisableRollback: in.DisableRollback})
	if err != nil {
		return nil, nativeErr(err)
	}
	return view(&st), nil
}

// nativeErr keeps the native API's error codes and statuses.
func nativeErr(err error) error {
	if ce, ok := err.(*core.Error); ok && ce.Code == "ValidationError" {
		return core.BadRequest("%s", ce.Message)
	}
	return err
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
	// The native API keeps the values of parameters the request leaves out.
	merged := map[string]any{}
	tmpl := in.Template
	if tmpl == "" {
		tmpl = st.Template
	}
	if t, err := Parse(tmpl); err == nil {
		for k, v := range st.Parameters {
			if _, ok := t.Parameters[k]; ok {
				merged[k] = v
			}
		}
	}
	for k, v := range in.Parameters {
		merged[k] = v
	}
	out, err := s.UpdateStack(c.R.Context(), c.P, st.Name, StackReq{Template: in.Template, Params: merged, KeepTags: true})
	if err != nil {
		if ce, ok := err.(*core.Error); ok && ce.Code == "ValidationError" && strings.Contains(ce.Message, "can not be updated") {
			return nil, core.Errf(http.StatusConflict, "ValidationError", "a stack in %s cannot be updated", st.Status)
		}
		return nil, nativeErr(err)
	}
	return view(&out), nil
}

func (s *Service) delete(c *httpx.Ctx) (any, error) {
	st, err := s.DeleteStack(c.P, c.Param("name"), "")
	if err != nil {
		return nil, err
	}
	return view(&st), nil
}
