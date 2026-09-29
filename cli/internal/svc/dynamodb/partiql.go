package dynamodb

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"strconv"
	"strings"

	"github.com/homecloudhq/homecloud/cli/internal/awsapi"
	bolt "go.etcd.io/bbolt"
)

// A PartiQL subset: SELECT, INSERT, UPDATE and DELETE on one table, with ?
// parameters, translated to the same expression trees as the JSON API.
//
//	SELECT * | path, ... FROM "table"["index"] [WHERE cond]
//	INSERT INTO "table" VALUE {'attr': value, ...}
//	UPDATE "table" SET path = value [, ...] [REMOVE path, ...] WHERE key = ... [AND cond]
//	DELETE FROM "table" WHERE key = ... [AND cond]

type pqTok struct {
	kind byte // i ident, q quoted ident, s string, n number, p punct, ? param, e EOF
	text string
	pos  int
}

func pqLex(src string) ([]pqTok, error) {
	var out []pqTok
	i := 0
	for i < len(src) {
		c := src[i]
		switch {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			i++
		case c == '\'' || c == '"':
			j := i + 1
			var b strings.Builder
			for {
				if j >= len(src) {
					return nil, pqSyntax(src, i)
				}
				if src[j] == c {
					if j+1 < len(src) && src[j+1] == c {
						b.WriteByte(c)
						j += 2
						continue
					}
					break
				}
				b.WriteByte(src[j])
				j++
			}
			k := byte('s')
			if c == '"' {
				k = 'q'
			}
			out = append(out, pqTok{k, b.String(), i})
			i = j + 1
		case isIdentStart(c):
			j := i
			for j < len(src) && isIdentChar(src[j]) {
				j++
			}
			out = append(out, pqTok{'i', src[i:j], i})
			i = j
		case c >= '0' && c <= '9' || (c == '.' && i+1 < len(src) && src[i+1] >= '0' && src[i+1] <= '9'):
			j := i
			for j < len(src) && (src[j] >= '0' && src[j] <= '9' || src[j] == '.' || src[j] == 'e' || src[j] == 'E' ||
				((src[j] == '-' || src[j] == '+') && (src[j-1] == 'e' || src[j-1] == 'E'))) {
				j++
			}
			out = append(out, pqTok{'n', src[i:j], i})
			i = j
		case c == '?':
			out = append(out, pqTok{'?', "?", i})
			i++
		default:
			two := ""
			if i+1 < len(src) {
				two = src[i : i+2]
			}
			switch two {
			case "<>", "!=", "<=", ">=", "<<", ">>":
				out = append(out, pqTok{'p', two, i})
				i += 2
				continue
			}
			if strings.IndexByte("()[]{},.:=<>*+-", c) < 0 {
				return nil, pqSyntax(src, i)
			}
			out = append(out, pqTok{'p', string(c), i})
			i++
		}
	}
	return append(out, pqTok{'e', "<EOF>", len(src)}), nil
}

func pqSyntax(src string, pos int) error {
	tok := "<EOF>"
	if pos < len(src) {
		end := pos + 1
		for end < len(src) && src[end] != ' ' {
			end++
		}
		tok = src[pos:end]
	}
	return validation("Statement wasn't well formed, can't be processed: unexpected token %q at position %d", tok, pos+1)
}

type pqParser struct {
	toks   []pqTok
	pos    int
	src    string
	params []AV
	nparam int
}

func (p *pqParser) peek() pqTok { return p.toks[p.pos] }
func (p *pqParser) next() pqTok {
	t := p.toks[p.pos]
	if t.kind != 'e' {
		p.pos++
	}
	return t
}
func (p *pqParser) fail() error { return pqSyntax(p.src, p.peek().pos) }
func (p *pqParser) kw(word string) bool {
	t := p.peek()
	if t.kind == 'i' && strings.EqualFold(t.text, word) {
		p.pos++
		return true
	}
	return false
}
func (p *pqParser) isPunct(s string) bool {
	t := p.peek()
	return t.kind == 'p' && t.text == s
}
func (p *pqParser) punct(s string) error {
	if !p.isPunct(s) {
		return p.fail()
	}
	p.pos++
	return nil
}

func (p *pqParser) name() (string, error) {
	t := p.next()
	if t.kind != 'i' && t.kind != 'q' {
		return "", pqSyntax(p.src, t.pos)
	}
	return t.text, nil
}

