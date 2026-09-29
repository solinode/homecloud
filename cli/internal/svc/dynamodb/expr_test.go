package dynamodb

import (
	"bytes"
	"encoding/json"
	"sort"
	"strings"
	"testing"
)

func TestNumberParseAndCanonical(t *testing.T) {
	cases := map[string]string{
		"0": "0", "-0": "0", "0.000": "0", "00012.3400": "12.34", "1E2": "100", "1.5e-3": "0.0015", "-12e1": "-120",
		"+7": "7", ".5": "0.5", "5.": "5", "123456789012345678901234567890123456789e-1": "", // 39 digits
		"12345678901234567890123456789012345678": "12345678901234567890123456789012345678",
		"1E125":                                  "1" + strings.Repeat("0", 125), "1E126": "", "1E-130": "0." + strings.Repeat("0", 129) + "1", "1E-131": "",
		"abc": "", "1e": "", "--1": "", "": "", "1.2.3": "", "0e999999999": "0",
	}
	for in, want := range cases {
		d, err := parseNumber(in)
		if want == "" {
			if err == nil {
				t.Errorf("parseNumber(%q) = %s, want error", in, d)
			}
			continue
		}
		if err != nil || d.String() != want {
			t.Errorf("parseNumber(%q) = %q, %v; want %q", in, d.String(), err, want)
		}
	}
}

func TestNumberOrderAndArithmetic(t *testing.T) {
	nums := []string{"-1e125", "-100", "-12.5", "-12.34", "-12.3", "-1", "-0.123", "-0.12", "-1e-130", "0", "1e-130", "0.12", "0.123", "1", "9.99", "10", "12.3", "12.34", "100", "99999999999999999999999999999999999999", "1e125"}
	var ds []decimal
	var enc [][]byte
	for _, n := range nums {
		d, err := parseNumber(n)
		if err != nil {
			t.Fatal(n, err)
		}
		ds = append(ds, d)
		enc = append(enc, appendComponent(nil, Num(d.String())))
	}
	for i := 1; i < len(ds); i++ {
		if ds[i-1].cmp(ds[i]) >= 0 || ds[i].cmp(ds[i-1]) <= 0 {
			t.Errorf("cmp: %s !< %s", nums[i-1], nums[i])
		}
		if bytes.Compare(enc[i-1], enc[i]) >= 0 {
			t.Errorf("key encoding: %s !< %s", nums[i-1], nums[i])
		}
	}
	add := func(a, b string) string {
		x, _ := parseNumber(a)
		y, _ := parseNumber(b)
		r, err := x.add(y)
		if err != nil {
			return "error"
		}
		return r.String()
	}
	for _, c := range [][3]string{
		{"0.1", "0.2", "0.3"}, {"1", "-1", "0"}, {"-5", "3", "-2"}, {"1e20", "1", "100000000000000000001"},
		{"12345678901234567890123456789012345678", "1", "12345678901234567890123456789012345679"}, {"12345678901234567890123456789012345678", "0.1", "error"}, {"9e125", "1e125", "error"}, {"0", "-2.5", "-2.5"}, {"99", "1", "100"},
		{"0.000001", "1000000", "1000000.000001"},
	} {
		if got := add(c[0], c[1]); got != c[2] {
			t.Errorf("%s + %s = %s, want %s", c[0], c[1], got, c[2])
		}
	}
}

func TestKeyComponentOrdering(t *testing.T) {
	// Strings and binary sort byte-wise, and a key component never sorts
	// between a shorter value and its extensions incorrectly.
	strs := []string{"", "\x00", "\x00\x00", "\x00a", "a", "a\x00", "a\x00b", "ab", "b", "é"}
	var enc [][]byte
	for _, s := range strs {
		enc = append(enc, appendComponent(nil, Str(s)))
	}
	if !sort.SliceIsSorted(enc, func(i, j int) bool { return bytes.Compare(enc[i], enc[j]) < 0 }) {
		t.Fatal("string key encoding does not preserve order")
	}
	// Composite keys: (a, 2) < (a, 10) < (ab, 1).
	k := func(p string, n string) []byte { return appendComponent(appendComponent(nil, Str(p)), Num(n)) }
	if !(bytes.Compare(k("a", "2"), k("a", "10")) < 0 && bytes.Compare(k("a", "10"), k("ab", "1")) < 0) {
		t.Fatal("composite ordering")
	}
	if !bytes.HasPrefix(k("a", "5"), appendComponent(nil, Str("a"))) || bytes.HasPrefix(k("ab", "5"), appendComponent(nil, Str("a"))) {
		t.Fatal("partition prefix")
	}
}

