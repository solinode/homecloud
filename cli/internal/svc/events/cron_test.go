package events

import (
	"testing"
	"time"
)

func TestCron(t *testing.T) {
	s, err := ParseSchedule("cron(0 12 ? * MON-FRI *)")
	if err != nil {
		t.Fatal(err)
	}
	mon := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC) // a Monday
	if !s.Due(mon, time.Time{}) {
		t.Error("should fire Monday noon")
	}
	if s.Due(mon.AddDate(0, 0, 5), time.Time{}) {
		t.Error("should not fire Saturday")
	}
	if got := s.Next(mon); !got.Equal(mon.AddDate(0, 0, 1)) {
		t.Errorf("next = %v", got)
	}
	s, _ = ParseSchedule("cron(*/15 * * * ? *)")
	if !s.Due(time.Date(2026, 1, 1, 3, 45, 0, 0, time.UTC), time.Time{}) || s.Due(time.Date(2026, 1, 1, 3, 46, 0, 0, time.UTC), time.Time{}) {
		t.Error("step minutes")
	}
	for _, bad := range []string{"cron(0 12 * * * *)", "rate(0 minutes)", "rate(5 weeks)", "every 5m"} {
		if _, err := ParseSchedule(bad); err == nil {
			t.Errorf("%s should fail", bad)
		}
	}
}

func TestRate(t *testing.T) {
	s, _ := ParseSchedule("rate(5 minutes)")
	last := time.Date(2026, 1, 1, 10, 0, 2, 500, time.UTC)
	if s.Due(time.Date(2026, 1, 1, 10, 4, 2, 0, time.UTC), last) {
		t.Error("too early")
	}
	if !s.Due(time.Date(2026, 1, 1, 10, 5, 2, 0, time.UTC), last) {
		t.Error("should be due")
	}
}

func TestPattern(t *testing.T) {
	ev := map[string]any{"source": "orders", "detail-type": "OrderPlaced", "detail": map[string]any{"total": 120.0, "status": "paid", "sku": "ab-12"}}
	cases := []struct {
		p    map[string]any
		want bool
	}{
		{map[string]any{"source": []any{"orders"}}, true},
		{map[string]any{"source": []any{"billing"}}, false},
		{map[string]any{"detail": map[string]any{"status": []any{"paid", "shipped"}}}, true},
		{map[string]any{"detail": map[string]any{"sku": []any{map[string]any{"prefix": "ab-"}}}}, true},
		{map[string]any{"detail": map[string]any{"total": []any{map[string]any{"numeric": []any{">", 100.0}}}}}, true},
		{map[string]any{"detail": map[string]any{"coupon": []any{map[string]any{"exists": false}}}}, true},
		{map[string]any{"detail": map[string]any{"status": []any{map[string]any{"anything-but": []any{"paid"}}}}}, false},
	}
	for i, c := range cases {
		if got := matchPattern(c.p, ev); got != c.want {
			t.Errorf("case %d: got %v", i, got)
		}
	}
}