func (p *pqParser) path() (docPath, error) {
	n, err := p.name()
	if err != nil {
		return nil, err
	}
	path := docPath{{Name: n}}
	for {
		switch {
		case p.isPunct("."):
			p.pos++
			n, err := p.name()
			if err != nil {
				return nil, err
			}
			path = append(path, pathElem{Name: n})
		case p.isPunct("["):
			p.pos++
			t := p.next()
			switch t.kind {
			case 'n':
				idx, err := strconv.Atoi(t.text)
				if err != nil {
					return nil, pqSyntax(p.src, t.pos)
				}
				path = append(path, pathElem{Index: idx, IsIdx: true})
			case 's':
				path = append(path, pathElem{Name: t.text})
			default:
				return nil, pqSyntax(p.src, t.pos)
			}
			if err := p.punct("]"); err != nil {
				return nil, err
			}
		default:
			return path, nil
		}
	}
}

// literal parses a value: 'str', number, true/false/null, {...}, [...], <<...>> or ?.
func (p *pqParser) literal() (AV, error) {
	t := p.peek()
	switch t.kind {
	case 's':
		p.pos++
		return Str(t.text), nil
	case 'n':
		p.pos++
		d, err := parseNumber(t.text)
		if err != nil {
			return AV{}, err
		}
		return Num(d.String()), nil
	case '?':
		p.pos++
		if p.nparam >= len(p.params) {
			return AV{}, validation("Number of parameters in request and statement don't match.")
		}
		v := p.params[p.nparam]
		p.nparam++
		return v, nil
	case 'i':
		switch strings.ToLower(t.text) {
		case "true", "false":
			p.pos++
			return Bool(strings.EqualFold(t.text, "true")), nil
		case "null":
			p.pos++
			return Null(), nil
		}
	case 'p':
		switch t.text {
		case "-":
			p.pos++
			n := p.next()
			if n.kind != 'n' {
				return AV{}, pqSyntax(p.src, n.pos)
			}
			d, err := parseNumber("-" + n.text)
			if err != nil {
				return AV{}, err
			}
			return Num(d.String()), nil
		case "{":
			p.pos++
			m := Item{}
			for !p.isPunct("}") {
				k := p.next()
				if k.kind != 's' && k.kind != 'q' {
					return AV{}, pqSyntax(p.src, k.pos)
				}
				if err := p.punct(":"); err != nil {
					return AV{}, err
				}
				v, err := p.literal()
				if err != nil {
					return AV{}, err
				}
				m[k.text] = v
				if !p.isPunct(",") {
					break
				}
				p.pos++
			}
			if err := p.punct("}"); err != nil {
				return AV{}, err
			}
			return Map(m), nil
		case "[":
			p.pos++
			l := []AV{}
			for !p.isPunct("]") {
				v, err := p.literal()
				if err != nil {
					return AV{}, err
				}
				l = append(l, v)
				if !p.isPunct(",") {
					break
				}
				p.pos++
			}
			if err := p.punct("]"); err != nil {
				return AV{}, err
			}
			return List(l), nil
		case "<<":
			p.pos++
			var elems []AV
			for !p.isPunct(">>") {
				v, err := p.literal()
				if err != nil {
					return AV{}, err
				}
				elems = append(elems, v)
				if !p.isPunct(",") {
					break
				}
				p.pos++
			}
			if err := p.punct(">>"); err != nil {
				return AV{}, err
			}
			return makeSet(elems)
		}
	}
	return AV{}, p.fail()
}

func makeSet(elems []AV) (AV, error) {
	if len(elems) == 0 {
		return AV{}, validation("Set must not be empty")
	}
	switch elems[0].Kind {
	case kS, kN:
		v := AV{Kind: kSS}
		if elems[0].Kind == kN {
			v.Kind = kNS
		}
		for _, e := range elems {
			if e.Kind != elems[0].Kind {
				return AV{}, validation("Sets must contain elements of one type")
			}
			v.SS = append(v.SS, e.S)
		}
		return v, v.validate()
	case kB:
		v := AV{Kind: kBS}
		for _, e := range elems {
			if e.Kind != kB {
				return AV{}, validation("Sets must contain elements of one type")
			}
			v.BS = append(v.BS, e.B)
		}
		return v, v.validate()
	}
	return AV{}, validation("Sets can only contain strings, numbers or binary values")
}

