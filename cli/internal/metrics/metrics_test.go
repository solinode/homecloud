package metrics

import (
	"strings"
	"testing"
)

func TestExposition(t *testing.T) {
	r := NewRegistry()
	c := r.NewCounter("t_requests_total", "Requests.\nSecond line.", "code")
	c.Inc("200")
	c.Inc("200")
	c.Add(0.5, `we"ird\`)
	g := r.NewGauge("t_temp", "Temperature.")
	g.Set(-3.25)
	h := r.NewHistogram("t_seconds", "Latency.", []float64{1, 0.1}, "op")
	h.Observe(0.05, "a")
	h.Observe(0.1, "a") // a bound is inclusive
	h.Observe(5, "a")
	r.NewGaugeFunc("t_func", "From a func.", func() float64 { return 7 })
	r.NewCollector("t_empty", "Never reported.", Gauge, func() []Sample { return nil })

	var b strings.Builder
	r.WriteText(&b)
	want := `# HELP t_requests_total Requests.\nSecond line.
# TYPE t_requests_total counter
t_requests_total{code="200"} 2
t_requests_total{code="we\"ird\\"} 0.5
# HELP t_temp Temperature.
# TYPE t_temp gauge
t_temp -3.25
# HELP t_seconds Latency.
# TYPE t_seconds histogram
t_seconds_bucket{op="a",le="0.1"} 2
t_seconds_bucket{op="a",le="1"} 2
t_seconds_bucket{op="a",le="+Inf"} 3
t_seconds_sum{op="a"} 5.15
t_seconds_count{op="a"} 3
# HELP t_func From a func.
# TYPE t_func gauge
t_func 7
`
	if b.String() != want {
		t.Fatalf("got:\n%s\nwant:\n%s", b.String(), want)
	}
	if c.Value("200") != 2 {
		t.Fatalf("value %v", c.Value("200"))
	}
}

func TestDuplicateFamilyPanics(t *testing.T) {
	r := NewRegistry()
	r.NewGauge("dup", "")
	defer func() {
		if recover() == nil {
			t.Fatal("no panic")
		}
	}()
	r.NewCounter("dup", "")
}