func item(t *testing.T, js string) Item {
	t.Helper()
	var it Item
	if err := json.Unmarshal([]byte(js), &it); err != nil {
		t.Fatal(err)
	}
	if err := validateItem(it); err != nil {
		t.Fatal(err)
	}
	return it
}

func ctxFor(t *testing.T, names map[string]string, values string) *exprCtx {
	t.Helper()
	var vals map[string]AV
	if values != "" {
		if err := json.Unmarshal([]byte(values), &vals); err != nil {
			t.Fatal(err)
		}
	}
	c, err := newExprCtx(names, vals)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestConditionEvaluation(t *testing.T) {
	it := item(t, `{"id":{"S":"x"},"n":{"N":"5"},"s":{"S":"hello"},"b":{"B":"AAEC"},"ss":{"SS":["a","b"]},"ns":{"NS":["1","2.5"]},
		"l":{"L":[{"S":"a"},{"N":"1"},{"M":{"deep":{"S":"yes"}}}]},"m":{"M":{"a b":{"N":"1"},"x":{"L":[{"N":"7"}]}}},"t":{"BOOL":true},"z":{"NULL":true},"e":{"S":""}}`)
	vals := `{":5":{"N":"5"},":4":{"N":"4.0"},":10":{"N":"1e1"},":he":{"S":"he"},":a":{"S":"a"},":c":{"S":"c"},":n25":{"N":"2.50"},
		":S":{"S":"S"},":SS":{"S":"SS"},":NULL":{"S":"NULL"},":yes":{"S":"yes"},":7":{"N":"7"},":true":{"BOOL":true},":b0":{"B":"AA=="},":empty":{"S":""},":one":{"N":"1"},":3":{"N":"3"}}`
	names := map[string]string{"#ab": "a b", "#n": "n"}
	cases := []struct {
		expr string
		want bool
	}{
		{"n = :5", true},
		{"n > :4 AND n < :10", true},
		{"n BETWEEN :4 AND :5", true},
		{"#n BETWEEN :4 AND :10", true},
		{"n <> :5", false},
		{"missing <> :5", true},
		{"missing = :5", false},
		{"missing < :5", false},
		{"s < :5", false}, // type mismatch
		{"begins_with(s, :he)", true},
		{"begins_with(b, :b0)", true},
		{"contains(s, :he)", true},
		{"contains(ss, :a)", true},
		{"contains(ss, :c)", false},
		{"contains(ns, :n25)", true},
		{"contains(l, :a)", true},
		{"attribute_exists(m.#ab)", true},
		{"attribute_exists(m.x[0])", true},
		{"attribute_exists(m.x[1])", false},
		{"m.x[0] = :7", true},
		{"l[2].deep = :yes", true},
		{"attribute_not_exists(nope) AND attribute_exists(id)", true},
		{"attribute_type(s, :S) AND attribute_type(ss, :SS) AND attribute_type(z, :NULL)", true},
		{"attribute_type(s, :SS)", false},
		{"size(ss) = :one OR size(s) = :5", true},
		{"size(l) = :3 AND size(m) < :3", true},
		{"size(n) = :one", false}, // size of a number is undefined
		{"n IN (:4, :5)", true},
		{"n IN (:4, :10)", false},
		{"NOT n = :5", false},
		{"NOT (n = :4 OR s = :a) AND t = :true", true},
		{"n = :4 OR n = :5 AND s = :a", false}, // AND binds tighter than OR... (n=4) OR (n=5 AND s=a)
		{"(n = :4 OR n = :5) AND NOT s = :a", true},
		{"e = :empty", true},
		{"z = :true", false},
	}
	for _, c := range cases {
		ctx := ctxFor(t, names, vals)
		e, err := parseCondition(c.expr, "ConditionExpression", ctx)
		if err != nil {
			t.Errorf("%s: %v", c.expr, err)
			continue
		}
		if got := evalCond(e, it); got != c.want {
			t.Errorf("%s = %v, want %v", c.expr, got, c.want)
		}
	}
}

func TestExpressionErrors(t *testing.T) {
	vals := `{":v":{"N":"1"},":s":{"S":"x"},":hi":{"N":"9"}}`
	cases := []struct{ expr, want string }{
		{"a = :nope", "An expression attribute value used in expression is not defined; attribute value: :nope"},
		{"#x = :v", "An expression attribute name used in the document path is not defined; attribute name: #x"},
		{"a = ", "Syntax error"},
		{"a = :v AND", "Syntax error"},
		{"a == :v", "Syntax error"},
		{"foo(a)", "Invalid function name; function: foo"},
		{"begins_with(a)", "Incorrect number of operands"},
		{"begins_with(a, :v)", "Incorrect operand type for operator or function; operator or function: begins_with, operand type: N"},
		{"attribute_exists(:v)", "Operator or function requires a document path"},
		{"size(a)", "The function is not allowed to be used this way in an expression; function: size"},
		{"a BETWEEN :hi AND :v", "The BETWEEN operator requires upper bound to be greater than or equal to lower bound"},
		{"a = :v $", "Invalid character encountered"},
		{"", "The expression can not be empty"},
	}
	for _, c := range cases {
		ctx := ctxFor(t, nil, vals)
		_, err := parseCondition(c.expr, "ConditionExpression", ctx)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%q: got %v, want %q", c.expr, err, c.want)
		}
	}
	// Unused placeholders.
	ctx := ctxFor(t, map[string]string{"#a": "a", "#b": "b"}, `{":v":{"N":"1"},":w":{"N":"2"}}`)
	if _, err := parseCondition("#a = :v", "ConditionExpression", ctx); err != nil {
		t.Fatal(err)
	}
	if err := ctx.finish(); err == nil || !strings.Contains(err.Error(), "unused in expressions: keys: {#b}") {
		t.Fatalf("unused names: %v", err)
	}
	ctx = ctxFor(t, nil, `{":v":{"N":"1"},":w":{"N":"2"}}`)
	_, _ = parseCondition("a = :v", "ConditionExpression", ctx)
	if err := ctx.finish(); err == nil || !strings.Contains(err.Error(), "ExpressionAttributeValues unused in expressions: keys: {:w}") {
		t.Fatalf("unused values: %v", err)
	}
	if _, err := newExprCtx(nil, map[string]AV{":x": {Kind: kSS, SS: []string{}}}); err == nil {
		t.Fatal("empty set accepted in ExpressionAttributeValues")
	}
	ctx = ctxFor(t, nil, `{":v":{"N":"1"}}`)
	if err := ctx.finish(); err == nil || !strings.Contains(err.Error(), "can only be specified when using expressions") {
		t.Fatalf("values without expressions: %v", err)
	}
}