// operand parses a path, literal or size(path).
func (p *pqParser) operand() (*expr, error) {
	t := p.peek()
	if t.kind == 'i' && strings.EqualFold(t.text, "size") && p.toks[p.pos+1].text == "(" {
		p.pos += 2
		path, err := p.path()
		if err != nil {
			return nil, err
		}
		if err := p.punct(")"); err != nil {
			return nil, err
		}
		return &expr{kind: eSize, path: path}, nil
	}
	if t.kind == 'q' || (t.kind == 'i' && !isPQLiteralWord(t.text)) {
		path, err := p.path()
		if err != nil {
			return nil, err
		}
		return &expr{kind: ePath, path: path}, nil
	}
	v, err := p.literal()
	if err != nil {
		return nil, err
	}
	return valExpr(v), nil
}

func isPQLiteralWord(s string) bool {
	switch strings.ToLower(s) {
	case "true", "false", "null":
		return true
	}
	return false
}

func (p *pqParser) cond() (*expr, error) {
	l, err := p.andCond()
	if err != nil {
		return nil, err
	}
	for p.kw("OR") {
		r, err := p.andCond()
		if err != nil {
			return nil, err
		}
		l = &expr{kind: eOr, args: []*expr{l, r}}
	}
	return l, nil
}

func (p *pqParser) andCond() (*expr, error) {
	l, err := p.notCond()
	if err != nil {
		return nil, err
	}
	for p.kw("AND") {
		r, err := p.notCond()
		if err != nil {
			return nil, err
		}
		l = &expr{kind: eAnd, args: []*expr{l, r}}
	}
	return l, nil
}

func (p *pqParser) notCond() (*expr, error) {
	if p.kw("NOT") {
		e, err := p.notCond()
		if err != nil {
			return nil, err
		}
		return &expr{kind: eNot, args: []*expr{e}}, nil
	}
	return p.primary()
}

func (p *pqParser) primary() (*expr, error) {
	if p.isPunct("(") {
		p.pos++
		e, err := p.cond()
		if err != nil {
			return nil, err
		}
		return e, p.punct(")")
	}
	t := p.peek()
	if t.kind == 'i' && p.toks[p.pos+1].kind == 'p' && p.toks[p.pos+1].text == "(" {
		fn := strings.ToLower(t.text)
		switch fn {
		case "begins_with", "contains", "attribute_type":
			p.pos += 2
			path, err := p.path()
			if err != nil {
				return nil, err
			}
			if err := p.punct(","); err != nil {
				return nil, err
			}
			v, err := p.literal()
			if err != nil {
				return nil, err
			}
			if err := p.punct(")"); err != nil {
				return nil, err
			}
			return &expr{kind: eFunc, op: fn, args: []*expr{{kind: ePath, path: path}, valExpr(v)}}, nil
		}
	}
	l, err := p.operand()
	if err != nil {
		return nil, err
	}
	switch {
	case p.kw("IS"):
		not := p.kw("NOT")
		switch {
		case p.kw("MISSING"):
		case p.kw("NULL"):
			e := &expr{kind: eFunc, op: "attribute_type", args: []*expr{l, valExpr(Str("NULL"))}}
			if not {
				e = &expr{kind: eNot, args: []*expr{e}}
			}
			return e, nil
		default:
			return nil, p.fail()
		}
		if l.kind != ePath {
			return nil, p.fail()
		}
		fn := "attribute_not_exists"
		if not {
			fn = "attribute_exists"
		}
		return &expr{kind: eFunc, op: fn, args: []*expr{l}}, nil
	case p.kw("BETWEEN"):
		lo, err := p.operand()
		if err != nil {
			return nil, err
		}
		if !p.kw("AND") {
			return nil, p.fail()
		}
		hi, err := p.operand()
		if err != nil {
			return nil, err
		}
		return &expr{kind: eBetween, args: []*expr{l, lo, hi}}, nil
	case p.kw("IN"):
		closeTok := "]"
		if p.isPunct("(") {
			closeTok = ")"
		} else if !p.isPunct("[") {
			return nil, p.fail()
		}
		p.pos++
		e := &expr{kind: eIn, args: []*expr{l}}
		for !p.isPunct(closeTok) {
			v, err := p.literal()
			if err != nil {
				return nil, err
			}
			e.args = append(e.args, valExpr(v))
			if !p.isPunct(",") {
				break
			}
			p.pos++
		}
		return e, p.punct(closeTok)
	}
	op := p.peek()
	if op.kind == 'p' {
		sym := op.text
		if sym == "!=" {
			sym = "<>"
		}
		if isComparator(sym) {
			p.pos++
			r, err := p.operand()
			if err != nil {
				return nil, err
			}
			return &expr{kind: eCmp, op: sym, args: []*expr{l, r}}, nil
		}
	}
	return nil, p.fail()
}

