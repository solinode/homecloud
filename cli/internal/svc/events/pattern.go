package events

import (
	"encoding/json"
	"fmt"
	"math"
	"net"
	"regexp"
	"strings"
)

// Event patterns, with EventBridge's full syntax:
//
//	{"source": ["app.orders"]}                         exact values (strings, numbers, booleans, null)
//	{"detail": {"state": ["running"]}}                 nested fields; arrays in events match if any element matches
//	{"prefix": "a"} / {"prefix": {"equals-ignore-case": "a"}}
//	{"suffix": ".png"} / {"suffix": {"equals-ignore-case": ".PNG"}}
//	{"anything-but": "x"} / ["x","y"] / {"prefix": "x"} / {"suffix": "x"} / {"equals-ignore-case": ...} / {"wildcard": ...}
//	{"numeric": [">", 0, "<=", 5]}
//	{"exists": true|false}
//	{"cidr": "10.0.0.0/24"}
//	{"equals-ignore-case": "x"}
//	{"wildcard": "img/*.png"}
//	{"$or": [{...}, {...}]}

// Pattern is a compiled event pattern.
type Pattern struct{ root objMatcher }

type objMatcher struct {
	fields map[string]fieldMatcher
	or     [][]objMatcher // each $or: any alternative must match
}

type fieldMatcher struct {
	nested *objMatcher
	values []valueMatcher // leaf: any must match
}

// valueMatcher tests a leaf value; present is false when the field is absent.
type valueMatcher func(v any, present bool) bool

// PatternError is an invalid pattern (InvalidEventPatternException).
type PatternError struct{ msg string }

func (e *PatternError) Error() string { return "Event pattern is not valid. Reason: " + e.msg }

func perr(format string, a ...any) error { return &PatternError{fmt.Sprintf(format, a...)} }

// CompilePattern parses and validates an event pattern.
func CompilePattern(raw []byte) (*Pattern, error) {
	var doc any
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.UseNumber()
	if err := dec.Decode(&doc); err != nil {
		return nil, perr("the pattern is not valid JSON: %v", err)
	}
	obj, ok := doc.(map[string]any)
	if !ok {
		return nil, perr("the pattern must be a JSON object")
	}
	if len(obj) == 0 {
		return nil, perr("the pattern must not be empty")
	}
	m, err := compileObject(obj, "")
	if err != nil {
		return nil, err
	}
	return &Pattern{root: m}, nil
}

func compileObject(obj map[string]any, path string) (objMatcher, error) {
	m := objMatcher{fields: map[string]fieldMatcher{}}
	for k, v := range obj {
		p := strings.TrimPrefix(path+"."+k, ".")
		if k == "$or" {
			alts, ok := v.([]any)
			if !ok || len(alts) < 2 {
				return m, perr("$or at %s must be an array of at least two patterns", path)
			}
			var group []objMatcher
			for _, a := range alts {
				ao, ok := a.(map[string]any)
				if !ok || len(ao) == 0 {
					return m, perr("each $or alternative at %s must be a non-empty object", path)
				}
				sub, err := compileObject(ao, path)
				if err != nil {
					return m, err
				}
				group = append(group, sub)
			}
			m.or = append(m.or, group)
			continue
		}
		switch x := v.(type) {
		case map[string]any:
			if len(x) == 0 {
				return m, perr("%s must not be an empty object", p)
			}
			sub, err := compileObject(x, p)
			if err != nil {
				return m, err
			}
			m.fields[k] = fieldMatcher{nested: &sub}
		case []any:
			var vms []valueMatcher
			for _, item := range x {
				vm, err := compileValue(item, p)
				if err != nil {
					return m, err
				}
				vms = append(vms, vm)
			}
			m.fields[k] = fieldMatcher{values: vms}
		default:
			return m, perr("match values at %s must be in an array", p)
		}
	}
	return m, nil
}

func num(v any) (float64, bool) {
	switch x := v.(type) {
	case json.Number:
		f, err := x.Float64()
		return f, err == nil
	case float64:
		return x, true
	}
	return 0, false
}

// equalLiteral compares a pattern literal with an event value.
func equalLiteral(lit, v any) bool {
	switch l := lit.(type) {
	case nil:
		return v == nil
	case string:
		s, ok := v.(string)
		return ok && s == l
	case bool:
		b, ok := v.(bool)
		return ok && b == l
	}
	if a, ok := num(lit); ok {
		b, ok := num(v)
		return ok && a == b
	}
	return false
}

