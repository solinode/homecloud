package cfn

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func mustParse(t *testing.T, src string) *Template {
	t.Helper()
	tp, err := Parse(src)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return tp
}

func newResolver(t *testing.T, tp *Template, in map[string]any) *resolver {
	t.Helper()
	ps, err := params(tp, in)
	if err != nil {
		t.Fatal(err)
	}
	st := &Stack{Name: "st", ARN: "arn:aws:cloudformation:us-east-1:123456789012:stack/st/5b3a3f60-0000-4000-8000-000000000000", Parameters: ps,
		Resources: map[string]*Resource{"Q": {PhysicalID: "http://h/1/q", NativeID: "q", Type: "AWS::SQS::Queue", Attributes: map[string]any{"arn": "arn:aws:sqs:us-east-1:123456789012:q"}}}}
	return &resolver{stack: st, params: ps, tmpl: tp}
}

func TestIntrinsics(t *testing.T) {
	tp := mustParse(t, `
Parameters:
  Env: {Type: String, Default: prod}
  Ports: {Type: CommaDelimitedList, Default: "80, 443"}
Mappings:
  M: {prod: {Size: "big"}}
Conditions:
  Prod: !Equals [!Ref Env, prod]
  NotProd: !Not [!Condition Prod]
  Both: !And [!Condition Prod, !Equals [!Ref "AWS::Region", us-east-1]]
Resources:
  Q: {Type: "AWS::SQS::Queue"}
`)
	r := newResolver(t, tp, nil)
	cases := []struct {
		name string
		in   string
		want any
	}{
		{"pseudo", `{"Fn::Join": ["|", [{"Ref": "AWS::Region"}, {"Ref": "AWS::AccountId"}, {"Ref": "AWS::StackName"}, {"Ref": "AWS::Partition"}, {"Ref": "AWS::URLSuffix"}]]}`, "us-east-1|123456789012|st|aws|amazonaws.com"},
		{"stackid", `{"Ref": "AWS::StackId"}`, "arn:aws:cloudformation:us-east-1:123456789012:stack/st/5b3a3f60-0000-4000-8000-000000000000"},
		{"if true", `{"Fn::If": ["Prod", "a", "b"]}`, "a"},
		{"if false", `{"Fn::If": ["NotProd", "a", "b"]}`, "b"},
		{"and", `{"Fn::If": ["Both", 1, 2]}`, float64(1)},
		{"findinmap", `{"Fn::FindInMap": ["M", {"Ref": "Env"}, "Size"]}`, "big"},
		{"select list param", `{"Fn::Select": [1, {"Ref": "Ports"}]}`, "443"},
		{"sub vars", `{"Fn::Sub": ["${A}-${Env}-${!Lit}-${Q.Arn}", {"A": "x"}]}`, "x-prod-${Lit}-arn:aws:sqs:us-east-1:123456789012:q"},
		{"split", `{"Fn::Split": [",", "a,b"]}`, []any{"a", "b"}},
		{"base64", `{"Fn::Base64": "hi"}`, "aGk="},
		{"length", `{"Fn::Length": [1, 2, 3]}`, float64(3)},
		{"getatt string form", `{"Fn::GetAtt": "Q.Arn"}`, "arn:aws:sqs:us-east-1:123456789012:q"},
		{"novalue in map", `{"a": 1, "b": {"Ref": "AWS::NoValue"}, "c": [{"Ref": "AWS::NoValue"}, 2]}`, map[string]any{"a": float64(1), "c": []any{float64(2)}}},
		{"numbers are not exponents", `{"Fn::Join": ["", [1000000, "x"]]}`, "1000000x"},
	}
	for _, c := range cases {
		var in any
		if err := json.Unmarshal([]byte(c.in), &in); err != nil {
			t.Fatal(err)
		}
		got, err := r.resolve(in)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: got %#v, want %#v", c.name, got, c.want)
		}
	}
	for _, bad := range []string{`{"Ref": "Nope"}`, `{"Fn::GetAtt": ["Q", "Bogus"]}`, `{"Fn::If": ["Missing", 1, 2]}`, `{"Fn::ImportValue": "x"}`, `{"Fn::Transform": {}}`,
		`{"Fn::FindInMap": ["M", "dev", "Size"]}`, `{"Fn::Select": [5, ["a"]]}`} {
		var in any
		_ = json.Unmarshal([]byte(bad), &in)
		if _, err := r.resolve(in); err == nil {
			t.Errorf("%s resolved", bad)
		}
	}
}

