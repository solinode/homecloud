package events

import (
	"encoding/json"
	"testing"
	"time"
)

const sampleEvent = `{
  "version": "0", "id": "1", "detail-type": "Object Created", "source": "aws.s3", "account": "123456789012",
  "time": "2026-01-01T00:00:00Z", "region": "us-east-1", "resources": ["arn:aws:s3:::photos"],
  "detail": {
    "bucket": {"name": "Photos"},
    "object": {"key": "img/cat.PNG", "size": 2048},
    "source-ip": "10.0.3.17",
    "tags": ["red", "blue"],
    "items": [{"sku": "a1", "qty": 2}, {"sku": "b2", "qty": 0}],
    "note": null,
    "flag": true,
    "price": 30.5
  }
}`

func TestPatternSyntax(t *testing.T) {
	cases := []struct {
		pattern string
		want    bool
	}{
		{`{"source": ["aws.s3"]}`, true},
		{`{"source": ["aws.ec2", "aws.s3"]}`, true},
		{`{"source": ["aws.ec2"]}`, false},
		{`{"detail": {"object": {"size": [2048]}}}`, true},
		{`{"detail": {"object": {"size": [2048.0]}}}`, true},
		{`{"detail": {"object": {"size": ["2048"]}}}`, false},
		{`{"detail": {"note": [null]}}`, true},
		{`{"detail": {"missing": [null]}}`, false},
		{`{"detail": {"flag": [true]}}`, true},
		{`{"detail": {"tags": ["blue"]}}`, true},
		{`{"detail": {"tags": ["green"]}}`, false},
		{`{"detail": {"object": {"key": [{"prefix": "img/"}]}}}`, true},
		{`{"detail": {"object": {"key": [{"prefix": {"equals-ignore-case": "IMG/"}}]}}}`, true},
		{`{"detail": {"object": {"key": [{"suffix": ".png"}]}}}`, false},
		{`{"detail": {"object": {"key": [{"suffix": {"equals-ignore-case": ".png"}}]}}}`, true},
		{`{"detail": {"bucket": {"name": [{"equals-ignore-case": "photos"}]}}}`, true},
		{`{"detail": {"object": {"key": [{"wildcard": "img/*.PNG"}]}}}`, true},
		{`{"detail": {"object": {"key": [{"wildcard": "img/*.jpg"}]}}}`, false},
		{`{"detail": {"object": {"size": [{"numeric": [">", 1000, "<=", 2048]}]}}}`, true},
		{`{"detail": {"object": {"size": [{"numeric": ["<", 1000]}]}}}`, false},
		{`{"detail": {"price": [{"numeric": ["=", 30.5]}]}}`, true},
		{`{"detail": {"object": {"size": [{"exists": true}]}}}`, true},
		{`{"detail": {"object": [{"exists": true}]}}`, false},
		{`{"detail": {"absent": [{"exists": false}]}}`, true},
		{`{"detail": {"object": {"key": [{"exists": false}]}}}`, false},
		{`{"detail": {"source-ip": [{"cidr": "10.0.0.0/16"}]}}`, true},
		{`{"detail": {"source-ip": [{"cidr": "192.168.0.0/16"}]}}`, false},
		{`{"detail": {"bucket": {"name": [{"anything-but": "Other"}]}}}`, true},
		{`{"detail": {"bucket": {"name": [{"anything-but": ["Photos", "Other"]}]}}}`, false},
		{`{"detail": {"object": {"size": [{"anything-but": [100, 200]}]}}}`, true},
		{`{"detail": {"object": {"key": [{"anything-but": {"prefix": "img/"}}]}}}`, false},
		{`{"detail": {"object": {"key": [{"anything-but": {"suffix": ".jpg"}}]}}}`, true},
		{`{"detail": {"bucket": {"name": [{"anything-but": {"equals-ignore-case": ["photos", "x"]}}]}}}`, false},
		{`{"detail": {"object": {"key": [{"anything-but": {"wildcard": "*.PNG"}}]}}}`, false},
		{`{"detail": {"absent": [{"anything-but": "x"}]}}`, false},
		{`{"detail": {"items": {"sku": ["b2"]}}}`, true},
		{`{"detail": {"items": {"sku": ["c3"]}}}`, false},
		{`{"source": ["aws.s3"], "$or": [{"detail": {"flag": [false]}}, {"detail": {"price": [{"numeric": [">", 10]}]}}]}`, true},
		{`{"$or": [{"source": ["x"]}, {"detail-type": ["y"]}]}`, false},
		{`{"resources": [{"prefix": "arn:aws:s3:::"}]}`, true},
	}
	for _, c := range cases {
		got, err := MatchJSON([]byte(c.pattern), []byte(sampleEvent))
		if err != nil {
			t.Errorf("%s: %v", c.pattern, err)
			continue
		}
		if got != c.want {
			t.Errorf("%s: got %v, want %v", c.pattern, got, c.want)
		}
	}
	for _, bad := range []string{
		`[]`, `{}`, `{"source": "aws.s3"}`, `{"source": [{"prefix": 1}]}`, `{"source": [{"bogus": "x"}]}`,
		`{"x": [{"numeric": [">", "a"]}]}`, `{"x": [{"numeric": ["~", 1]}]}`, `{"x": [{"numeric": [">", 1, ">", 2]}]}`,
		`{"x": [{"exists": "yes"}]}`, `{"x": [{"cidr": "nope"}]}`, `{"x": [{"prefix": "a", "suffix": "b"}]}`,
		`{"$or": [{"a": ["b"]}]}`, `{"x": [["nested"]]}`, `{"x": [{"anything-but": {"numeric": [">", 1]}}]}`, `not json`,
	} {
		if _, err := CompilePattern([]byte(bad)); err == nil {
			t.Errorf("%s: expected an invalid pattern", bad)
		}
	}
}