func compileValue(item any, path string) (valueMatcher, error) {
	obj, isObj := item.(map[string]any)
	if !isObj {
		if _, isArr := item.([]any); isArr {
			return nil, perr("nested arrays are not allowed at %s", path)
		}
		lit := item
		return func(v any, present bool) bool { return present && equalLiteral(lit, v) }, nil
	}
	if len(obj) != 1 {
		return nil, perr("a matcher object at %s must have exactly one key", path)
	}
	for op, arg := range obj {
		switch op {
		case "exists":
			want, ok := arg.(bool)
			if !ok {
				return nil, perr("exists at %s must be true or false", path)
			}
			return func(v any, present bool) bool {
				_, isObj := v.(map[string]any)
				return (present && !isObj) == want
			}, nil
		case "prefix", "suffix":
			test, err := stringTest(op, arg, path)
			if err != nil {
				return nil, err
			}
			return func(v any, present bool) bool { s, ok := v.(string); return present && ok && test(s) }, nil
		case "equals-ignore-case":
			s, ok := arg.(string)
			if !ok {
				return nil, perr("equals-ignore-case at %s must be a string", path)
			}
			return func(v any, present bool) bool { x, ok := v.(string); return present && ok && strings.EqualFold(x, s) }, nil
		case "wildcard":
			s, ok := arg.(string)
			if !ok {
				return nil, perr("wildcard at %s must be a string", path)
			}
			re, err := wildcardRegexp(s, path)
			if err != nil {
				return nil, err
			}
			return func(v any, present bool) bool { x, ok := v.(string); return present && ok && re.MatchString(x) }, nil
		case "numeric":
			test, err := numericTest(arg, path)
			if err != nil {
				return nil, err
			}
			return func(v any, present bool) bool {
				f, ok := num(v)
				return present && ok && test(f)
			}, nil
		case "cidr":
			s, ok := arg.(string)
			if !ok {
				return nil, perr("cidr at %s must be a string", path)
			}
			_, network, err := net.ParseCIDR(s)
			if err != nil {
				return nil, perr("cidr at %s is not a valid CIDR block", path)
			}
			return func(v any, present bool) bool {
				x, ok := v.(string)
				ip := net.ParseIP(x)
				return present && ok && ip != nil && network.Contains(ip)
			}, nil
		case "anything-but":
			test, err := anythingBut(arg, path)
			if err != nil {
				return nil, err
			}
			return func(v any, present bool) bool { return present && test(v) }, nil
		default:
			return nil, perr("unrecognized match type %s at %s", op, path)
		}
	}
	return nil, perr("empty matcher at %s", path)
}

// stringTest compiles prefix/suffix matchers ("x" or {"equals-ignore-case": "x"}).
func stringTest(op string, arg any, path string) (func(string) bool, error) {
	fold := false
	s, ok := arg.(string)
	if !ok {
		m, isObj := arg.(map[string]any)
		inner, isStr := m["equals-ignore-case"].(string)
		if !isObj || len(m) != 1 || !isStr {
			return nil, perr("%s at %s must be a string or {\"equals-ignore-case\": string}", op, path)
		}
		s, fold = inner, true
	}
	if fold {
		s = strings.ToLower(s)
	}
	return func(x string) bool {
		if fold {
			x = strings.ToLower(x)
		}
		if op == "prefix" {
			return strings.HasPrefix(x, s)
		}
		return strings.HasSuffix(x, s)
	}, nil
}

func wildcardRegexp(s, path string) (*regexp.Regexp, error) {
	if strings.Contains(s, "**") {
		return nil, perr("wildcard at %s must not contain consecutive *", path)
	}
	var b strings.Builder
	b.WriteString("^")
	for i := 0; i < len(s); i++ {
		switch {
		case s[i] == '\\' && i+1 < len(s) && (s[i+1] == '*' || s[i+1] == '\\'):
			b.WriteString(regexp.QuoteMeta(string(s[i+1])))
			i++
		case s[i] == '*':
			b.WriteString(".*")
		default:
			b.WriteString(regexp.QuoteMeta(string(s[i])))
		}
	}
	b.WriteString("$")
	return regexp.MustCompile("(?s)" + b.String()), nil
}

func numericTest(arg any, path string) (func(float64) bool, error) {
	cond, ok := arg.([]any)
	if !ok || len(cond) == 0 || len(cond)%2 != 0 || len(cond) > 4 {
		return nil, perr("numeric at %s must be [op, number] or [op, number, op, number]", path)
	}
	type c struct {
		op string
		n  float64
	}
	var cs []c
	for i := 0; i < len(cond); i += 2 {
		op, ok1 := cond[i].(string)
		n, ok2 := num(cond[i+1])
		if !ok1 || !ok2 {
			return nil, perr("numeric at %s must alternate operators and numbers", path)
		}
		switch op {
		case "<", "<=", "=", ">", ">=":
		default:
			return nil, perr("unrecognized numeric operator %q at %s", op, path)
		}
		if math.Abs(n) > 5e9 {
			return nil, perr("numeric values at %s must be between -5e9 and 5e9", path)
		}
		cs = append(cs, c{op, n})
	}
	if len(cs) == 2 && (cs[0].op == "=" || cs[1].op == "=" || strings.HasPrefix(cs[0].op, strings.TrimSuffix(cs[1].op, "="))) {
		return nil, perr("a numeric range at %s needs a lower and an upper bound", path)
	}
	return func(v float64) bool {
		for _, x := range cs {
			ok := map[string]bool{"<": v < x.n, "<=": v <= x.n, "=": v == x.n, ">": v > x.n, ">=": v >= x.n}[x.op]
			if !ok {
				return false
			}
		}
		return true
	}, nil
}

