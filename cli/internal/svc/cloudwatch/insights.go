package cloudwatch

import (
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
)

// A CloudWatch Logs Insights subset:
//
//	fields @timestamp, @message, level, latency * 2 as double
//	filter level = "ERROR" and (latency > 100 or @message like /timeout/)
//	parse @message "user=* action=*" as user, action     (or /(?<user>\w+)/)
//	stats count(*) as n, avg(latency), pct(latency, 99) by level, bin(5m)
//	sort n desc | limit 20 | dedup user | display user, n
//
// Records carry @timestamp, @message, @logStream, @log, @ingestionTime and the
// fields of JSON messages (nested keys joined with dots).

// tsVal is a timestamp in milliseconds; it renders as "2006-01-02 15:04:05.000".
type tsVal int64

type record map[string]any

type insightsQuery struct {
	cmds []func(rs []record, st *qstate) ([]record, error)
}

type qstate struct {
	cols    []string // output columns so far
	display []string // set by display
	sorted  bool
	limit   int
}

func (st *qstate) addCol(name string) {
	for _, c := range st.cols {
		if c == name {
			return
		}
	}
	st.cols = append(st.cols, name)
}

// ParseInsights compiles a query string.
func ParseInsights(q string) (*insightsQuery, error) {
	out := &insightsQuery{}
	for _, part := range splitPipes(q) {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		cmd, rest := part, ""
		if i := strings.IndexFunc(part, unicode.IsSpace); i >= 0 {
			cmd, rest = part[:i], strings.TrimSpace(part[i:])
		}
		var f func(rs []record, st *qstate) ([]record, error)
		var err error
		switch strings.ToLower(cmd) {
		case "fields":
			f, err = parseFieldsCmd(rest, false)
		case "display":
			f, err = parseFieldsCmd(rest, true)
		case "filter":
			f, err = parseFilterCmd(rest)
		case "stats":
			f, err = parseStatsCmd(rest)
		case "sort":
			f, err = parseSortCmd(rest)
		case "limit":
			n, e := strconv.Atoi(rest)
			if e != nil || n < 1 {
				return nil, fmt.Errorf("limit needs a positive number")
			}
			f = func(rs []record, st *qstate) ([]record, error) {
				st.limit = n
				if len(rs) > n {
					rs = rs[:n]
				}
				return rs, nil
			}
		case "parse":
			f, err = parseParseCmd(rest)
		case "dedup":
			f, err = parseDedupCmd(rest)
		default:
			return nil, fmt.Errorf("unsupported command %q (supported: fields, display, filter, stats, sort, limit, parse, dedup)", cmd)
		}
		if err != nil {
			return nil, fmt.Errorf("%s: %v", cmd, err)
		}
		out.cmds = append(out.cmds, f)
	}
	return out, nil
}

// splitPipes splits on | outside quotes and regex literals.
func splitPipes(s string) []string {
	var out []string
	var quote byte
	start := 0
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case quote != 0:
			if c == '\\' {
				i++
			} else if c == quote {
				quote = 0
			}
		case c == '"' || c == '\'' || c == '`':
			quote = c
		case c == '/' && regexCanStart(s[:i]):
			quote = '/'
		case c == '|':
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	return append(out, s[start:])
}

// regexCanStart reports whether a / at this point opens a regex literal (after
// like, =~, a comma, an opening bracket or an operator) rather than dividing.
func regexCanStart(before string) bool {
	t := strings.TrimRightFunc(before, unicode.IsSpace)
	if t == "" {
		return true
	}
	lower := strings.ToLower(t)
	if strings.HasSuffix(lower, "like") || strings.HasSuffix(t, "=~") || strings.HasSuffix(lower, "parse") {
		return true
	}
	last := t[len(t)-1]
	return strings.IndexByte("(,[=!<>", last) >= 0
}

// Run evaluates the query over events and returns rows of (field, value) pairs.
// groups[i] is the log group of events[i].
func (q *insightsQuery) Run(events []LogEvent, groups []string, account string, limit int) ([][][2]string, int, error) {
	rs := make([]record, 0, len(events))
	for i, e := range events {
		r := record{"@timestamp": tsVal(e.Timestamp.UnixMilli()), "@message": e.Message, "@logStream": e.Stream,
			"@log": account + ":" + groups[i], "@ingestionTime": tsVal(e.Ingestion.UnixMilli())}
		if m := strings.TrimSpace(e.Message); strings.HasPrefix(m, "{") {
			var doc map[string]any
			if json.Unmarshal([]byte(m), &doc) == nil {
				flatten("", doc, r)
			}
		}
		rs = append(rs, r)
	}
	st := &qstate{}
	var err error
	for _, c := range q.cmds {
		if rs, err = c(rs, st); err != nil {
			return nil, 0, err
		}
	}
	matched := len(rs)
	if !st.sorted { // newest first by default
		sort.SliceStable(rs, func(i, j int) bool { return tsOf(rs[i]) > tsOf(rs[j]) })
	}
	if limit <= 0 {
		limit = 1000
	}
	if st.limit > 0 && st.limit < limit {
		limit = st.limit
	}
	if len(rs) > limit {
		rs = rs[:limit]
	}
	cols := st.display
	if cols == nil {
		cols = st.cols
	}
	if len(cols) == 0 {
		cols = []string{"@timestamp", "@message"}
	}
	out := make([][][2]string, 0, len(rs))
	for _, r := range rs {
		row := [][2]string{}
		for _, c := range cols {
			v, ok := r[c]
			if !ok || v == nil {
				continue
			}
			row = append(row, [2]string{c, render(v)})
		}
		out = append(out, row)
	}
	return out, matched, nil
}

