package cfn

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/homecloudhq/homecloud/cli/internal/core"
)

// ---- intrinsic functions ----

// noValue is what Ref AWS::NoValue resolves to: the property is left out.
type noValueT struct{}

var noValue = noValueT{}

type resolver struct {
	stack    *Stack
	params   map[string]any
	tmpl     *Template
	imports  func(name string) (any, error)
	conds    map[string]bool
	condBusy map[string]bool
	// noResolve makes references to resources that do not exist yet resolve to a
	// placeholder (used to describe change sets).
	placeholder bool
}

var subRe = regexp.MustCompile(`\$\{([^}]+)\}`)

// toStr renders a scalar the way CloudFormation does (whole numbers without exponent).
func toStr(v any) string {
	switch x := v.(type) {
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	case string:
		return x
	case nil:
		return ""
	case bool:
		return strconv.FormatBool(x)
	case []any, map[string]any:
		b, _ := json.Marshal(x)
		return string(b)
	}
	return fmt.Sprint(v)
}

func accountOf(arn string) string {
	parts := strings.Split(arn, ":")
	if len(parts) > 4 {
		return parts[4]
	}
	return ""
}

func (r *resolver) ref(name string) (any, error) {
	switch name {
	case "HC::StackName", "AWS::StackName":
		return r.stack.Name, nil
	case "HC::AccountId", "AWS::AccountId":
		return accountOf(r.stack.ARN), nil
	case "HC::Region", "AWS::Region":
		return core.Region, nil
	case "AWS::StackId":
		return r.stack.ARN, nil
	case "AWS::Partition":
		return "aws", nil
	case "AWS::URLSuffix":
		return "amazonaws.com", nil
	case "AWS::NoValue":
		return noValue, nil
	case "AWS::NotificationARNs":
		out := []any{}
		for _, a := range r.stack.NotificationARNs {
			out = append(out, a)
		}
		return out, nil
	}
	if v, ok := r.params[name]; ok {
		return v, nil
	}
	if res := r.stack.Resources[name]; res != nil && res.PhysicalID != "" {
		return res.PhysicalID, nil
	}
	if r.placeholder && r.tmpl != nil {
		if _, ok := r.tmpl.Resources[name]; ok {
			return "<" + name + ">", nil
		}
	}
	return nil, fmt.Errorf("Template format error: Unresolved resource dependencies [%s] in the Resources block of the template", name)
}