// ---- statements ----

type pqStmt struct {
	kind  string // SELECT INSERT UPDATE DELETE
	table string
	index string
	proj  *projection
	where *expr
	item  Item
	upd   *updateExpr
}

func parsePartiQL(src string, params []AV) (*pqStmt, error) {
	for i := range params {
		if err := params[i].validate(); err != nil {
			return nil, err
		}
	}
	toks, err := pqLex(src)
	if err != nil {
		return nil, err
	}
	p := &pqParser{toks: toks, src: src, params: params}
	st := &pqStmt{}
	switch {
	case p.kw("SELECT"):
		st.kind = "SELECT"
		if p.isPunct("*") {
			p.pos++
		} else {
			st.proj = &projection{}
			for {
				path, err := p.path()
				if err != nil {
					return nil, err
				}
				st.proj.paths = append(st.proj.paths, path)
				if !p.isPunct(",") {
					break
				}
				p.pos++
			}
		}
		if !p.kw("FROM") {
			return nil, p.fail()
		}
		if st.table, err = p.name(); err != nil {
			return nil, err
		}
		if p.isPunct(".") {
			p.pos++
			if st.index, err = p.name(); err != nil {
				return nil, err
			}
		}
		if p.kw("WHERE") {
			if st.where, err = p.cond(); err != nil {
				return nil, err
			}
		}
	case p.kw("INSERT"):
		st.kind = "INSERT"
		if !p.kw("INTO") {
			return nil, p.fail()
		}
		if st.table, err = p.name(); err != nil {
			return nil, err
		}
		if !p.kw("VALUE") {
			return nil, p.fail()
		}
		v, err := p.literal()
		if err != nil {
			return nil, err
		}
		if v.Kind != kM {
			return nil, validation("Unexpected value type in INSERT: expected a tuple")
		}
		st.item = v.M
	case p.kw("UPDATE"):
		st.kind = "UPDATE"
		if st.table, err = p.name(); err != nil {
			return nil, err
		}
		st.upd = &updateExpr{}
		for {
			switch {
			case p.kw("SET"):
				path, err := p.path()
				if err != nil {
					return nil, err
				}
				if err := p.punct("="); err != nil {
					return nil, err
				}
				a, err := p.setValue(path)
				if err != nil {
					return nil, err
				}
				st.upd.actions = append(st.upd.actions, a)
				for p.isPunct(",") {
					p.pos++
					path, err := p.path()
					if err != nil {
						return nil, err
					}
					if err := p.punct("="); err != nil {
						return nil, err
					}
					a, err := p.setValue(path)
					if err != nil {
						return nil, err
					}
					st.upd.actions = append(st.upd.actions, a)
				}
				continue
			case p.kw("REMOVE"):
				for {
					path, err := p.path()
					if err != nil {
						return nil, err
					}
					st.upd.actions = append(st.upd.actions, updateAction{kind: "REMOVE", path: path})
					if !p.isPunct(",") {
						break
					}
					p.pos++
				}
				continue
			}
			break
		}
		if len(st.upd.actions) == 0 {
			return nil, p.fail()
		}
		if !p.kw("WHERE") {
			return nil, validation("Where clause does not contain a mandatory equality on all key attributes")
		}
		if st.where, err = p.cond(); err != nil {
			return nil, err
		}
	case p.kw("DELETE"):
		st.kind = "DELETE"
		if !p.kw("FROM") {
			return nil, p.fail()
		}
		if st.table, err = p.name(); err != nil {
			return nil, err
		}
		if !p.kw("WHERE") {
			return nil, validation("Where clause does not contain a mandatory equality on all key attributes")
		}
		if st.where, err = p.cond(); err != nil {
			return nil, err
		}
	default:
		return nil, p.fail()
	}
	if p.kw("RETURNING") {
		return nil, validation("RETURNING is not supported by HomeCloud")
	}
	if p.peek().kind != 'e' {
		return nil, p.fail()
	}
	if p.nparam != len(params) {
		return nil, validation("Number of parameters in request and statement don't match.")
	}
	if st.upd != nil {
		paths := make([]docPath, len(st.upd.actions))
		for i, a := range st.upd.actions {
			paths[i] = a.path
		}
		if err := checkPaths("UpdateExpression", paths); err != nil {
			return nil, err
		}
	}
	return st, nil
}