func tsOf(r record) int64 {
	if t, ok := r["@timestamp"].(tsVal); ok {
		return int64(t)
	}
	return 0
}

func flatten(prefix string, v any, into record) {
	switch x := v.(type) {
	case map[string]any:
		for k, sub := range x {
			name := k
			if prefix != "" {
				name = prefix + "." + k
			}
			flatten(name, sub, into)
		}
	case []any:
		for i, sub := range x {
			flatten(prefix+"."+strconv.Itoa(i), sub, into)
		}
	default:
		if prefix != "" {
			if _, reserved := into[prefix]; !reserved {
				into[prefix] = x
			}
		}
	}
}

func render(v any) string {
	switch x := v.(type) {
	case tsVal:
		return time.UnixMilli(int64(x)).UTC().Format("2006-01-02 15:04:05.000")
	case float64:
		if x == math.Trunc(x) && math.Abs(x) < 1e15 {
			return strconv.FormatInt(int64(x), 10)
		}
		return strconv.FormatFloat(x, 'f', -1, 64)
	case string:
		return x
	case bool:
		return strconv.FormatBool(x)
	}
	b, _ := json.Marshal(v)
	return string(b)
}

// ---- commands ----

type namedExpr struct {
	name string
	e    qexpr
}

func parseNamedExprs(s string) ([]namedExpr, error) {
	var out []namedExpr
	for _, part := range splitArgsTop(s) {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		name := part
		if i := lastAs(part); i >= 0 {
			name = strings.Trim(strings.TrimSpace(part[i+4:]), "`")
			part = strings.TrimSpace(part[:i])
			if name == "" {
				return nil, fmt.Errorf("missing name after as")
			}
		} else {
			name = strings.Trim(part, "`")
		}
		e, err := parseExpr(part)
		if err != nil {
			return nil, err
		}
		out = append(out, namedExpr{name: normalizeName(name), e: e})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("needs at least one field")
	}
	return out, nil
}

// lastAs finds a trailing " as name" outside parentheses and quotes.
func lastAs(s string) int {
	depth, quote := 0, byte(0)
	found := -1
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case quote != 0:
			if c == '\\' {
				i++
			} else if c == quote {
				quote = 0
			}
		case c == '"' || c == '\'' || c == '`' || c == '/':
			if c != '/' || regexCanStart(s[:i]) {
				quote = c
			}
		case c == '(' || c == '[':
			depth++
		case c == ')' || c == ']':
			depth--
		case depth == 0 && i+4 <= len(s) && (c == ' ' || c == '\t') && strings.EqualFold(s[i+1:min(i+4, len(s))], "as "):
			found = i
		}
	}
	return found
}

func normalizeName(s string) string { return strings.Join(strings.Fields(s), "") }

func parseFieldsCmd(s string, display bool) (func([]record, *qstate) ([]record, error), error) {
	exprs, err := parseNamedExprs(s)
	if err != nil {
		return nil, err
	}
	return func(rs []record, st *qstate) ([]record, error) {
		names := []string{}
		for _, ne := range exprs {
			names = append(names, ne.name)
			if _, isField := ne.e.(fieldRef); isField && string(ne.e.(fieldRef)) == ne.name {
				continue
			}
			for _, r := range rs {
				v, err := ne.e.eval(r)
				if err != nil {
					return nil, err
				}
				r[ne.name] = v
			}
		}
		if display {
			st.display = names
		} else {
			for _, n := range names {
				st.addCol(n)
			}
		}
		return rs, nil
	}, nil
}

func parseFilterCmd(s string) (func([]record, *qstate) ([]record, error), error) {
	e, err := parseExpr(s)
	if err != nil {
		return nil, err
	}
	return func(rs []record, st *qstate) ([]record, error) {
		out := rs[:0:0]
		for _, r := range rs {
			v, err := e.eval(r)
			if err != nil {
				return nil, err
			}
			if truthy(v) {
				out = append(out, r)
			}
		}
		return out, nil
	}, nil
}

