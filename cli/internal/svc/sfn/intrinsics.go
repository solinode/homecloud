package sfn

import (
	"crypto/md5"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"hash"
	"math"
	"math/rand/v2"
	"strings"

	"github.com/homecloudhq/homecloud/cli/internal/core"
)

// Intrinsic functions, callable from ".$" fields and nestable:
// States.Format, StringToJson, JsonToString, Array, ArrayPartition,
// ArrayContains, ArrayRange, ArrayGetItem, ArrayLength, ArrayUnique,
// Base64Encode, Base64Decode, Hash, JsonMerge, MathRandom, MathAdd,
// StringSplit and UUID.

type iexpr struct {
	fn    string  // intrinsic name, or "" for a value
	args  []iexpr // for functions
	lit   any     // literal value
	path  string  // a $ or $$ path
	isLit bool
}

type iparser struct {
	s   string
	pos int
}

func intrinsicErr(format string, a ...any) error {
	return fail("States.IntrinsicFailure", format, a...)
}

func parseIntrinsic(s string) (iexpr, error) {
	p := &iparser{s: s}
	e, err := p.expr()
	if err != nil {
		return iexpr{}, err
	}
	p.space()
	if p.pos != len(p.s) {
		return iexpr{}, intrinsicErr("unexpected %q in %s", p.s[p.pos:], s)
	}
	if e.fn == "" {
		return iexpr{}, intrinsicErr("%s is not an intrinsic function call", s)
	}
	return e, nil
}

func (p *iparser) space() {
	for p.pos < len(p.s) && (p.s[p.pos] == ' ' || p.s[p.pos] == '\t' || p.s[p.pos] == '\n') {
		p.pos++
	}
}

func (p *iparser) expr() (iexpr, error) {
	p.space()
	if p.pos >= len(p.s) {
		return iexpr{}, intrinsicErr("incomplete intrinsic function")
	}
	switch c := p.s[p.pos]; {
	case c == '\'':
		var b strings.Builder
		p.pos++
		for p.pos < len(p.s) && p.s[p.pos] != '\'' {
			if p.s[p.pos] == '\\' && p.pos+1 < len(p.s) {
				p.pos++
			}
			b.WriteByte(p.s[p.pos])
			p.pos++
		}
		if p.pos >= len(p.s) {
			return iexpr{}, intrinsicErr("unterminated string in %s", p.s)
		}
		p.pos++
		return iexpr{lit: b.String(), isLit: true}, nil
	case c == '$':
		start := p.pos
		depth := 0
		for p.pos < len(p.s) {
			ch := p.s[p.pos]
			if ch == '[' || ch == '(' {
				depth++
			} else if ch == ']' || ch == ')' {
				if depth == 0 {
					break
				}
				depth--
			} else if (ch == ',' || ch == ' ') && depth == 0 {
				break
			}
			p.pos++
		}
		return iexpr{path: p.s[start:p.pos]}, nil
	case strings.HasPrefix(p.s[p.pos:], "States."):
		start := p.pos
		for p.pos < len(p.s) && p.s[p.pos] != '(' {
			p.pos++
		}
		name := strings.TrimSpace(p.s[start:p.pos])
		if p.pos >= len(p.s) {
			return iexpr{}, intrinsicErr("%s needs (arguments)", name)
		}
		p.pos++ // (
		e := iexpr{fn: name}
		p.space()
		if p.pos < len(p.s) && p.s[p.pos] == ')' {
			p.pos++
			return e, nil
		}
		for {
			a, err := p.expr()
			if err != nil {
				return iexpr{}, err
			}
			e.args = append(e.args, a)
			p.space()
			if p.pos >= len(p.s) {
				return iexpr{}, intrinsicErr("missing ) in %s", p.s)
			}
			if p.s[p.pos] == ',' {
				p.pos++
				continue
			}
			if p.s[p.pos] == ')' {
				p.pos++
				return e, nil
			}
			return iexpr{}, intrinsicErr("unexpected %q in %s", string(p.s[p.pos]), p.s)
		}
	default:
		start := p.pos
		for p.pos < len(p.s) && p.s[p.pos] != ',' && p.s[p.pos] != ')' && p.s[p.pos] != ' ' {
			p.pos++
		}
		tok := p.s[start:p.pos]
		var v any
		if err := json.Unmarshal([]byte(tok), &v); err != nil {
			return iexpr{}, intrinsicErr("bad argument %q", tok)
		}
		return iexpr{lit: v, isLit: true}, nil
	}
}

func evalIntrinsic(s string, input, ctx any) (any, error) {
	e, err := parseIntrinsic(s)
	if err != nil {
		return nil, err
	}
	return e.eval(input, ctx)
}