// setValue parses the right side of SET path = ...: a value, path +/- value,
// list_append(a, b), set_add(path, set) or set_delete(path, set).
func (p *pqParser) setValue(path docPath) (updateAction, error) {
	t := p.peek()
	if t.kind == 'i' && p.toks[p.pos+1].text == "(" {
		fn := strings.ToLower(t.text)
		if fn == "list_append" || fn == "set_add" || fn == "set_delete" {
			p.pos += 2
			a, err := p.operand()
			if err != nil {
				return updateAction{}, err
			}
			if err := p.punct(","); err != nil {
				return updateAction{}, err
			}
			b, err := p.operand()
			if err != nil {
				return updateAction{}, err
			}
			if err := p.punct(")"); err != nil {
				return updateAction{}, err
			}
			switch fn {
			case "list_append":
				return updateAction{kind: "SET", path: path, val: &expr{kind: eListAppend, args: []*expr{a, b}}}, nil
			case "set_add":
				return updateAction{kind: "ADD", path: path, val: b}, nil
			default:
				return updateAction{kind: "DELETE", path: path, val: b}, nil
			}
		}
	}
	l, err := p.operand()
	if err != nil {
		return updateAction{}, err
	}
	if p.isPunct("+") || p.isPunct("-") {
		k := ePlus
		if p.next().text == "-" {
			k = eMinus
		}
		r, err := p.operand()
		if err != nil {
			return updateAction{}, err
		}
		return updateAction{kind: "SET", path: path, val: &expr{kind: k, args: []*expr{l, r}}}, nil
	}
	return updateAction{kind: "SET", path: path, val: l}, nil
}

// keyFromWhere extracts equality conditions on the key attributes from the
// top-level conjunction of where.
func keyFromWhere(where *expr, ks schema) Item {
	key := Item{}
	var walk func(e *expr)
	walk = func(e *expr) {
		switch e.kind {
		case eAnd:
			walk(e.args[0])
			walk(e.args[1])
		case eCmp:
			if e.op != "=" {
				return
			}
			l, r := e.args[0], e.args[1]
			if l.kind == eValue && r.kind == ePath {
				l, r = r, l
			}
			if l.kind == ePath && r.kind == eValue && len(l.path) == 1 && ks.has(l.path[0].Name) {
				key[l.path[0].Name] = r.val
			}
		}
	}
	if where != nil {
		walk(where)
	}
	return key
}

func (s *Service) authorizePQ(q *awsapi.Req, st *pqStmt) error {
	action := map[string]string{"SELECT": "dynamodb:PartiQLSelect", "INSERT": "dynamodb:PartiQLInsert", "UPDATE": "dynamodb:PartiQLUpdate", "DELETE": "dynamodb:PartiQLDelete"}[st.kind]
	res := s.tableARN(st.table)
	if st.index != "" {
		res += "/index/" + st.index
	}
	return q.Authorize(action, res)
}

