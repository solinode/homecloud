package events

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Schedule decides whether a rule fires at a given minute.
type Schedule interface {
	Due(t time.Time, last time.Time) bool
	Next(after time.Time) time.Time
}

type rate struct{ every time.Duration }

func (r rate) Due(t, last time.Time) bool {
	return last.IsZero() || !t.Truncate(time.Minute).Before(last.Truncate(time.Minute).Add(r.every))
}
func (r rate) Next(after time.Time) time.Time {
	return after.UTC().Add(r.every)
}

// cron is an AWS-style 6-field expression: minutes hours day-of-month month day-of-week year.
type cron struct {
	fields [6]map[int]bool
	anyDOM bool
	anyDOW bool
}

var monthNames = map[string]int{"JAN": 1, "FEB": 2, "MAR": 3, "APR": 4, "MAY": 5, "JUN": 6, "JUL": 7, "AUG": 8, "SEP": 9, "OCT": 10, "NOV": 11, "DEC": 12}
var dayNames = map[string]int{"SUN": 1, "MON": 2, "TUE": 3, "WED": 4, "THU": 5, "FRI": 6, "SAT": 7}

func parseField(f string, lo, hi int, names map[string]int) (map[int]bool, error) {
	out := map[int]bool{}
	if f == "*" || f == "?" {
		for i := lo; i <= hi; i++ {
			out[i] = true
		}
		return out, nil
	}
	val := func(s string) (int, error) {
		if n, ok := names[strings.ToUpper(s)]; ok {
			return n, nil
		}
		return strconv.Atoi(s)
	}
	for _, part := range strings.Split(f, ",") {
		step := 1
		if b, s, ok := strings.Cut(part, "/"); ok {
			n, err := strconv.Atoi(s)
			if err != nil || n < 1 {
				return nil, fmt.Errorf("bad step %q", part)
			}
			part, step = b, n
		}
		start, end := lo, hi
		switch {
		case part == "*":
		case strings.Contains(part, "-"):
			a, b, _ := strings.Cut(part, "-")
			var err error
			if start, err = val(a); err != nil {
				return nil, fmt.Errorf("bad value %q", a)
			}
			if end, err = val(b); err != nil {
				return nil, fmt.Errorf("bad value %q", b)
			}
		default:
			n, err := val(part)
			if err != nil {
				return nil, fmt.Errorf("bad value %q", part)
			}
			start = n
			if step == 1 {
				end = n
			}
		}
		if start < lo || end > hi || start > end {
			return nil, fmt.Errorf("value out of range in %q", f)
		}
		for i := start; i <= end; i += step {
			out[i] = true
		}
	}
	return out, nil
}

func (c cron) Due(t, last time.Time) bool {
	t = t.UTC()
	if !last.IsZero() && last.UTC().Truncate(time.Minute).Equal(t.Truncate(time.Minute)) {
		return false
	}
	return c.matches(t)
}

func (c cron) matches(t time.Time) bool {
	if !c.fields[0][t.Minute()] || !c.fields[1][t.Hour()] || !c.fields[3][int(t.Month())] || !c.fields[5][t.Year()] {
		return false
	}
	dom := c.fields[2][t.Day()]
	dow := c.fields[4][int(t.Weekday())+1]
	switch {
	case c.anyDOM:
		return dow
	case c.anyDOW:
		return dom
	}
	return dom && dow
}

func (c cron) Next(after time.Time) time.Time {
	t := after.UTC().Truncate(time.Minute).Add(time.Minute)
	for i := 0; i < 366*24*60; i++ {
		if c.matches(t) {
			return t
		}
		t = t.Add(time.Minute)
	}
	return time.Time{}
}

// ParseSchedule parses "rate(5 minutes)" or "cron(0 12 * * ? *)".
func ParseSchedule(expr string) (Schedule, error) {
	expr = strings.TrimSpace(expr)
	if inner, ok := strings.CutPrefix(expr, "rate("); ok && strings.HasSuffix(inner, ")") {
		parts := strings.Fields(strings.TrimSuffix(inner, ")"))
		if len(parts) != 2 {
			return nil, fmt.Errorf("rate expressions look like rate(5 minutes)")
		}
		n, err := strconv.Atoi(parts[0])
		if err != nil || n < 1 {
			return nil, fmt.Errorf("rate value must be a positive integer")
		}
		unit := map[string]time.Duration{"minute": time.Minute, "minutes": time.Minute, "hour": time.Hour, "hours": time.Hour, "day": 24 * time.Hour, "days": 24 * time.Hour}[parts[1]]
		if unit == 0 {
			return nil, fmt.Errorf("rate unit must be minute(s), hour(s) or day(s)")
		}
		return rate{every: time.Duration(n) * unit}, nil
	}
	if inner, ok := strings.CutPrefix(expr, "cron("); ok && strings.HasSuffix(inner, ")") {
		f := strings.Fields(strings.TrimSuffix(inner, ")"))
		if len(f) != 6 {
			return nil, fmt.Errorf("cron expressions have 6 fields: minutes hours day-of-month month day-of-week year")
		}
		if (f[2] == "?") == (f[4] == "?") {
			return nil, fmt.Errorf("exactly one of day-of-month or day-of-week must be '?'")
		}
		var c cron
		limits := [6][2]int{{0, 59}, {0, 23}, {1, 31}, {1, 12}, {1, 7}, {1970, 2199}}
		for i, fld := range f {
			names := map[string]int(nil)
			if i == 3 {
				names = monthNames
			}
			if i == 4 {
				names = dayNames
			}
			m, err := parseField(fld, limits[i][0], limits[i][1], names)
			if err != nil {
				return nil, fmt.Errorf("field %d: %v", i+1, err)
			}
			c.fields[i] = m
		}
		c.anyDOM, c.anyDOW = f[2] == "?", f[4] == "?"
		return c, nil
	}
	return nil, fmt.Errorf("schedule must be rate(...) or cron(...)")
}
