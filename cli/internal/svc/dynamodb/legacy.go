package dynamodb

import (
	"sort"
	"strings"
)

// Legacy (pre-expression) parameters: Expected, KeyConditions, QueryFilter,
// ScanFilter, AttributesToGet and AttributeUpdates. They are translated to the
// same expression trees the expression parameters produce.

type legacyCond struct {
	ComparisonOperator string
	AttributeValueList []AV
	// Expected only:
	Value  *AV
	Exists *bool
}

func pathExpr(name string) *expr { return &expr{kind: ePath, path: docPath{{Name: name}}} }
func valExpr(v AV) *expr         { return &expr{kind: eValue, val: v} }

func legacyOne(attr string, c legacyCond) (*expr, error) {
	for i := range c.AttributeValueList {
		if err := c.AttributeValueList[i].validate(); err != nil {
			return nil, err
		}
	}
	args := c.AttributeValueList
	need := func(n int) error {
		if len(args) != n {
			return invalidParam("Invalid number of argument(s) for the %s ComparisonOperator", c.ComparisonOperator)
		}
		return nil
	}
	p := pathExpr(attr)
	switch op := c.ComparisonOperator; op {
	case "EQ", "NE", "LT", "LE", "GT", "GE":
		if err := need(1); err != nil {
			return nil, err
		}
		sym := map[string]string{"EQ": "=", "NE": "<>", "LT": "<", "LE": "<=", "GT": ">", "GE": ">="}[op]
		if op != "EQ" && op != "NE" && !isScalarKey(args[0].Kind) {
			return nil, invalidParam("ComparisonOperator %s is not valid for %s AttributeValue type", op, args[0].Kind)
		}
		return &expr{kind: eCmp, op: sym, args: []*expr{p, valExpr(args[0])}}, nil
	case "NOT_NULL", "NULL":
		if err := need(0); err != nil {
			return nil, err
		}
		fn := "attribute_exists"
		if op == "NULL" {
			fn = "attribute_not_exists"
		}
		return &expr{kind: eFunc, op: fn, args: []*expr{p}}, nil
	case "CONTAINS", "NOT_CONTAINS":
		if err := need(1); err != nil {
			return nil, err
		}
		e := &expr{kind: eFunc, op: "contains", args: []*expr{p, valExpr(args[0])}}
		if op == "NOT_CONTAINS" {
			// NOT_CONTAINS is false for a missing attribute.
			return &expr{kind: eAnd, args: []*expr{{kind: eFunc, op: "attribute_exists", args: []*expr{p}}, {kind: eNot, args: []*expr{e}}}}, nil
		}
		return e, nil
	case "BEGINS_WITH":
		if err := need(1); err != nil {
			return nil, err
		}
		return &expr{kind: eFunc, op: "begins_with", args: []*expr{p, valExpr(args[0])}}, nil
	case "IN":
		if len(args) == 0 {
			return nil, invalidParam("Invalid number of argument(s) for the IN ComparisonOperator")
		}
		in := &expr{kind: eIn, args: []*expr{p}}
		for _, a := range args {
			in.args = append(in.args, valExpr(a))
		}
		return in, nil
	case "BETWEEN":
		if err := need(2); err != nil {
			return nil, err
		}
		return &expr{kind: eBetween, args: []*expr{p, valExpr(args[0]), valExpr(args[1])}}, nil
	}
	return nil, validation("1 validation error detected: Value '%s' at 'comparisonOperator' failed to satisfy constraint: Member must satisfy enum value set: [IN, NULL, BETWEEN, LT, NOT_CONTAINS, EQ, GT, NOT_NULL, NE, LE, BEGINS_WITH, GE, CONTAINS]", c.ComparisonOperator)
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// legacyConditions combines QueryFilter/ScanFilter/KeyConditions entries.
func legacyConditions(m map[string]legacyCond, op string) (*expr, error) {
	var out *expr
	for _, attr := range sortedKeys(m) {
		e, err := legacyOne(attr, m[attr])
		if err != nil {
			return nil, err
		}
		out = combine(out, e, op)
	}
	return out, nil
}

func combine(a, b *expr, op string) *expr {
	if a == nil {
		return b
	}
	k := eAnd
	if strings.EqualFold(op, "OR") {
		k = eOr
	}
	return &expr{kind: k, args: []*expr{a, b}}
}

// legacyExpected converts the Expected parameter.
func legacyExpected(m map[string]legacyCond, op string) (*expr, error) {
	var out *expr
	for _, attr := range sortedKeys(m) {
		c := m[attr]
		var e *expr
		var err error
		switch {
		case c.ComparisonOperator != "":
			if c.Value != nil {
				return nil, invalidParam("Value and ComparisonOperator cannot be used together for Attribute: %s", attr)
			}
			e, err = legacyOne(attr, c)
		case c.Exists != nil && !*c.Exists:
			if c.Value != nil {
				return nil, invalidParam("Value cannot be used when Exists is false for Attribute: %s", attr)
			}
			e = &expr{kind: eFunc, op: "attribute_not_exists", args: []*expr{pathExpr(attr)}}
		default:
			if c.Value == nil {
				return nil, invalidParam("Value must be provided when Exists is true for Attribute: %s", attr)
			}
			if err := c.Value.validate(); err != nil {
				return nil, err
			}
			e = &expr{kind: eCmp, op: "=", args: []*expr{pathExpr(attr), valExpr(*c.Value)}}
		}
		if err != nil {
			return nil, err
		}
		out = combine(out, e, op)
	}
	return out, nil
}

// legacyKeyConditions converts KeyConditions to a keyCond.
func legacyKeyConditions(m map[string]legacyCond, ks schema) (keyCond, error) {
	var kc keyCond
	pk, ok := m[ks.PK.Name]
	if !ok {
		return kc, validation("Query condition missed key schema element: %s", ks.PK.Name)
	}
	if pk.ComparisonOperator != "EQ" || len(pk.AttributeValueList) != 1 {
		return kc, validation("Query key condition not supported")
	}
	if err := pk.AttributeValueList[0].validate(); err != nil {
		return kc, err
	}
	kc.PK = pk.AttributeValueList[0]
	if err := checkKeyAttr(ks.PK, kc.PK, ""); err != nil {
		return kc, err
	}
	for attr, c := range m {
		if attr == ks.PK.Name {
			continue
		}
		if ks.SK == nil || attr != ks.SK.Name {
			return kc, validation("Query condition missed key schema element: %s", ks.PK.Name)
		}
		ops := map[string]string{"EQ": "=", "LT": "<", "LE": "<=", "GT": ">", "GE": ">=", "BETWEEN": "BETWEEN", "BEGINS_WITH": "begins_with"}
		op, ok := ops[c.ComparisonOperator]
		if !ok {
			return kc, validation("Query key condition not supported")
		}
		n := 1
		if op == "BETWEEN" {
			n = 2
		}
		if len(c.AttributeValueList) != n {
			return kc, invalidParam("Invalid number of argument(s) for the %s ComparisonOperator", c.ComparisonOperator)
		}
		for i := range c.AttributeValueList {
			if err := c.AttributeValueList[i].validate(); err != nil {
				return kc, err
			}
			if c.AttributeValueList[i].Kind.String() != ks.SK.Type {
				return kc, invalidParam("Condition parameter type does not match schema type")
			}
		}
		kc.SKOp, kc.SK1 = op, c.AttributeValueList[0]
		if n == 2 {
			kc.SK2 = c.AttributeValueList[1]
		}
	}
	return kc, nil
}

type attributeUpdate struct {
	Action string
	Value  *AV
}

// legacyUpdates converts AttributeUpdates to an update expression.
func legacyUpdates(m map[string]attributeUpdate) (*updateExpr, error) {
	u := &updateExpr{}
	for _, attr := range sortedKeys(m) {
		a := m[attr]
		if a.Value != nil {
			if err := a.Value.validate(); err != nil {
				return nil, err
			}
		}
		p := docPath{{Name: attr}}
		action := a.Action
		if action == "" {
			action = "PUT"
		}
		switch action {
		case "PUT":
			if a.Value == nil {
				return nil, invalidParam("Only DELETE action is allowed when no attribute value is specified")
			}
			u.actions = append(u.actions, updateAction{kind: "SET", path: p, val: valExpr(*a.Value)})
		case "DELETE":
			if a.Value == nil {
				u.actions = append(u.actions, updateAction{kind: "REMOVE", path: p})
			} else {
				if !isSet(a.Value.Kind) {
					return nil, invalidParam("DELETE action with value is not supported for the type %s", a.Value.Kind)
				}
				u.actions = append(u.actions, updateAction{kind: "DELETE", path: p, val: valExpr(*a.Value)})
			}
		case "ADD":
			if a.Value == nil {
				return nil, invalidParam("Only DELETE action is allowed when no attribute value is specified")
			}
			if a.Value.Kind == kL {
				// Legacy ADD appends to lists.
				val := &expr{kind: eListAppend, args: []*expr{{kind: eIfNotExists, path: p, args: []*expr{{kind: ePath, path: p}, valExpr(List(nil))}}, valExpr(*a.Value)}}
				u.actions = append(u.actions, updateAction{kind: "SET", path: p, val: val})
			} else {
				if a.Value.Kind != kN && !isSet(a.Value.Kind) {
					return nil, invalidParam("ADD action is not supported for the type %s", a.Value.Kind)
				}
				u.actions = append(u.actions, updateAction{kind: "ADD", path: p, val: valExpr(*a.Value)})
			}
		default:
			return nil, validation("1 validation error detected: Value '%s' at 'attributeUpdates.%s.member.action' failed to satisfy constraint: Member must satisfy enum value set: [ADD, PUT, DELETE]", action, attr)
		}
	}
	return u, nil
}

func mixErr(nonExpr, exprParams []string) error {
	return validation("Can not use both expression and non-expression parameters in the same request: Non-expression parameters: {%s} Expression parameters: {%s}", strings.Join(nonExpr, ", "), strings.Join(exprParams, ", "))
}