// pqWrite converts a write statement to a transaction operation.
func (s *Service) pqWrite(st *pqStmt) (txOp, error) {
	o := txOp{table: st.table}
	t, err := s.itemTable(st.table)
	if err != nil {
		return o, err
	}
	ks := t.schema()
	exists := &expr{kind: eFunc, op: "attribute_exists", args: []*expr{pathExpr(ks.PK.Name)}}
	switch st.kind {
	case "INSERT":
		o.kind, o.item = "Put", st.item
		if o.key, err = prepItem(t, st.item); err != nil {
			return o, err
		}
		o.cond = condition{expr: &expr{kind: eFunc, op: "attribute_not_exists", args: []*expr{pathExpr(ks.PK.Name)}}}
		return o, nil
	case "UPDATE", "DELETE":
		key := keyFromWhere(st.where, ks)
		if len(key) != len(ks.names()) {
			return o, validation("Where clause does not contain a mandatory equality on all key attributes")
		}
		if o.key, err = ks.keyFrom(key); err != nil {
			return o, err
		}
		o.keyIt = key
		if st.kind == "UPDATE" {
			for _, a := range st.upd.actions {
				if ks.has(a.path[0].Name) {
					return o, validation("One or more parameter values were invalid: Cannot update attribute %s. This attribute is part of the key", a.path[0].Name)
				}
			}
			o.kind, o.upd = "Update", st.upd
			o.cond = condition{expr: &expr{kind: eAnd, args: []*expr{exists, st.where}}}
		} else {
			o.kind = "Delete"
			o.cond = condition{expr: st.where}
		}
		return o, nil
	}
	return o, validation("unsupported statement")
}

type pqNext struct {
	Key Item `json:"k"`
}

func (s *Service) execStatement(q *awsapi.Req, stmt string, params []AV, consistent bool, limit int, nextToken string) (map[string]any, error) {
	st, err := parsePartiQL(stmt, params)
	if err != nil {
		return nil, err
	}
	if err := s.authorizePQ(q, st); err != nil {
		return nil, err
	}
	t, err := s.itemTable(st.table)
	if err != nil {
		return nil, err
	}
	if st.kind != "SELECT" {
		o, err := s.pqWrite(st)
		if err != nil {
			return nil, err
		}
		if o.kind == "Put" {
			err = s.itemTx(o.table, func(t *Table, tb *bolt.Bucket) error {
				_, err := s.putTx(tb, t, o.key, o.item, o.cond)
				return err
			})
			var ae *apiError
			if errors.As(err, &ae) && ae.Code == "ConditionalCheckFailedException" {
				return nil, errf("DuplicateItemException", "Duplicate primary key exists in table")
			}
		} else {
			err = s.itemTx(o.table, func(t *Table, tb *bolt.Bucket) error {
				if o.kind == "Update" {
					_, _, err := s.updateTx(tb, t, o.keyIt, o.key, exprUpdater(o.upd, t.schema()), o.cond)
					return err
				}
				_, err := s.deleteTx(tb, t, o.key, o.cond)
				return err
			})
		}
		if err != nil {
			return nil, err
		}
		return map[string]any{"Items": []Item{}}, nil
	}
	// SELECT: a query when the partition key is fixed, otherwise a scan.
	r := readReq{index: st.index, forward: true, filter: st.where, proj: st.proj, limit: limit}
	ks := t.schema()
	if st.index != "" {
		ix, local := t.index(st.index)
		if ix == nil {
			return nil, validation("The table does not have the specified index: %s", st.index)
		}
		ks = ix.schema()
		if consistent && !local {
			return nil, validation("Consistent reads are not supported on global secondary indexes")
		}
	}
	if pk, ok := keyFromWhere(st.where, schema{PK: ks.PK})[ks.PK.Name]; ok && pk.Kind.String() == ks.PK.Type {
		r.kc = &keyCond{PK: pk}
	}
	if nextToken != "" {
		b, err := base64.RawURLEncoding.DecodeString(nextToken)
		var nt pqNext
		if err != nil || json.Unmarshal(b, &nt) != nil {
			return nil, validation("Invalid NextToken")
		}
		r.start = nt.Key
	}
	rr, err := s.read(st.table, r)
	if err != nil {
		return nil, err
	}
	items := rr.items
	if items == nil {
		items = []Item{}
	}
	out := map[string]any{"Items": items}
	if rr.last != nil {
		b, _ := json.Marshal(pqNext{Key: rr.last})
		out["NextToken"] = base64.RawURLEncoding.EncodeToString(b)
		out["LastEvaluatedKey"] = rr.last
	}
	return out, nil
}

func (s *Service) awsExecuteStatement(q *awsapi.Req) (any, error) {
	var in struct {
		Statement              string
		Parameters             []AV
		ConsistentRead         bool
		NextToken              string
		Limit                  int
		ReturnConsumedCapacity string
	}
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	return s.execStatement(q, in.Statement, in.Parameters, in.ConsistentRead, in.Limit, in.NextToken)
}

