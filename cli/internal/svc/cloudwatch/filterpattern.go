package cloudwatch

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// CloudWatch Logs filter patterns (FilterLogEvents, metric and subscription filters):
//
//	ERROR                         messages containing the term (case-sensitive)
//	ERROR Exception               every term
//	?ERROR ?WARN                  any of the ? terms
//	"Failed to connect"           an exact phrase
//	ERROR -Retrying               exclude messages with a term
//	%time(out|d out)%             a regular expression
//	{ $.level = "ERROR" }         JSON messages: = != < > <= >=, * wildcards in
//	                              strings, && || ( ), IS NULL, NOT EXISTS, IS TRUE/FALSE
//	[ip, user, ..., status = 5*]  space-delimited messages with named fields

// Matcher reports whether a log message matches a filter pattern.
type Matcher func(message string) bool

// ParseFilterPattern compiles a filter pattern. An empty pattern matches everything.
func ParseFilterPattern(p string) (Matcher, error) {
	p = strings.TrimSpace(p)
	switch {
	case p == "" || p == `""`:
		return func(string) bool { return true }, nil
	case strings.HasPrefix(p, "{"):
		if !strings.HasSuffix(p, "}") {
			return nil, fmt.Errorf("JSON filter patterns must end with }")
		}
		pp := &jsonPatternParser{toks: nil}
		if err := pp.lex(p[1 : len(p)-1]); err != nil {
			return nil, err
		}
		e, err := pp.parseOr()
		if err != nil {
			return nil, err
		}
		if pp.pos != len(pp.toks) {
			return nil, fmt.Errorf("unexpected %q in filter pattern", pp.toks[pp.pos].s)
		}
		return func(msg string) bool {
			var doc any
			if json.Unmarshal([]byte(strings.TrimSpace(msg)), &doc) != nil {
				return false
			}
			return e(doc)
		}, nil
	case strings.HasPrefix(p, "["):
		if !strings.HasSuffix(p, "]") {
			return nil, fmt.Errorf("space-delimited filter patterns must end with ]")
		}
		return parseSpaceDelimited(p[1 : len(p)-1])
	case len(p) >= 2 && strings.HasPrefix(p, "%") && strings.HasSuffix(p, "%"):
		re, err := regexp.Compile(p[1 : len(p)-1])
		if err != nil {
			return nil, fmt.Errorf("invalid regular expression: %v", err)
		}
		return re.MatchString, nil
	}
	return parseTerms(p)
}

// ---- unstructured terms ----

func parseTerms(p string) (Matcher, error) {
	var all, anyOf, none []string
	for i := 0; i < len(p); {
		for i < len(p) && p[i] == ' ' {
			i++
		}
		if i >= len(p) {
			break
		}
		kind := byte(0)
		if p[i] == '?' || p[i] == '-' {
			kind = p[i]
			i++
		}
		var term string
		if i < len(p) && p[i] == '"' {
			end := strings.IndexByte(p[i+1:], '"')
			if end < 0 {
				return nil, fmt.Errorf("unterminated quote in filter pattern")
			}
			term = p[i+1 : i+1+end]
			i += end + 2
		} else {
			end := strings.IndexByte(p[i:], ' ')
			if end < 0 {
				end = len(p) - i
			}
			term = p[i : i+end]
			i += end
		}
		if term == "" {
			continue
		}
		switch kind {
		case '?':
			anyOf = append(anyOf, term)
		case '-':
			none = append(none, term)
		default:
			all = append(all, term)
		}
	}
	return func(msg string) bool {
		for _, t := range all {
			if !strings.Contains(msg, t) {
				return false
			}
		}
		for _, t := range none {
			if strings.Contains(msg, t) {
				return false
			}
		}
		if len(anyOf) == 0 {
			return true
		}
		for _, t := range anyOf {
			if strings.Contains(msg, t) {
				return true
			}
		}
		return false
	}, nil
}

// ---- JSON patterns ----

