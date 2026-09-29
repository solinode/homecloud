package sns

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"net"
	"strings"
)

// Subscription filter policies, with the semantics of Amazon SNS:
//
//	{"store": ["example_corp"]}                         exact string match
//	{"price": [{"numeric": [">=", 100, "<", 200]}]}     numeric ranges; 5 matches 5.0
//	{"event": [{"prefix": "order-"}, {"suffix": ".png"}, {"equals-ignore-case": "PAID"}]}
//	{"region": [{"anything-but": ["us-east-1"]}]}      also {"anything-but": {"prefix": "x"}}
//	{"customer": [{"exists": false}]}
//	{"source_ip": [{"cidr": "10.0.0.0/24"}]}
//	{"$or": [{"a": ["x"]}, {"b": ["y"]}]}
//
// Keys are ANDed and the values of one key are ORed. With FilterPolicyScope
// MessageAttributes (the default) keys name message attributes: String
// attributes match string conditions, Number attributes numeric ones, and a
// String.Array matches if any element does. With scope MessageBody the policy
// is matched against the JSON message body and may nest objects; arrays in the
// body match if any element does.

const (
	scopeAttributes = "MessageAttributes"
	scopeBody       = "MessageBody"
)

type filterError string

func (e filterError) Error() string { return string(e) }

func decodeJSON(b []byte) (any, error) {
	d := json.NewDecoder(bytes.NewReader(b))
	d.UseNumber()
	var v any
	if err := d.Decode(&v); err != nil {
		return nil, err
	}
	if d.More() {
		return nil, fmt.Errorf("trailing data")
	}
	return v, nil
}

// parsePolicy validates a filter policy and returns it decoded. An empty
// policy ("" or {}) returns nil.
func parsePolicy(raw []byte, scope string) (map[string]any, error) {
	if len(bytes.TrimSpace(raw)) == 0 || string(bytes.TrimSpace(raw)) == "null" {
		return nil, nil
	}
	v, err := decodeJSON(raw)
	if err != nil {
		return nil, filterError("failed to parse JSON")
	}
	m, ok := v.(map[string]any)
	if !ok {
		return nil, filterError("Filter policy must be a JSON object")
	}
	if len(m) == 0 {
		return nil, nil
	}
	combos, err := checkObject(m, scope, 0)
	if err != nil {
		return nil, err
	}
	if combos > 150 {
		return nil, filterError(fmt.Sprintf("Filter policy is too complex (%d combinations, the limit is 150)", combos))
	}
	return m, nil
}

// checkObject validates a policy object and returns its number of value combinations.
func checkObject(m map[string]any, scope string, depth int) (int, error) {
	if depth > 5 {
		return 0, filterError("Filter policy is nested too deeply")
	}
	combos := 1
	for k, v := range m {
		if k == "$or" {
			arr, ok := v.([]any)
			if !ok || len(arr) < 2 {
				return 0, filterError("$or must be an array of at least two objects")
			}
			sum := 0
			for _, alt := range arr {
				o, ok := alt.(map[string]any)
				if !ok || len(o) == 0 {
					return 0, filterError("$or must contain objects")
				}
				c, err := checkObject(o, scope, depth+1)
				if err != nil {
					return 0, err
				}
				sum += c
			}
			combos *= sum
			continue
		}
		switch t := v.(type) {
		case []any:
			if len(t) == 0 {
				return 0, filterError(fmt.Sprintf("Empty arrays are not allowed (key %q)", k))
			}
			for _, c := range t {
				if err := checkCondition(c); err != nil {
					return 0, filterError(fmt.Sprintf("%s (key %q)", err.Error(), k))
				}
			}
			combos *= len(t)
		case map[string]any:
			if scope != scopeBody {
				return 0, filterError(fmt.Sprintf("Nested objects require FilterPolicyScope MessageBody (key %q)", k))
			}
			c, err := checkObject(t, scope, depth+1)
			if err != nil {
				return 0, err
			}
			combos *= c
		default:
			return 0, filterError(fmt.Sprintf("Match value must be an array (key %q)", k))
		}
	}
	return combos, nil
}

