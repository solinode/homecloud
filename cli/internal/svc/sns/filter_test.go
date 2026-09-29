package sns

import "testing"

func TestFilterPolicyAttributes(t *testing.T) {
	attrs := map[string]Attribute{
		"store":    {DataType: "String", StringValue: "example_corp"},
		"event":    {DataType: "String", StringValue: "order_placed"},
		"price":    {DataType: "Number", StringValue: "210.75"},
		"tags":     {DataType: "String.Array", StringValue: `["a", "b", 7]`},
		"ip":       {DataType: "String", StringValue: "10.0.0.17"},
		"customer": {DataType: "String", StringValue: "Alice"},
		"blob":     {DataType: "Binary", BinaryValue: []byte{1}},
	}
	cases := []struct {
		policy string
		want   bool
	}{
		{`{"store": ["example_corp"]}`, true},
		{`{"store": ["other", "example_corp"]}`, true},
		{`{"store": ["other"]}`, false},
		{`{"store": ["example_corp"], "event": ["order_cancelled"]}`, false},
		{`{"missing": ["x"]}`, false},
		{`{"price": [210.75]}`, true},
		{`{"price": ["210.75"]}`, false}, // strings do not match Number attributes
		{`{"price": [{"numeric": [">", 200, "<=", 300]}]}`, true},
		{`{"price": [{"numeric": ["<", 100]}]}`, false},
		{`{"price": [{"numeric": ["=", 210.75]}]}`, true},
		{`{"event": [{"prefix": "order_"}]}`, true},
		{`{"event": [{"suffix": "_placed"}]}`, true},
		{`{"event": [{"suffix": "_x"}]}`, false},
		{`{"customer": [{"equals-ignore-case": "ALICE"}]}`, true},
		{`{"store": [{"anything-but": ["example_corp", "x"]}]}`, false},
		{`{"store": [{"anything-but": "other"}]}`, true},
		{`{"event": [{"anything-but": {"prefix": "order"}}]}`, false},
		{`{"event": [{"anything-but": {"suffix": "cancelled"}}]}`, true},
		{`{"missing": [{"anything-but": "x"}]}`, false},
		{`{"customer": [{"exists": true}]}`, true},
		{`{"missing": [{"exists": false}]}`, true},
		{`{"customer": [{"exists": false}]}`, false},
		{`{"tags": ["b"]}`, true},
		{`{"tags": [7]}`, true},
		{`{"tags": ["z"]}`, false},
		{`{"ip": [{"cidr": "10.0.0.0/24"}]}`, true},
		{`{"ip": [{"cidr": "10.1.0.0/24"}]}`, false},
		{`{"event": [{"wildcard": "order_*"}]}`, true},
		{`{"blob": [{"exists": true}]}`, false}, // Binary attributes are not matched
		{`{"store": ["nope"], "$or": [{"event": ["order_placed"]}, {"price": [1]}]}`, false},
		{`{"store": ["example_corp"], "$or": [{"event": ["x"]}, {"price": [{"numeric": [">", 1]}]}]}`, true},
		{`{"$or": [{"event": ["x"]}, {"price": [1]}]}`, false},
	}
	for _, c := range cases {
		p, err := parsePolicy([]byte(c.policy), scopeAttributes)
		if err != nil {
			t.Fatalf("%s: %v", c.policy, err)
		}
		if got := matchPolicy(p, scopeAttributes, attrs, ""); got != c.want {
			t.Errorf("%s: got %v, want %v", c.policy, got, c.want)
		}
	}
}

func TestFilterPolicyBody(t *testing.T) {
	body := `{"order": {"id": 5, "status": "paid", "items": [{"sku": "A"}, {"sku": "B"}], "tags": ["x", "y"]}, "flag": true, "gone": null}`
	cases := []struct {
		policy string
		want   bool
	}{
		{`{"order": {"status": ["paid"]}}`, true},
		{`{"order": {"status": ["unpaid"]}}`, false},
		{`{"order": {"id": [{"numeric": [">=", 5]}]}}`, true},
		{`{"order": {"items": {"sku": ["B"]}}}`, true},
		{`{"order": {"items": {"sku": ["C"]}}}`, false},
		{`{"order": {"tags": ["y"]}}`, true},
		{`{"flag": [true]}`, true},
		{`{"gone": [null]}`, true},
		{`{"order": {"missing": [{"exists": false}]}}`, true},
		{`{"order": {"status": ["paid"]}, "$or": [{"flag": [false]}, {"order": {"id": [5]}}]}`, true},
	}
	for _, c := range cases {
		p, err := parsePolicy([]byte(c.policy), scopeBody)
		if err != nil {
			t.Fatalf("%s: %v", c.policy, err)
		}
		if got := matchPolicy(p, scopeBody, nil, body); got != c.want {
			t.Errorf("%s: got %v, want %v", c.policy, got, c.want)
		}
	}
	p, _ := parsePolicy([]byte(`{"a": ["b"]}`), scopeBody)
	if matchPolicy(p, scopeBody, nil, "not json") {
		t.Error("non-JSON body matched a body policy")
	}
}

func TestFilterPolicyValidation(t *testing.T) {
	bad := []string{
		`[]`, `{"a": "b"}`, `{"a": []}`, `{"a": [{"numeric": [">", "x"]}]}`, `{"a": [{"numeric": ["<", 5, ">", 1]}]}`,
		`{"a": [{"prefix": 1}]}`, `{"a": [{"bogus": 1}]}`, `{"a": {"b": ["c"]}}`, `{"$or": [{"a": ["b"]}]}`,
		`{"a": [{"cidr": "nope"}]}`, `{"a": [{"exists": "yes"}]}`,
	}
	for _, b := range bad {
		if _, err := parsePolicy([]byte(b), scopeAttributes); err == nil {
			t.Errorf("%s: accepted", b)
		}
	}
	if _, err := parsePolicy([]byte(`{"a": {"b": ["c"]}}`), scopeBody); err != nil {
		t.Errorf("nested body policy rejected: %v", err)
	}
}
