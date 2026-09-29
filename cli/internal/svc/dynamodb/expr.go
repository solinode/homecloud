package dynamodb

import (
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

// This file implements DynamoDB's expression language: condition, filter and
// key condition expressions, projection expressions and document paths.
// Update expressions are in update.go.

// ---- lexer ----

type tokKind int

const (
	tEOF tokKind = iota
	tIdent
	tName  // #placeholder
	tValue // :placeholder
	tNumber
	tPunct
)

type token struct {
	kind tokKind
	text string
	pos  int
}

func isIdentStart(c byte) bool {
	return c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}
func isIdentChar(c byte) bool { return isIdentStart(c) || (c >= '0' && c <= '9') }

func lex(src, what string) ([]token, error) {
	var toks []token
	i := 0
	for i < len(src) {
		c := src[i]
		switch {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			i++
		case c == '#' || c == ':':
			j := i + 1
			for j < len(src) && isIdentChar(src[j]) {
				j++
			}
			if j == i+1 {
				return nil, syntaxErr(what, src, string(c), i)
			}
			k := tName
			if c == ':' {
				k = tValue
			}
			toks = append(toks, token{k, src[i:j], i})
			i = j
		case isIdentStart(c):
			j := i
			for j < len(src) && isIdentChar(src[j]) {
				j++
			}
			toks = append(toks, token{tIdent, src[i:j], i})
			i = j
		case c >= '0' && c <= '9':
			j := i
			for j < len(src) && src[j] >= '0' && src[j] <= '9' {
				j++
			}
			toks = append(toks, token{tNumber, src[i:j], i})
			i = j
		case c == '<' || c == '>':
			if i+1 < len(src) && (src[i+1] == '=' || (c == '<' && src[i+1] == '>')) {
				toks = append(toks, token{tPunct, src[i : i+2], i})
				i += 2
			} else {
				toks = append(toks, token{tPunct, src[i : i+1], i})
				i++
			}
		case strings.IndexByte("=()[],.+-", c) >= 0:
			toks = append(toks, token{tPunct, src[i : i+1], i})
			i++
		default:
			_, n := utf8.DecodeRuneInString(src[i:])
			return nil, validation("Invalid %s: Invalid character encountered; character: %q, position: %d", what, src[i:i+n], i+1)
		}
	}
	return append(toks, token{tEOF, "<EOF>", len(src)}), nil
}

func syntaxErr(what, src, tok string, pos int) error {
	lo, hi := pos-8, pos+len(tok)+8
	if lo < 0 {
		lo = 0
	}
	if hi > len(src) {
		hi = len(src)
	}
	return validation("Invalid %s: Syntax error; token: %q, near: %q", what, tok, strings.TrimSpace(src[lo:hi]))
}

// ---- document paths ----

type pathElem struct {
	Name  string
	Index int
	IsIdx bool
}

type docPath []pathElem

func (p docPath) String() string {
	var b strings.Builder
	b.WriteByte('[')
	for i, e := range p {
		if i > 0 {
			b.WriteString(", ")
		}
		if e.IsIdx {
			b.WriteString("[" + strconv.Itoa(e.Index) + "]")
		} else {
			b.WriteString(e.Name)
		}
	}
	b.WriteByte(']')
	return b.String()
}

func getPath(it Item, p docPath) (AV, bool) {
	v, ok := it[p[0].Name]
	if !ok {
		return AV{}, false
	}
	for _, e := range p[1:] {
		if e.IsIdx {
			if v.Kind != kL || e.Index >= len(v.L) {
				return AV{}, false
			}
			v = v.L[e.Index]
		} else {
			if v.Kind != kM {
				return AV{}, false
			}
			if v, ok = v.M[e.Name]; !ok {
				return AV{}, false
			}
		}
	}
	return v, true
}

// pathRelation reports whether two paths overlap (one is a prefix of the other)
// or conflict (they diverge on name vs index at the same position).
func pathRelation(a, b docPath) (overlap, conflict bool) {
	n := len(a)
	if len(b) < n {
		n = len(b)
	}
	for i := 0; i < n; i++ {
		x, y := a[i], b[i]
		if x.IsIdx != y.IsIdx {
			return false, true
		}
		if x.Name != y.Name || x.Index != y.Index {
			return false, false
		}
	}
	return true, false
}

func checkPaths(what string, paths []docPath) error {
	for i := range paths {
		for j := i + 1; j < len(paths); j++ {
			ov, cf := pathRelation(paths[i], paths[j])
			if ov {
				return validation("Invalid %s: Two document paths overlap with each other; must remove or rewrite one of these paths; path one: %s, path two: %s", what, paths[i], paths[j])
			}
			if cf {
				return validation("Invalid %s: Two document paths conflict with each other; must remove or rewrite one of these paths; path one: %s, path two: %s", what, paths[i], paths[j])
			}
		}
	}
	return nil
}

// ---- expression context (placeholders) ----

type exprCtx struct {
	names      map[string]string
	values     map[string]AV
	usedNames  map[string]bool
	usedValues map[string]bool
	used       bool // an expression was parsed
}

func newExprCtx(names map[string]string, values map[string]AV) (*exprCtx, error) {
	if names != nil && len(names) == 0 {
		return nil, validation("ExpressionAttributeNames must not be empty")
	}
	if values != nil && len(values) == 0 {
		return nil, validation("ExpressionAttributeValues must not be empty")
	}
	for k, v := range values {
		if !strings.HasPrefix(k, ":") {
			return nil, validation("ExpressionAttributeValues contains invalid key: Syntax error; key: %q", k)
		}
		if err := v.validate(); err != nil {
			if ae, ok := err.(*apiError); ok {
				return nil, validation("ExpressionAttributeValues contains invalid value: %s for key %s", ae.Message, k)
			}
			return nil, err
		}
		values[k] = v
	}
	for k := range names {
		if !strings.HasPrefix(k, "#") {
			return nil, validation("ExpressionAttributeNames contains invalid key: Syntax error; key: %q", k)
		}
	}
	return &exprCtx{names: names, values: values, usedNames: map[string]bool{}, usedValues: map[string]bool{}}, nil
}

// finish reports placeholders that no expression used.
func (c *exprCtx) finish() error {
	if !c.used {
		if c.values != nil {
			return validation("ExpressionAttributeValues can only be specified when using expressions")
		}
		if c.names != nil {
			return validation("ExpressionAttributeNames can only be specified when using expressions")
		}
		return nil
	}
	var unusedN, unusedV []string
	for k := range c.names {
		if !c.usedNames[k] {
			unusedN = append(unusedN, k)
		}
	}
	for k := range c.values {
		if !c.usedValues[k] {
			unusedV = append(unusedV, k)
		}
	}
	sort.Strings(unusedN)
	sort.Strings(unusedV)
	if len(unusedN) > 0 {
		return validation("Value provided in ExpressionAttributeNames unused in expressions: keys: {%s}", strings.Join(unusedN, ", "))
	}
	if len(unusedV) > 0 {
		return validation("Value provided in ExpressionAttributeValues unused in expressions: keys: {%s}", strings.Join(unusedV, ", "))
	}
	return nil
}

// ---- AST ----

type exprKind int

const (
	ePath exprKind = iota
	eValue
	eSize
	eIfNotExists
	eListAppend
	ePlus
	eMinus
	eCmp
	eBetween
	eIn
	eAnd
	eOr
	eNot
	eFunc
)

type expr struct {
	kind exprKind
	op   string // comparator or function name
	path docPath
	val  AV
	args []*expr
}

// ---- parser ----

type parser struct {
	toks []token
	pos  int
	src  string
	what string
	ctx  *exprCtx
}

func newParser(src, what string, ctx *exprCtx) (*parser, error) {
	if strings.TrimSpace(src) == "" {
		return nil, validation("Invalid %s: The expression can not be empty;", what)
	}
	toks, err := lex(src, what)
	if err != nil {
		return nil, err
	}
	ctx.used = true
	return &parser{toks: toks, src: src, what: what, ctx: ctx}, nil
}

func (p *parser) peek() token { return p.toks[p.pos] }
func (p *parser) peekN(n int) token {
	if p.pos+n < len(p.toks) {
		return p.toks[p.pos+n]
	}
	return p.toks[len(p.toks)-1]
}
func (p *parser) next() token {
	t := p.toks[p.pos]
	if t.kind != tEOF {
		p.pos++
	}
	return t
}
func (p *parser) fail(t token) error { return syntaxErr(p.what, p.src, t.text, t.pos) }

func (p *parser) isKeyword(t token, kw string) bool {
	return t.kind == tIdent && strings.EqualFold(t.text, kw)
}

func (p *parser) expect(punct string) error {
	t := p.next()
	if t.kind != tPunct || t.text != punct {
		return p.fail(t)
	}
	return nil
}

var reservedInExpr = map[string]bool{"AND": true, "OR": true, "NOT": true, "BETWEEN": true, "IN": true}

func (p *parser) pathElemName(t token) (string, error) {
	switch t.kind {
	case tName:
		n, ok := p.ctx.names[t.text]
		if !ok {
			return "", validation("Invalid %s: An expression attribute name used in the document path is not defined; attribute name: %s", p.what, t.text)
		}
		p.ctx.usedNames[t.text] = true
		return n, nil
	case tIdent:
		if reservedInExpr[strings.ToUpper(t.text)] {
			return "", validation("Invalid %s: Attribute name is a reserved keyword; reserved keyword: %s", p.what, t.text)
		}
		return t.text, nil
	}
	return "", p.fail(t)
}

func (p *parser) parsePath() (docPath, error) {
	name, err := p.pathElemName(p.next())
	if err != nil {
		return nil, err
	}
	path := docPath{{Name: name}}
	for {
		t := p.peek()
		if t.kind != tPunct || (t.text != "." && t.text != "[") {
			return path, nil
		}
		p.next()
		if t.text == "." {
			name, err := p.pathElemName(p.next())
			if err != nil {
				return nil, err
			}
			path = append(path, pathElem{Name: name})
			continue
		}
		n := p.next()
		if n.kind != tNumber {
			return nil, p.fail(n)
		}
		idx, err := strconv.Atoi(n.text)
		if err != nil {
			return nil, p.fail(n)
		}
		if err := p.expect("]"); err != nil {
			return nil, err
		}
		path = append(path, pathElem{Index: idx, IsIdx: true})
	}
}

func (p *parser) value(t token) (*expr, error) {
	v, ok := p.ctx.values[t.text]
	if !ok {
		return nil, validation("Invalid %s: An expression attribute value used in expression is not defined; attribute value: %s", p.what, t.text)
	}
	p.ctx.usedValues[t.text] = true
	return &expr{kind: eValue, val: v}, nil
}

// parseCondition parses a complete condition expression.
func parseCondition(src, what string, ctx *exprCtx) (*expr, error) {
	p, err := newParser(src, what, ctx)
	if err != nil {
		return nil, err
	}
	e, err := p.parseOr()
	if err != nil {
		return nil, err
	}
	if t := p.peek(); t.kind != tEOF {
		return nil, p.fail(t)
	}
	return e, nil
}

func (p *parser) parseOr() (*expr, error) {
	l, err := p.parseAnd()
	if err != nil {
		return nil, err
	}
	for p.isKeyword(p.peek(), "OR") {
		p.next()
		r, err := p.parseAnd()
		if err != nil {
			return nil, err
		}
		l = &expr{kind: eOr, args: []*expr{l, r}}
	}
	return l, nil
}

func (p *parser) parseAnd() (*expr, error) {
	l, err := p.parseNot()
	if err != nil {
		return nil, err
	}
	for p.isKeyword(p.peek(), "AND") {
		p.next()
		r, err := p.parseNot()
		if err != nil {
			return nil, err
		}
		l = &expr{kind: eAnd, args: []*expr{l, r}}
	}
	return l, nil
}

func (p *parser) parseNot() (*expr, error) {
	if p.isKeyword(p.peek(), "NOT") {
		p.next()
		e, err := p.parseNot()
		if err != nil {
			return nil, err
		}
		return &expr{kind: eNot, args: []*expr{e}}, nil
	}
	return p.parsePrimary()
}

var condFuncs = map[string]int{"attribute_exists": 1, "attribute_not_exists": 1, "attribute_type": 2, "begins_with": 2, "contains": 2, "size": 1}

func (p *parser) parsePrimary() (*expr, error) {
	t := p.peek()
	if t.kind == tPunct && t.text == "(" {
		p.next()
		e, err := p.parseOr()
		if err != nil {
			return nil, err
		}
		if err := p.expect(")"); err != nil {
			return nil, err
		}
		return e, nil
	}
	if t.kind == tIdent && p.peekN(1).kind == tPunct && p.peekN(1).text == "(" && t.text != "size" {
		return p.parseFunc()
	}
	l, err := p.parseCondOperand()
	if err != nil {
		return nil, err
	}
	op := p.peek()
	switch {
	case op.kind == tPunct && isComparator(op.text):
		p.next()
		r, err := p.parseCondOperand()
		if err != nil {
			return nil, err
		}
		return &expr{kind: eCmp, op: op.text, args: []*expr{l, r}}, nil
	case p.isKeyword(op, "BETWEEN"):
		p.next()
		lo, err := p.parseCondOperand()
		if err != nil {
			return nil, err
		}
		if !p.isKeyword(p.peek(), "AND") {
			return nil, p.fail(p.peek())
		}
		p.next()
		hi, err := p.parseCondOperand()
		if err != nil {
			return nil, err
		}
		if lo.kind == eValue && hi.kind == eValue {
			if c, ok := compareAV(lo.val, hi.val); ok && c > 0 {
				return nil, validation("Invalid %s: The BETWEEN operator requires upper bound to be greater than or equal to lower bound; lower bound operand: %s, upper bound operand: %s", p.what, lo.val.describe(), hi.val.describe())
			}
		}
		return &expr{kind: eBetween, args: []*expr{l, lo, hi}}, nil
	case p.isKeyword(op, "IN"):
		p.next()
		if err := p.expect("("); err != nil {
			return nil, err
		}
		args := []*expr{l}
		for {
			e, err := p.parseCondOperand()
			if err != nil {
				return nil, err
			}
			args = append(args, e)
			t := p.next()
			if t.kind == tPunct && t.text == ")" {
				break
			}
			if t.kind != tPunct || t.text != "," {
				return nil, p.fail(t)
			}
		}
		if len(args) > 101 {
			return nil, validation("Invalid %s: The IN operator is provided with too many operands; number of operands: %d", p.what, len(args)-1)
		}
		return &expr{kind: eIn, args: args}, nil
	}
	if l.kind == eSize {
		return nil, validation("Invalid %s: The function is not allowed to be used this way in an expression; function: size", p.what)
	}
	return nil, p.fail(op)
}

func isComparator(s string) bool {
	switch s {
	case "=", "<>", "<", "<=", ">", ">=":
		return true
	}
	return false
}

// parseCondOperand parses a path, a :value or size(path).
func (p *parser) parseCondOperand() (*expr, error) {
	t := p.peek()
	switch t.kind {
	case tValue:
		p.next()
		return p.value(t)
	case tIdent, tName:
		if t.kind == tIdent && p.peekN(1).kind == tPunct && p.peekN(1).text == "(" {
			if t.text != "size" {
				if _, ok := condFuncs[t.text]; ok {
					return nil, validation("Invalid %s: The function is not allowed to be used this way in an expression; function: %s", p.what, t.text)
				}
				return nil, validation("Invalid %s: Invalid function name; function: %s", p.what, t.text)
			}
			f, err := p.parseFunc()
			if err != nil {
				return nil, err
			}
			return f, nil
		}
		path, err := p.parsePath()
		if err != nil {
			return nil, err
		}
		return &expr{kind: ePath, path: path}, nil
	}
	return nil, p.fail(t)
}

func (p *parser) parseFunc() (*expr, error) {
	name := p.next()
	nargs, ok := condFuncs[name.text]
	if !ok {
		return nil, validation("Invalid %s: Invalid function name; function: %s", p.what, name.text)
	}
	p.next() // (
	var args []*expr
	if t := p.peek(); !(t.kind == tPunct && t.text == ")") {
		for {
			e, err := p.parseCondOperand()
			if err != nil {
				return nil, err
			}
			args = append(args, e)
			t := p.next()
			if t.kind == tPunct && t.text == ")" {
				break
			}
			if t.kind != tPunct || t.text != "," {
				return nil, p.fail(t)
			}
		}
	} else {
		p.next()
	}
	if len(args) != nargs {
		return nil, validation("Invalid %s: Incorrect number of operands for operator or function; operator or function: %s, number of operands: %d", p.what, name.text, len(args))
	}
	switch name.text {
	case "attribute_exists", "attribute_not_exists", "size", "attribute_type":
		if args[0].kind != ePath {
			return nil, validation("Invalid %s: Operator or function requires a document path; operator or function: %s", p.what, name.text)
		}
	}
	switch name.text {
	case "size":
		return &expr{kind: eSize, path: args[0].path}, nil
	case "attribute_type":
		if args[1].kind == eValue {
			if args[1].val.Kind != kS {
				return nil, validation("Invalid %s: Incorrect operand type for operator or function; operator or function: attribute_type, operand type: %s", p.what, args[1].val.Kind)
			}
			if kindOf(args[1].val.S) == kInvalid {
				return nil, validation("Invalid %s: Invalid attribute type name found; type: %s, valid types: {B,NULL,SS,BOOL,L,BS,N,NS,S,M}", p.what, args[1].val.S)
			}
		}
	case "begins_with":
		for _, a := range args {
			if a.kind == eValue && a.val.Kind != kS && a.val.Kind != kB {
				return nil, validation("Invalid %s: Incorrect operand type for operator or function; operator or function: begins_with, operand type: %s", p.what, a.val.Kind)
			}
		}
	}
	return &expr{kind: eFunc, op: name.text, args: args}, nil
}

// ---- evaluation ----

func evalOperand(e *expr, it Item) (AV, bool) {
	switch e.kind {
	case ePath:
		return getPath(it, e.path)
	case eValue:
		return e.val, true
	case eSize:
		v, ok := getPath(it, e.path)
		if !ok {
			return AV{}, false
		}
		n := 0
		switch v.Kind {
		case kS:
			n = len(v.S)
		case kB:
			n = len(v.B)
		case kSS, kNS:
			n = len(v.SS)
		case kBS:
			n = len(v.BS)
		case kM:
			n = len(v.M)
		case kL:
			n = len(v.L)
		default:
			return AV{}, false
		}
		return NumInt(int64(n)), true
	}
	return AV{}, false
}

// evalCond evaluates a condition against an item (an empty item when the item
// does not exist).
func evalCond(e *expr, it Item) bool {
	switch e.kind {
	case eAnd:
		return evalCond(e.args[0], it) && evalCond(e.args[1], it)
	case eOr:
		return evalCond(e.args[0], it) || evalCond(e.args[1], it)
	case eNot:
		return !evalCond(e.args[0], it)
	case eCmp:
		a, okA := evalOperand(e.args[0], it)
		b, okB := evalOperand(e.args[1], it)
		if !okA || !okB {
			return e.op == "<>"
		}
		switch e.op {
		case "=":
			return equalAV(a, b)
		case "<>":
			return !equalAV(a, b)
		}
		c, ok := compareAV(a, b)
		if !ok {
			return false
		}
		switch e.op {
		case "<":
			return c < 0
		case "<=":
			return c <= 0
		case ">":
			return c > 0
		case ">=":
			return c >= 0
		}
	case eBetween:
		v, ok := evalOperand(e.args[0], it)
		lo, ok2 := evalOperand(e.args[1], it)
		hi, ok3 := evalOperand(e.args[2], it)
		if !ok || !ok2 || !ok3 {
			return false
		}
		c1, ok := compareAV(v, lo)
		c2, ok2 := compareAV(v, hi)
		return ok && ok2 && c1 >= 0 && c2 <= 0
	case eIn:
		v, ok := evalOperand(e.args[0], it)
		if !ok {
			return false
		}
		for _, a := range e.args[1:] {
			if w, ok := evalOperand(a, it); ok && equalAV(v, w) {
				return true
			}
		}
		return false
	case eFunc:
		return evalFunc(e, it)
	}
	return false
}

func evalFunc(e *expr, it Item) bool {
	switch e.op {
	case "attribute_exists":
		_, ok := getPath(it, e.args[0].path)
		return ok
	case "attribute_not_exists":
		_, ok := getPath(it, e.args[0].path)
		return !ok
	case "attribute_type":
		v, ok := getPath(it, e.args[0].path)
		t, ok2 := evalOperand(e.args[1], it)
		return ok && ok2 && t.Kind == kS && v.Kind.String() == t.S
	case "begins_with":
		v, ok := evalOperand(e.args[0], it)
		pre, ok2 := evalOperand(e.args[1], it)
		if !ok || !ok2 || v.Kind != pre.Kind {
			return false
		}
		switch v.Kind {
		case kS:
			return strings.HasPrefix(v.S, pre.S)
		case kB:
			return len(v.B) >= len(pre.B) && string(v.B[:len(pre.B)]) == string(pre.B)
		}
		return false
	case "contains":
		v, ok := evalOperand(e.args[0], it)
		x, ok2 := evalOperand(e.args[1], it)
		if !ok || !ok2 {
			return false
		}
		switch v.Kind {
		case kS:
			return x.Kind == kS && strings.Contains(v.S, x.S)
		case kB:
			return x.Kind == kB && strings.Contains(string(v.B), string(x.B))
		case kSS:
			return x.Kind == kS && containsStr(v.SS, x.S)
		case kNS:
			if x.Kind != kN {
				return false
			}
			for _, s := range v.SS {
				if c, ok := compareAV(Num(s), x); ok && c == 0 {
					return true
				}
			}
			return false
		case kBS:
			if x.Kind != kB {
				return false
			}
			for _, b := range v.BS {
				if string(b) == string(x.B) {
					return true
				}
			}
			return false
		case kL:
			for _, el := range v.L {
				if equalAV(el, x) {
					return true
				}
			}
		}
	}
	return false
}

func containsStr(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}

// condPaths collects the top-level attribute names a condition reads.
func condAttrs(e *expr, out map[string]bool) {
	if e == nil {
		return
	}
	switch e.kind {
	case ePath, eSize:
		out[e.path[0].Name] = true
	}
	for _, a := range e.args {
		condAttrs(a, out)
	}
	if e.kind == eFunc || e.kind == eIfNotExists {
		for _, a := range e.args {
			if a.kind == ePath {
				out[a.path[0].Name] = true
			}
		}
	}
}

// ---- key conditions ----

// keyCond is a parsed key condition: equality on the partition key and an
// optional sort key predicate.
type keyCond struct {
	PK   AV
	SKOp string // "", "=", "<", "<=", ">", ">=", "BETWEEN", "begins_with"
	SK1  AV
	SK2  AV
}

func parseKeyCondition(src string, ctx *exprCtx, ks schema) (keyCond, error) {
	const what = "KeyConditionExpression"
	var kc keyCond
	e, err := parseCondition(src, what, ctx)
	if err != nil {
		return kc, err
	}
	var conj []*expr
	var flatten func(e *expr) error
	flatten = func(e *expr) error {
		switch e.kind {
		case eAnd:
			if err := flatten(e.args[0]); err != nil {
				return err
			}
			return flatten(e.args[1])
		case eOr:
			return validation("Invalid operator used in KeyConditionExpression: OR")
		case eNot:
			return validation("Invalid operator used in KeyConditionExpression: NOT")
		case eIn:
			return validation("Invalid operator used in KeyConditionExpression: IN")
		}
		conj = append(conj, e)
		return nil
	}
	if err := flatten(e); err != nil {
		return kc, err
	}
	if len(conj) > 2 {
		return kc, validation("Conditions can be of length 1 or 2 only")
	}
	seen := map[string]bool{}
	for _, c := range conj {
		var attr string
		var op string
		var vals []AV
		switch c.kind {
		case eCmp:
			l, r := c.args[0], c.args[1]
			op = c.op
			if l.kind == eValue && r.kind == ePath {
				l, r = r, l
				op = flipCmp(op)
			}
			if l.kind != ePath || r.kind != eValue {
				return kc, validation("Invalid condition in KeyConditionExpression: %s operator must have one attribute and one value", c.op)
			}
			if len(l.path) != 1 {
				return kc, validation("KeyConditionExpressions cannot contain nested attribute references; key: %s", l.path)
			}
			if op == "<>" {
				return kc, validation("Unsupported operator in KeyConditionExpression: <>")
			}
			attr, vals = l.path[0].Name, []AV{r.val}
		case eBetween:
			if c.args[0].kind != ePath || c.args[1].kind != eValue || c.args[2].kind != eValue {
				return kc, validation("Invalid condition in KeyConditionExpression: BETWEEN operator must have one attribute and two values")
			}
			attr, op, vals = c.args[0].path[0].Name, "BETWEEN", []AV{c.args[1].val, c.args[2].val}
		case eFunc:
			if c.op != "begins_with" {
				return kc, validation("Invalid operator used in KeyConditionExpression: %s", c.op)
			}
			if c.args[0].kind != ePath || c.args[1].kind != eValue {
				return kc, validation("Invalid condition in KeyConditionExpression: begins_with operator must have one attribute and one value")
			}
			attr, op, vals = c.args[0].path[0].Name, "begins_with", []AV{c.args[1].val}
		default:
			return kc, validation("Invalid condition in KeyConditionExpression")
		}
		if seen[attr] {
			return kc, validation("KeyConditionExpressions must only contain one condition per key")
		}
		seen[attr] = true
		var def KeyDef
		switch {
		case attr == ks.PK.Name:
			if op != "=" {
				return kc, validation("Query key condition not supported")
			}
			def = ks.PK
		case ks.SK != nil && attr == ks.SK.Name:
			def = *ks.SK
		default:
			return kc, validation("Query condition missed key schema element: %s", ks.PK.Name)
		}
		for _, v := range vals {
			if v.Kind.String() != def.Type {
				if op == "begins_with" && def.Type == "N" {
					return kc, validation("Invalid KeyConditionExpression: Incorrect operand type for operator or function; operator or function: begins_with, operand type: N")
				}
				return kc, invalidParam("Condition parameter type does not match schema type")
			}
		}
		if op == "begins_with" && def.Type == "N" {
			return kc, validation("Invalid KeyConditionExpression: Incorrect operand type for operator or function; operator or function: begins_with, operand type: N")
		}
		if attr == ks.PK.Name {
			kc.PK = vals[0]
		} else {
			kc.SKOp, kc.SK1 = op, vals[0]
			if len(vals) > 1 {
				kc.SK2 = vals[1]
			}
		}
	}
	if !seen[ks.PK.Name] {
		return kc, validation("Query condition missed key schema element: %s", ks.PK.Name)
	}
	if err := checkKeyAttr(ks.PK, kc.PK, ""); err != nil {
		return kc, err
	}
	return kc, nil
}

func flipCmp(op string) string {
	switch op {
	case "<":
		return ">"
	case "<=":
		return ">="
	case ">":
		return "<"
	case ">=":
		return "<="
	}
	return op
}

// skMatch reports where sk lies relative to the sort key condition: -1 below
// the range, 0 inside, +1 above.
func (kc keyCond) skMatch(sk AV) int {
	c1, _ := compareAV(sk, kc.SK1)
	switch kc.SKOp {
	case "":
		return 0
	case "=":
		return sign(c1)
	case "<":
		if c1 < 0 {
			return 0
		}
		return 1
	case "<=":
		if c1 <= 0 {
			return 0
		}
		return 1
	case ">":
		if c1 > 0 {
			return 0
		}
		return -1
	case ">=":
		if c1 >= 0 {
			return 0
		}
		return -1
	case "BETWEEN":
		if c1 < 0 {
			return -1
		}
		if c2, _ := compareAV(sk, kc.SK2); c2 > 0 {
			return 1
		}
		return 0
	case "begins_with":
		switch sk.Kind {
		case kS:
			if strings.HasPrefix(sk.S, kc.SK1.S) {
				return 0
			}
		case kB:
			if len(sk.B) >= len(kc.SK1.B) && string(sk.B[:len(kc.SK1.B)]) == string(kc.SK1.B) {
				return 0
			}
		}
		return sign(c1)
	}
	return 0
}

func sign(c int) int {
	switch {
	case c < 0:
		return -1
	case c > 0:
		return 1
	}
	return 0
}

// lowerBound is a key prefix to seek to for the sort key condition (after the
// partition prefix), or nil to start at the partition's first item.
func (kc keyCond) lowerBound() []byte {
	switch kc.SKOp {
	case "=", ">", ">=", "BETWEEN":
		return appendComponent(nil, kc.SK1)
	case "begins_with":
		return escapeComponent(nil, rawKeyBytes(kc.SK1))
	}
	return nil
}

// ---- projection ----

type projection struct {
	paths []docPath
}

func parseProjection(src string, ctx *exprCtx) (*projection, error) {
	const what = "ProjectionExpression"
	p, err := newParser(src, what, ctx)
	if err != nil {
		return nil, err
	}
	var pr projection
	for {
		path, err := p.parsePath()
		if err != nil {
			return nil, err
		}
		pr.paths = append(pr.paths, path)
		t := p.next()
		if t.kind == tEOF {
			break
		}
		if t.kind != tPunct || t.text != "," {
			return nil, p.fail(t)
		}
	}
	if err := checkPaths(what, pr.paths); err != nil {
		return nil, err
	}
	return &pr, nil
}

func projectionOf(names []string) *projection {
	pr := &projection{}
	for _, n := range names {
		pr.paths = append(pr.paths, docPath{{Name: n}})
	}
	return pr
}

// apply returns the projected item. List elements selected by index are
// returned in index order, compacted.
func (pr *projection) apply(it Item) Item {
	if pr == nil || it == nil {
		return it
	}
	out := Item{}
	type node struct {
		v        AV
		children map[string]*node
		elems    map[int]*node
		leaf     bool
	}
	root := &node{children: map[string]*node{}}
	for _, p := range pr.paths {
		cur := AV{Kind: kM, M: it}
		n := root
		ok := true
		for _, e := range p {
			if e.IsIdx {
				if cur.Kind != kL || e.Index >= len(cur.L) {
					ok = false
					break
				}
				cur = cur.L[e.Index]
				if n.elems == nil {
					n.elems = map[int]*node{}
				}
				c := n.elems[e.Index]
				if c == nil {
					c = &node{}
					n.elems[e.Index] = c
				}
				n = c
			} else {
				if cur.Kind != kM {
					ok = false
					break
				}
				v, has := cur.M[e.Name]
				if !has {
					ok = false
					break
				}
				cur = v
				if n.children == nil {
					n.children = map[string]*node{}
				}
				c := n.children[e.Name]
				if c == nil {
					c = &node{}
					n.children[e.Name] = c
				}
				n = c
			}
		}
		if ok {
			n.leaf, n.v = true, cur
		}
	}
	var build func(n *node) (AV, bool)
	build = func(n *node) (AV, bool) {
		if n.leaf {
			return n.v.clone(), true
		}
		if len(n.children) > 0 {
			m := Item{}
			for k, c := range n.children {
				if v, ok := build(c); ok {
					m[k] = v
				}
			}
			if len(m) == 0 {
				return AV{}, false
			}
			return Map(m), true
		}
		if len(n.elems) > 0 {
			idx := make([]int, 0, len(n.elems))
			for i := range n.elems {
				idx = append(idx, i)
			}
			sort.Ints(idx)
			var l []AV
			for _, i := range idx {
				if v, ok := build(n.elems[i]); ok {
					l = append(l, v)
				}
			}
			if len(l) == 0 {
				return AV{}, false
			}
			return List(l), true
		}
		return AV{}, false
	}
	for k, c := range root.children {
		if v, ok := build(c); ok {
			out[k] = v
		}
	}
	return out
}