func parseSortCmd(s string) (func([]record, *qstate) ([]record, error), error) {
	type key struct {
		e    qexpr
		desc bool
	}
	var keys []key
	for _, part := range splitArgsTop(s) {
		f := strings.Fields(strings.TrimSpace(part))
		if len(f) == 0 {
			continue
		}
		desc := false
		last := strings.ToLower(f[len(f)-1])
		if len(f) > 1 && (last == "desc" || last == "asc") {
			desc = last == "desc"
			f = f[:len(f)-1]
		}
		e, err := parseExpr(strings.Join(f, " "))
		if err != nil {
			return nil, err
		}
		keys = append(keys, key{e, desc})
	}
	if len(keys) == 0 {
		return nil, fmt.Errorf("needs a field")
	}
	return func(rs []record, st *qstate) ([]record, error) {
		st.sorted = true
		vals := make([][]any, len(rs))
		for i, r := range rs {
			for _, k := range keys {
				v, _ := k.e.eval(r)
				vals[i] = append(vals[i], v)
			}
		}
		idx := make([]int, len(rs))
		for i := range idx {
			idx[i] = i
		}
		sort.SliceStable(idx, func(a, b int) bool {
			for k, key := range keys {
				c := cmpValues(vals[idx[a]][k], vals[idx[b]][k])
				if c == 0 {
					continue
				}
				if key.desc {
					return c > 0
				}
				return c < 0
			}
			return false
		})
		out := make([]record, len(rs))
		for i, j := range idx {
			out[i] = rs[j]
		}
		return out, nil
	}, nil
}

func parseDedupCmd(s string) (func([]record, *qstate) ([]record, error), error) {
	exprs, err := parseNamedExprs(s)
	if err != nil {
		return nil, err
	}
	return func(rs []record, st *qstate) ([]record, error) {
		seen := map[string]bool{}
		out := rs[:0:0]
		for _, r := range rs {
			var k strings.Builder
			for _, ne := range exprs {
				v, _ := ne.e.eval(r)
				k.WriteString(render(v) + "\x00")
			}
			if !seen[k.String()] {
				seen[k.String()] = true
				out = append(out, r)
			}
		}
		return out, nil
	}, nil
}

func parseParseCmd(s string) (func([]record, *qstate) ([]record, error), error) {
	// parse <field> "glob with *" as a, b   |   parse <field> /regex with (?<name>..)/
	src := "@message"
	s = strings.TrimSpace(s)
	if s != "" && s[0] != '"' && s[0] != '\'' && s[0] != '/' {
		i := strings.IndexAny(s, " \t")
		if i < 0 {
			return nil, fmt.Errorf("needs a pattern")
		}
		src, s = strings.Trim(s[:i], "`"), strings.TrimSpace(s[i:])
	}
	if s == "" {
		return nil, fmt.Errorf("needs a pattern")
	}
	var re *regexp.Regexp
	var names []string
	switch s[0] {
	case '/':
		end := strings.LastIndexByte(s, '/')
		if end <= 0 {
			return nil, fmt.Errorf("unterminated regular expression")
		}
		var err error
		if re, err = regexp.Compile(strings.ReplaceAll(s[1:end], "(?<", "(?P<")); err != nil {
			return nil, err
		}
		for _, n := range re.SubexpNames() {
			if n != "" {
				names = append(names, n)
			}
		}
	case '"', '\'':
		q := s[0]
		end := strings.LastIndexByte(s, q)
		if end <= 0 {
			return nil, fmt.Errorf("unterminated pattern")
		}
		glob := s[1:end]
		rest := strings.TrimSpace(s[end+1:])
		after, ok := cutPrefixFold(rest, "as ")
		if !ok {
			return nil, fmt.Errorf("a glob pattern needs: as field1, field2")
		}
		for _, n := range strings.Split(after, ",") {
			names = append(names, strings.Trim(strings.TrimSpace(n), "`"))
		}
		parts := strings.Split(glob, "*")
		if len(parts)-1 != len(names) {
			return nil, fmt.Errorf("the pattern has %d * but %d names", len(parts)-1, len(names))
		}
		var b strings.Builder
		for i, p := range parts {
			if i > 0 {
				b.WriteString("(.*?)")
			}
			b.WriteString(regexp.QuoteMeta(p))
		}
		if strings.HasSuffix(glob, "*") {
			b.WriteString("$")
		}
		re = regexp.MustCompile(b.String())
	default:
		return nil, fmt.Errorf("needs a quoted pattern or /regex/")
	}
	return func(rs []record, st *qstate) ([]record, error) {
		for _, n := range names {
			st.addCol(n)
		}
		for _, r := range rs {
			v, _ := r[src].(string)
			m := re.FindStringSubmatch(v)
			if m == nil {
				continue
			}
			if re.SubexpNames()[0] == "" && len(re.SubexpNames()) > 1 && re.SubexpNames()[1] != "" {
				for i, n := range re.SubexpNames() {
					if n != "" {
						r[n] = m[i]
					}
				}
				continue
			}
			for i, n := range names {
				if i+1 < len(m) {
					r[n] = m[i+1]
				}
			}
		}
		return rs, nil
	}, nil
}

func cutPrefixFold(s, prefix string) (string, bool) {
	if len(s) >= len(prefix) && strings.EqualFold(s[:len(prefix)], prefix) {
		return s[len(prefix):], true
	}
	return s, false
}

// ---- stats ----

type aggSpec struct {
	name string
	fn   string
	arg  qexpr // nil for count(*)
	pct  float64
}