func checkCondition(c any) error {
	switch t := c.(type) {
	case string, json.Number, bool, nil:
		return nil
	case map[string]any:
		if len(t) != 1 {
			return filterError("Match conditions must have exactly one operator")
		}
		for op, v := range t {
			switch op {
			case "prefix", "suffix", "equals-ignore-case", "wildcard":
				if _, ok := v.(string); !ok {
					return filterError(op + " match pattern must be a string")
				}
			case "exists":
				if _, ok := v.(bool); !ok {
					return filterError("exists match pattern must be either true or false")
				}
			case "cidr":
				s, _ := v.(string)
				if _, _, err := net.ParseCIDR(s); err != nil {
					return filterError("Malformed CIDR")
				}
			case "numeric":
				if _, err := numericRange(v); err != nil {
					return err
				}
			case "anything-but":
				switch a := v.(type) {
				case string, json.Number:
				case []any:
					if len(a) == 0 {
						return filterError("anything-but list must not be empty")
					}
					for _, x := range a {
						switch x.(type) {
						case string, json.Number:
						default:
							return filterError("anything-but list must contain strings or numbers")
						}
					}
				case map[string]any:
					if len(a) != 1 {
						return filterError("anything-but object must have one operator")
					}
					for op2, v2 := range a {
						switch op2 {
						case "prefix", "suffix":
							if _, ok := v2.(string); !ok {
								return filterError("anything-but " + op2 + " must be a string")
							}
						case "equals-ignore-case":
							switch v2.(type) {
							case string, []any:
							default:
								return filterError("anything-but equals-ignore-case must be a string or list")
							}
						default:
							return filterError("Unsupported anything-but operator: " + op2)
						}
					}
				default:
					return filterError("Invalid anything-but value")
				}
			default:
				return filterError("Unrecognized match type " + op)
			}
		}
		return nil
	}
	return filterError("Match value must be a string, number, boolean, null or object")
}

type bound struct {
	op string
	v  float64
}

func numericRange(v any) ([]bound, error) {
	arr, ok := v.([]any)
	if !ok || (len(arr) != 2 && len(arr) != 4) {
		return nil, filterError("Value of numeric must be an array of one or two comparisons")
	}
	var out []bound
	for i := 0; i < len(arr); i += 2 {
		op, _ := arr[i].(string)
		n, ok := arr[i+1].(json.Number)
		if !ok {
			return nil, filterError("Value of " + op + " must be numeric")
		}
		f, err := n.Float64()
		if err != nil || math.Abs(f) > 1e9 {
			return nil, filterError("Numeric values must be between -1.0E9 and 1.0E9")
		}
		switch op {
		case "=", "<", "<=", ">", ">=":
		default:
			return nil, filterError("Unrecognized numeric range operator: " + op)
		}
		if len(arr) == 4 && (op == "=" || (i == 0 && op[0] != '>') || (i == 2 && op[0] != '<')) {
			return nil, filterError("Bad numeric range: use a lower bound (>, >=) then an upper bound (<, <=)")
		}
		out = append(out, bound{op, f})
	}
	if len(out) == 2 && out[0].v >= out[1].v {
		return nil, filterError("Bottom must be less than top")
	}
	return out, nil
}

// lookup returns the values at a key path and whether the key is present.
type lookup func(path []string) (vals []any, present bool)

// matchPolicy evaluates a policy against a message.
func matchPolicy(policy map[string]any, scope string, attrs map[string]Attribute, body string) bool {
	if policy == nil {
		return true
	}
	var get lookup
	if scope == scopeBody {
		doc, err := decodeJSON([]byte(body))
		if err != nil {
			return false
		}
		if _, ok := doc.(map[string]any); !ok {
			return false
		}
		get = func(path []string) ([]any, bool) {
			vals := resolve(doc, path)
			return vals, len(vals) > 0
		}
	} else {
		get = func(path []string) ([]any, bool) {
			a, ok := attrs[path[0]]
			if !ok || len(path) != 1 {
				return nil, false
			}
			base, _, _ := strings.Cut(a.DataType, ".")
			switch {
			case a.DataType == "String.Array":
				v, err := decodeJSON([]byte(a.StringValue))
				if arr, ok := v.([]any); ok && err == nil {
					return arr, true
				}
				return nil, true
			case base == "String":
				return []any{a.StringValue}, true
			case base == "Number":
				return []any{json.Number(strings.TrimSpace(a.StringValue))}, true
			}
			return nil, false // Binary attributes are not matched
		}
	}
	return matchObject(policy, get, nil)
}

// resolve collects the leaf values at path in a JSON document, descending
// into arrays; a JSON array leaf contributes its elements.
func resolve(v any, path []string) []any {
	if len(path) == 0 {
		if arr, ok := v.([]any); ok {
			var out []any
			for _, x := range arr {
				if _, isObj := x.(map[string]any); !isObj {
					out = append(out, x)
				}
			}
			return out
		}
		return []any{v}
	}
	switch t := v.(type) {
	case map[string]any:
		child, ok := t[path[0]]
		if !ok {
			return nil
		}
		return resolve(child, path[1:])
	case []any:
		var out []any
		for _, x := range t {
			out = append(out, resolve(x, path)...)
		}
		return out
	}
	return nil
}

