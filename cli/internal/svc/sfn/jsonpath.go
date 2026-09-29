package sfn

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// Reference paths: the JSONPath that Amazon States Language uses — "$",
// "$.a.b", "$['a b']", "$.list[0]", "$.list[-1]", "$.list[1:3]", "$.list[*].id",
// "$.*", "$.items[?(@.price > 10)]", "$.items[?(@.tag)]" — and "$$" for the
// context object. Paths with a wildcard, slice or filter select a list.

type segKind int

const (
	segKey segKind = iota
	segIndex
	segWildcard
	segSlice
	segFilter
)

type segment struct {
	kind       segKind
	key        string
	index      int
	start, end *int
	filter     *pathFilter
}

// pathFilter is "[?(@.field op value)]" or "[?(@.field)]".
type pathFilter struct {
	field []string
	op    string
	value any
}

func parsePath(p string) ([]segment, error) {
	if p == "$" || p == "$$" {
		return nil, nil
	}
	rest, ok := strings.CutPrefix(p, "$")
	if !ok {
		return nil, fmt.Errorf("path %q must start with $", p)
	}
	rest = strings.TrimPrefix(rest, "$") // context object paths ($$.Execution.Id)
	var segs []segment
	for rest != "" {
		switch {
		case strings.HasPrefix(rest, ".*"):
			segs = append(segs, segment{kind: segWildcard})
			rest = rest[2:]
		case strings.HasPrefix(rest, "."):
			rest = rest[1:]
			end := strings.IndexAny(rest, ".[")
			if end < 0 {
				end = len(rest)
			}
			if end == 0 {
				return nil, fmt.Errorf("empty segment in %q", p)
			}
			segs = append(segs, segment{kind: segKey, key: rest[:end]})
			rest = rest[end:]
		case strings.HasPrefix(rest, "['") || strings.HasPrefix(rest, `["`):
			q := rest[1:2]
			end := strings.Index(rest[2:], q+"]")
			if end < 0 {
				return nil, fmt.Errorf("unterminated bracket in %q", p)
			}
			segs = append(segs, segment{kind: segKey, key: rest[2 : 2+end]})
			rest = rest[end+4:]
		case strings.HasPrefix(rest, "[?("):
			end := strings.Index(rest, ")]")
			if end < 0 {
				return nil, fmt.Errorf("unterminated filter in %q", p)
			}
			f, err := parseFilter(rest[3:end])
			if err != nil {
				return nil, fmt.Errorf("path %q: %v", p, err)
			}
			segs = append(segs, segment{kind: segFilter, filter: f})
			rest = rest[end+2:]
		case strings.HasPrefix(rest, "["):
			end := strings.IndexByte(rest, ']')
			if end < 0 {
				return nil, fmt.Errorf("unterminated index in %q", p)
			}
			inner := strings.TrimSpace(rest[1:end])
			rest = rest[end+1:]
			if inner == "*" {
				segs = append(segs, segment{kind: segWildcard})
				continue
			}
			if a, b, isSlice := strings.Cut(inner, ":"); isSlice {
				s := segment{kind: segSlice}
				if a = strings.TrimSpace(a); a != "" {
					n, err := strconv.Atoi(a)
					if err != nil {
						return nil, fmt.Errorf("bad slice in %q", p)
					}
					s.start = &n
				}
				if b = strings.TrimSpace(b); b != "" {
					n, err := strconv.Atoi(b)
					if err != nil {
						return nil, fmt.Errorf("bad slice in %q", p)
					}
					s.end = &n
				}
				segs = append(segs, s)
				continue
			}
			n, err := strconv.Atoi(inner)
			if err != nil {
				return nil, fmt.Errorf("bad index in %q", p)
			}
			segs = append(segs, segment{kind: segIndex, index: n})
		default:
			return nil, fmt.Errorf("cannot parse path %q", p)
		}
	}
	return segs, nil
}

func parseFilter(expr string) (*pathFilter, error) {
	expr = strings.TrimSpace(expr)
	for _, op := range []string{"==", "!=", "<=", ">=", "<", ">"} {
		if l, r, ok := strings.Cut(expr, op); ok {
			f, err := filterField(l)
			if err != nil {
				return nil, err
			}
			r = strings.TrimSpace(r)
			var v any
			if strings.HasPrefix(r, "'") && strings.HasSuffix(r, "'") && len(r) >= 2 {
				v = r[1 : len(r)-1]
			} else if err := json.Unmarshal([]byte(r), &v); err != nil {
				return nil, fmt.Errorf("bad filter value %q", r)
			}
			return &pathFilter{field: f, op: op, value: v}, nil
		}
	}
	f, err := filterField(expr)
	if err != nil {
		return nil, err
	}
	return &pathFilter{field: f}, nil
}

func filterField(s string) ([]string, error) {
	s = strings.TrimSpace(s)
	rest, ok := strings.CutPrefix(s, "@")
	if !ok {
		return nil, fmt.Errorf("filters compare @ fields, got %q", s)
	}
	rest = strings.TrimPrefix(rest, ".")
	if rest == "" {
		return nil, nil
	}
	return strings.Split(rest, "."), nil
}