func (s *Service) awsBatchExecuteStatement(q *awsapi.Req) (any, error) {
	var in struct {
		Statements []struct {
			Statement      string
			Parameters     []AV
			ConsistentRead bool
		}
		ReturnConsumedCapacity string
	}
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	if len(in.Statements) == 0 || len(in.Statements) > maxBatchWrite {
		return nil, validation("1 validation error detected: Value at 'statements' failed to satisfy constraint: Member must have length less than or equal to 25")
	}
	var responses []map[string]any
	for _, st := range in.Statements {
		parsed, err := parsePartiQL(st.Statement, st.Parameters)
		resp := map[string]any{}
		if err == nil {
			resp["TableName"] = parsed.table
			if parsed.kind == "SELECT" {
				// Batch reads must name a single item by its full key.
				t, terr := s.itemTable(parsed.table)
				if terr != nil {
					err = terr
				} else if key := keyFromWhere(parsed.where, t.schema()); len(key) != len(t.schema().names()) || parsed.index != "" {
					err = validation("Select statements within BatchExecuteStatement must specify the full primary key of a single item")
				}
			}
		}
		var out map[string]any
		if err == nil {
			out, err = s.execStatement(q, st.Statement, st.Parameters, st.ConsistentRead, 0, "")
		}
		if err != nil {
			var ae *apiError
			var aw *awsapi.Error
			code, msg := "InternalServerError", err.Error()
			switch {
			case errors.As(err, &ae):
				code, msg = strings.TrimSuffix(ae.Code, "Exception"), ae.Message
			case errors.As(err, &aw):
				if aw.Status == 403 {
					return nil, err
				}
				code, msg = strings.TrimSuffix(aw.Code, "Exception"), aw.Message
			}
			if code == "ConditionalCheckFailed" {
				code = "ConditionalCheckFailed"
			}
			resp["Error"] = map[string]any{"Code": code, "Message": msg}
		} else if items, ok := out["Items"].([]Item); ok && len(items) > 0 {
			resp["Item"] = items[0]
		}
		responses = append(responses, resp)
	}
	return map[string]any{"Responses": responses}, nil
}

func (s *Service) awsExecuteTransaction(q *awsapi.Req) (any, error) {
	var in struct {
		TransactStatements []struct {
			Statement  string
			Parameters []AV
		}
		ClientRequestToken     string
		ReturnConsumedCapacity string
	}
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	if len(in.TransactStatements) == 0 || len(in.TransactStatements) > maxTransact {
		return nil, validation("1 validation error detected: Value at 'transactStatements' failed to satisfy constraint: Member must have length less than or equal to %d", maxTransact)
	}
	var stmts []*pqStmt
	reads, writes := 0, 0
	for _, ts := range in.TransactStatements {
		st, err := parsePartiQL(ts.Statement, ts.Parameters)
		if err != nil {
			return nil, err
		}
		if err := s.authorizePQ(q, st); err != nil {
			return nil, err
		}
		if st.kind == "SELECT" {
			reads++
		} else {
			writes++
		}
		stmts = append(stmts, st)
	}
	if reads > 0 && writes > 0 {
		return nil, validation("Transaction must contain either all reads or all writes")
	}
	if reads > 0 {
		var out []map[string]any
		for i, st := range stmts {
			res, err := s.execStatement(q, in.TransactStatements[i].Statement, in.TransactStatements[i].Parameters, true, 0, "")
			if err != nil {
				return nil, err
			}
			r := map[string]any{}
			if items := res["Items"].([]Item); len(items) > 0 {
				r["Item"] = items[0]
			}
			_ = st
			out = append(out, r)
		}
		return map[string]any{"Responses": out}, nil
	}
	ops := make([]txOp, len(stmts))
	seen := map[string]bool{}
	for i, st := range stmts {
		o, err := s.pqWrite(st)
		if err != nil {
			return nil, err
		}
		id := o.table + "\x00" + string(o.key)
		if seen[id] {
			return nil, validation("Transaction request cannot include multiple operations on one item")
		}
		seen[id] = true
		ops[i] = o
	}
	if err := s.transact(ops, map[string]float64{}); err != nil {
		return nil, err
	}
	return map[string]any{}, nil
}