func (r *resolver) getAtt(id, attr string) (any, error) {
	res := r.stack.Resources[id]
	if res == nil || res.native() == "" {
		if r.placeholder && r.tmpl != nil {
			if _, ok := r.tmpl.Resources[id]; ok {
				return "<" + id + "." + attr + ">", nil
			}
		}
		return nil, fmt.Errorf("GetAtt: resource %q has not been created", id)
	}
	if a, ok := awsTypes[res.Type]; ok && a.Att != nil {
		if v, ok := a.Att(&attrView{ID: res.native(), Physical: res.PhysicalID, Attrs: res.Attributes, Props: res.Properties, Account: accountOf(r.stack.ARN), Stack: r.stack.Name}, attr); ok {
			return v, nil
		}
		return nil, fmt.Errorf("Template error: resource %s does not support attribute type %s in Fn::GetAtt", id, attr)
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

func asList(v any) []any {
	l, _ := v.([]any)
	return l
}

// cond evaluates a named condition of the template.
func (r *resolver) cond(name string) (bool, error) {
	if b, ok := r.conds[name]; ok {
		return b, nil
	}
	if r.tmpl == nil {
		return false, fmt.Errorf("Template format error: Unresolved condition dependency %s", name)
	}
	def, ok := r.tmpl.Conditions[name]
	if !ok {
		return false, fmt.Errorf("Template format error: Unresolved condition dependency %s in Resources block of the template", name)
	}
	if r.condBusy[name] {
		return false, fmt.Errorf("Template format error: circular condition dependency at %s", name)
	}
	if r.condBusy == nil {
		r.condBusy = map[string]bool{}
	}
	r.condBusy[name] = true
	defer delete(r.condBusy, name)
	b, err := r.evalBool(def)
	if err != nil {
		return false, err
	}
	if r.conds == nil {
		r.conds = map[string]bool{}
	}
	r.conds[name] = b
	return b, nil
}

func (r *resolver) evalBool(v any) (bool, error) {
	if b, ok := v.(bool); ok {
		return b, nil
	}
	m, ok := v.(map[string]any)
	if !ok || len(m) != 1 {
		return false, fmt.Errorf("Template format error: invalid condition expression")
	}
	for k, arg := range m {
		switch k {
		case "Condition":
			return r.cond(toStr(arg))
		case "Fn::Equals":
			p := asList(arg)
			if len(p) != 2 {
				return false, fmt.Errorf("Template format error: Fn::Equals takes two values")
			}
			a, err := r.resolve(p[0])
			if err != nil {
				return false, err
			}
			b, err := r.resolve(p[1])
			if err != nil {
				return false, err
			}
			return reflect.DeepEqual(normalize(a), normalize(b)) || toStr(a) == toStr(b), nil
		case "Fn::Not":
			p := asList(arg)
			if len(p) != 1 {
				return false, fmt.Errorf("Template format error: Fn::Not takes one condition")
			}
			b, err := r.evalBool(p[0])
			return !b, err
		case "Fn::And", "Fn::Or":
			p := asList(arg)
			if len(p) < 2 {
				return false, fmt.Errorf("Template format error: %s takes at least two conditions", k)
			}
			res := k == "Fn::And"
			for _, c := range p {
				b, err := r.evalBool(c)
				if err != nil {
					return false, err
				}
				if k == "Fn::And" {
					res = res && b
				} else {
					res = res || b
				}
			}
			return res, nil
		}
	}
	return false, fmt.Errorf("Template format error: invalid condition expression")
}

func (r *resolver) resolve(v any) (any, error) {
	switch x := v.(type) {
	case map[string]any:
		if len(x) == 1 {
			for k, arg := range x {
				if out, handled, err := r.intrinsic(k, arg); handled {
					return out, err
				}
			}
		}
		out := map[string]any{}
		for k, val := range x {
			rv, err := r.resolve(val)
			if err != nil {
				return nil, err
			}
			if rv == noValue {
				continue
			}
			out[k] = rv
		}
		return out, nil
	case []any:
		out := make([]any, 0, len(x))
		for _, val := range x {
			rv, err := r.resolve(val)
			if err != nil {
				return nil, err
			}
			if rv == noValue {
				continue
			}
			out = append(out, rv)
		}
		return out, nil
	}
	return v, nil
}

func (r *resolver) intrinsic(k string, arg any) (any, bool, error) {
	fail := func(f string, a ...any) (any, bool, error) {
		return nil, true, fmt.Errorf("Template error: "+f, a...)
	}
	switch k {
	case "Ref":
		out, err := r.ref(toStr(arg))
		return out, true, err
	case "Fn::GetAtt":
		var id, attr string
		switch g := arg.(type) {
		case string:
			id, attr, _ = strings.Cut(g, ".")
		case []any:
			if len(g) != 2 {
				return fail("Fn::GetAtt takes [LogicalId, Attribute]")
			}
			id = toStr(g[0])
			a, err := r.resolve(g[1])
			if err != nil {
				return nil, true, err
			}
			attr = toStr(a)
		default:
			return fail("Fn::GetAtt takes [LogicalId, Attribute]")
		}
		out, err := r.getAtt(id, attr)
		return out, true, err
	case "Fn::Sub":
		out, err := r.sub(arg)
		return out, true, err
	case "Fn::Join":
		parts := asList(arg)
		if len(parts) != 2 {
			return fail("Fn::Join takes [delimiter, list]")
		}
		list, err := r.resolve(parts[1])
		if err != nil {
			return nil, true, err
		}
		items, ok := list.([]any)
		if !ok {
			return fail("Fn::Join needs a list of values")
		}
		strs := make([]string, len(items))
		for i, it := range items {
			strs[i] = toStr(it)
		}
		return strings.Join(strs, toStr(parts[0])), true, nil
	case "Fn::Select":
		parts := asList(arg)
		if len(parts) != 2 {
			return fail("Fn::Select takes [index, list]")
		}
		idx, err := r.resolve(parts[0])
		if err != nil {
			return nil, true, err
		}
		list, err := r.resolve(parts[1])
		if err != nil {
			return nil, true, err
		}
		items, _ := list.([]any)
		i, _ := strconv.Atoi(toStr(idx))
		if i < 0 || i >= len(items) {
			return fail("Fn::Select index %d out of range", i)
		}
		return items[i], true, nil
	case "Fn::Split":
		parts := asList(arg)
		if len(parts) != 2 {
			return fail("Fn::Split takes [delimiter, string]")
		}
		s, err := r.resolve(parts[1])
		if err != nil {
			return nil, true, err
		}
		out := []any{}
		for _, p := range strings.Split(toStr(s), toStr(parts[0])) {
			out = append(out, p)
		}
		return out, true, nil
	case "Fn::If":
		parts := asList(arg)
		if len(parts) != 3 {
			return fail("Fn::If takes [condition, true-value, false-value]")
		}
		b, err := r.cond(toStr(parts[0]))
		if err != nil {
			return nil, true, err
		}
		branch := parts[2]
		if b {
			branch = parts[1]
		}
		out, err := r.resolve(branch)
		return out, true, err
	case "Fn::Equals", "Fn::And", "Fn::Or", "Fn::Not", "Condition":
		b, err := r.evalBool(map[string]any{k: arg})
		return b, true, err
	case "Fn::FindInMap":
		parts := asList(arg)
		if len(parts) != 3 {
			return fail("Fn::FindInMap takes [MapName, TopLevelKey, SecondLevelKey]")
		}
		var keys [3]string
		for i := range keys {
			kv, err := r.resolve(parts[i])
			if err != nil {
				return nil, true, err
			}
			keys[i] = toStr(kv)
		}
		v, ok := r.tmpl.mapping(keys[0], keys[1], keys[2])
		if !ok {
			return fail("Fn::FindInMap cannot find %s/%s/%s in Mappings", keys[0], keys[1], keys[2])
		}
		return v, true, nil
	case "Fn::ImportValue":
		name, err := r.resolve(arg)
		if err != nil {
			return nil, true, err
		}
		if r.imports == nil {
			return fail("Fn::ImportValue is not available here")
		}
		out, err := r.imports(toStr(name))
		return out, true, err
	case "Fn::Base64":
		s, err := r.resolve(arg)
		if err != nil {
			return nil, true, err
		}
		return base64.StdEncoding.EncodeToString([]byte(toStr(s))), true, nil
	case "Fn::GetAZs":
		region := core.Region
		if a, err := r.resolve(arg); err == nil && toStr(a) != "" {
			region = toStr(a)
		}
		return []any{region + "a", region + "b", region + "c"}, true, nil
	case "Fn::Length":
		l, err := r.resolve(arg)
		if err != nil {
			return nil, true, err
		}
		return float64(len(asList(l))), true, nil
	case "Fn::ToJsonString":
		l, err := r.resolve(arg)
		if err != nil {
			return nil, true, err
		}
		b, _ := json.Marshal(l)
		return string(b), true, nil
	case "Fn::Cidr":
		return fail("Fn::Cidr is not supported")
	case "Fn::Transform":
		return fail("Fn::Transform (macros) is not supported")
	}
	return nil, false, nil
}

func (t *Template) mapping(name, k1, k2 string) (any, bool) {
	if t == nil {
		return nil, false
	}
	m, ok := t.Mappings[name]
	if !ok {
		return nil, false
	}
	l, ok := m[k1]
	if !ok {
		return nil, false
	}
	v, ok := l[k2]
	return v, ok
}

func (r *resolver) sub(arg any) (any, error) {
	tmpl, vars := "", map[string]any{}
	switch a := arg.(type) {
	case string:
		tmpl = a
	case []any:
		if len(a) != 2 {
			return nil, fmt.Errorf("Template error: Fn::Sub takes a string or [string, variables]")
		}
		tmpl = toStr(a[0])
		m, _ := a[1].(map[string]any)
		for k, v := range m {
			rv, err := r.resolve(v)
			if err != nil {
				return nil, err
			}
			vars[k] = rv
		}
	default:
		return nil, fmt.Errorf("Template error: Fn::Sub takes a string or [string, variables]")
	}
	var firstErr error
	out := subRe.ReplaceAllStringFunc(tmpl, func(m string) string {
		name := m[2 : len(m)-1]
		if strings.HasPrefix(name, "!") {
			return "${" + name[1:] + "}"
		}
		if v, ok := vars[name]; ok {
			return toStr(v)
		}
		var v any
		var err error
		if id, attr, ok := strings.Cut(name, "."); ok && (r.stack.Resources[id] != nil || (r.tmpl != nil && hasRes(r.tmpl, id))) {
			v, err = r.getAtt(id, attr)
		} else {
			v, err = r.ref(name)
		}
		if err != nil && firstErr == nil {
			firstErr = err
		}
		return toStr(v)
	})
	return out, firstErr
}

func hasRes(t *Template, id string) bool { _, ok := t.Resources[id]; return ok }

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
			switch ga := x["Fn::GetAtt"].(type) {
			case []any:
				if len(ga) > 0 {
					if id := toStr(ga[0]); ids[id] {
						found[id] = true
					}
				}
			case string:
				id, _, _ := strings.Cut(ga, ".")
				if ids[id] {
					found[id] = true
				}
			}
			if s, ok := x["Fn::Sub"]; ok {
				str := toStr(s)
				if arr, ok := s.([]any); ok && len(arr) > 0 {
					str = toStr(arr[0])
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
		if ids[d] {
			found[d] = true
		}
	case []any:
		for _, x := range d {
			if id := toStr(x); ids[id] {
				found[id] = true
			}
		}
	}
	out := make([]string, 0, len(found))
	for id := range found {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

// paramRefs lists the parameters a resource definition reads.
func paramRefs(def ResourceDef, params map[string]ParamDef) []string {
	found := map[string]bool{}
	var walk func(v any)
	walk = func(v any) {
		switch x := v.(type) {
		case map[string]any:
			if ref, ok := x["Ref"].(string); ok {
				if _, isP := params[ref]; isP {
					found[ref] = true
				}
			}
			if s, ok := x["Fn::Sub"]; ok {
				str := toStr(s)
				if arr, ok := s.([]any); ok && len(arr) > 0 {
					str = toStr(arr[0])
				}
				for _, m := range subRe.FindAllStringSubmatch(str, -1) {
					if _, isP := params[m[1]]; isP {
						found[m[1]] = true
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
			return fmt.Errorf("Circular dependency between resources: [%s]", strings.Join(append(path, id), ", "))
		case 2:
			return nil
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

// checkRefs verifies every Ref/GetAtt/DependsOn names something that exists.
func checkRefs(t *Template) error {
	known := map[string]bool{"AWS::StackName": true, "AWS::StackId": true, "AWS::Region": true, "AWS::AccountId": true, "AWS::Partition": true,
		"AWS::URLSuffix": true, "AWS::NoValue": true, "AWS::NotificationARNs": true, "HC::StackName": true, "HC::AccountId": true, "HC::Region": true}
	for n := range t.Parameters {
		known[n] = true
	}
	for n := range t.Resources {
		known[n] = true
	}
	var bad []string
	seen := map[string]bool{}
	miss := func(n string) {
		if !known[n] && !seen[n] {
			seen[n] = true
			bad = append(bad, n)
		}
	}
	var walk func(v any)
	walk = func(v any) {
		switch x := v.(type) {
		case map[string]any:
			if ref, ok := x["Ref"].(string); ok {
				miss(ref)
			}
			switch ga := x["Fn::GetAtt"].(type) {
			case []any:
				if len(ga) > 0 {
					miss(toStr(ga[0]))
				}
			case string:
				id, _, _ := strings.Cut(ga, ".")
				miss(id)
			}
			if s, ok := x["Fn::Sub"]; ok {
				str, vars := toStr(s), map[string]any{}
				if arr, ok := s.([]any); ok && len(arr) == 2 {
					str = toStr(arr[0])
					vars, _ = arr[1].(map[string]any)
				}
				for _, m := range subRe.FindAllStringSubmatch(str, -1) {
					id, _, _ := strings.Cut(m[1], ".")
					if _, isVar := vars[m[1]]; !isVar && !strings.HasPrefix(id, "!") {
						miss(id)
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
	for _, r := range t.Resources {
		walk(r.Properties)
		switch d := r.DependsOn.(type) {
		case string:
			miss(d)
		case []any:
			for _, x := range d {
				miss(toStr(x))
			}
		}
	}
	for _, o := range t.Outputs {
		walk(o["Value"])
	}
	if len(bad) > 0 {
		sort.Strings(bad)
		return fmt.Errorf("Template format error: Unresolved resource dependencies [%s] in the Resources block of the template", strings.Join(bad, ", "))
	}
	return nil
}
