package iam

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/homecloudhq/homecloud/cli/internal/core"
)

// CondContext holds the condition keys of a request (aws:SourceIp,
// aws:username, ...). Keys are lower-case; a key may have several values.
// aws:CurrentTime and aws:EpochTime are computed when absent.
type CondContext map[string][]string

func (c CondContext) get(key string) ([]string, bool) {
	k := strings.ToLower(key)
	if v, ok := c[k]; ok {
		return v, true
	}
	switch k {
	case "aws:currenttime":
		return []string{time.Now().UTC().Format(time.RFC3339)}, true
	case "aws:epochtime":
		return []string{strconv.FormatInt(time.Now().Unix(), 10)}, true
	}
	return nil, false
}

// with returns a copy of c with extra keys.
func (c CondContext) with(kv ...string) CondContext {
	out := CondContext{}
	for k, v := range c {
		out[k] = v
	}
	for i := 0; i+1 < len(kv); i += 2 {
		if kv[i+1] != "" {
			out[strings.ToLower(kv[i])] = []string{kv[i+1]}
		}
	}
	return out
}

type condition struct {
	op       string // as written, e.g. "ForAnyValue:StringLikeIfExists"
	base     string // lower-case base operator, e.g. "stringlike"
	set      string // "", "foranyvalue" or "forallvalues"
	ifExists bool
	key      string
	values   []string
}

// negated operators match when no value matches (and when the key is absent).
var negated = map[string]string{
	"stringnotequals":           "stringequals",
	"stringnotequalsignorecase": "stringequalsignorecase",
	"stringnotlike":             "stringlike",
	"numericnotequals":          "numericequals",
	"datenotequals":             "dateequals",
	"notipaddress":              "ipaddress",
	"arnnotequals":              "arnequals",
	"arnnotlike":                "arnlike",
}

var operators = map[string]bool{
	"stringequals": true, "stringequalsignorecase": true, "stringlike": true,
	"numericequals": true, "numericlessthan": true, "numericlessthanequals": true, "numericgreaterthan": true, "numericgreaterthanequals": true,
	"dateequals": true, "datelessthan": true, "datelessthanequals": true, "dategreaterthan": true, "dategreaterthanequals": true,
	"bool": true, "binaryequals": true, "ipaddress": true, "arnequals": true, "arnlike": true, "null": true,
}

// parseConditions parses and checks a statement's Condition block.
func parseConditions(raw json.RawMessage) ([]condition, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var block map[string]map[string]any
	if err := dec.Decode(&block); err != nil {
		return nil, fmt.Errorf("must be an object of operator -> {key: value(s)}")
	}
	var out []condition
	ops := make([]string, 0, len(block))
	for op := range block {
		ops = append(ops, op)
	}
	sort.Strings(ops)
	for _, op := range ops {
		c := condition{op: op}
		name := op
		if p, rest, ok := strings.Cut(name, ":"); ok {
			switch strings.ToLower(p) {
			case "foranyvalue", "forallvalues":
				c.set = strings.ToLower(p)
			default:
				return nil, fmt.Errorf("unknown set operator %q", p)
			}
			name = rest
		}
		lname := strings.ToLower(name)
		if strings.HasSuffix(lname, "ifexists") {
			c.ifExists = true
			lname = strings.TrimSuffix(lname, "ifexists")
		}
		if !operators[lname] && negated[lname] == "" {
			return nil, fmt.Errorf("unknown condition operator %q", op)
		}
		c.base = lname
		keys := make([]string, 0, len(block[op]))
		for k := range block[op] {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			vals, err := condValues(block[op][k])
			if err != nil {
				return nil, fmt.Errorf("%s %s: %v", op, k, err)
			}
			cc := c
			cc.key, cc.values = k, vals
			if err := cc.check(); err != nil {
				return nil, fmt.Errorf("%s %s: %v", op, k, err)
			}
			out = append(out, cc)
		}
	}
	return out, nil
}

func condValues(v any) ([]string, error) {
	switch t := v.(type) {
	case string:
		return []string{t}, nil
	case json.Number:
		return []string{t.String()}, nil
	case bool:
		return []string{strconv.FormatBool(t)}, nil
	case []any:
		var out []string
		for _, x := range t {
			vs, err := condValues(x)
			if err != nil || len(vs) != 1 {
				return nil, fmt.Errorf("values must be strings, numbers or booleans")
			}
			out = append(out, vs[0])
		}
		return out, nil
	}
	return nil, fmt.Errorf("values must be strings, numbers or booleans")
}

// check validates literal values (variables are checked when evaluated).
func (c condition) check() error {
	pos := c.positive()
	for _, v := range c.values {
		if strings.Contains(v, "${") {
			continue
		}
		switch {
		case strings.HasPrefix(pos, "numeric"):
			if _, err := strconv.ParseFloat(v, 64); err != nil {
				return fmt.Errorf("%q is not a number", v)
			}
		case strings.HasPrefix(pos, "date"):
			if _, ok := parseDate(v); !ok {
				return fmt.Errorf("%q is not a date", v)
			}
		case pos == "ipaddress":
			if _, ok := parseCIDR(v); !ok {
				return fmt.Errorf("%q is not an IP address or CIDR block", v)
			}
		case pos == "bool", pos == "null":
			if !strings.EqualFold(v, "true") && !strings.EqualFold(v, "false") {
				return fmt.Errorf("%q must be true or false", v)
			}
		}
	}
	return nil
}