func TestKeyConditions(t *testing.T) {
	ks := schema{PK: KeyDef{"pk", "S"}, SK: &KeyDef{"sk", "N"}}
	vals := `{":p":{"S":"a"},":1":{"N":"1"},":2":{"N":"2"},":s":{"S":"x"}}`
	ok := []string{"pk = :p", "pk = :p AND sk > :1", ":p = pk AND :1 < sk", "sk BETWEEN :1 AND :2 AND pk = :p", "(pk = :p) AND (sk <= :2)"}
	for _, e := range ok {
		if _, err := parseKeyCondition(e, ctxFor(t, nil, vals), ks); err != nil {
			t.Errorf("%s: %v", e, err)
		}
	}
	bad := map[string]string{
		"sk = :1":                          "Query condition missed key schema element: pk",
		"pk = :p OR sk = :1":               "Invalid operator used in KeyConditionExpression: OR",
		"pk > :p":                          "Query key condition not supported",
		"pk = :p AND other = :1":           "Query condition missed key schema element",
		"pk = :p AND sk = :s":              "Condition parameter type does not match schema type",
		"pk = :1":                          "Condition parameter type does not match schema type",
		"pk = :p AND begins_with(sk, :1)":  "begins_with",
		"pk = :p AND sk = :1 AND sk = :2":  "Conditions can be of length 1 or 2 only",
		"pk = :p AND attribute_exists(sk)": "Invalid operator used in KeyConditionExpression: attribute_exists",
	}
	for e, want := range bad {
		if _, err := parseKeyCondition(e, ctxFor(t, nil, vals), ks); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: got %v, want %q", e, err, want)
		}
	}
	kc, _ := parseKeyCondition("pk = :p AND sk BETWEEN :1 AND :2", ctxFor(t, nil, vals), ks)
	for n, want := range map[string]int{"0.5": -1, "1": 0, "1.5": 0, "2": 0, "2.01": 1} {
		if got := kc.skMatch(Num(n)); got != want {
			t.Errorf("between skMatch(%s) = %d", n, got)
		}
	}
}