type ptok struct {
	kind string // "sel", "op", "str", "num", "word", "(", ")", "&&", "||"
	s    string
}

type jsonPatternParser struct {
	toks []ptok
	pos  int
}

func (pp *jsonPatternParser) lex(s string) error {
	for i := 0; i < len(s); {
		c := s[i]
		switch {
		case c == ' ' || c == '\t' || c == '\n':
			i++
		case c == '(' || c == ')':
			pp.toks = append(pp.toks, ptok{kind: string(c), s: string(c)})
			i++
		case strings.HasPrefix(s[i:], "&&") || strings.HasPrefix(s[i:], "||"):
			pp.toks = append(pp.toks, ptok{kind: s[i : i+2], s: s[i : i+2]})
			i += 2
		case strings.HasPrefix(s[i:], "!=") || strings.HasPrefix(s[i:], "<=") || strings.HasPrefix(s[i:], ">="):
			pp.toks = append(pp.toks, ptok{kind: "op", s: s[i : i+2]})
			i += 2
		case c == '=' || c == '<' || c == '>':
			pp.toks = append(pp.toks, ptok{kind: "op", s: string(c)})
			i++
		case c == '"':
			j := i + 1
			var b strings.Builder
			for j < len(s) && s[j] != '"' {
				if s[j] == '\\' && j+1 < len(s) {
					j++
				}
				b.WriteByte(s[j])
				j++
			}
			if j >= len(s) {
				return fmt.Errorf("unterminated string in filter pattern")
			}
			pp.toks = append(pp.toks, ptok{kind: "str", s: b.String()})
			i = j + 1
		case c == '$':
			j := i + 1
			for j < len(s) && !strings.ContainsRune(" \t\n()=!<>&|", rune(s[j])) {
				j++
			}
			pp.toks = append(pp.toks, ptok{kind: "sel", s: s[i:j]})
			i = j
		default:
			j := i
			for j < len(s) && !strings.ContainsRune(" \t\n()=!<>&|\"", rune(s[j])) {
				j++
			}
			if j == i {
				return fmt.Errorf("unexpected %q in filter pattern", string(c))
			}
			w := s[i:j]
			if _, err := strconv.ParseFloat(w, 64); err == nil {
				pp.toks = append(pp.toks, ptok{kind: "num", s: w})
			} else {
				pp.toks = append(pp.toks, ptok{kind: "word", s: w})
			}
			i = j
		}
	}
	return nil
}

func (pp *jsonPatternParser) peek() *ptok {
	if pp.pos < len(pp.toks) {
		return &pp.toks[pp.pos]
	}
	return nil
}

type jexpr func(doc any) bool

func (pp *jsonPatternParser) parseOr() (jexpr, error) {
	l, err := pp.parseAnd()
	if err != nil {
		return nil, err
	}
	for t := pp.peek(); t != nil && t.kind == "||"; t = pp.peek() {
		pp.pos++
		r, err := pp.parseAnd()
		if err != nil {
			return nil, err
		}
		a, b := l, r
		l = func(d any) bool { return a(d) || b(d) }
	}
	return l, nil
}

func (pp *jsonPatternParser) parseAnd() (jexpr, error) {
	l, err := pp.parseUnary()
	if err != nil {
		return nil, err
	}
	for t := pp.peek(); t != nil && t.kind == "&&"; t = pp.peek() {
		pp.pos++
		r, err := pp.parseUnary()
		if err != nil {
			return nil, err
		}
		a, b := l, r
		l = func(d any) bool { return a(d) && b(d) }
	}
	return l, nil
}

