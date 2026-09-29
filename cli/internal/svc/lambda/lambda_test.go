package lambda

import (
	"strings"
	"testing"
	"time"

	"github.com/homecloudhq/homecloud/cli/internal/core"
)

func TestParseRef(t *testing.T) {
	cases := []struct{ in, name, qual string }{
		{"fn", "fn", ""},
		{"fn:live", "fn", "live"},
		{"fn:$LATEST", "fn", "$LATEST"},
		{"arn:aws:lambda:us-east-1:123456789012:function:fn", "fn", ""},
		{"arn:aws:lambda:us-east-1:123456789012:function:fn:3", "fn", "3"},
		{"123456789012:function:fn:prod", "fn", "prod"},
		{"arn:hc:lambda:local-1:123456789012:function:old", "old", ""},
	}
	for _, c := range cases {
		n, q := parseRef(c.in)
		if n != c.name || q != c.qual {
			t.Errorf("parseRef(%q) = %q, %q; want %q, %q", c.in, n, q, c.name, c.qual)
		}
	}
}

func TestClassify(t *testing.T) {
	body, fe, to := classify([]byte(`{"errorMessage": "boom", "errorType": "ValueError", "requestId": "r", "stackTrace": []}`), "r")
	if fe != "Unhandled" || to || !strings.Contains(string(body), "boom") {
		t.Fatalf("python error: %s %q %v", body, fe, to)
	}
	if _, fe, _ := classify([]byte(`{"errorType":"Error","errorMessage":"x","trace":["a"]}`), "r"); fe != "Unhandled" {
		t.Fatal("node errors carry trace")
	}
	// A result that happens to mention errorMessage alongside other keys is a normal result.
	if _, fe, _ := classify([]byte(`{"errorMessage":"x","errorType":"y","statusCode":400}`), "r"); fe != "" {
		t.Fatal("ordinary results must not be treated as errors")
	}
	body, fe, to = classify([]byte("Task timed out after 3.00 seconds"), "req-1")
	if fe != "Unhandled" || !to || !strings.Contains(string(body), "req-1 Task timed out after 3.00 seconds") {
		t.Fatalf("timeout: %s", body)
	}
	if body, fe, _ := classify(nil, "r"); string(body) != "null" || fe != "" {
		t.Fatalf("empty response: %s", body)
	}
}

func TestReportAndEmulatorLines(t *testing.T) {
	d, b, ok := parseReport("REPORT RequestId: abc\tInit Duration: 0.03 ms\tDuration: 84.22 ms\tBilled Duration: 85 ms\tMemory Size: 128 MB\tMax Memory Used: 128 MB\t")
	if !ok || d != 84.22 || b != 85 {
		t.Fatalf("parseReport = %v %v %v", d, b, ok)
	}
	if !isEmulatorLine("29 Sep 2026 13:04:37,154 [INFO] (rapid) INIT START(type: on-demand, phase: init)") {
		t.Fatal("emulator line not recognised")
	}
	for _, l := range []string{"START RequestId: x Version: $LATEST", "hello world", "[ERROR] ValueError: boom", ""} {
		if isEmulatorLine(l) {
			t.Fatalf("%q is function output", l)
		}
	}
}

func TestLineLogUntil(t *testing.T) {
	l := newLineLog()
	l.add("stale")
	mark := l.mark()
	go func() {
		time.Sleep(20 * time.Millisecond)
		for _, s := range []string{"START RequestId: r1", "out", "END RequestId: r1", "REPORT RequestId: r1\tDuration: 1 ms"} {
			l.add(s)
		}
	}()
	got := l.until(mark, "REPORT RequestId: r1", time.Second)
	if len(got) != 4 || got[0].text != "START RequestId: r1" || !strings.HasPrefix(got[3].text, "REPORT") {
		t.Fatalf("until returned %v", got)
	}
	// Consumed lines are dropped; a missing marker returns what is there after the timeout.
	l.add("next")
	if got := l.until(l.mark()-1, "REPORT RequestId: r2", 10*time.Millisecond); len(got) != 1 || got[0].text != "next" {
		t.Fatalf("timeout returned %v", got)
	}
}

