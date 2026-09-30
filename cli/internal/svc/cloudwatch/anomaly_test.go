package cloudwatch

import (
	"math"
	"strings"
	"testing"
	"time"
)

// periodicHist is hours of a daily cycle sampled every 5 minutes, ending at end.
func periodicHist(end time.Time, days int) hist {
	var h hist
	for t := end.Add(-time.Duration(days) * 24 * time.Hour); t.Before(end); t = t.Add(5 * time.Minute) {
		h.t = append(h.t, t)
		h.v = append(h.v, cycle(t)+float64(t.Minute()/5%3)-1) // noise of -1, 0, +1
	}
	return h
}

func cycle(t time.Time) float64 {
	return 100 + 20*math.Sin(2*math.Pi*float64(t.Hour())/24)
}

func TestBandModel(t *testing.T) {
	end := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	h := periodicHist(end, 2)
	m := trainBand(h, nil, time.UTC)
	if m == nil || m.level != 1 {
		t.Fatalf("model %+v", m)
	}
	at := time.Date(2026, 9, 30, 18, 10, 0, 0, time.UTC)
	lo, hi := m.band(at, 2)
	want := cycle(at)
	if lo >= want || hi <= want || hi-lo > 4 || hi-lo < 0.5 {
		t.Fatalf("band [%v, %v] around %v", lo, hi, want)
	}
	if l1, h1 := m.band(at, 1); h1-l1 >= hi-lo {
		t.Fatalf("1 stddev band %v not narrower than 2 stddev band %v", h1-l1, hi-lo)
	}
	// Excluded ranges are ignored: a bad day marked as excluded does not move the band.
	bad := periodicHist(end, 2)
	for i, ts := range bad.t {
		if ts.After(end.Add(-30 * time.Hour)) && ts.Before(end.Add(-24*time.Hour)) {
			bad.v[i] += 500
		}
	}
	excl := []timeRange{{end.Add(-30 * time.Hour), end.Add(-24 * time.Hour)}}
	polluted := trainBand(bad, nil, time.UTC)
	clean := trainBand(bad, excl, time.UTC)
	pl, ph := polluted.band(end.Add(-27*time.Hour), 2)
	cl, ch := clean.band(end.Add(-27*time.Hour), 2)
	if ph-pl < 100 || ch-cl > 4 {
		t.Fatalf("polluted width %v, excluded width %v", ph-pl, ch-cl)
	}
	// Too little history: no model.
	if trainBand(hist{t: h.t[:9], v: h.v[:9]}, nil, time.UTC) != nil {
		t.Fatal("model from 9 datapoints")
	}
	if trainBand(hist{t: h.t[:11], v: h.v[:11]}, nil, time.UTC) != nil {
		t.Fatal("model from under an hour of history")
	}
	// Under a day of history: one rolling window.
	r := trainBand(periodicHist(end, 1), nil, time.UTC)
	if r == nil || r.level != 0 {
		// exactly 24h minus one step is below the day threshold.
		t.Fatalf("rolling model %+v", r)
	}
	// Time zones shift the hour-of-day buckets.
	ny, _ := time.LoadLocation("America/New_York")
	if bucketKey(1, at, time.UTC) == bucketKey(1, at, ny) {
		t.Fatal("time zone ignored")
	}
}

func TestFillLinear(t *testing.T) {
	_, svc := newAWS(t)
	base := time.Now().UTC().Truncate(time.Hour).Add(-time.Hour)
	// Datapoints at minutes 1, 4 and 5; minute 0 and 2-3 are missing.
	for _, m := range []int{1, 4, 5} {
		svc.Put("F", "x", nil, "None", float64(m*10), base.Add(time.Duration(m)*time.Minute))
	}
	q := func(expr string) []MetricDataQuery {
		return []MetricDataQuery{{Id: "m", MetricStat: &MetricStat{Metric: Metric{Namespace: "F", MetricName: "x"}, Period: 60, Stat: "Sum"}, ReturnData: new(bool)},
			{Id: "f", Expression: expr}}
	}
	res, _, err := svc.evalMetricQueries(q("FILL(m, LINEAR)"), base, base.Add(7*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	f := res["f"].series
	got := map[int]float64{}
	for i, ts := range f.t {
		got[int(ts.Sub(base).Minutes())] = f.v[i]
	}
	want := map[int]float64{1: 10, 2: 20, 3: 30, 4: 40, 5: 50}
	if len(got) != len(want) {
		t.Fatalf("linear fill %v", got)
	}
	for k, v := range want {
		if math.Abs(got[k]-v) > 1e-9 {
			t.Fatalf("linear fill %v", got)
		}
	}
	res, _, err = svc.evalMetricQueries(q("FILL(m, REPEAT)"), base, base.Add(7*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	r := res["f"].series
	if len(r.v) != 6 || r.v[1] != 10 || r.v[2] != 10 || r.v[5] != 50 {
		t.Fatalf("repeat fill %v", r.v)
	}
	if _, _, err := svc.evalMetricQueries(q("FILL(m)"), base, base.Add(time.Minute)); err == nil || !strings.Contains(err.Error(), "FILL needs") {
		t.Fatalf("FILL(m): %v", err)
	}
}