func (c condition) positive() string {
	if p := negated[c.base]; p != "" {
		return p
	}
	return c.base
}

func evalConditions(raw json.RawMessage, ctx CondContext, vars bool) (bool, error) {
	conds, err := parseConditions(raw)
	if err != nil {
		return false, err
	}
	for _, c := range conds {
		if !c.eval(ctx, vars) {
			return false, nil
		}
	}
	return true, nil
}

func (c condition) eval(ctx CondContext, vars bool) bool {
	vals, present := ctx.get(c.key)
	present = present && len(vals) > 0
	if c.base == "null" {
		for _, v := range c.values {
			if strings.EqualFold(v, "true") == !present {
				return true
			}
		}
		return false
	}
	neg := negated[c.base] != ""
	if !present {
		switch {
		case c.ifExists, c.set == "forallvalues":
			return true
		case c.set == "foranyvalue":
			return false
		}
		return neg // negated operators hold when the key is absent
	}
	pos := c.positive()
	matchesAny := func(v string) bool {
		for _, pv := range c.values {
			if vars {
				var ok bool
				if pv, ok = substitute(pv, ctx, pos == "stringlike" || pos == "arnlike"); !ok {
					continue
				}
			}
			if compare(pos, v, pv) {
				return true
			}
		}
		return false
	}
	single := func(v string) bool { return matchesAny(v) != neg }
	switch c.set {
	case "forallvalues":
		for _, v := range vals {
			if !single(v) {
				return false
			}
		}
		return true
	case "foranyvalue":
		for _, v := range vals {
			if single(v) {
				return true
			}
		}
		return false
	}
	for _, v := range vals {
		if matchesAny(v) {
			return !neg
		}
	}
	return neg
}

// compare applies a positive operator to a request value and a policy value.
func compare(op, v, pv string) bool {
	switch op {
	case "stringequals", "binaryequals":
		return v == pv
	case "stringequalsignorecase":
		return strings.EqualFold(v, pv)
	case "stringlike":
		return match(pv, v, false)
	case "bool":
		return strings.EqualFold(v, pv)
	case "arnequals", "arnlike":
		return match(core.CanonicalARN(pv), core.CanonicalARN(v), false)
	case "ipaddress":
		ip := net.ParseIP(v)
		n, ok := parseCIDR(pv)
		return ip != nil && ok && n.Contains(ip)
	}
	if strings.HasPrefix(op, "numeric") {
		a, err1 := strconv.ParseFloat(v, 64)
		b, err2 := strconv.ParseFloat(pv, 64)
		if err1 != nil || err2 != nil {
			return false
		}
		return cmpOrdered(strings.TrimPrefix(op, "numeric"), a, b)
	}
	if strings.HasPrefix(op, "date") {
		a, ok1 := parseDate(v)
		b, ok2 := parseDate(pv)
		if !ok1 || !ok2 {
			return false
		}
		return cmpOrdered(strings.TrimPrefix(op, "date"), float64(a.UnixNano()), float64(b.UnixNano()))
	}
	return false
}

func cmpOrdered(rel string, a, b float64) bool {
	switch rel {
	case "equals":
		return a == b
	case "lessthan":
		return a < b
	case "lessthanequals":
		return a <= b
	case "greaterthan":
		return a > b
	case "greaterthanequals":
		return a >= b
	}
	return false
}

func parseDate(s string) (time.Time, bool) {
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04:05Z0700", "2006-01-02T15:04Z07:00", "2006-01-02T15:04:05", "2006-01-02"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t, true
		}
	}
	if n, err := strconv.ParseInt(s, 10, 64); err == nil {
		return time.Unix(n, 0), true
	}
	return time.Time{}, false
}

func parseCIDR(s string) (*net.IPNet, bool) {
	if !strings.Contains(s, "/") {
		ip := net.ParseIP(s)
		if ip == nil {
			return nil, false
		}
		if ip.To4() != nil {
			s += "/32"
		} else {
			s += "/128"
		}
	}
	_, n, err := net.ParseCIDR(s)
	return n, err == nil
}

// substitute replaces policy variables (${aws:username}, ${aws:PrincipalTag/team}, ...)
// with request values. glob escapes ${*} and ${?} for wildcard matching. It
// reports false when a variable has no value (the element then matches nothing).
func substitute(s string, ctx CondContext, glob bool) (string, bool) {
	if !strings.Contains(s, "${") {
		return s, true
	}
	var b strings.Builder
	for {
		i := strings.Index(s, "${")
		if i < 0 {
			b.WriteString(s)
			return b.String(), true
		}
		j := strings.IndexByte(s[i:], '}')
		if j < 0 {
			b.WriteString(s)
			return b.String(), true
		}
		b.WriteString(s[:i])
		name := s[i+2 : i+j]
		def := ""
		hasDef := false
		if k, d, ok := strings.Cut(name, ","); ok {
			name, def, hasDef = strings.TrimSpace(k), strings.Trim(strings.TrimSpace(d), "'"), true
		}
		switch name {
		case "*", "?":
			if glob {
				b.WriteString(`\` + name)
			} else {
				b.WriteString(name)
			}
		case "$":
			b.WriteString("$")
		default:
			v, ok := ctx.get(name)
			switch {
			case ok && len(v) > 0:
				b.WriteString(v[0])
			case hasDef:
				b.WriteString(def)
			default:
				return "", false
			}
		}
		s = s[i+j+1:]
	}
}
