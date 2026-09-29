package dynamodb

import (
	"sort"
	"strings"
)

// updateAction is one action of an update expression.
type updateAction struct {
	kind string // SET, REMOVE, ADD, DELETE
	path docPath
	val  *expr // SET value, ADD/DELETE operand
}

type updateExpr struct {
	actions []updateAction
}

const updWhat = "UpdateExpression"

func parseUpdate(src string, ctx *exprCtx) (*updateExpr, error) {
	p, err := newParser(src, updWhat, ctx)
	if err != nil {
		return nil, err
	}
	u := &updateExpr{}
	seen := map[string]bool{}
	for p.peek().kind != tEOF {
		t := p.next()
		kw := strings.ToUpper(t.text)
		if t.kind != tIdent || (kw != "SET" && kw != "REMOVE" && kw != "ADD" && kw != "DELETE") {
			return nil, p.fail(t)
		}
		if seen[kw] {
			return nil, validation("Invalid UpdateExpression: The %q section can only be used once in an update expression;", kw)
		}
		seen[kw] = true
		for {
			path, err := p.parsePath()
			if err != nil {
				return nil, err
			}
			a := updateAction{kind: kw, path: path}
			switch kw {
			case "SET":
				if err := p.expect("="); err != nil {
					return nil, err
				}
				if a.val, err = p.parseSetValue(); err != nil {
					return nil, err
				}
			case "ADD", "DELETE":
				if a.val, err = p.parseUpdOperand(); err != nil {
					return nil, err
				}
				if a.val.kind == eValue {
					k := a.val.val.Kind
					if kw == "ADD" && k != kN && !isSet(k) {
						return nil, validation("Invalid UpdateExpression: Incorrect operand type for operator or function; operator: ADD, operand type: %s", nameOfKind(k))
					}
					if kw == "DELETE" && !isSet(k) {
						return nil, validation("Invalid UpdateExpression: Incorrect operand type for operator or function; operator: DELETE, operand type: %s", nameOfKind(k))
					}
				}
			}
			u.actions = append(u.actions, a)
			t := p.peek()
			if t.kind == tPunct && t.text == "," {
				p.next()
				continue
			}
			break
		}
	}
	if len(u.actions) == 0 {
		return nil, validation("Invalid UpdateExpression: The expression can not be empty;")
	}
	paths := make([]docPath, len(u.actions))
	for i, a := range u.actions {
		paths[i] = a.path
	}
	if err := checkPaths(updWhat, paths); err != nil {
		return nil, err
	}
	return u, nil
}

func nameOfKind(k Kind) string {
	switch k {
	case kS:
		return "STRING"
	case kN:
		return "NUMBER"
	case kB:
		return "BINARY"
	case kM:
		return "MAP"
	case kL:
		return "LIST"
	case kBOOL:
		return "BOOLEAN"
	case kNULL:
		return "NULL"
	}
	return k.String()
}

// parseSetValue parses operand [+|- operand].
func (p *parser) parseSetValue() (*expr, error) {
	l, err := p.parseUpdOperand()
	if err != nil {
		return nil, err
	}
	t := p.peek()
	if t.kind == tPunct && (t.text == "+" || t.text == "-") {
		p.next()
		r, err := p.parseUpdOperand()
		if err != nil {
			return nil, err
		}
		k := ePlus
		if t.text == "-" {
			k = eMinus
		}
		for _, x := range []*expr{l, r} {
			if x.kind == eValue && x.val.Kind != kN {
				return nil, validation("Invalid UpdateExpression: Incorrect operand type for operator or function; operator: %s, operand type: %s", t.text, nameOfKind(x.val.Kind))
			}
		}
		return &expr{kind: k, args: []*expr{l, r}}, nil
	}
	return l, nil
}

// parseUpdOperand parses a path, :value, if_not_exists(path, operand) or
// list_append(operand, operand).
func (p *parser) parseUpdOperand() (*expr, error) {
	t := p.peek()
	switch t.kind {
	case tValue:
		p.next()
		return p.value(t)
	case tName:
		path, err := p.parsePath()
		if err != nil {
			return nil, err
		}
		return &expr{kind: ePath, path: path}, nil
	case tIdent:
		if p.peekN(1).kind == tPunct && p.peekN(1).text == "(" {
			p.next()
			p.next()
			var args []*expr
			for {
				a, err := p.parseUpdOperand()
				if err != nil {
					return nil, err
				}
				args = append(args, a)
				n := p.next()
				if n.kind == tPunct && n.text == ")" {
					break
				}
				if n.kind != tPunct || n.text != "," {
					return nil, p.fail(n)
				}
			}
			switch t.text {
			case "if_not_exists":
				if len(args) != 2 {
					return nil, validation("Invalid UpdateExpression: Incorrect number of operands for operator or function; operator or function: if_not_exists, number of operands: %d", len(args))
				}
				if args[0].kind != ePath {
					return nil, validation("Invalid UpdateExpression: Operator or function requires a document path; operator or function: if_not_exists")
				}
				return &expr{kind: eIfNotExists, path: args[0].path, args: args}, nil
			case "list_append":
				if len(args) != 2 {
					return nil, validation("Invalid UpdateExpression: Incorrect number of operands for operator or function; operator or function: list_append, number of operands: %d", len(args))
				}
				for _, a := range args {
					if a.kind == eValue && a.val.Kind != kL {
						return nil, validation("Invalid UpdateExpression: Incorrect operand type for operator or function; operator or function: list_append, operand type: %s", nameOfKind(a.val.Kind))
					}
				}
				return &expr{kind: eListAppend, args: args}, nil
			case "attribute_exists", "attribute_not_exists", "attribute_type", "begins_with", "contains", "size":
				return nil, validation("Invalid UpdateExpression: The function is not allowed in an update expression; function: %s", t.text)
			}
			return nil, validation("Invalid UpdateExpression: Invalid function name; function: %s", t.text)
		}
		path, err := p.parsePath()
		if err != nil {
			return nil, err
		}
		return &expr{kind: ePath, path: path}, nil
	}
	return nil, p.fail(t)
}