func (e iexpr) eval(input, ctx any) (any, error) {
	switch {
	case e.isLit:
		return e.lit, nil
	case e.path != "":
		src := input
		if strings.HasPrefix(e.path, "$$") {
			src = ctx
		}
		v, err := get(src, e.path)
		if err != nil {
			return nil, fail("States.Runtime", "%v", err)
		}
		return v, nil
	}
	args := make([]any, len(e.args))
	for i, a := range e.args {
		v, err := a.eval(input, ctx)
		if err != nil {
			return nil, err
		}
		args[i] = v
	}
	return callIntrinsic(e.fn, args)
}

func need(fn string, args []any, n ...int) error {
	for _, want := range n {
		if len(args) == want {
			return nil
		}
	}
	return intrinsicErr("%s takes %v argument(s), got %d", fn, n, len(args))
}

func asString(fn string, v any) (string, error) {
	s, ok := v.(string)
	if !ok {
		return "", intrinsicErr("%s needs a string argument, got %s", fn, jsonText(v))
	}
	return s, nil
}

func asArray(fn string, v any) ([]any, error) {
	a, ok := v.([]any)
	if !ok {
		return nil, intrinsicErr("%s needs an array argument, got %s", fn, jsonText(v))
	}
	return a, nil
}

func asInt(fn string, v any) (int, error) {
	f, ok := v.(float64)
	if !ok || f != math.Trunc(f) {
		return 0, intrinsicErr("%s needs an integer argument, got %s", fn, jsonText(v))
	}
	return int(f), nil
}

