package cloudwatch

import (
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
)

// MetricDataQuery, MetricStat, Metric and Dimension are the AWS API shapes
// (GetMetricData and metric-math alarms).
type MetricDataQuery struct {
	Id         string
	MetricStat *MetricStat `json:",omitempty"`
	Expression string      `json:",omitempty"`
	Label      string      `json:",omitempty"`
	ReturnData *bool       `json:",omitempty"`
	Period     int         `json:",omitempty"`
	AccountId  string      `json:",omitempty"`
}

type MetricStat struct {
	Metric Metric
	Period int
	Stat   string
	Unit   string `json:",omitempty"`
}

type Metric struct {
	Namespace  string      `json:",omitempty"`
	MetricName string      `json:",omitempty"`
	Dimensions []Dimension `json:",omitempty"`
}

type Dimension struct {
	Name  string
	Value string
}

func dimMap(ds []Dimension) map[string]string {
	if len(ds) == 0 {
		return nil
	}
	m := make(map[string]string, len(ds))
	for _, d := range ds {
		m[d.Name] = d.Value
	}
	return m
}

func dimList(m map[string]string) []Dimension {
	out := make([]Dimension, 0, len(m))
	for k, v := range m {
		out = append(out, Dimension{Name: k, Value: v})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// tseries is a time series ordered by time.
type tseries struct {
	label  string
	period time.Duration
	t      []time.Time
	v      []float64
}

// mval is an expression value: a scalar, a time series or an array of them.
type mval struct {
	scalar *float64
	series *tseries
	array  []*tseries
}

func scalarVal(f float64) mval { return mval{scalar: &f} }

var queryIDRe = regexp.MustCompile(`^[a-z][a-zA-Z0-9_]{0,254}$`)

type mathEnv struct {
	s          *Service
	queries    map[string]MetricDataQuery
	order      []string
	start, end time.Time
	done       map[string]mval
	busy       map[string]bool
}

// evalMetricQueries evaluates GetMetricData queries over [start, end) and returns every query's value by ID.
func (s *Service) evalMetricQueries(qs []MetricDataQuery, start, end time.Time) (map[string]mval, []string, error) {
	env := &mathEnv{s: s, queries: map[string]MetricDataQuery{}, start: start, end: end, done: map[string]mval{}, busy: map[string]bool{}}
	for _, q := range qs {
		if !queryIDRe.MatchString(q.Id) {
			return nil, nil, fmt.Errorf("query ID %q must start with a lowercase letter and contain only letters, digits and underscores", q.Id)
		}
		if _, dup := env.queries[q.Id]; dup {
			return nil, nil, fmt.Errorf("duplicate query ID %q", q.Id)
		}
		if (q.MetricStat == nil) == (q.Expression == "") {
			return nil, nil, fmt.Errorf("query %s needs exactly one of MetricStat or Expression", q.Id)
		}
		if ms := q.MetricStat; ms != nil {
			if ms.Metric.Namespace == "" || ms.Metric.MetricName == "" {
				return nil, nil, fmt.Errorf("query %s: MetricStat.Metric needs Namespace and MetricName", q.Id)
			}
			if !validStat(ms.Stat) {
				return nil, nil, fmt.Errorf("query %s: unsupported statistic %q", q.Id, ms.Stat)
			}
			if ms.Period <= 0 {
				return nil, nil, fmt.Errorf("query %s: MetricStat.Period must be positive", q.Id)
			}
		}
		env.queries[q.Id] = q
		env.order = append(env.order, q.Id)
	}
	for _, id := range env.order {
		if _, err := env.eval(id); err != nil {
			return nil, nil, err
		}
	}
	return env.done, env.order, nil
}

func (e *mathEnv) eval(id string) (mval, error) {
	if v, ok := e.done[id]; ok {
		return v, nil
	}
	q, ok := e.queries[id]
	if !ok {
		return mval{}, fmt.Errorf("unknown metric or expression ID %q", id)
	}
	if e.busy[id] {
		return mval{}, fmt.Errorf("expression %s refers to itself", id)
	}
	e.busy[id] = true
	defer delete(e.busy, id)
	var v mval
	if ms := q.MetricStat; ms != nil {
		period := time.Duration(ms.Period) * time.Second
		bs, _ := e.s.buckets(ms.Metric.Namespace, ms.Metric.MetricName, dimMap(ms.Metric.Dimensions), e.start, e.end, period, ms.Unit)
		ts := &tseries{label: q.Label, period: period}
		if ts.label == "" {
			ts.label = ms.Metric.MetricName
		}
		for _, b := range bs {
			if f, ok := b.Stat(ms.Stat); ok {
				ts.t = append(ts.t, b.T)
				ts.v = append(ts.v, f)
			}
		}
		v = mval{series: ts}
	} else {
		p := &mathParser{src: q.Expression, env: e, period: time.Duration(q.Period) * time.Second}
		if err := p.lex(); err != nil {
			return mval{}, fmt.Errorf("expression %s: %v", id, err)
		}
		node, err := p.expr()
		if err != nil {
			return mval{}, fmt.Errorf("expression %s: %v", id, err)
		}
		if p.pos != len(p.toks) {
			return mval{}, fmt.Errorf("expression %s: unexpected %q", id, p.toks[p.pos])
		}
		if v, err = node(); err != nil {
			return mval{}, fmt.Errorf("expression %s: %v", id, err)
		}
		label := q.Label
		if label == "" {
			label = q.Id
		}
		if v.series != nil {
			c := *v.series
			c.label = label
			v.series = &c
		}
	}
	e.done[id] = v
	return v, nil
}

// ---- expression parser ----

type mathParser struct {
	src    string
	toks   []string
	pos    int
	env    *mathEnv
	period time.Duration
}

func (p *mathParser) lex() error {
	s := p.src
	for i := 0; i < len(s); {
		c := rune(s[i])
		switch {
		case unicode.IsSpace(c):
			i++
		case strings.ContainsRune("+-*/^(),[]", c):
			p.toks = append(p.toks, string(c))
			i++
		case c == '"' || c == '\'':
			j := strings.IndexByte(s[i+1:], s[i])
			if j < 0 {
				return fmt.Errorf("unterminated string")
			}
			p.toks = append(p.toks, s[i:i+j+2])
			i += j + 2
		case unicode.IsDigit(c) || c == '.':
			j := i
			for j < len(s) && (unicode.IsDigit(rune(s[j])) || s[j] == '.' || s[j] == 'e' || s[j] == 'E') {
				j++
			}
			p.toks = append(p.toks, s[i:j])
			i = j
		case unicode.IsLetter(c) || c == '_':
			j := i
			for j < len(s) && (unicode.IsLetter(rune(s[j])) || unicode.IsDigit(rune(s[j])) || s[j] == '_') {
				j++
			}
			p.toks = append(p.toks, s[i:j])
			i = j
		default:
			return fmt.Errorf("unexpected %q", string(c))
		}
	}
	return nil
}

type mnode func() (mval, error)

func (p *mathParser) peek() string {
	if p.pos < len(p.toks) {
		return p.toks[p.pos]
	}
	return ""
}

func (p *mathParser) expr() (mnode, error) {
	l, err := p.term()
	if err != nil {
		return nil, err
	}
	for op := p.peek(); op == "+" || op == "-"; op = p.peek() {
		p.pos++
		r, err := p.term()
		if err != nil {
			return nil, err
		}
		l = binaryNode(op, l, r)
	}
	return l, nil
}

func (p *mathParser) term() (mnode, error) {
	l, err := p.unary()
	if err != nil {
		return nil, err
	}
	for op := p.peek(); op == "*" || op == "/" || op == "^"; op = p.peek() {
		p.pos++
		r, err := p.unary()
		if err != nil {
			return nil, err
		}
		l = binaryNode(op, l, r)
	}
	return l, nil
}

func (p *mathParser) unary() (mnode, error) {
	if p.peek() == "-" {
		p.pos++
		x, err := p.unary()
		if err != nil {
			return nil, err
		}
		return binaryNode("*", func() (mval, error) { return scalarVal(-1), nil }, x), nil
	}
	return p.primary()
}

func (p *mathParser) primary() (mnode, error) {
	t := p.peek()
	if t == "" {
		return nil, fmt.Errorf("incomplete expression")
	}
	p.pos++
	switch {
	case t == "(":
		e, err := p.expr()
		if err != nil {
			return nil, err
		}
		if p.peek() != ")" {
			return nil, fmt.Errorf("missing )")
		}
		p.pos++
		return e, nil
	case t == "[":
		var items []mnode
		for p.peek() != "]" {
			e, err := p.expr()
			if err != nil {
				return nil, err
			}
			items = append(items, e)
			if p.peek() == "," {
				p.pos++
			} else if p.peek() != "]" {
				return nil, fmt.Errorf("expected , or ]")
			}
		}
		p.pos++
		return func() (mval, error) {
			var out []*tseries
			for _, it := range items {
				v, err := it()
				if err != nil {
					return mval{}, err
				}
				switch {
				case v.series != nil:
					out = append(out, v.series)
				case v.array != nil:
					out = append(out, v.array...)
				default:
					return mval{}, fmt.Errorf("arrays may contain only time series")
				}
			}
			return mval{array: out}, nil
		}, nil
	case t[0] == '"' || t[0] == '\'':
		str := t[1 : len(t)-1]
		return func() (mval, error) { return mval{}, fmt.Errorf("unexpected string %q", str) }, nil
	case unicode.IsDigit(rune(t[0])) || t[0] == '.':
		f, err := strconv.ParseFloat(t, 64)
		if err != nil {
			return nil, fmt.Errorf("bad number %q", t)
		}
		return func() (mval, error) { return scalarVal(f), nil }, nil
	}
	if p.peek() == "(" {
		p.pos++
		var args []mnode
		var strArg string
		for p.peek() != ")" {
			if a := p.peek(); a != "" && (a[0] == '"' || a[0] == '\'') {
				strArg = a[1 : len(a)-1]
				p.pos++
			} else if a == "REPEAT" || a == "LINEAR" {
				strArg = a
				p.pos++
			} else {
				e, err := p.expr()
				if err != nil {
					return nil, err
				}
				args = append(args, e)
			}
			if p.peek() == "," {
				p.pos++
			} else if p.peek() != ")" {
				return nil, fmt.Errorf("expected , or ) in %s(", t)
			}
		}
		p.pos++
		return p.function(strings.ToUpper(t), args, strArg)
	}
	id := t
	env := p.env
	return func() (mval, error) { return env.eval(id) }, nil
}

func binaryNode(op string, l, r mnode) mnode {
	return func() (mval, error) {
		a, err := l()
		if err != nil {
			return mval{}, err
		}
		b, err := r()
		if err != nil {
			return mval{}, err
		}
		return combine(op, a, b)
	}
}

func applyOp(op string, x, y float64) (float64, bool) {
	var r float64
	switch op {
	case "+":
		r = x + y
	case "-":
		r = x - y
	case "*":
		r = x * y
	case "/":
		if y == 0 {
			return 0, false
		}
		r = x / y
	case "^":
		r = math.Pow(x, y)
	}
	return r, finite(r)
}

func combine(op string, a, b mval) (mval, error) {
	switch {
	case a.array != nil:
		out := make([]*tseries, 0, len(a.array))
		for _, s := range a.array {
			v, err := combine(op, mval{series: s}, b)
			if err != nil {
				return mval{}, err
			}
			out = append(out, v.series)
		}
		return mval{array: out}, nil
	case b.array != nil:
		out := make([]*tseries, 0, len(b.array))
		for _, s := range b.array {
			v, err := combine(op, a, mval{series: s})
			if err != nil {
				return mval{}, err
			}
			out = append(out, v.series)
		}
		return mval{array: out}, nil
	case a.scalar != nil && b.scalar != nil:
		r, ok := applyOp(op, *a.scalar, *b.scalar)
		if !ok {
			return mval{}, fmt.Errorf("invalid arithmetic")
		}
		return scalarVal(r), nil
	case a.series != nil && b.scalar != nil:
		return mval{series: mapSeries(a.series, func(x float64) (float64, bool) { return applyOp(op, x, *b.scalar) })}, nil
	case a.scalar != nil && b.series != nil:
		return mval{series: mapSeries(b.series, func(y float64) (float64, bool) { return applyOp(op, *a.scalar, y) })}, nil
	case a.series != nil && b.series != nil:
		idx := map[int64]float64{}
		for i, t := range b.series.t {
			idx[t.UnixNano()] = b.series.v[i]
		}
		out := &tseries{label: a.series.label, period: a.series.period}
		for i, t := range a.series.t {
			y, ok := idx[t.UnixNano()]
			if !ok {
				continue
			}
			if r, ok := applyOp(op, a.series.v[i], y); ok {
				out.t = append(out.t, t)
				out.v = append(out.v, r)
			}
		}
		return mval{series: out}, nil
	}
	return mval{}, fmt.Errorf("invalid operands")
}

func mapSeries(s *tseries, f func(float64) (float64, bool)) *tseries {
	out := &tseries{label: s.label, period: s.period}
	for i, t := range s.t {
		if r, ok := f(s.v[i]); ok {
			out.t = append(out.t, t)
			out.v = append(out.v, r)
		}
	}
	return out
}

func reduce(name string, vs []float64) (float64, bool) {
	if len(vs) == 0 {
		return 0, false
	}
	switch name {
	case "SUM", "AVG", "STDDEV":
		sum := 0.0
		for _, v := range vs {
			sum += v
		}
		if name == "SUM" {
			return sum, true
		}
		mean := sum / float64(len(vs))
		if name == "AVG" {
			return mean, true
		}
		d := 0.0
		for _, v := range vs {
			d += (v - mean) * (v - mean)
		}
		return math.Sqrt(d / float64(len(vs))), true
	case "MIN":
		m := vs[0]
		for _, v := range vs {
			m = math.Min(m, v)
		}
		return m, true
	case "MAX":
		m := vs[0]
		for _, v := range vs {
			m = math.Max(m, v)
		}
		return m, true
	}
	return 0, false
}

// grid returns the period-aligned timestamps of the query window.
func (p *mathParser) grid(period time.Duration) []time.Time {
	if period <= 0 {
		period = time.Minute
	}
	var out []time.Time
	for t := p.env.start.Truncate(period); t.Before(p.env.end); t = t.Add(period) {
		if !t.Before(p.env.start) {
			out = append(out, t)
		}
		if len(out) > 100800 {
			break
		}
	}
	return out
}

func (p *mathParser) function(name string, args []mnode, strArg string) (mnode, error) {
	arg := func(i int) (mval, error) {
		if i >= len(args) {
			return mval{}, fmt.Errorf("%s needs %d argument(s)", name, i+1)
		}
		return args[i]()
	}
	elementwise := map[string]func(float64) float64{"ABS": math.Abs, "CEIL": math.Ceil, "FLOOR": math.Floor, "SQRT": math.Sqrt,
		"LOG": math.Log, "LOG10": math.Log10, "EXP": math.Exp}
	switch name {
	case "SUM", "AVG", "MIN", "MAX", "STDDEV":
		return func() (mval, error) {
			v, err := arg(0)
			if err != nil {
				return mval{}, err
			}
			switch {
			case v.scalar != nil:
				return v, nil
			case v.series != nil:
				r, ok := reduce(name, v.series.v)
				if !ok {
					return mval{series: &tseries{label: v.series.label}}, nil
				}
				return scalarVal(r), nil
			}
			// Across an array: one value per timestamp.
			byT := map[int64][]float64{}
			var period time.Duration
			for _, s := range v.array {
				period = s.period
				for i, t := range s.t {
					byT[t.UnixNano()] = append(byT[t.UnixNano()], s.v[i])
				}
			}
			keys := make([]int64, 0, len(byT))
			for k := range byT {
				keys = append(keys, k)
			}
			sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
			out := &tseries{period: period}
			for _, k := range keys {
				r, _ := reduce(name, byT[k])
				out.t = append(out.t, time.Unix(0, k).UTC())
				out.v = append(out.v, r)
			}
			return mval{series: out}, nil
		}, nil
	case "ABS", "CEIL", "FLOOR", "SQRT", "LOG", "LOG10", "EXP":
		f := elementwise[name]
		return func() (mval, error) {
			v, err := arg(0)
			if err != nil {
				return mval{}, err
			}
			g := func(x float64) (float64, bool) { r := f(x); return r, finite(r) }
			switch {
			case v.scalar != nil:
				r, ok := g(*v.scalar)
				if !ok {
					return mval{}, fmt.Errorf("%s of an invalid value", name)
				}
				return scalarVal(r), nil
			case v.series != nil:
				return mval{series: mapSeries(v.series, g)}, nil
			}
			out := make([]*tseries, 0, len(v.array))
			for _, s := range v.array {
				out = append(out, mapSeries(s, g))
			}
			return mval{array: out}, nil
		}, nil
	case "METRICS":
		env := p.env
		return func() (mval, error) {
			var out []*tseries
			for _, id := range env.order {
				q := env.queries[id]
				if q.MetricStat == nil {
					continue
				}
				v, err := env.eval(id)
				if err != nil {
					return mval{}, err
				}
				if strArg != "" && !strings.Contains(v.series.label, strArg) {
					continue
				}
				out = append(out, v.series)
			}
			return mval{array: out}, nil
		}, nil
	case "PERIOD", "DATAPOINT_COUNT":
		return func() (mval, error) {
			v, err := arg(0)
			if err != nil {
				return mval{}, err
			}
			if v.series == nil {
				return mval{}, fmt.Errorf("%s needs a time series", name)
			}
			if name == "PERIOD" {
				return scalarVal(v.series.period.Seconds()), nil
			}
			return scalarVal(float64(len(v.series.v))), nil
		}, nil
	case "RATE", "DIFF", "RUNNING_SUM":
		return func() (mval, error) {
			v, err := arg(0)
			if err != nil {
				return mval{}, err
			}
			one := func(s *tseries) *tseries {
				out := &tseries{label: s.label, period: s.period}
				acc := 0.0
				for i := range s.t {
					switch name {
					case "RUNNING_SUM":
						acc += s.v[i]
						out.t, out.v = append(out.t, s.t[i]), append(out.v, acc)
					default:
						if i == 0 {
							continue
						}
						d := s.v[i] - s.v[i-1]
						if name == "RATE" {
							d /= s.t[i].Sub(s.t[i-1]).Seconds()
						}
						out.t, out.v = append(out.t, s.t[i]), append(out.v, d)
					}
				}
				return out
			}
			switch {
			case v.series != nil:
				return mval{series: one(v.series)}, nil
			case v.array != nil:
				out := make([]*tseries, 0, len(v.array))
				for _, s := range v.array {
					out = append(out, one(s))
				}
				return mval{array: out}, nil
			}
			return mval{}, fmt.Errorf("%s needs a time series", name)
		}, nil
	case "FILL":
		if len(args) == 0 {
			return nil, fmt.Errorf("FILL(metric, value)")
		}
		repeat := strArg == "REPEAT"
		if strArg == "LINEAR" {
			return nil, fmt.Errorf("FILL(..., LINEAR) is not supported; use a value or REPEAT")
		}
		if len(args) == 1 && !repeat {
			return nil, fmt.Errorf("FILL needs a value or REPEAT")
		}
		return func() (mval, error) {
			v, err := arg(0)
			if err != nil {
				return mval{}, err
			}
			fillVal := 0.0
			if !repeat {
				fv, err := arg(1)
				if err != nil {
					return mval{}, err
				}
				if fv.scalar == nil {
					return mval{}, fmt.Errorf("FILL value must be a number or REPEAT")
				}
				fillVal = *fv.scalar
			}
			one := func(s *tseries) *tseries {
				have := map[int64]float64{}
				for i, t := range s.t {
					have[t.UnixNano()] = s.v[i]
				}
				out := &tseries{label: s.label, period: s.period}
				last, seen := 0.0, false
				for _, t := range p.grid(s.period) {
					if x, ok := have[t.UnixNano()]; ok {
						out.t, out.v = append(out.t, t), append(out.v, x)
						last, seen = x, true
						continue
					}
					if repeat && !seen {
						continue
					}
					val := fillVal
					if repeat {
						val = last
					}
					out.t, out.v = append(out.t, t), append(out.v, val)
				}
				return out
			}
			switch {
			case v.series != nil:
				return mval{series: one(v.series)}, nil
			case v.array != nil:
				out := make([]*tseries, 0, len(v.array))
				for _, s := range v.array {
					out = append(out, one(s))
				}
				return mval{array: out}, nil
			}
			return v, nil
		}, nil
	}
	return nil, fmt.Errorf("unsupported function %s (supported: SUM, AVG, MIN, MAX, STDDEV, ABS, CEIL, FLOOR, SQRT, LOG, LOG10, EXP, METRICS, PERIOD, DATAPOINT_COUNT, RATE, DIFF, RUNNING_SUM, FILL)", name)
}