func parseStatsCmd(s string) (func([]record, *qstate) ([]record, error), error) {
	aggPart, byPart := s, ""
	if i := indexKeyword(s, "by"); i >= 0 {
		aggPart, byPart = s[:i], s[i+2:]
	}
	var aggs []aggSpec
	for _, part := range splitArgsTop(aggPart) {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		name := normalizeName(part)
		if i := lastAs(part); i >= 0 {
			name = strings.Trim(strings.TrimSpace(part[i+4:]), "`")
			part = strings.TrimSpace(part[:i])
		}
		open := strings.IndexByte(part, '(')
		if open < 0 || !strings.HasSuffix(part, ")") {
			return nil, fmt.Errorf("%q is not an aggregation such as count(*)", part)
		}
		fn := strings.ToLower(strings.TrimSpace(part[:open]))
		args := splitArgsTop(part[open+1 : len(part)-1])
		a := aggSpec{name: name, fn: fn}
		switch fn {
		case "count", "count_distinct", "sum", "avg", "min", "max", "earliest", "latest", "stddev", "pct", "percentile", "median":
		default:
			return nil, fmt.Errorf("unsupported aggregation %s", fn)
		}
		if len(args) > 0 && strings.TrimSpace(args[0]) != "*" && strings.TrimSpace(args[0]) != "" {
			e, err := parseExpr(args[0])
			if err != nil {
				return nil, err
			}
			a.arg = e
		} else if fn != "count" {
			return nil, fmt.Errorf("%s needs a field", fn)
		}
		if fn == "pct" || fn == "percentile" {
			if len(args) != 2 {
				return nil, fmt.Errorf("pct(field, percentile)")
			}
			p, err := strconv.ParseFloat(strings.TrimSpace(args[1]), 64)
			if err != nil {
				return nil, fmt.Errorf("pct needs a number")
			}
			a.pct = p
		}
		if fn == "median" {
			a.fn, a.pct = "pct", 50
		}
		aggs = append(aggs, a)
	}
	if len(aggs) == 0 {
		return nil, fmt.Errorf("needs an aggregation")
	}
	var by []namedExpr
	if strings.TrimSpace(byPart) != "" {
		var err error
		if by, err = parseNamedExprs(byPart); err != nil {
			return nil, err
		}
	}
	return func(rs []record, st *qstate) ([]record, error) {
		type group struct {
			keys record
			vals [][]any
			recs []record
		}
		groups := map[string]*group{}
		var order []string
		for _, r := range rs {
			keys := record{}
			var k strings.Builder
			for _, b := range by {
				v, err := b.e.eval(r)
				if err != nil {
					return nil, err
				}
				keys[b.name] = v
				k.WriteString(render(v) + "\x00")
			}
			g := groups[k.String()]
			if g == nil {
				g = &group{keys: keys, vals: make([][]any, len(aggs))}
				groups[k.String()] = g
				order = append(order, k.String())
			}
			for i, a := range aggs {
				if a.arg == nil {
					g.vals[i] = append(g.vals[i], true)
					continue
				}
				v, err := a.arg.eval(r)
				if err != nil {
					return nil, err
				}
				if v != nil {
					g.vals[i] = append(g.vals[i], v)
				}
			}
			g.recs = append(g.recs, r)
		}
		out := make([]record, 0, len(groups))
		for _, k := range order {
			g := groups[k]
			r := record{}
			for n, v := range g.keys {
				r[n] = v
			}
			for i, a := range aggs {
				r[a.name] = aggregate(a, g.vals[i])
			}
			out = append(out, r)
		}
		st.cols, st.display = nil, nil
		for _, b := range by {
			st.addCol(b.name)
		}
		for _, a := range aggs {
			st.addCol(a.name)
		}
		// Time-binned stats come back in time order.
		if len(by) > 0 {
			sort.SliceStable(out, func(i, j int) bool {
				for _, b := range by {
					if c := cmpValues(out[i][b.name], out[j][b.name]); c != 0 {
						return c < 0
					}
				}
				return false
			})
			st.sorted = true
		}
		return out, nil
	}, nil
}

func aggregate(a aggSpec, vals []any) any {
	nums := func() []float64 {
		var out []float64
		for _, v := range vals {
			if f, ok := toNum(v); ok {
				out = append(out, f)
			}
		}
		return out
	}
	switch a.fn {
	case "count":
		return float64(len(vals))
	case "count_distinct":
		seen := map[string]bool{}
		for _, v := range vals {
			seen[render(v)] = true
		}
		return float64(len(seen))
	case "sum", "avg", "stddev":
		ns := nums()
		if len(ns) == 0 {
			return nil
		}
		sum := 0.0
		for _, n := range ns {
			sum += n
		}
		if a.fn == "sum" {
			return sum
		}
		mean := sum / float64(len(ns))
		if a.fn == "avg" {
			return mean
		}
		v := 0.0
		for _, n := range ns {
			v += (n - mean) * (n - mean)
		}
		if len(ns) > 1 {
			v /= float64(len(ns) - 1)
		}
		return math.Sqrt(v)
	case "min", "max":
		var best any
		for _, v := range vals {
			if best == nil || (a.fn == "min" && cmpValues(v, best) < 0) || (a.fn == "max" && cmpValues(v, best) > 0) {
				best = v
			}
		}
		return best
	case "earliest":
		if len(vals) > 0 {
			return vals[0]
		}
	case "latest":
		if len(vals) > 0 {
			return vals[len(vals)-1]
		}
	case "pct", "percentile":
		ns := nums()
		if len(ns) == 0 {
			return nil
		}
		sort.Float64s(ns)
		i := int(math.Ceil(a.pct/100*float64(len(ns)))) - 1
		return ns[max(0, min(i, len(ns)-1))]
	}
	return nil
}