func jsonText(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func callIntrinsic(fn string, args []any) (any, error) {
	switch fn {
	case "States.Format":
		if len(args) == 0 {
			return nil, intrinsicErr("States.Format needs a template")
		}
		tmpl, err := asString(fn, args[0])
		if err != nil {
			return nil, err
		}
		var b strings.Builder
		n := 1
		for i := 0; i < len(tmpl); i++ {
			if tmpl[i] == '{' && i+1 < len(tmpl) && tmpl[i+1] == '}' {
				if n >= len(args) {
					return nil, intrinsicErr("States.Format: more {} than arguments")
				}
				switch v := args[n].(type) {
				case string:
					b.WriteString(v)
				default:
					b.WriteString(jsonText(v))
				}
				n++
				i++
				continue
			}
			b.WriteByte(tmpl[i])
		}
		if n != len(args) {
			return nil, intrinsicErr("States.Format: %d arguments for %d {}", len(args)-1, n-1)
		}
		return b.String(), nil
	case "States.StringToJson":
		if err := need(fn, args, 1); err != nil {
			return nil, err
		}
		s, err := asString(fn, args[0])
		if err != nil {
			return nil, err
		}
		var v any
		if err := json.Unmarshal([]byte(s), &v); err != nil {
			return nil, intrinsicErr("States.StringToJson: %v", err)
		}
		return v, nil
	case "States.JsonToString":
		if err := need(fn, args, 1); err != nil {
			return nil, err
		}
		return jsonText(args[0]), nil
	case "States.Array":
		return append([]any{}, args...), nil
	case "States.ArrayPartition":
		if err := need(fn, args, 2); err != nil {
			return nil, err
		}
		a, err := asArray(fn, args[0])
		if err != nil {
			return nil, err
		}
		size, err := asInt(fn, args[1])
		if err != nil || size < 1 {
			return nil, intrinsicErr("States.ArrayPartition chunk size must be a positive integer")
		}
		out := []any{}
		for i := 0; i < len(a); i += size {
			out = append(out, append([]any{}, a[i:min(i+size, len(a))]...))
		}
		return out, nil
	case "States.ArrayContains":
		if err := need(fn, args, 2); err != nil {
			return nil, err
		}
		a, err := asArray(fn, args[0])
		if err != nil {
			return nil, err
		}
		for _, x := range a {
			if jsonEqual(x, args[1]) {
				return true, nil
			}
		}
		return false, nil
	case "States.ArrayRange":
		if err := need(fn, args, 3); err != nil {
			return nil, err
		}
		var n [3]int
		for i := range n {
			v, err := asInt(fn, args[i])
			if err != nil {
				return nil, err
			}
			n[i] = v
		}
		if n[2] == 0 {
			return nil, intrinsicErr("States.ArrayRange step must not be 0")
		}
		out := []any{}
		for v := n[0]; (n[2] > 0 && v <= n[1]) || (n[2] < 0 && v >= n[1]); v += n[2] {
			if len(out) >= 1000 {
				return nil, intrinsicErr("States.ArrayRange returns at most 1000 items")
			}
			out = append(out, float64(v))
		}
		return out, nil
	case "States.ArrayGetItem":
		if err := need(fn, args, 2); err != nil {
			return nil, err
		}
		a, err := asArray(fn, args[0])
		if err != nil {
			return nil, err
		}
		i, err := asInt(fn, args[1])
		if err != nil {
			return nil, err
		}
		if i < 0 || i >= len(a) {
			return nil, intrinsicErr("States.ArrayGetItem index %d is out of range", i)
		}
		return a[i], nil
	case "States.ArrayLength":
		if err := need(fn, args, 1); err != nil {
			return nil, err
		}
		a, err := asArray(fn, args[0])
		if err != nil {
			return nil, err
		}
		return float64(len(a)), nil
	case "States.ArrayUnique":
		if err := need(fn, args, 1); err != nil {
			return nil, err
		}
		a, err := asArray(fn, args[0])
		if err != nil {
			return nil, err
		}
		seen := map[string]bool{}
		out := []any{}
		for _, x := range a {
			if k := jsonText(x); !seen[k] {
				seen[k] = true
				out = append(out, x)
			}
		}
		return out, nil
	case "States.Base64Encode":
		if err := need(fn, args, 1); err != nil {
			return nil, err
		}
		s, err := asString(fn, args[0])
		if err != nil {
			return nil, err
		}
		return base64.StdEncoding.EncodeToString([]byte(s)), nil
	case "States.Base64Decode":
		if err := need(fn, args, 1); err != nil {
			return nil, err
		}
		s, err := asString(fn, args[0])
		if err != nil {
			return nil, err
		}
		b, err := base64.StdEncoding.DecodeString(s)
		if err != nil {
			return nil, intrinsicErr("States.Base64Decode: %v", err)
		}
		return string(b), nil
	case "States.Hash":
		if err := need(fn, args, 2); err != nil {
			return nil, err
		}
		algo, err := asString(fn, args[1])
		if err != nil {
			return nil, err
		}
		var h hash.Hash
		switch algo {
		case "MD5":
			h = md5.New()
		case "SHA-1":
			h = sha1.New()
		case "SHA-256":
			h = sha256.New()
		case "SHA-384":
			h = sha512.New384()
		case "SHA-512":
			h = sha512.New()
		default:
			return nil, intrinsicErr("States.Hash algorithm must be MD5, SHA-1, SHA-256, SHA-384 or SHA-512")
		}
		data, ok := args[0].(string)
		if !ok {
			data = jsonText(args[0])
		}
		h.Write([]byte(data))
		return hex.EncodeToString(h.Sum(nil)), nil
	case "States.JsonMerge":
		if err := need(fn, args, 3); err != nil {
			return nil, err
		}
		a, ok1 := args[0].(map[string]any)
		b, ok2 := args[1].(map[string]any)
		if !ok1 || !ok2 {
			return nil, intrinsicErr("States.JsonMerge merges two JSON objects")
		}
		if deep, _ := args[2].(bool); deep {
			return nil, intrinsicErr("States.JsonMerge supports only shallow merges (false)")
		}
		out := map[string]any{}
		for k, v := range a {
			out[k] = v
		}
		for k, v := range b {
			out[k] = v
		}
		return out, nil
	case "States.MathRandom":
		if err := need(fn, args, 2, 3); err != nil {
			return nil, err
		}
		lo, err1 := asInt(fn, args[0])
		hi, err2 := asInt(fn, args[1])
		if err1 != nil || err2 != nil || hi <= lo {
			return nil, intrinsicErr("States.MathRandom needs integers start < end")
		}
		r := rand.IntN(hi - lo)
		if len(args) == 3 {
			seed, err := asInt(fn, args[2])
			if err != nil {
				return nil, err
			}
			r = rand.New(rand.NewPCG(uint64(seed), 0)).IntN(hi - lo)
		}
		return float64(lo + r), nil
	case "States.MathAdd":
		if err := need(fn, args, 2); err != nil {
			return nil, err
		}
		a, ok1 := args[0].(float64)
		b, ok2 := args[1].(float64)
		if !ok1 || !ok2 {
			return nil, intrinsicErr("States.MathAdd needs two numbers")
		}
		return a + b, nil
	case "States.StringSplit":
		if err := need(fn, args, 2); err != nil {
			return nil, err
		}
		s, err := asString(fn, args[0])
		if err != nil {
			return nil, err
		}
		d, err := asString(fn, args[1])
		if err != nil {
			return nil, err
		}
		out := []any{}
		for _, part := range strings.FieldsFunc(s, func(r rune) bool { return strings.ContainsRune(d, r) }) {
			out = append(out, part)
		}
		return out, nil
	case "States.UUID":
		if err := need(fn, args, 0); err != nil {
			return nil, err
		}
		h := []byte(core.RandHex(32))
		h[12] = '4'
		h[16] = "89ab"[strings.IndexByte("0123456789abcdef", h[16])%4]
		return string(h[0:8]) + "-" + string(h[8:12]) + "-" + string(h[12:16]) + "-" + string(h[16:20]) + "-" + string(h[20:32]), nil
	}
	return nil, intrinsicErr("unknown intrinsic function %s", fn)
}