func matchObject(policy map[string]any, get lookup, path []string) bool {
	for k, v := range policy {
		if k == "$or" {
			ok := false
			for _, alt := range v.([]any) {
				if matchObject(alt.(map[string]any), get, path) {
					ok = true
					break
				}
			}
			if !ok {
				return false
			}
			continue
		}
		p := append(append([]string(nil), path...), k)
		switch t := v.(type) {
		case map[string]any:
			if !matchObject(t, get, p) {
				return false
			}
		case []any:
			vals, present := get(p)
			if !matchAny(t, vals, present) {
				return false
			}
		}
	}
	return true
}

func matchAny(conds, vals []any, present bool) bool {
	for _, c := range conds {
		if matchCondition(c, vals, present) {
			return true
		}
	}
	return false
}

func num(v any) (float64, bool) {
	switch t := v.(type) {
	case json.Number:
		f, err := t.Float64()
		return f, err == nil
	case float64:
		return t, true
	}
	return 0, false
}

func matchCondition(c any, vals []any, present bool) bool {
	anyVal := func(f func(v any) bool) bool {
		for _, v := range vals {
			if f(v) {
				return true
			}
		}
		return false
	}
	switch t := c.(type) {
	case string:
		return anyVal(func(v any) bool { s, ok := v.(string); return ok && s == t })
	case json.Number:
		want, _ := t.Float64()
		return anyVal(func(v any) bool { f, ok := num(v); return ok && f == want })
	case bool:
		return anyVal(func(v any) bool { b, ok := v.(bool); return ok && b == t })
	case nil:
		return anyVal(func(v any) bool { return v == nil })
	case map[string]any:
		for op, arg := range t {
			switch op {
			case "exists":
				return present == arg.(bool)
			case "prefix":
				p := arg.(string)
				return anyVal(func(v any) bool { s, ok := v.(string); return ok && strings.HasPrefix(s, p) })
			case "suffix":
				p := arg.(string)
				return anyVal(func(v any) bool { s, ok := v.(string); return ok && strings.HasSuffix(s, p) })
			case "equals-ignore-case":
				p := arg.(string)
				return anyVal(func(v any) bool { s, ok := v.(string); return ok && strings.EqualFold(s, p) })
			case "wildcard":
				p := arg.(string)
				return anyVal(func(v any) bool { s, ok := v.(string); return ok && wildcard(p, s) })
			case "cidr":
				_, n, _ := net.ParseCIDR(arg.(string))
				return anyVal(func(v any) bool {
					s, ok := v.(string)
					ip := net.ParseIP(s)
					return ok && ip != nil && n.Contains(ip)
				})
			case "numeric":
				bounds, _ := numericRange(arg)
				return anyVal(func(v any) bool {
					f, ok := num(v)
					if !ok {
						return false
					}
					for _, b := range bounds {
						if !compare(f, b) {
							return false
						}
					}
					return true
				})
			case "anything-but":
				if !present {
					return false
				}
				return anyVal(func(v any) bool { return !excluded(arg, v) })
			}
		}
	}
	return false
}

func compare(f float64, b bound) bool {
	switch b.op {
	case "=":
		return f == b.v
	case "<":
		return f < b.v
	case "<=":
		return f <= b.v
	case ">":
		return f > b.v
	case ">=":
		return f >= b.v
	}
	return false
}

// excluded reports whether v is one of the anything-but values.
func excluded(arg, v any) bool {
	eq := func(x any) bool {
		switch t := x.(type) {
		case string:
			s, ok := v.(string)
			return ok && s == t
		case json.Number:
			want, _ := t.Float64()
			f, ok := num(v)
			return ok && f == want
		}
		return false
	}
	switch t := arg.(type) {
	case string, json.Number:
		return eq(t)
	case []any:
		for _, x := range t {
			if eq(x) {
				return true
			}
		}
	case map[string]any:
		s, ok := v.(string)
		if !ok {
			return false
		}
		for op, p := range t {
			switch op {
			case "prefix":
				return strings.HasPrefix(s, p.(string))
			case "suffix":
				return strings.HasSuffix(s, p.(string))
			case "equals-ignore-case":
				if list, ok := p.([]any); ok {
					for _, x := range list {
						if xs, ok := x.(string); ok && strings.EqualFold(xs, s) {
							return true
						}
					}
					return false
				}
				ps, _ := p.(string)
				return strings.EqualFold(ps, s)
			}
		}
	}
	return false
}

// wildcard matches s against a pattern where * matches any run of characters.
func wildcard(p, s string) bool {
	parts := strings.Split(p, "*")
	if len(parts) == 1 {
		return p == s
	}
	if !strings.HasPrefix(s, parts[0]) {
		return false
	}
	s = s[len(parts[0]):]
	for _, mid := range parts[1 : len(parts)-1] {
		i := strings.Index(s, mid)
		if i < 0 {
			return false
		}
		s = s[i+len(mid):]
	}
	return strings.HasSuffix(s, parts[len(parts)-1])
}