func (pp *jsonPatternParser) parseUnary() (jexpr, error) {
	t := pp.peek()
	if t == nil {
		return nil, fmt.Errorf("incomplete filter pattern")
	}
	if t.kind == "(" {
		pp.pos++
		e, err := pp.parseOr()
		if err != nil {
			return nil, err
		}
		if t := pp.peek(); t == nil || t.kind != ")" {
			return nil, fmt.Errorf("missing ) in filter pattern")
		}
		pp.pos++
		return e, nil
	}
	if t.kind != "sel" {
		return nil, fmt.Errorf("expected a $ selector, got %q", t.s)
	}
	sel, err := parseSelector(t.s)
	if err != nil {
		return nil, err
	}
	pp.pos++
	op := pp.peek()
	if op == nil {
		return nil, fmt.Errorf("selector %s needs a comparison", t.s)
	}
	if op.kind == "word" {
		w1 := strings.ToUpper(op.s)
		pp.pos++
		w2t := pp.peek()
		if w2t == nil {
			return nil, fmt.Errorf("incomplete %s", w1)
		}
		pp.pos++
		w2 := strings.ToUpper(w2t.s)
		switch w1 + " " + w2 {
		case "IS NULL":
			return func(d any) bool { v, ok := sel(d); return ok && v == nil }, nil
		case "NOT EXISTS":
			return func(d any) bool { _, ok := sel(d); return !ok }, nil
		case "IS TRUE":
			return func(d any) bool { v, ok := sel(d); return ok && v == true }, nil
		case "IS FALSE":
			return func(d any) bool { v, ok := sel(d); return ok && v == false }, nil
		}
		return nil, fmt.Errorf("unsupported %s %s", w1, w2)
	}
	if op.kind != "op" {
		return nil, fmt.Errorf("expected a comparison after %s", t.s)
	}
	pp.pos++
	val := pp.peek()
	if val == nil || (val.kind != "str" && val.kind != "num" && val.kind != "word") {
		return nil, fmt.Errorf("expected a value after %s %s", t.s, op.s)
	}
	pp.pos++
	cmp, err := valueComparer(op.s, val.s, val.kind == "num")
	if err != nil {
		return nil, err
	}
	return func(d any) bool {
		v, ok := sel(d)
		return ok && cmp(v)
	}, nil
}

// valueComparer compares a JSON value with a pattern value.
func valueComparer(op, want string, numeric bool) (func(v any) bool, error) {
	if numeric || (op != "=" && op != "!=") {
		n, err := strconv.ParseFloat(want, 64)
		if err != nil {
			return nil, fmt.Errorf("%s needs a number, got %q", op, want)
		}
		return func(v any) bool {
			var f float64
			switch x := v.(type) {
			case float64:
				f = x
			case string:
				var err error
				if f, err = strconv.ParseFloat(x, 64); err != nil {
					return false
				}
			default:
				return false
			}
			switch op {
			case "=":
				return f == n
			case "!=":
				return f != n
			case "<":
				return f < n
			case ">":
				return f > n
			case "<=":
				return f <= n
			case ">=":
				return f >= n
			}
			return false
		}, nil
	}
	glob := wildcard(want)
	return func(v any) bool {
		var s string
		switch x := v.(type) {
		case string:
			s = x
		case float64, bool:
			s = fmt.Sprint(x)
		default:
			return false
		}
		if op == "=" {
			return glob(s)
		}
		return !glob(s)
	}, nil
}

// wildcard matches * as any run of characters.
func wildcard(p string) func(string) bool {
	if !strings.Contains(p, "*") {
		return func(s string) bool { return s == p }
	}
	parts := strings.Split(p, "*")
	var re strings.Builder
	re.WriteString("^")
	for i, part := range parts {
		if i > 0 {
			re.WriteString(".*")
		}
		re.WriteString(regexp.QuoteMeta(part))
	}
	re.WriteString("$")
	r := regexp.MustCompile(re.String())
	return r.MatchString
}