// indexKeyword finds a standalone keyword outside parentheses and quotes.
func indexKeyword(s, kw string) int {
	depth, quote := 0, byte(0)
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case quote != 0:
			if c == '\\' {
				i++
			} else if c == quote {
				quote = 0
			}
		case c == '"' || c == '\'' || c == '`':
			quote = c
		case c == '(' || c == '[':
			depth++
		case c == ')' || c == ']':
			depth--
		case depth == 0 && i+len(kw) <= len(s) && strings.EqualFold(s[i:i+len(kw)], kw):
			before := i == 0 || s[i-1] == ' ' || s[i-1] == '\t' || s[i-1] == ')'
			after := i+len(kw) == len(s) || s[i+len(kw)] == ' ' || s[i+len(kw)] == '\t'
			if before && after {
				return i
			}
		}
	}
	return -1
}

func splitArgsTop(s string) []string {
	var out []string
	depth, quote, start := 0, byte(0), 0
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case quote != 0:
			if c == '\\' {
				i++
			} else if c == quote {
				quote = 0
			}
		case c == '"' || c == '\'' || c == '`':
			quote = c
		case c == '/' && regexCanStart(s[start:i]):
			quote = '/'
		case c == '(' || c == '[':
			depth++
		case c == ')' || c == ']':
			depth--
		case c == ',' && depth == 0:
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	return append(out, s[start:])
}

// ---- expressions ----

type qexpr interface {
	eval(r record) (any, error)
}

type (
	fieldRef string
	literal  struct{ v any }
	unaryOp  struct {
		op string
		x  qexpr
	}
	binaryOp struct {
		op   string
		l, r qexpr
	}
	likeOp struct {
		x   qexpr
		re  *regexp.Regexp
		neg bool
	}
	inOp struct {
		x    qexpr
		list []qexpr
		neg  bool
	}
	call struct {
		fn   string
		args []qexpr
	}
)

func (f fieldRef) eval(r record) (any, error) { return r[string(f)], nil }
func (l literal) eval(record) (any, error)    { return l.v, nil }

func (u unaryOp) eval(r record) (any, error) {
	v, err := u.x.eval(r)
	if err != nil {
		return nil, err
	}
	if u.op == "not" {
		return !truthy(v), nil
	}
	if f, ok := toNum(v); ok {
		return -f, nil
	}
	return nil, nil
}

func (b binaryOp) eval(r record) (any, error) {
	l, err := b.l.eval(r)
	if err != nil {
		return nil, err
	}
	switch b.op {
	case "and":
		if !truthy(l) {
			return false, nil
		}
		rv, err := b.r.eval(r)
		return truthy(rv), err
	case "or":
		if truthy(l) {
			return true, nil
		}
		rv, err := b.r.eval(r)
		return truthy(rv), err
	}
	rv, err := b.r.eval(r)
	if err != nil {
		return nil, err
	}
	switch b.op {
	case "=", "==":
		return l != nil && rv != nil && cmpValues(l, rv) == 0, nil
	case "!=":
		return l != nil && rv != nil && cmpValues(l, rv) != 0, nil
	case "<":
		return l != nil && rv != nil && cmpValues(l, rv) < 0, nil
	case ">":
		return l != nil && rv != nil && cmpValues(l, rv) > 0, nil
	case "<=":
		return l != nil && rv != nil && cmpValues(l, rv) <= 0, nil
	case ">=":
		return l != nil && rv != nil && cmpValues(l, rv) >= 0, nil
	}
	a, ok1 := toNum(l)
	c, ok2 := toNum(rv)
	if !ok1 || !ok2 {
		return nil, nil
	}
	switch b.op {
	case "+":
		return a + c, nil
	case "-":
		return a - c, nil
	case "*":
		return a * c, nil
	case "/":
		if c == 0 {
			return nil, nil
		}
		return a / c, nil
	case "%":
		if c == 0 {
			return nil, nil
		}
		return math.Mod(a, c), nil
	case "^":
		return math.Pow(a, c), nil
	}
	return nil, fmt.Errorf("unknown operator %s", b.op)
}

func (l likeOp) eval(r record) (any, error) {
	v, err := l.x.eval(r)
	if err != nil || v == nil {
		return false, err
	}
	return l.re.MatchString(render(v)) != l.neg, nil
}

func (in inOp) eval(r record) (any, error) {
	v, err := in.x.eval(r)
	if err != nil || v == nil {
		return false, err
	}
	for _, e := range in.list {
		w, err := e.eval(r)
		if err != nil {
			return nil, err
		}
		if cmpValues(v, w) == 0 {
			return !in.neg, nil
		}
	}
	return in.neg, nil
}

