package cfn

import (
	"strings"
	"testing"
)

const tmpl = `
Description: demo
Parameters:
  Env: {Type: String, Default: dev, AllowedValues: [dev, prod]}
Resources:
  Worker:
    Type: HC::Lambda::Function
    Properties:
      name: !Sub "worker-${Env}"
      environment:
        QUEUE: !Ref Jobs
        BUCKET_ARN: !GetAtt Uploads.arn
  Jobs:
    Type: HC::SQS::Queue
    Properties: {name: !Join ["-", [jobs, !Ref Env]]}
  Uploads:
    Type: HC::S3::Bucket
    Properties: {name: !Sub "uploads-${HC::StackName}"}
  Trigger:
    Type: HC::Lambda::EventSourceMapping
    Properties: {function_name: !Ref Worker, queue_name: !Ref Jobs}
Outputs:
  Queue: {Value: !Ref Jobs}
`

func TestParseAndOrder(t *testing.T) {
	tp, err := Parse(tmpl)
	if err != nil {
		t.Fatal(err)
	}
	ord, err := order(tp)
	if err != nil {
		t.Fatal(err)
	}
	pos := map[string]int{}
	for i, id := range ord {
		pos[id] = i
	}
	if !(pos["Jobs"] < pos["Worker"] && pos["Uploads"] < pos["Worker"] && pos["Worker"] < pos["Trigger"]) {
		t.Fatalf("bad order %v", ord)
	}
}

func TestResolve(t *testing.T) {
	tp, _ := Parse(tmpl)
	st := &Stack{Name: "demo", ARN: "arn:aws:cloudformation:us-east-1:123456789012:stack/demo", Resources: map[string]*Resource{
		"Jobs":    {PhysicalID: "jobs-dev"},
		"Uploads": {PhysicalID: "uploads-demo", Attributes: map[string]any{"arn": "arn:aws:s3:::uploads-demo"}},
	}}
	ps, err := params(tp, map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	r := &resolver{stack: st, params: ps}
	v, err := r.resolve(tp.Resources["Worker"].Properties)
	if err != nil {
		t.Fatal(err)
	}
	m := v.(map[string]any)
	env := m["environment"].(map[string]any)
	if m["name"] != "worker-dev" || env["QUEUE"] != "jobs-dev" || env["BUCKET_ARN"] != "arn:aws:s3:::uploads-demo" {
		t.Fatalf("resolved %v", m)
	}
	j, _ := r.resolve(tp.Resources["Jobs"].Properties)
	if j.(map[string]any)["name"] != "jobs-dev" {
		t.Fatalf("join %v", j)
	}
	u, _ := r.resolve(tp.Resources["Uploads"].Properties)
	if u.(map[string]any)["name"] != "uploads-demo" {
		t.Fatalf("pseudo parameter %v", u)
	}
	if _, err := params(tp, map[string]any{"Env": "staging"}); err == nil {
		t.Fatal("AllowedValues not enforced")
	}
}

func TestAliasBombs(t *testing.T) {
	if _, err := Parse("Resources: &r {X: *r}\n"); err == nil {
		t.Fatal("recursive alias accepted")
	}
	bomb := "a: &a [x,x,x,x,x,x,x,x,x,x]\nb: &b [*a,*a,*a,*a,*a,*a,*a,*a,*a,*a]\nc: &c [*b,*b,*b,*b,*b,*b,*b,*b,*b,*b]\nd: &d [*c,*c,*c,*c,*c,*c,*c,*c,*c,*c]\ne: &e [*d,*d,*d,*d,*d,*d,*d,*d,*d,*d]\nResources: {Q: {Type: HC::SQS::Queue, Properties: {x: *e}}}\n"
	if _, err := Parse(bomb); err == nil || !strings.Contains(err.Error(), "expands") {
		t.Fatalf("alias bomb not rejected: %v", err)
	}
}

func TestCycleAndUnknownType(t *testing.T) {
	cyc := `{"Resources":{"A":{"Type":"HC::SQS::Queue","Properties":{"name":{"Ref":"B"}}},"B":{"Type":"HC::SQS::Queue","Properties":{"name":{"Ref":"A"}}}}}`
	tp, err := Parse(cyc)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := order(tp); err == nil || !strings.Contains(err.Error(), "circular") {
		t.Fatalf("expected cycle error, got %v", err)
	}
	if _, err := Parse(`{"Resources":{"A":{"Type":"AWS::Nope"}}}`); err == nil {
		t.Fatal("unknown type accepted")
	}
}

func TestScalarTypes(t *testing.T) {
	tp, err := Parse("Parameters: {P: {Type: String, NoEcho: true}}\nResources:\n  Q: {Type: HC::SQS::Queue, Properties: {name: q, visibility_timeout: 45, fifo: false}}\n")
	if err != nil {
		t.Fatal(err)
	}
	props := tp.Resources["Q"].Properties
	if props["visibility_timeout"] != 45 && props["visibility_timeout"] != 45.0 {
		t.Fatalf("number decoded as %T %v", props["visibility_timeout"], props["visibility_timeout"])
	}
	if props["fifo"] != false || !tp.Parameters["P"].NoEcho {
		t.Fatalf("booleans decoded wrong: %v %v", props["fifo"], tp.Parameters["P"].NoEcho)
	}
}