func (f *pathFilter) match(v any) bool {
	cur := v
	for _, k := range f.field {
		m, ok := cur.(map[string]any)
		if !ok {
			return false
		}
		if cur, ok = m[k]; !ok {
			return false
		}
	}
	if f.op == "" {
		return true
	}
	a, aNum := cur.(float64)
	b, bNum := f.value.(float64)
	if aNum && bNum {
		switch f.op {
		case "==":
			return a == b
		case "!=":
			return a != b
		case "<":
			return a < b
		case ">":
			return a > b
		case "<=":
			return a <= b
		case ">=":
			return a >= b
		}
	}
	as, aStr := cur.(string)
	bs, bStr := f.value.(string)
	if aStr && bStr {
		switch f.op {
		case "==":
			return as == bs
		case "!=":
			return as != bs
		case "<":
			return as < bs
		case ">":
			return as > bs
		case "<=":
			return as <= bs
		case ">=":
			return as >= bs
		}
	}
	switch f.op {
	case "==":
		return jsonEqual(cur, f.value)
	case "!=":
		return !jsonEqual(cur, f.value)
	}
	return false
}

func jsonEqual(a, b any) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return string(x) == string(y)
}

// get reads a path from a JSON value.
func get(doc any, p string) (any, error) {
	segs, err := parsePath(p)
	if err != nil {
		return nil, err
	}
	nodes := []any{doc}
	multi := false
	for _, s := range segs {
		var next []any
		for _, cur := range nodes {
			switch s.kind {
			case segKey:
				m, ok := cur.(map[string]any)
				if !ok {
					if multi {
						continue
					}
					return nil, fmt.Errorf("path %s: %q is not an object", p, s.key)
				}
				v, ok := m[s.key]
				if !ok {
					if multi {
						continue
					}
					return nil, fmt.Errorf("path %s: field %q not found", p, s.key)
				}
				next = append(next, v)
			case segIndex:
				arr, ok := cur.([]any)
				i := s.index
				if ok && i < 0 {
					i += len(arr)
				}
				if !ok || i < 0 || i >= len(arr) {
					if multi {
						continue
					}
					return nil, fmt.Errorf("path %s: no index %d", p, s.index)
				}
				next = append(next, arr[i])
			case segWildcard:
				switch c := cur.(type) {
				case []any:
					next = append(next, c...)
				case map[string]any:
					for _, v := range c {
						next = append(next, v)
					}
				}
			case segSlice:
				arr, ok := cur.([]any)
				if !ok {
					continue
				}
				a, b := 0, len(arr)
				if s.start != nil {
					a = *s.start
					if a < 0 {
						a += len(arr)
					}
				}
				if s.end != nil {
					b = *s.end
					if b < 0 {
						b += len(arr)
					}
				}
				a, b = max(0, min(a, len(arr))), max(0, min(b, len(arr)))
				if a < b {
					next = append(next, arr[a:b]...)
				}
			case segFilter:
				arr, ok := cur.([]any)
				if !ok {
					continue
				}
				for _, v := range arr {
					if s.filter.match(v) {
						next = append(next, v)
					}
				}
			}
		}
		if s.kind == segWildcard || s.kind == segSlice || s.kind == segFilter {
			multi = true
		}
		nodes = next
	}
	if multi {
		if nodes == nil {
			nodes = []any{}
		}
		return nodes, nil
	}
	return nodes[0], nil
}

// set writes value at a path inside doc, creating objects as needed, and returns the new document.
func set(doc any, p string, value any) (any, error) {
	segs, err := parsePath(p)
	if err != nil {
		return nil, err
	}
	if len(segs) == 0 {
		return value, nil
	}
	root, ok := clone(doc).(map[string]any)
	if !ok {
		root = map[string]any{}
	}
	cur := root
	for i, s := range segs {
		if s.kind != segKey {
			return nil, fmt.Errorf("ResultPath %s may use only field names", p)
		}
		if i == len(segs)-1 {
			cur[s.key] = value
			break
		}
		next, ok := cur[s.key].(map[string]any)
		if !ok {
			next = map[string]any{}
			cur[s.key] = next
		}
		cur = next
	}
	return root, nil
}

func clone(v any) any {
	b, _ := json.Marshal(v)
	var out any
	_ = json.Unmarshal(b, &out)
	return out
}

// resolveParams evaluates a Parameters/ResultSelector/ItemSelector template:
// keys ending in ".$" hold paths (input, or the context object for "$$") or
// intrinsic functions.
func resolveParams(tmpl any, input, ctx any) (any, error) {
	switch t := tmpl.(type) {
	case map[string]any:
		out := map[string]any{}
		for k, v := range t {
			if name, ok := strings.CutSuffix(k, ".$"); ok {
				p, isStr := v.(string)
				if !isStr {
					return nil, fail("States.Runtime", "the value of %s must be a path or an intrinsic function", k)
				}
				val, err := evalRef(p, input, ctx)
				if err != nil {
					return nil, err
				}
				out[name] = val
				continue
			}
			r, err := resolveParams(v, input, ctx)
			if err != nil {
				return nil, err
			}
			out[k] = r
		}
		return out, nil
	case []any:
		out := make([]any, len(t))
		for i, v := range t {
			r, err := resolveParams(v, input, ctx)
			if err != nil {
				return nil, err
			}
			out[i] = r
		}
		return out, nil
	}
	return tmpl, nil
}

// evalRef evaluates a ".$" value: a path, a context path or an intrinsic function.
func evalRef(p string, input, ctx any) (any, error) {
	p = strings.TrimSpace(p)
	switch {
	case strings.HasPrefix(p, "States."):
		return evalIntrinsic(p, input, ctx)
	case strings.HasPrefix(p, "$$"):
		v, err := get(ctx, p)
		if err != nil {
			return nil, fail("States.Runtime", "%v", err)
		}
		return v, nil
	}
	v, err := get(input, p)
	if err != nil {
		return nil, fail("States.Runtime", "%v", err)
	}
	return v, nil
}
