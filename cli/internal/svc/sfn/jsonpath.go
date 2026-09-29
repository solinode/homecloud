package sfn

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// Reference paths support the subset of JSONPath that Amazon States Language
// uses in practice: "$", "$.a.b", "$['a']", "$.list[0]" and "$$" for the context object.

type segment struct {
	key   string
	index int
	isIdx bool
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
		case strings.HasPrefix(rest, "."):
			rest = rest[1:]
			end := strings.IndexAny(rest, ".[")
			if end < 0 {
				end = len(rest)
			}
			if end == 0 {
				return nil, fmt.Errorf("empty segment in %q", p)
			}
			segs = append(segs, segment{key: rest[:end]})
			rest = rest[end:]
		case strings.HasPrefix(rest, "['"):
			end := strings.Index(rest, "']")
			if end < 0 {
				return nil, fmt.Errorf("unterminated bracket in %q", p)
			}
			segs = append(segs, segment{key: rest[2:end]})
			rest = rest[end+2:]
		case strings.HasPrefix(rest, "["):
			end := strings.Index(rest, "]")
			if end < 0 {
				return nil, fmt.Errorf("unterminated index in %q", p)
			}
			n, err := strconv.Atoi(rest[1:end])
			if err != nil {
				return nil, fmt.Errorf("bad index in %q", p)
			}
			segs = append(segs, segment{index: n, isIdx: true})
			rest = rest[end+1:]
		default:
			return nil, fmt.Errorf("cannot parse path %q", p)
		}
	}
	return segs, nil
}

// get reads a path from a JSON value.
func get(doc any, p string) (any, error) {
	segs, err := parsePath(p)
	if err != nil {
		return nil, err
	}
	cur := doc
	for _, s := range segs {
		if s.isIdx {
			arr, ok := cur.([]any)
			if !ok || s.index < 0 || s.index >= len(arr) {
				return nil, fmt.Errorf("path %s: no index %d", p, s.index)
			}
			cur = arr[s.index]
			continue
		}
		m, ok := cur.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("path %s: %q is not an object", p, s.key)
		}
		v, ok := m[s.key]
		if !ok {
			return nil, fmt.Errorf("path %s: field %q not found", p, s.key)
		}
		cur = v
	}
	return cur, nil
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
		if s.isIdx {
			return nil, fmt.Errorf("ResultPath %s may not use array indexes", p)
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

// resolveParams evaluates a Parameters/ResultSelector template: keys ending in
// ".$" are paths evaluated against input (or the context object for "$$").
func resolveParams(tmpl any, input, ctx any) (any, error) {
	switch t := tmpl.(type) {
	case map[string]any:
		out := map[string]any{}
		for k, v := range t {
			if name, ok := strings.CutSuffix(k, ".$"); ok {
				p, _ := v.(string)
				src := input
				if strings.HasPrefix(p, "$$") {
					src = ctx
				}
				if strings.HasPrefix(p, "States.") {
					val, err := intrinsic(p, input, ctx)
					if err != nil {
						return nil, err
					}
					out[name] = val
					continue
				}
				val, err := get(src, p)
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

// intrinsic supports States.Format('...{}', $.path) and States.JsonToString / StringToJson.
func intrinsic(expr string, input, ctx any) (any, error) {
	name, args, ok := strings.Cut(expr, "(")
	if !ok || !strings.HasSuffix(args, ")") {
		return nil, fmt.Errorf("bad intrinsic %q", expr)
	}
	parts := splitArgs(strings.TrimSuffix(args, ")"))
	val := func(a string) (any, error) {
		a = strings.TrimSpace(a)
		if strings.HasPrefix(a, "'") && strings.HasSuffix(a, "'") {
			return strings.ReplaceAll(a[1:len(a)-1], `\'`, "'"), nil
		}
		if strings.HasPrefix(a, "$$") {
			return get(ctx, a)
		}
		if strings.HasPrefix(a, "$") {
			return get(input, a)
		}
		var v any
		if err := json.Unmarshal([]byte(a), &v); err != nil {
			return nil, fmt.Errorf("bad argument %q", a)
		}
		return v, nil
	}
	switch name {
	case "States.Format":
		if len(parts) == 0 {
			return nil, fmt.Errorf("States.Format needs a template")
		}
		t, err := val(parts[0])
		if err != nil {
			return nil, err
		}
		out := fmt.Sprint(t)
		for _, a := range parts[1:] {
			v, err := val(a)
			if err != nil {
				return nil, err
			}
			out = strings.Replace(out, "{}", fmt.Sprint(v), 1)
		}
		return out, nil
	case "States.JsonToString":
		v, err := val(parts[0])
		if err != nil {
			return nil, err
		}
		b, _ := json.Marshal(v)
		return string(b), nil
	case "States.StringToJson":
		v, err := val(parts[0])
		if err != nil {
			return nil, err
		}
		var out any
		if err := json.Unmarshal([]byte(fmt.Sprint(v)), &out); err != nil {
			return nil, err
		}
		return out, nil
	case "States.Array":
		out := []any{}
		for _, a := range parts {
			v, err := val(a)
			if err != nil {
				return nil, err
			}
			out = append(out, v)
		}
		return out, nil
	}
	return nil, fmt.Errorf("unsupported intrinsic %s", name)
}

func splitArgs(s string) []string {
	var out []string
	depth, quote, start := 0, false, 0
	for i, r := range s {
		switch {
		case r == '\'' && (i == 0 || s[i-1] != '\\'):
			quote = !quote
		case quote:
		case r == '(' || r == '[' || r == '{':
			depth++
		case r == ')' || r == ']' || r == '}':
			depth--
		case r == ',' && depth == 0:
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	if strings.TrimSpace(s[start:]) != "" {
		out = append(out, s[start:])
	}
	return out
}