var durationUnits = map[string]int64{"ms": 1, "s": 1000, "sec": 1000, "m": 60000, "min": 60000, "h": 3600000, "hr": 3600000, "d": 86400000, "w": 7 * 86400000}

func parseDurationMS(s string) (int64, bool) {
	s = strings.TrimSpace(s)
	i := strings.IndexFunc(s, func(r rune) bool { return !unicode.IsDigit(r) })
	if i <= 0 {
		return 0, false
	}
	n, err := strconv.ParseInt(s[:i], 10, 64)
	u, ok := durationUnits[strings.ToLower(s[i:])]
	if err != nil || !ok || n <= 0 {
		return 0, false
	}
	return n * u, true
}

func (c call) eval(r record) (any, error) {
	if c.fn == "bin" || c.fn == "datefloor" { // bin(5m) / datefloor(@timestamp, 1h)
		var src qexpr = fieldRef("@timestamp")
		arg := c.args[len(c.args)-1]
		if len(c.args) == 2 {
			src = c.args[0]
		}
		lit, _ := arg.(literal)
		ms, ok := parseDurationMS(fmt.Sprint(lit.v))
		if !ok {
			return nil, fmt.Errorf("%s needs a period such as 5m", c.fn)
		}
		v, err := src.eval(r)
		if err != nil {
			return nil, err
		}
		t, ok := v.(tsVal)
		if !ok {
			return nil, nil
		}
		return tsVal(int64(t) - int64(t)%ms), nil
	}
	args := make([]any, len(c.args))
	for i, a := range c.args {
		v, err := a.eval(r)
		if err != nil {
			return nil, err
		}
		args[i] = v
	}
	str := func(i int) string {
		if i < len(args) && args[i] != nil {
			return render(args[i])
		}
		return ""
	}
	num := func(i int) (float64, bool) {
		if i < len(args) {
			return toNum(args[i])
		}
		return 0, false
	}
	switch c.fn {
	case "ispresent":
		return len(args) > 0 && args[0] != nil, nil
	case "isempty":
		return len(args) == 0 || args[0] == nil || str(0) == "", nil
	case "isblank":
		return len(args) == 0 || args[0] == nil || strings.TrimSpace(str(0)) == "", nil
	case "strlen":
		if args[0] == nil {
			return nil, nil
		}
		return float64(len([]rune(str(0)))), nil
	case "tolower":
		return strings.ToLower(str(0)), nil
	case "toupper":
		return strings.ToUpper(str(0)), nil
	case "trim":
		return strings.TrimSpace(str(0)), nil
	case "ltrim":
		return strings.TrimLeft(str(0), " \t"), nil
	case "rtrim":
		return strings.TrimRight(str(0), " \t"), nil
	case "concat":
		var b strings.Builder
		for i := range args {
			b.WriteString(str(i))
		}
		return b.String(), nil
	case "replace":
		return strings.ReplaceAll(str(0), str(1), str(2)), nil
	case "strcontains":
		return strings.Contains(str(0), str(1)), nil
	case "substr":
		s := []rune(str(0))
		start, _ := num(1)
		st := max(0, min(int(start), len(s)))
		end := len(s)
		if l, ok := num(2); ok {
			end = min(len(s), st+int(l))
		}
		return string(s[st:end]), nil
	case "abs", "ceil", "floor", "sqrt", "log":
		f, ok := num(0)
		if !ok {
			return nil, nil
		}
		return map[string]func(float64) float64{"abs": math.Abs, "ceil": math.Ceil, "floor": math.Floor, "sqrt": math.Sqrt, "log": math.Log}[c.fn](f), nil
	case "greatest", "least":
		var best any
		for _, a := range args {
			if a == nil {
				continue
			}
			if best == nil || (c.fn == "greatest" && cmpValues(a, best) > 0) || (c.fn == "least" && cmpValues(a, best) < 0) {
				best = a
			}
		}
		return best, nil
	case "coalesce":
		for _, a := range args {
			if a != nil {
				return a, nil
			}
		}
		return nil, nil
	case "frommillis":
		f, ok := num(0)
		if !ok {
			return nil, nil
		}
		return tsVal(int64(f)), nil
	case "tomillis":
		if t, ok := args[0].(tsVal); ok {
			return float64(t), nil
		}
		return nil, nil
	}
	return nil, fmt.Errorf("unsupported function %s", c.fn)
}

func truthy(v any) bool {
	switch x := v.(type) {
	case nil:
		return false
	case bool:
		return x
	case float64:
		return x != 0
	case string:
		return x != ""
	}
	return true
}

func toNum(v any) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, true
	case tsVal:
		return float64(x), true
	case bool:
		if x {
			return 1, true
		}
		return 0, true
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(x), 64)
		return f, err == nil
	}
	return 0, false
}