func TestInputTransformer(t *testing.T) {
	var ev map[string]any
	_ = json.Unmarshal([]byte(sampleEvent), &ev)
	r := Rule{Name: "r1", ARN: "arn:aws:events:us-east-1:1:rule/r1"}
	it := &InputTransformer{InputPathsMap: map[string]string{"key": "$.detail.object.key", "size": "$.detail.object.size", "tags": "$.detail.tags", "none": "$.detail.nope"},
		InputTemplate: `{"file": <key>, "msg": "object <key> is <size> bytes (<none>)", "tags": <tags>, "missing": <none>, "rule": "<aws.events.rule-name>"}`}
	out, err := transform(it, r, ev, []byte(sampleEvent))
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("not JSON: %s", out)
	}
	if got["file"] != "img/cat.PNG" || got["msg"] != "object img/cat.PNG is 2048 bytes ()" || got["missing"] != nil || got["rule"] != "r1" || len(got["tags"].([]any)) != 2 {
		t.Fatalf("transformed: %s", out)
	}
	out, _ = transform(&InputTransformer{InputPathsMap: map[string]string{"b": "$.detail.bucket.name"}, InputTemplate: `"bucket <b> changed"`}, r, ev, []byte(sampleEvent))
	if string(out) != `"bucket Photos changed"` {
		t.Fatalf("string template: %s", out)
	}
	p, _ := payloadFor(Target{InputPath: "$.detail.object"}, r, ev, []byte(sampleEvent))
	if string(p) != `{"key":"img/cat.PNG","size":2048}` {
		t.Fatalf("input path: %s", p)
	}
}

func TestCronExtensions(t *testing.T) {
	must := func(expr string, loc *time.Location) Schedule {
		s, err := ParseScheduleIn(expr, loc)
		if err != nil {
			t.Fatalf("%s: %v", expr, err)
		}
		return s
	}
	// Last day of the month.
	s := must("cron(0 9 L * ? *)", time.UTC)
	if got := s.Next(time.Date(2026, 2, 3, 0, 0, 0, 0, time.UTC)); !got.Equal(time.Date(2026, 2, 28, 9, 0, 0, 0, time.UTC)) {
		t.Errorf("L: %v", got)
	}
	// Third Friday.
	s = must("cron(30 10 ? * 6#3 *)", time.UTC)
	if got := s.Next(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)); !got.Equal(time.Date(2026, 1, 16, 10, 30, 0, 0, time.UTC)) {
		t.Errorf("#: %v", got)
	}
	// Last Friday.
	s = must("cron(0 0 ? * 6L *)", time.UTC)
	if got := s.Next(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)); !got.Equal(time.Date(2026, 1, 30, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("6L: %v", got)
	}
	// Nearest weekday to the 1st (Feb 1 2026 is a Sunday -> Monday 2nd).
	s = must("cron(0 8 1W * ? *)", time.UTC)
	if got := s.Next(time.Date(2026, 1, 31, 0, 0, 0, 0, time.UTC)); !got.Equal(time.Date(2026, 2, 2, 8, 0, 0, 0, time.UTC)) {
		t.Errorf("W: %v", got)
	}
	// Time zones.
	ny, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skip("no tzdata")
	}
	s = must("cron(0 9 * * ? *)", ny)
	if got := s.Next(time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)); !got.Equal(time.Date(2026, 7, 1, 13, 0, 0, 0, time.UTC)) {
		t.Errorf("tz: %v", got)
	}
	a := must("at(2026-03-01T10:00:00)", ny)
	if a.Due(time.Date(2026, 3, 1, 14, 59, 0, 0, time.UTC), time.Time{}) || !a.Due(time.Date(2026, 3, 1, 15, 0, 0, 0, time.UTC), time.Time{}) {
		t.Error("at in time zone")
	}
	if a.Due(time.Date(2026, 3, 2, 0, 0, 0, 0, time.UTC), time.Date(2026, 3, 1, 15, 0, 0, 0, time.UTC)) {
		t.Error("at fires once")
	}
	if _, err := ParseSchedule("at(2026-03-01T10:00:00)"); err == nil {
		t.Error("rules must not accept at()")
	}
}