func TestPickVersion(t *testing.T) {
	a := Alias{FunctionVersion: "2", Weights: map[string]float64{"1": 0.25}}
	if v := pickVersion(a, 0.1); v != "1" {
		t.Fatalf("r=0.1 -> %s", v)
	}
	if v := pickVersion(a, 0.5); v != "2" {
		t.Fatalf("r=0.5 -> %s", v)
	}
	if v := pickVersion(Alias{FunctionVersion: "$LATEST"}, 0.99); v != "$LATEST" {
		t.Fatal(v)
	}
}

func TestApplyConfig(t *testing.T) {
	f := Function{Runtime: "python3.12", Handler: "lambda_function.lambda_handler", PackageType: "Zip"}
	bad := []configInput{
		{MemoryMB: 64},
		{TimeoutSec: 901},
		{Handler: "nodot"},
		{Runtime: "cobol1"},
		{Environment: map[string]string{"AWS_REGION": "x"}},
		{Environment: map[string]string{"1BAD": "x"}},
		{Architectures: []string{"mips"}},
		{Layers: &[]string{"a:1", "b:1", "c:1", "d:1", "e:1", "f:1"}},
		{ImageConfig: &ImageConfig{Command: []string{"x"}}},
	}
	for i, in := range bad {
		g := f
		if err := applyConfig(&g, in); err == nil {
			t.Errorf("case %d: expected an error", i)
		}
	}
	desc := "d"
	if err := applyConfig(&f, configInput{MemoryMB: 512, TimeoutSec: 30, Description: &desc, Runtime: "provided.al2023", Handler: "bootstrap",
		Environment: map[string]string{"GREETING": "hi"}, Architectures: []string{"arm64"}}); err != nil {
		t.Fatal(err)
	}
	if f.MemoryMB != 512 || f.TimeoutSec != 30 || f.Handler != "bootstrap" || f.Environment["GREETING"] != "hi" || f.Architectures[0] != "arm64" {
		t.Fatalf("config not applied: %+v", f)
	}
	img := Function{PackageType: "Image"}
	if err := applyConfig(&img, configInput{Runtime: "python3.12"}); err == nil {
		t.Fatal("image functions have no runtime")
	}
}

func TestRuntimes(t *testing.T) {
	for _, name := range []string{"python3.9", "python3.13", "nodejs18.x", "nodejs22.x", "java17", "java21", "ruby3.3", "dotnet8", "provided.al2023", "provided.al2"} {
		rt, ok := findRuntime(name)
		if !ok || !strings.HasPrefix(rt.Image, "public.ecr.aws/lambda/") {
			t.Errorf("runtime %s: %+v", name, rt)
		}
	}
	rt, _ := findRuntime("java21")
	if !rt.validHandler("example.Handler::handleRequest") {
		t.Error("java handler rejected")
	}
}

func TestMatchRouteAndPrincipal(t *testing.T) {
	s := &Service{}
	var found bool
	for _, r := range s.awsRoutes() {
		if p, ok := matchRoute(r.path, strings.Split("2015-03-31/functions/fn/aliases/live", "/")); ok && r.method == "GET" {
			found = r.op == "GetAlias" && p["fn"] == "fn" && p["alias"] == "live"
		}
	}
	if !found {
		t.Fatal("GetAlias route not matched")
	}
	if p := principalOf("123456789012").(map[string]string); p["AWS"] != core.ARN("123456789012", "iam", "root") {
		t.Fatal(p)
	}
	if p := principalOf("events.amazonaws.com").(map[string]string); p["Service"] != "events.amazonaws.com" {
		t.Fatal(p)
	}
	if n, v, ok := parseLayerRef("arn:aws:lambda:us-east-1:123456789012:layer:libs:3"); !ok || n != "libs" || v != 3 {
		t.Fatal(n, v, ok)
	}
}