func TestUpdateExpressions(t *testing.T) {
	ks := schema{PK: KeyDef{"id", "S"}}
	base := item(t, `{"id":{"S":"k"},"n":{"N":"10"},"l":{"L":[{"N":"0"},{"N":"1"},{"N":"2"},{"N":"3"}]},"m":{"M":{"a":{"M":{"b":{"L":[{"S":"x"},{"S":"y"}]}}}}},
		"ss":{"SS":["a","b"]},"ns":{"NS":["1","2"]},"gone":{"S":"bye"}}`)
	vals := `{":one":{"N":"1"},":l":{"L":[{"S":"z"}]},":s":{"SS":["b","c"]},":ns":{"NS":["2","3"]},":v":{"S":"v"},":dec":{"N":"0.5"},":x":{"SS":["a","b"]}}`
	cases := []struct {
		expr string
		want string // JSON of selected attributes after the update
		attr []string
	}{
		{"SET n = n + :one", `{"n":{"N":"11"}}`, []string{"n"}},
		{"SET n = n - :dec, c = if_not_exists(c, :one)", `{"n":{"N":"9.5"},"c":{"N":"1"}}`, []string{"n", "c"}},
		{"SET n = if_not_exists(n, :one)", `{"n":{"N":"10"}}`, []string{"n"}},
		{"SET l = list_append(l, :l)", `{"l":{"L":[{"N":"0"},{"N":"1"},{"N":"2"},{"N":"3"},{"S":"z"}]}}`, []string{"l"}},
		{"SET l = list_append(:l, l)", `{"l":{"L":[{"S":"z"},{"N":"0"},{"N":"1"},{"N":"2"},{"N":"3"}]}}`, []string{"l"}},
		{"SET m.a.b[1] = :v, m.a.c = :one", `{"m":{"M":{"a":{"M":{"b":{"L":[{"S":"x"},{"S":"v"}]},"c":{"N":"1"}}}}}}`, []string{"m"}},
		{"SET m.a.b[9] = :v", `{"m":{"M":{"a":{"M":{"b":{"L":[{"S":"x"},{"S":"y"},{"S":"v"}]}}}}}}`, []string{"m"}},
		{"REMOVE l[1], l[3], gone", `{"l":{"L":[{"N":"0"},{"N":"2"}]}}`, []string{"l", "gone"}},
		{"REMOVE m.a.b[0]", `{"m":{"M":{"a":{"M":{"b":{"L":[{"S":"y"}]}}}}}}`, []string{"m"}},
		{"ADD n :one, ss :s, ns :ns, fresh :one", `{"n":{"N":"11"},"ss":{"SS":["a","b","c"]},"ns":{"NS":["1","2","3"]},"fresh":{"N":"1"}}`, []string{"n", "ss", "ns", "fresh"}},
		{"DELETE ss :s", `{"ss":{"SS":["a"]}}`, []string{"ss"}},
		{"DELETE ss :x", `{}`, []string{"ss"}},
		{"SET a = :v REMOVE gone ADD n :one DELETE ns :ns", `{"a":{"S":"v"},"n":{"N":"11"},"ns":{"NS":["1"]}}`, []string{"a", "gone", "n", "ns"}},
		{"set x = :v remove gone", `{"x":{"S":"v"}}`, []string{"x", "gone"}},
	}
	for _, c := range cases {
		u, err := parseUpdate(c.expr, ctxFor(t, nil, vals))
		if err != nil {
			t.Errorf("%s: %v", c.expr, err)
			continue
		}
		got, err := u.apply(base, ks)
		if err != nil {
			t.Errorf("%s: %v", c.expr, err)
			continue
		}
		sel := Item{}
		for _, a := range c.attr {
			if v, ok := got[a]; ok {
				sel[a] = v
			}
		}
		if string(encodeItem(sel)) != string(encodeItem(item(t, c.want))) {
			t.Errorf("%s:\n got %s\nwant %s", c.expr, encodeItem(sel), c.want)
		}
	}
	// The base item is never modified.
	if base["n"].S != "10" || len(base["l"].L) != 4 {
		t.Fatal("apply modified its input")
	}
	errs := map[string]string{
		"SET id = :v":                  "Cannot update attribute id. This attribute is part of the key",
		"SET a = missing + :one":       "refers to an attribute that does not exist",
		"SET a = gone + :one":          "incorrect data type",
		"SET nope.x = :v":              "document path provided in the update expression is invalid",
		"SET a = :v, a = :one":         "Two document paths overlap",
		"SET m.a = :v REMOVE m.a.b":    "Two document paths overlap",
		"SET l[0] = :v REMOVE l.x":     "Two document paths conflict",
		"ADD gone :one":                "incorrect data type",
		"ADD n :v":                     "Incorrect operand type for operator or function; operator: ADD",
		"SET a = :v SET b = :v":        "can only be used once",
		"SET l = list_append(l, :one)": "Incorrect operand type for operator or function; operator or function: list_append",
		"DELETE ss :ns":                "incorrect data type",
		"SET a = size(l)":              "not allowed in an update expression",
	}
	for e, want := range errs {
		u, err := parseUpdate(e, ctxFor(t, nil, vals))
		if err == nil {
			_, err = u.apply(base, ks)
		}
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: got %v, want %q", e, err, want)
		}
	}
}