func anythingBut(arg any, path string) (func(any) bool, error) {
	switch a := arg.(type) {
	case []any:
		for _, x := range a {
			switch x.(type) {
			case string, json.Number, float64:
			default:
				return nil, perr("anything-but lists at %s may contain only strings or numbers", path)
			}
		}
		return func(v any) bool {
			for _, x := range a {
				if equalLiteral(x, v) {
					return false
				}
			}
			return true
		}, nil
	case map[string]any:
		if len(a) != 1 {
			return nil, perr("anything-but at %s takes one of prefix, suffix, equals-ignore-case or wildcard", path)
		}
		for op, inner := range a {
			var tests []func(string) bool
			vals := []any{inner}
			if list, ok := inner.([]any); ok && (op == "equals-ignore-case" || op == "wildcard") {
				vals = list
			}
			for _, iv := range vals {
				s, ok := iv.(string)
				if !ok {
					return nil, perr("anything-but %s at %s must be a string", op, path)
				}
				switch op {
				case "prefix":
					tests = append(tests, func(x string) bool { return strings.HasPrefix(x, s) })
				case "suffix":
					tests = append(tests, func(x string) bool { return strings.HasSuffix(x, s) })
				case "equals-ignore-case":
					tests = append(tests, func(x string) bool { return strings.EqualFold(x, s) })
				case "wildcard":
					re, err := wildcardRegexp(s, path)
					if err != nil {
						return nil, err
					}
					tests = append(tests, re.MatchString)
				default:
					return nil, perr("anything-but does not support %s (at %s)", op, path)
				}
			}
			return func(v any) bool {
				s, ok := v.(string)
				if !ok {
					return false
				}
				for _, t := range tests {
					if t(s) {
						return false
					}
				}
				return true
			}, nil
		}
	case string, json.Number, float64:
		return func(v any) bool { return !equalLiteral(a, v) }, nil
	}
	return nil, perr("anything-but at %s must be a value, a list or a matcher", path)
}

// Match reports whether an event (decoded JSON) matches the pattern.
func (p *Pattern) Match(event map[string]any) bool { return p.root.match(event) }

func (m objMatcher) match(ev map[string]any) bool {
	for k, f := range m.fields {
		v, present := ev[k]
		if f.nested != nil {
			if !matchNested(*f.nested, v) {
				return false
			}
			continue
		}
		if !matchLeaf(f.values, v, present) {
			return false
		}
	}
	for _, group := range m.or {
		any := false
		for _, alt := range group {
			if alt.match(ev) {
				any = true
				break
			}
		}
		if !any {
			return false
		}
	}
	return true
}

// matchNested matches an object pattern against a value that is an object
// or an array of objects (EventBridge flattens arrays).
func matchNested(m objMatcher, v any) bool {
	switch x := v.(type) {
	case map[string]any:
		return m.match(x)
	case []any:
		for _, e := range x {
			if o, ok := e.(map[string]any); ok && m.match(o) {
				return true
			}
		}
		return false
	case nil:
		// A missing object still matches when every nested matcher accepts absence.
		return m.match(map[string]any{})
	}
	return false
}

func matchLeaf(vms []valueMatcher, v any, present bool) bool {
	vals := []any{v}
	if arr, ok := v.([]any); ok {
		if len(arr) == 0 {
			vals = nil
		} else {
			vals = arr
		}
	}
	for _, vm := range vms {
		if len(vals) == 0 {
			if vm(nil, false) {
				return true
			}
			continue
		}
		for _, x := range vals {
			if vm(x, present) {
				return true
			}
		}
	}
	return false
}

// MatchJSON matches a raw pattern against a raw event.
func MatchJSON(pattern, event []byte) (bool, error) {
	p, err := CompilePattern(pattern)
	if err != nil {
		return false, err
	}
	var ev map[string]any
	dec := json.NewDecoder(strings.NewReader(string(event)))
	dec.UseNumber()
	if err := dec.Decode(&ev); err != nil {
		return false, fmt.Errorf("the event is not a JSON object")
	}
	return p.Match(ev), nil
}