// cmpValues orders numbers numerically and everything else as strings; nil sorts first.
func cmpValues(a, b any) int {
	if a == nil || b == nil {
		switch {
		case a == nil && b == nil:
			return 0
		case a == nil:
			return -1
		}
		return 1
	}
	_, as := a.(string)
	_, bs := b.(string)
	if x, ok := toNum(a); ok {
		if y, ok := toNum(b); ok && (!as || !bs) {
			switch {
			case x < y:
				return -1
			case x > y:
				return 1
			}
			return 0
		}
	}
	return strings.Compare(render(a), render(b))
}

// ---- expression parser ----

type qlexer struct {
	s   string
	pos int
}

type qtok struct {
	kind string // num str regex ident op ( ) [ ] , eof
	s    string
}

func (l *qlexer) next() (qtok, error) {
	for l.pos < len(l.s) && unicode.IsSpace(rune(l.s[l.pos])) {
		l.pos++
	}
	if l.pos >= len(l.s) {
		return qtok{kind: "eof"}, nil
	}
	s, i := l.s, l.pos
	c := s[i]
	switch {
	case c == '"' || c == '\'':
		var b strings.Builder
		j := i + 1
		for j < len(s) && s[j] != c {
			if s[j] == '\\' && j+1 < len(s) {
				j++
			}
			b.WriteByte(s[j])
			j++
		}
		if j >= len(s) {
			return qtok{}, fmt.Errorf("unterminated string")
		}
		l.pos = j + 1
		return qtok{kind: "str", s: b.String()}, nil
	case c == '`':
		j := strings.IndexByte(s[i+1:], '`')
		if j < 0 {
			return qtok{}, fmt.Errorf("unterminated `")
		}
		l.pos = i + j + 2
		return qtok{kind: "ident", s: s[i+1 : i+1+j]}, nil
	case c == '/' && regexCanStart(s[:i]):
		j := i + 1
		for j < len(s) && s[j] != '/' {
			if s[j] == '\\' {
				j++
			}
			j++
		}
		if j >= len(s) {
			return qtok{}, fmt.Errorf("unterminated regular expression")
		}
		l.pos = j + 1
		// Flags such as /abc/i are not supported by Insights either; (?i) works.
		return qtok{kind: "regex", s: s[i+1 : j]}, nil
	case unicode.IsDigit(rune(c)):
		j := i
		for j < len(s) && (unicode.IsDigit(rune(s[j])) || s[j] == '.' || s[j] == 'e' || s[j] == 'E') {
			j++
		}
		// Durations like 5m inside bin().
		k := j
		for k < len(s) && unicode.IsLetter(rune(s[k])) {
			k++
		}
		if k > j {
			l.pos = k
			return qtok{kind: "str", s: s[i:k]}, nil
		}
		l.pos = j
		return qtok{kind: "num", s: s[i:j]}, nil
	case c == '@' || c == '_' || c == '$' || unicode.IsLetter(rune(c)):
		j := i + 1
		for j < len(s) && (s[j] == '_' || s[j] == '.' || s[j] == '@' || s[j] == '$' || unicode.IsLetter(rune(s[j])) || unicode.IsDigit(rune(s[j]))) {
			j++
		}
		l.pos = j
		return qtok{kind: "ident", s: s[i:j]}, nil
	case strings.ContainsRune("()[],", rune(c)):
		l.pos++
		return qtok{kind: string(c), s: string(c)}, nil
	}
	for _, op := range []string{"=~", "!=", "<=", ">=", "==", "=", "<", ">", "+", "-", "*", "/", "%", "^"} {
		if strings.HasPrefix(s[i:], op) {
			l.pos += len(op)
			return qtok{kind: "op", s: op}, nil
		}
	}
	return qtok{}, fmt.Errorf("unexpected %q", string(c))
}

type qparser struct {
	toks []qtok
	pos  int
}

func parseExpr(s string) (qexpr, error) {
	lx := &qlexer{s: s}
	var toks []qtok
	for {
		t, err := lx.next()
		if err != nil {
			return nil, err
		}
		toks = append(toks, t)
		if t.kind == "eof" {
			break
		}
	}
	p := &qparser{toks: toks}
	e, err := p.or()
	if err != nil {
		return nil, err
	}
	if p.peek().kind != "eof" {
		return nil, fmt.Errorf("unexpected %q", p.peek().s)
	}
	return e, nil
}

func (p *qparser) peek() qtok { return p.toks[p.pos] }
func (p *qparser) take() qtok { t := p.toks[p.pos]; p.pos++; return t }

func (p *qparser) isWord(w string) bool {
	t := p.peek()
	return t.kind == "ident" && strings.EqualFold(t.s, w)
}

func (p *qparser) or() (qexpr, error) {
	l, err := p.and()
	if err != nil {
		return nil, err
	}
	for p.isWord("or") || (p.peek().kind == "op" && p.peek().s == "||") {
		p.take()
		r, err := p.and()
		if err != nil {
			return nil, err
		}
		l = binaryOp{op: "or", l: l, r: r}
	}
	return l, nil
}