func TestShortFormTags(t *testing.T) {
	tp := mustParse(t, `
Parameters: {P: {Type: String, Default: x}}
Conditions: {C: !Equals [!Ref P, x]}
Resources:
  A:
    Type: AWS::SQS::Queue
    Properties:
      QueueName: !If [C, !Sub "${P}-1", !Join ["-", [a, b]]]
      Tags:
        - Key: k
          Value: !GetAtt B.Arn
  B: {Type: AWS::SNS::Topic}
Outputs: {O: {Value: !Ref A}}
`)
	got := tp.Resources["A"].Properties
	b, _ := json.Marshal(got)
	if !strings.Contains(string(b), `{"Fn::If":["C",{"Fn::Sub":"${P}-1"},{"Fn::Join":["-",["a","b"]]}]}`) || !strings.Contains(string(b), `{"Fn::GetAtt":["B","Arn"]}`) {
		t.Fatalf("short forms: %s", b)
	}
	if tp.Conditions["C"] == nil {
		t.Fatal("conditions dropped")
	}
	ord, err := order(tp)
	if err != nil || !reflect.DeepEqual(ord, []string{"B", "A"}) {
		t.Fatalf("order %v %v", ord, err)
	}
}

func TestYAMLDatesStayStrings(t *testing.T) {
	tp := mustParse(t, "Resources:\n  X:\n    Type: AWS::IAM::Policy\n    Properties:\n      Doc: {Version: 2012-10-17}\n")
	if v := tp.Resources["X"].Properties["Doc"].(map[string]any)["Version"]; v != "2012-10-17" {
		t.Fatalf("version %v", v)
	}
}

func TestUnsupportedTransformAndBadRefs(t *testing.T) {
	if _, err := Parse("Transform: AWS::Serverless-2016-10-31\nResources: {Q: {Type: AWS::SQS::Queue}}\n"); err == nil || !strings.Contains(err.Error(), "transforms") {
		t.Fatalf("transform accepted: %v", err)
	}
	if _, err := Parse(`{"Resources":{"A":{"Type":"AWS::SQS::Queue","DependsOn":"Nope"}}}`); err == nil || !strings.Contains(err.Error(), "Nope") {
		t.Fatalf("dangling DependsOn: %v", err)
	}
	if _, err := Parse(`{"Resources":{"A":{"Type":"AWS::SQS::Queue","Properties":{"X":{"Fn::Sub":"${Nope}"}}}}}`); err == nil {
		t.Fatal("dangling Sub accepted")
	}
	if _, err := Parse(`{"Resources":{"A":{"Type":"AWS::SQS::Queue","Properties":{"X":{"Fn::Sub":["${V}",{"V":"1"}]}}}}}`); err != nil {
		t.Fatalf("Sub variable rejected: %v", err)
	}
	if _, err := Parse(`{"Resources":{"bad_id":{"Type":"AWS::SQS::Queue"}}}`); err == nil {
		t.Fatal("non-alphanumeric logical id accepted")
	}
	if _, err := Parse(`{"Resources":{}}`); err == nil {
		t.Fatal("empty template accepted")
	}
}

func TestParamsConstraints(t *testing.T) {
	tp := mustParse(t, `
Parameters:
  N: {Type: Number, MinValue: 1, MaxValue: 3, Default: 2}
  S: {Type: String, AllowedPattern: "[a-z]+", MinLength: 2, MaxLength: 4, Default: ab}
  L: {Type: "List<Number>", Default: "1,2"}
  Secret: {Type: String, NoEcho: "true", Default: s}
Resources: {Q: {Type: AWS::SQS::Queue}}
`)
	ok, err := params(tp, map[string]any{"N": "3", "S": "abcd"})
	if err != nil || ok["N"] != "3" {
		t.Fatalf("%v %v", ok, err)
	}
	if !tp.Parameters["Secret"].NoEcho {
		t.Fatal(`NoEcho: "true" (a string) must count`)
	}
	for _, in := range []map[string]any{{"N": "0"}, {"N": "4"}, {"N": "x"}, {"S": "A"}, {"S": "a"}, {"S": "abcde"}, {"L": "1,x"}, {"Z": "1"}} {
		if _, err := params(tp, in); err == nil {
			t.Errorf("%v accepted", in)
		}
	}
}