// parseSelector compiles "$.a.b[0].c" (and "$.*" / "[*]", matching any element).
func parseSelector(s string) (func(doc any) (any, bool), error) {
	segs, err := selectorSegments(s)
	if err != nil {
		return nil, err
	}
	var walk func(cur any, segs []string) (any, bool)
	walk = func(cur any, segs []string) (any, bool) {
		if len(segs) == 0 {
			return cur, true
		}
		seg := segs[0]
		if seg == "*" {
			switch c := cur.(type) {
			case []any:
				for _, x := range c {
					if v, ok := walk(x, segs[1:]); ok {
						return v, true
					}
				}
			case map[string]any:
				for _, x := range c {
					if v, ok := walk(x, segs[1:]); ok {
						return v, true
					}
				}
			}
			return nil, false
		}
		if strings.HasPrefix(seg, "[") {
			n, _ := strconv.Atoi(seg[1 : len(seg)-1])
			arr, ok := cur.([]any)
			if !ok || n < 0 || n >= len(arr) {
				return nil, false
			}
			return walk(arr[n], segs[1:])
		}
		m, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		v, ok := m[seg]
		if !ok {
			return nil, false
		}
		return walk(v, segs[1:])
	}
	return func(doc any) (any, bool) { return walk(doc, segs) }, nil
}

func selectorSegments(s string) ([]string, error) {
	rest, ok := strings.CutPrefix(s, "$")
	if !ok {
		return nil, fmt.Errorf("selectors start with $")
	}
	var segs []string
	for rest != "" {
		switch {
		case strings.HasPrefix(rest, "."):
			rest = rest[1:]
			end := strings.IndexAny(rest, ".[")
			if end < 0 {
				end = len(rest)
			}
			if end == 0 {
				return nil, fmt.Errorf("empty field in selector %s", s)
			}
			segs = append(segs, rest[:end])
			rest = rest[end:]
		case strings.HasPrefix(rest, "["):
			end := strings.IndexByte(rest, ']')
			if end < 0 {
				return nil, fmt.Errorf("unterminated [ in selector %s", s)
			}
			inner := rest[1:end]
			if inner == "*" {
				segs = append(segs, "*")
			} else if _, err := strconv.Atoi(inner); err == nil {
				segs = append(segs, "["+inner+"]")
			} else {
				segs = append(segs, strings.Trim(inner, `'"`))
			}
			rest = rest[end+1:]
		default:
			return nil, fmt.Errorf("cannot parse selector %s", s)
		}
	}
	return segs, nil
}

// ---- space-delimited patterns ----

type sdField struct {
	name     string
	ellipsis bool
	test     func(v string) bool // nil = any value
}

func parseSpaceDelimited(p string) (Matcher, error) {
	fields, err := parseSDFields(p)
	if err != nil {
		return nil, err
	}
	return func(msg string) bool { return matchSD(fields, splitFields(msg), nil) }, nil
}

func parseSDFields(p string) ([]sdField, error) {
	var fields []sdField
	for _, raw := range splitTopLevel(p, ',') {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			return nil, fmt.Errorf("empty field in space-delimited filter pattern")
		}
		if raw == "..." {
			fields = append(fields, sdField{ellipsis: true})
			continue
		}
		f, err := parseSDField(raw)
		if err != nil {
			return nil, err
		}
		fields = append(fields, f)
	}
	return fields, nil
}

// FieldMatcher matches a message and returns the values it extracted: the
// named fields of a space-delimited pattern ("$status": "404").
type FieldMatcher func(message string) (bool, map[string]string)

// ParseFilterPatternFields compiles a pattern that also extracts named fields.
func ParseFilterPatternFields(p string) (FieldMatcher, error) {
	t := strings.TrimSpace(p)
	if strings.HasPrefix(t, "[") && strings.HasSuffix(t, "]") {
		fields, err := parseSDFields(t[1 : len(t)-1])
		if err != nil {
			return nil, err
		}
		return func(msg string) (bool, map[string]string) {
			caps := map[string]string{}
			ok := matchSD(fields, splitFields(msg), caps)
			return ok, caps
		}, nil
	}
	m, err := ParseFilterPattern(p)
	if err != nil {
		return nil, err
	}
	return func(msg string) (bool, map[string]string) { return m(msg), map[string]string{} }, nil
}