func (p *qparser) and() (qexpr, error) {
	l, err := p.not()
	if err != nil {
		return nil, err
	}
	for p.isWord("and") {
		p.take()
		r, err := p.not()
		if err != nil {
			return nil, err
		}
		l = binaryOp{op: "and", l: l, r: r}
	}
	return l, nil
}

func (p *qparser) not() (qexpr, error) {
	if p.isWord("not") {
		p.take()
		x, err := p.not()
		if err != nil {
			return nil, err
		}
		return unaryOp{op: "not", x: x}, nil
	}
	return p.cmp()
}

func (p *qparser) cmp() (qexpr, error) {
	l, err := p.add()
	if err != nil {
		return nil, err
	}
	neg := false
	if p.isWord("not") {
		nt := p.toks[p.pos+1]
		if nt.kind == "ident" && (strings.EqualFold(nt.s, "like") || strings.EqualFold(nt.s, "in")) {
			p.take()
			neg = true
		}
	}
	switch {
	case p.isWord("like") || (p.peek().kind == "op" && p.peek().s == "=~"):
		p.take()
		t := p.take()
		var re *regexp.Regexp
		switch t.kind {
		case "regex":
			re, err = regexp.Compile(strings.ReplaceAll(t.s, "(?<", "(?P<"))
		case "str":
			re, err = regexp.Compile(regexp.QuoteMeta(t.s))
		default:
			return nil, fmt.Errorf("like needs a string or /regex/")
		}
		if err != nil {
			return nil, err
		}
		return likeOp{x: l, re: re, neg: neg}, nil
	case p.isWord("in"):
		p.take()
		if p.take().kind != "[" {
			return nil, fmt.Errorf("in needs a [list]")
		}
		var list []qexpr
		for p.peek().kind != "]" {
			e, err := p.add()
			if err != nil {
				return nil, err
			}
			list = append(list, e)
			if p.peek().kind == "," {
				p.take()
			} else if p.peek().kind != "]" {
				return nil, fmt.Errorf("expected , or ] in list")
			}
		}
		p.take()
		return inOp{x: l, list: list, neg: neg}, nil
	}
	if t := p.peek(); t.kind == "op" {
		switch t.s {
		case "=", "==", "!=", "<", ">", "<=", ">=":
			p.take()
			r, err := p.add()
			if err != nil {
				return nil, err
			}
			return binaryOp{op: t.s, l: l, r: r}, nil
		}
	}
	return l, nil
}

func (p *qparser) add() (qexpr, error) {
	l, err := p.mul()
	if err != nil {
		return nil, err
	}
	for t := p.peek(); t.kind == "op" && (t.s == "+" || t.s == "-"); t = p.peek() {
		p.take()
		r, err := p.mul()
		if err != nil {
			return nil, err
		}
		l = binaryOp{op: t.s, l: l, r: r}
	}
	return l, nil
}

func (p *qparser) mul() (qexpr, error) {
	l, err := p.unary()
	if err != nil {
		return nil, err
	}
	for t := p.peek(); t.kind == "op" && (t.s == "*" || t.s == "/" || t.s == "%" || t.s == "^"); t = p.peek() {
		p.take()
		r, err := p.unary()
		if err != nil {
			return nil, err
		}
		l = binaryOp{op: t.s, l: l, r: r}
	}
	return l, nil
}

func (p *qparser) unary() (qexpr, error) {
	if t := p.peek(); t.kind == "op" && t.s == "-" {
		p.take()
		x, err := p.unary()
		if err != nil {
			return nil, err
		}
		return unaryOp{op: "-", x: x}, nil
	}
	return p.primary()
}

func (p *qparser) primary() (qexpr, error) {
	t := p.take()
	switch t.kind {
	case "num":
		f, err := strconv.ParseFloat(t.s, 64)
		if err != nil {
			return nil, err
		}
		return literal{f}, nil
	case "str":
		return literal{t.s}, nil
	case "(":
		e, err := p.or()
		if err != nil {
			return nil, err
		}
		if p.take().kind != ")" {
			return nil, fmt.Errorf("missing )")
		}
		return e, nil
	case "ident":
		if p.peek().kind == "(" {
			p.take()
			var args []qexpr
			for p.peek().kind != ")" {
				if p.peek().kind == "op" && p.peek().s == "*" { // count(*)
					p.take()
					args = append(args, literal{"*"})
				} else {
					e, err := p.or()
					if err != nil {
						return nil, err
					}
					args = append(args, e)
				}
				if p.peek().kind == "," {
					p.take()
				} else if p.peek().kind != ")" {
					return nil, fmt.Errorf("expected , or ) in call to %s", t.s)
				}
			}
			p.take()
			fn := strings.ToLower(t.s)
			if (fn == "bin" && len(args) != 1) || (fn == "datefloor" && len(args) != 2) {
				return nil, fmt.Errorf("wrong number of arguments to %s", fn)
			}
			return call{fn: fn, args: args}, nil
		}
		switch strings.ToLower(t.s) {
		case "true":
			return literal{true}, nil
		case "false":
			return literal{false}, nil
		case "null":
			return literal{nil}, nil
		}
		return fieldRef(t.s), nil
	}
	return nil, fmt.Errorf("unexpected %q", t.s)
}