func TestProjection(t *testing.T) {
	it := item(t, `{"a":{"S":"1"},"l":{"L":[{"S":"x"},{"S":"y"},{"M":{"k":{"N":"1"},"j":{"N":"2"}}}]},"m":{"M":{"p":{"S":"q"},"r":{"S":"s"}}}}`)
	pr, err := parseProjection("a, l[2].k, l[0], m.p, missing, m.nope", ctxFor(t, nil, ""))
	if err != nil {
		t.Fatal(err)
	}
	got := string(encodeItem(pr.apply(it)))
	want := `{"a":{"S":"1"},"l":{"L":[{"S":"x"},{"M":{"k":{"N":"1"}}}]},"m":{"M":{"p":{"S":"q"}}}}`
	if got != want {
		t.Fatalf("projection:\n got %s\nwant %s", got, want)
	}
	if _, err := parseProjection("a, a.b", ctxFor(t, nil, "")); err == nil || !strings.Contains(err.Error(), "overlap") {
		t.Fatalf("overlap: %v", err)
	}
}

func TestAttributeValidation(t *testing.T) {
	bad := map[string]string{
		`{"a":{}}`:                                "Supplied AttributeValue is empty",
		`{"a":{"S":"x","N":"1"}}`:                 "more than one datatypes",
		`{"a":{"SS":[]}}`:                         "may not be empty",
		`{"a":{"SS":["x","x"]}}`:                  "contains duplicates",
		`{"a":{"NS":["1","1.0"]}}`:                "contains duplicates",
		`{"a":{"N":"x"}}`:                         "cannot be converted into a number",
		`{"a":{"NULL":false}}`:                    "Null attribute value types must have the value of true",
		`{"a":{"M":{"b":{"L":[{"N":"1e200"}]}}}}`: "Number overflow",
	}
	for js, want := range bad {
		var it Item
		if err := json.Unmarshal([]byte(js), &it); err != nil {
			t.Fatal(js, err)
		}
		if err := validateItem(it); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: got %v, want %q", js, err, want)
		}
	}
	// Round trip keeps every type.
	js := `{"b":{"B":"AAEC"},"bool":{"BOOL":false},"bs":{"BS":["AA==","AQ=="]},"l":{"L":[{"NULL":true}]},"m":{"M":{"x":{"N":"-1.5"}}},"n":{"N":"3"},"ns":{"NS":["1","2"]},"s":{"S":"<é>"},"ss":{"SS":["a"]}}`
	it := item(t, js)
	var back Item
	if err := json.Unmarshal(encodeItem(it), &back); err != nil || !equalItems(it, back) {
		t.Fatalf("round trip: %v %s", err, encodeItem(it))
	}
	// Sets compare without order; numbers numerically after canonicalization.
	if !equalAV(item(t, `{"a":{"NS":["1","2"]}}`)["a"], item(t, `{"a":{"NS":["2.0","1"]}}`)["a"]) {
		t.Fatal("set equality")
	}
	ks := schema{PK: KeyDef{"pk", "S"}, SK: &KeyDef{"sk", "B"}}
	for js, want := range map[string]string{
		`{"pk":{"S":""},"sk":{"B":"AA=="}}`:  "cannot contain an empty string value",
		`{"pk":{"S":"a"},"sk":{"B":""}}`:     "empty binary",
		`{"pk":{"N":"1"},"sk":{"B":"AA=="}}`: "Type mismatch for key pk",
		`{"pk":{"S":"a"}}`:                   "Missing the key sk",
	} {
		if _, err := ks.keyOf(item(t, js)); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: got %v, want %q", js, err, want)
		}
	}
}