var sdCond = regexp.MustCompile(`^\s*([A-Za-z0-9_\-.$]+)\s*(=|!=|<=|>=|<|>)\s*("(?:[^"\\]|\\.)*"|[^\s&|]+)\s*`)

// parseSDField parses "name", or "name <op> value" conditions joined by && or ||
// (evaluated left to right).
func parseSDField(raw string) (sdField, error) {
	name := raw
	if i := strings.IndexAny(raw, " =!<>"); i >= 0 {
		name = raw[:i]
	}
	if !strings.ContainsAny(raw, "=<>") {
		return sdField{name: name}, nil
	}
	var test func(string) bool
	rest, join := raw, ""
	for rest != "" {
		m := sdCond.FindStringSubmatch(rest)
		if m == nil {
			return sdField{}, fmt.Errorf("cannot parse field condition %q", raw)
		}
		val := m[3]
		numeric := true
		if strings.HasPrefix(val, `"`) {
			val, numeric = strings.Trim(val, `"`), false
		} else if _, err := strconv.ParseFloat(val, 64); err != nil {
			numeric = false
		}
		cmp, err := valueComparer(m[2], val, numeric)
		if err != nil {
			return sdField{}, err
		}
		c := func(v string) bool { return cmp(v) }
		if test == nil {
			test = c
		} else {
			prev := test
			if join == "&&" {
				test = func(v string) bool { return prev(v) && c(v) }
			} else {
				test = func(v string) bool { return prev(v) || c(v) }
			}
		}
		rest = strings.TrimSpace(rest[len(m[0]):])
		switch {
		case strings.HasPrefix(rest, "&&"), strings.HasPrefix(rest, "||"):
			join, rest = rest[:2], strings.TrimSpace(rest[2:])
		case rest != "":
			return sdField{}, fmt.Errorf("cannot parse field condition %q", raw)
		}
	}
	return sdField{name: name, test: test}, nil
}

func splitTopLevel(s string, sep byte) []string {
	var out []string
	quote, start := false, 0
	for i := 0; i < len(s); i++ {
		switch {
		case s[i] == '"':
			quote = !quote
		case s[i] == sep && !quote:
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	return append(out, s[start:])
}

// splitFields splits a message on spaces; "quoted" and [bracketed] runs are one field.
func splitFields(msg string) []string {
	var out []string
	for i := 0; i < len(msg); {
		if msg[i] == ' ' || msg[i] == '\t' {
			i++
			continue
		}
		switch msg[i] {
		case '"':
			end := strings.IndexByte(msg[i+1:], '"')
			if end >= 0 {
				out = append(out, msg[i+1:i+1+end])
				i += end + 2
				continue
			}
		case '[':
			end := strings.IndexByte(msg[i+1:], ']')
			if end >= 0 {
				out = append(out, msg[i+1:i+1+end])
				i += end + 2
				continue
			}
		}
		end := strings.IndexAny(msg[i:], " \t")
		if end < 0 {
			end = len(msg) - i
		}
		out = append(out, msg[i:i+end])
		i += end
	}
	return out
}

// matchSD matches fields against values; caps (when not nil) receives the
// named fields' values as "$name".
func matchSD(fields []sdField, vals []string, caps map[string]string) bool {
	if len(fields) == 0 {
		return len(vals) == 0
	}
	f := fields[0]
	if f.ellipsis {
		for k := 0; k <= len(vals); k++ {
			if matchSD(fields[1:], vals[k:], caps) {
				return true
			}
		}
		return false
	}
	if len(vals) == 0 {
		return false
	}
	if f.test != nil && !f.test(vals[0]) {
		return false
	}
	if !matchSD(fields[1:], vals[1:], caps) {
		return false
	}
	if caps != nil && f.name != "" {
		caps["$"+f.name] = vals[0]
	}
	return true
}
