package cloudwatch

import (
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/homecloudhq/homecloud/cli/internal/svc/svctest"
)

func newTestService(t *testing.T) *Service {
	t.Helper()
	s, err := New(svctest.Env(t))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestFilterPatterns(t *testing.T) {
	cases := []struct {
		pattern, msg string
		want         bool
	}{
		{"", "anything", true},
		{"ERROR", "an ERROR happened", true},
		{"ERROR", "an error happened", false}, // case-sensitive
		{"ERROR timeout", "ERROR: db timeout", true},
		{"ERROR timeout", "ERROR: db refused", false},
		{"?ERROR ?WARN", "WARN low disk", true},
		{"?ERROR ?WARN", "INFO ok", false},
		{`"connection refused"`, "dial: connection refused", true},
		{`"connection refused"`, "connection was refused", false},
		{"ERROR -retrying", "ERROR retrying", false},
		{"ERROR -retrying", "ERROR fatal", true},
		{"%[0-9]{3} ms%", "took 250 ms", true},
		{`{ $.level = "ERROR" }`, `{"level":"ERROR"}`, true},
		{`{ $.level = "ERROR" }`, `{"level":"INFO"}`, false},
		{`{ $.level = "ERR*" }`, `{"level":"ERROR"}`, true},
		{`{ $.level != "ERROR" }`, `{"level":"INFO"}`, true},
		{`{ $.latency > 100 }`, `{"latency":250}`, true},
		{`{ $.latency > 100 }`, `{"latency":50}`, false},
		{`{ $.latency > 100 }`, `not json`, false},
		{`{ $.user.id = 7 }`, `{"user":{"id":7}}`, true},
		{`{ $.items[1] = "b" }`, `{"items":["a","b"]}`, true},
		{`{ ($.a = 1) && ($.b = 2) }`, `{"a":1,"b":2}`, true},
		{`{ ($.a = 1) && ($.b = 2) }`, `{"a":1,"b":3}`, false},
		{`{ $.a = 1 || $.b = 2 }`, `{"a":0,"b":2}`, true},
		{`{ $.x IS NULL }`, `{"x":null}`, true},
		{`{ $.x NOT EXISTS }`, `{"y":1}`, true},
		{`{ $.x NOT EXISTS }`, `{"x":1}`, false},
		{`{ $.ok IS TRUE }`, `{"ok":true}`, true},
		{`{ $.ok IS FALSE }`, `{"ok":true}`, false},
		{`[ip, user, status = 404, size]`, `10.0.0.1 bob 404 512`, true},
		{`[ip, user, status = 404, size]`, `10.0.0.1 bob 200 512`, false},
		{`[ip, user, status = 4*, size]`, `10.0.0.1 bob 403 512`, true},
		{`[..., status = 5* || status = 429, size > 100]`, `a b c 503 512`, true},
		{`[..., status = 5* || status = 429, size > 100]`, `a b c 503 12`, false},
		{`[ip, ts, request, status]`, `1.2.3.4 [10/Oct/2000:13:55:36] "GET / HTTP/1.1" 200`, true},
		{`[ip, ts, request = "GET*", status]`, `1.2.3.4 [10/Oct/2000:13:55:36] "GET / HTTP/1.1" 200`, true},
	}
	for _, c := range cases {
		m, err := ParseFilterPattern(c.pattern)
		if err != nil {
			t.Errorf("%q: %v", c.pattern, err)
			continue
		}
		if got := m(c.msg); got != c.want {
			t.Errorf("%q on %q = %v, want %v", c.pattern, c.msg, got, c.want)
		}
	}
	for _, bad := range []string{`{ $.a = }`, `{ $.a = 1`, `{ level = 1 }`, `[a, b`, `"unterminated`, `{ $.a > "x" }`, `%(%`} {
		if _, err := ParseFilterPattern(bad); err == nil {
			t.Errorf("%q: expected an error", bad)
		}
	}
}

func runInsights(t *testing.T, q string, msgs ...string) [][][2]string {
	t.Helper()
	iq, err := ParseInsights(q)
	if err != nil {
		t.Fatalf("%q: %v", q, err)
	}
	base := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	var ev []LogEvent
	var groups []string
	for i, m := range msgs {
		ts := base.Add(time.Duration(i) * time.Minute)
		ev = append(ev, LogEvent{Timestamp: ts, Ingestion: ts, Message: m, Stream: "s"})
		groups = append(groups, "g")
	}
	rows, _, err := iq.Run(ev, groups, "123456789012", 0)
	if err != nil {
		t.Fatalf("%q: %v", q, err)
	}
	return rows
}

func flat(rows [][][2]string) string {
	var b strings.Builder
	for _, r := range rows {
		for i, f := range r {
			if i > 0 {
				b.WriteString(",")
			}
			b.WriteString(f[0] + "=" + f[1])
		}
		b.WriteString(";")
	}
	return b.String()
}

func TestInsights(t *testing.T) {
	logs := []string{
		`{"level":"INFO","latency":10,"user":{"id":"a"}}`,
		`{"level":"ERROR","latency":300,"user":{"id":"b"}}`,
		`{"level":"INFO","latency":30,"user":{"id":"a"}}`,
		`plain text line with timeout`,
		`{"level":"ERROR","latency":100,"user":{"id":"a"}}`,
	}
	cases := map[string]string{
		`fields level, latency | filter level = "ERROR" | sort latency desc`:                            "level=ERROR,latency=300;level=ERROR,latency=100;",
		`filter latency > 20 and level != "ERROR" | fields user.id`:                                     "user.id=a;",
		`stats count(*) as n by level | sort n desc`:                                                    "level=ERROR,n=2;level=INFO,n=2;n=1;",
		`filter ispresent(level) | stats avg(latency) as avg, max(latency) by level`:                    "level=ERROR,avg=200,max(latency)=300;level=INFO,avg=20,max(latency)=30;",
		`filter @message like /timeout/ | fields @message`:                                              "@message=plain text line with timeout;",
		`filter @message like "timeout" | stats count(*)`:                                               "count(*)=1;",
		`filter level in ["ERROR", "WARN"] | fields latency * 2 as double | sort double`:                "double=200;double=600;",
		`filter not ispresent(level) | display @logStream`:                                              "@logStream=s;",
		`fields @timestamp | sort @timestamp asc | limit 1`:                                             "@timestamp=2026-01-01 12:00:00.000;",
		`stats count(*) as c by bin(2m)`:                                                                "bin(2m)=2026-01-01 12:00:00.000,c=2;bin(2m)=2026-01-01 12:02:00.000,c=2;bin(2m)=2026-01-01 12:04:00.000,c=1;",
		`parse @message "plain * line with *" as what, why | filter ispresent(what) | fields what, why`: "what=text,why=timeout;",
		`filter ispresent(user.id) | dedup user.id | fields user.id | sort user.id`:                     "user.id=a;user.id=b;",
		`stats pct(latency, 50) as p50, count_distinct(user.id) as users`:                               "p50=30,users=2;",
		`filter strlen(level) = 5 and tolower(level) = "error" | fields concat(level, "!") as x`:        "x=ERROR!;x=ERROR!;",
	}
	for q, want := range cases {
		if got := flat(runInsights(t, q, logs...)); got != want {
			t.Errorf("%s\n got %s\nwant %s", q, got, want)
		}
	}
	// Default: newest first, @timestamp and @message.
	rows := runInsights(t, "", "one", "two")
	if flat(rows) != "@timestamp=2026-01-01 12:01:00.000,@message=two;@timestamp=2026-01-01 12:00:00.000,@message=one;" {
		t.Errorf("default query: %s", flat(rows))
	}
	for _, bad := range []string{"bogus x", "stats nope(x)", "limit -1", "filter (a = 1", "sort", "parse @message \"*\""} {
		if _, err := ParseInsights(bad); err == nil {
			t.Errorf("%q: expected an error", bad)
		}
	}
}

func TestMetricMath(t *testing.T) {
	env := newTestService(t)
	base := time.Now().Truncate(time.Minute).Add(-10 * time.Minute)
	for i := 0; i < 5; i++ {
		env.Put("M", "a", nil, "Count", float64(i+1), base.Add(time.Duration(i)*time.Minute))
		if i%2 == 0 {
			env.Put("M", "b", nil, "Count", 10, base.Add(time.Duration(i)*time.Minute))
		}
	}
	ms := func(id, name string) MetricDataQuery {
		return MetricDataQuery{Id: id, MetricStat: &MetricStat{Metric: Metric{Namespace: "M", MetricName: name}, Period: 60, Stat: "Sum"}}
	}
	expr := func(id, e string) MetricDataQuery { return MetricDataQuery{Id: id, Expression: e} }
	cases := map[string]string{
		"a + b":               "11,13,15",
		"a * 2 - 1":           "1,3,5,7,9",
		"SUM([a, b])":         "11,2,13,4,15",
		"SUM(METRICS())":      "11,2,13,4,15",
		"a - AVG(a)":          "-2,-1,0,1,2",
		"RATE(a) * 60":        "1,1,1,1",
		"DIFF(b)":             "0,0",
		"RUNNING_SUM(a)":      "1,3,6,10,15",
		"FILL(b, 0)":          "10,0,10,0,10",
		"FILL(b, REPEAT)":     "10,10,10,10,10",
		"MAX(METRICS()) / 10": "1,0.2,1,0.4,1",
		"ABS(-a)":             "1,2,3,4,5",
	}
	for e, want := range cases {
		res, _, err := env.evalMetricQueries([]MetricDataQuery{ms("a", "a"), ms("b", "b"), expr("e", e)}, base, base.Add(5*time.Minute))
		if err != nil {
			t.Errorf("%s: %v", e, err)
			continue
		}
		var got []string
		for _, v := range res["e"].series.v {
			got = append(got, strconv.FormatFloat(v, 'f', -1, 64))
		}
		if strings.Join(got, ",") != want {
			t.Errorf("%s = %s, want %s", e, strings.Join(got, ","), want)
		}
	}
	for _, bad := range []string{"a +", "NOPE(a)", "c", "e", "(a"} {
		if _, _, err := env.evalMetricQueries([]MetricDataQuery{ms("a", "a"), expr("e", bad)}, base, base.Add(5*time.Minute)); err == nil {
			t.Errorf("%q: expected an error", bad)
		}
	}
}