func TestCapabilityDetection(t *testing.T) {
	plain := mustParse(t, `{"Resources":{"Q":{"Type":"AWS::SQS::Queue"}}}`)
	if need, _ := capabilities(plain); need != nil {
		t.Fatalf("plain: %v", need)
	}
	role := mustParse(t, `{"Resources":{"R":{"Type":"AWS::IAM::Role","Properties":{"AssumeRolePolicyDocument":{}}}}}`)
	if need, _ := capabilities(role); !reflect.DeepEqual(need, []string{"CAPABILITY_IAM"}) {
		t.Fatalf("role: %v", need)
	}
	named := mustParse(t, `{"Resources":{"R":{"Type":"AWS::IAM::Role","Properties":{"RoleName":"x"}}}}`)
	if need, _ := capabilities(named); !reflect.DeepEqual(need, []string{"CAPABILITY_NAMED_IAM"}) {
		t.Fatalf("named: %v", need)
	}
	for given, ok := range map[string]bool{"CAPABILITY_IAM": false, "CAPABILITY_NAMED_IAM": true} {
		if err := checkCapabilities(named, []string{given}); (err == nil) != ok {
			t.Errorf("%s on a named role: %v", given, err)
		}
	}
	if err := checkCapabilities(role, []string{"CAPABILITY_IAM"}); err != nil {
		t.Fatal(err)
	}
	if err := checkCapabilities(role, nil); err == nil || !strings.Contains(err.Error(), "Requires capabilities : [CAPABILITY_IAM]") {
		t.Fatalf("missing capability: %v", err)
	}
}

func TestBucketKey(t *testing.T) {
	for _, c := range []struct{ url, bucket, key string }{
		{"https://b.s3.amazonaws.com/a/b.yaml", "b", "a/b.yaml"},
		{"https://b.s3.us-east-1.amazonaws.com/t.json?X-Amz-Signature=1", "b", "t.json"},
		{"https://s3.us-east-1.amazonaws.com/b/dir/t.yaml", "b", "dir/t.yaml"},
		{"http://hc.local:8080/b/t%20x.yaml", "b", "t x.yaml"},
	} {
		b, k, ok := bucketKey(c.url, "hc.local:8080")
		if !ok || b != c.bucket || k != c.key {
			t.Errorf("%s -> %q %q %v", c.url, b, k, ok)
		}
	}
	for _, u := range []string{"https://example.com/b/t.yaml", "ftp://b.s3.amazonaws.com/x", "not a url", "https://b.s3.amazonaws.com/"} {
		if _, _, ok := bucketKey(u, "hc.local:8080"); ok {
			t.Errorf("%s accepted", u)
		}
	}
}

func TestGeneratedSecret(t *testing.T) {
	v, err := generateSecret(map[string]any{"PasswordLength": float64(40), "ExcludeCharacters": "abc", "ExcludeNumbers": true, "ExcludePunctuation": true})
	if err != nil || len(v) != 40 || strings.ContainsAny(v, "abc0123456789!#$") {
		t.Fatalf("%q %v", v, err)
	}
	v, err = generateSecret(map[string]any{"SecretStringTemplate": `{"user":"u"}`, "GenerateStringKey": "pw"})
	var m map[string]string
	if err != nil || json.Unmarshal([]byte(v), &m) != nil || m["user"] != "u" || len(m["pw"]) != 32 {
		t.Fatalf("%q %v", v, err)
	}
	if _, err := generateSecret(map[string]any{"PasswordLength": float64(0)}); err == nil {
		t.Fatal("zero length accepted")
	}
}

func TestDiff(t *testing.T) {
	s := &Service{}
	old := mustParse(t, `
Parameters: {N: {Type: String, Default: a}}
Resources:
  Keep: {Type: AWS::SQS::Queue, Properties: {QueueName: keep}}
  Change: {Type: AWS::SQS::Queue, Properties: {QueueName: !Ref N}}
  Gone: {Type: AWS::SNS::Topic}
  Dep: {Type: AWS::SSM::Parameter, Properties: {Type: String, Value: !Ref Change}}
`)
	st := &Stack{Name: "d", ARN: "arn:aws:cloudformation:us-east-1:123456789012:stack/d/1", Parameters: map[string]any{"N": "a"}, Resources: map[string]*Resource{
		"Keep": {PhysicalID: "k"}, "Change": {PhysicalID: "c"}, "Gone": {PhysicalID: "g"}, "Dep": {PhysicalID: "d"}}}
	next := mustParse(t, `
Parameters: {N: {Type: String, Default: a}}
Resources:
  Keep: {Type: AWS::SQS::Queue, Properties: {QueueName: keep}}
  Change: {Type: AWS::SQS::Queue, Properties: {QueueName: !Ref N}}
  Dep: {Type: AWS::SSM::Parameter, Properties: {Type: String, Value: !Ref Change}}
  New: {Type: AWS::SNS::Topic}
`)
	got := map[string]string{}
	for _, c := range s.diff(old, st, next, map[string]any{"N": "b"}) {
		got[c.LogicalID] = c.Action
	}
	want := map[string]string{"Change": "Modify", "Dep": "Modify", "Gone": "Remove", "New": "Add"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("diff %v, want %v", got, want)
	}
	if ch := s.diff(old, st, old, map[string]any{"N": "a"}); len(ch) != 0 {
		t.Fatalf("identical template: %v", ch)
	}
}