var (
	errUpdType    = validation("An operand in the update expression has an incorrect data type")
	errUpdMissing = validation("The provided expression refers to an attribute that does not exist in the item")
	errUpdPath    = validation("The document path provided in the update expression is invalid for update")
)

// evalUpd evaluates a SET/ADD/DELETE operand against the item before the update.
func evalUpd(e *expr, it Item) (AV, error) {
	switch e.kind {
	case ePath:
		v, ok := getPath(it, e.path)
		if !ok {
			return AV{}, errUpdMissing
		}
		return v, nil
	case eValue:
		return e.val, nil
	case eIfNotExists:
		if v, ok := getPath(it, e.path); ok {
			return v, nil
		}
		return evalUpd(e.args[1], it)
	case eListAppend:
		a, err := evalUpd(e.args[0], it)
		if err != nil {
			return AV{}, err
		}
		b, err := evalUpd(e.args[1], it)
		if err != nil {
			return AV{}, err
		}
		if a.Kind != kL || b.Kind != kL {
			return AV{}, errUpdType
		}
		l := make([]AV, 0, len(a.L)+len(b.L))
		l = append(l, a.L...)
		return List(append(l, b.L...)), nil
	case ePlus, eMinus:
		a, err := evalUpd(e.args[0], it)
		if err != nil {
			return AV{}, err
		}
		b, err := evalUpd(e.args[1], it)
		if err != nil {
			return AV{}, err
		}
		if a.Kind != kN || b.Kind != kN {
			return AV{}, errUpdType
		}
		x, _ := parseDecimal(a.S)
		y, _ := parseDecimal(b.S)
		if e.kind == eMinus {
			y = y.negate()
		}
		r, err := x.add(y)
		if err != nil {
			return AV{}, err
		}
		return Num(r.String()), nil
	}
	return AV{}, errUpdType
}

// apply returns the updated copy of it (it is not modified). keys are the
// table's key attribute names, which cannot be changed.
func (u *updateExpr) apply(it Item, keys schema) (Item, error) {
	for _, a := range u.actions {
		if keys.has(a.path[0].Name) {
			return nil, validation("One or more parameter values were invalid: Cannot update attribute %s. This attribute is part of the key", a.path[0].Name)
		}
	}
	// Every operand sees the item as it was before the update.
	vals := make([]AV, len(u.actions))
	for i, a := range u.actions {
		if a.val == nil {
			continue
		}
		v, err := evalUpd(a.val, it)
		if err != nil {
			return nil, err
		}
		vals[i] = v.clone()
	}
	out := cloneItem(it)
	root := Map(out)
	var removes []int
	for i, a := range u.actions {
		switch a.kind {
		case "SET":
			if err := setPath(&root, a.path, vals[i]); err != nil {
				return nil, err
			}
		case "REMOVE":
			removes = append(removes, i)
		}
	}
	// Remove list elements from the highest index down so indexes refer to the
	// list before the update.
	sort.SliceStable(removes, func(x, y int) bool {
		px, py := u.actions[removes[x]].path, u.actions[removes[y]].path
		lx, ly := px[len(px)-1], py[len(py)-1]
		return lx.IsIdx && ly.IsIdx && lx.Index > ly.Index
	})
	for _, i := range removes {
		if err := removePath(&root, u.actions[i].path); err != nil {
			return nil, err
		}
	}
	for i, a := range u.actions {
		switch a.kind {
		case "ADD":
			if err := addPath(&root, a.path, vals[i]); err != nil {
				return nil, err
			}
		case "DELETE":
			if err := deleteFromSet(&root, a.path, vals[i]); err != nil {
				return nil, err
			}
		}
	}
	return root.M, nil
}

func setPath(root *AV, path docPath, v AV) (err error) {
	return withParent(root, path, func(parent *AV, last pathElem) error {
		if last.IsIdx {
			if parent.Kind != kL {
				return errUpdPath
			}
			if last.Index < len(parent.L) {
				parent.L[last.Index] = v
			} else {
				parent.L = append(parent.L, v)
			}
			return nil
		}
		if parent.Kind != kM {
			return errUpdPath
		}
		parent.M[last.Name] = v
		return nil
	})
}

func removePath(root *AV, path docPath) error {
	return withParent(root, path, func(parent *AV, last pathElem) error {
		if last.IsIdx {
			if parent.Kind != kL {
				return errUpdPath
			}
			if last.Index < len(parent.L) {
				parent.L = append(parent.L[:last.Index:last.Index], parent.L[last.Index+1:]...)
			}
			return nil
		}
		if parent.Kind != kM {
			return errUpdPath
		}
		delete(parent.M, last.Name)
		return nil
	})
}

func addPath(root *AV, path docPath, v AV) error {
	return withParent(root, path, func(parent *AV, last pathElem) error {
		cur, ok := getChild(parent, last)
		var nv AV
		switch {
		case !ok:
			if v.Kind != kN && !isSet(v.Kind) {
				return errUpdType
			}
			nv = v
		case cur.Kind == kN && v.Kind == kN:
			x, _ := parseDecimal(cur.S)
			y, _ := parseDecimal(v.S)
			r, err := x.add(y)
			if err != nil {
				return err
			}
			nv = Num(r.String())
		case isSet(cur.Kind) && cur.Kind == v.Kind:
			nv = setUnion(cur, v)
		default:
			return errUpdType
		}
		return putChild(parent, last, nv)
	})
}

func deleteFromSet(root *AV, path docPath, v AV) error {
	return withParent(root, path, func(parent *AV, last pathElem) error {
		cur, ok := getChild(parent, last)
		if !ok {
			return nil
		}
		if !isSet(v.Kind) || cur.Kind != v.Kind {
			return errUpdType
		}
		nv := setMinus(cur, v)
		if setLen(nv) == 0 {
			if last.IsIdx {
				parent.L = append(parent.L[:last.Index:last.Index], parent.L[last.Index+1:]...)
				return nil
			}
			delete(parent.M, last.Name)
			return nil
		}
		return putChild(parent, last, nv)
	})
}

// withParent walks to the container of the path's last element (which must
// exist) and calls fn with it; changes are written back along the path.
func withParent(root *AV, path docPath, fn func(parent *AV, last pathElem) error) error {
	if len(path) == 1 {
		return fn(root, path[0])
	}
	first := path[0]
	child, ok := getChild(root, first)
	if !ok || (child.Kind != kM && child.Kind != kL) {
		return errUpdPath
	}
	if err := withParent(&child, path[1:], fn); err != nil {
		return err
	}
	return putChild(root, first, child)
}

func getChild(parent *AV, e pathElem) (AV, bool) {
	if e.IsIdx {
		if parent.Kind != kL || e.Index >= len(parent.L) {
			return AV{}, false
		}
		return parent.L[e.Index], true
	}
	if parent.Kind != kM {
		return AV{}, false
	}
	v, ok := parent.M[e.Name]
	return v, ok
}

func putChild(parent *AV, e pathElem, v AV) error {
	if e.IsIdx {
		if parent.Kind != kL {
			return errUpdPath
		}
		if e.Index < len(parent.L) {
			parent.L[e.Index] = v
		} else {
			parent.L = append(parent.L, v)
		}
		return nil
	}
	if parent.Kind != kM {
		return errUpdPath
	}
	parent.M[e.Name] = v
	return nil
}

func setLen(v AV) int {
	if v.Kind == kBS {
		return len(v.BS)
	}
	return len(v.SS)
}

func setUnion(a, b AV) AV {
	out := a.clone()
	if a.Kind == kBS {
		seen := map[string]bool{}
		for _, x := range a.BS {
			seen[string(x)] = true
		}
		for _, x := range b.BS {
			if !seen[string(x)] {
				out.BS = append(out.BS, x)
				seen[string(x)] = true
			}
		}
		return out
	}
	seen := map[string]bool{}
	for _, x := range a.SS {
		seen[x] = true
	}
	for _, x := range b.SS {
		if !seen[x] {
			out.SS = append(out.SS, x)
			seen[x] = true
		}
	}
	return out
}

func setMinus(a, b AV) AV {
	out := AV{Kind: a.Kind}
	if a.Kind == kBS {
		drop := map[string]bool{}
		for _, x := range b.BS {
			drop[string(x)] = true
		}
		for _, x := range a.BS {
			if !drop[string(x)] {
				out.BS = append(out.BS, x)
			}
		}
		return out
	}
	drop := map[string]bool{}
	for _, x := range b.SS {
		drop[x] = true
	}
	for _, x := range a.SS {
		if !drop[x] {
			out.SS = append(out.SS, x)
		}
	}
	return out
}

// updatedPaths lists the paths an update writes (for UPDATED_OLD/UPDATED_NEW).
func (u *updateExpr) updatedPaths() *projection {
	pr := &projection{}
	for _, a := range u.actions {
		pr.paths = append(pr.paths, a.path)
	}
	return pr
}
