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

// at is a one-time schedule (EventBridge Scheduler "at(2026-01-01T09:00:00)").
type at struct{ t time.Time }

func (a at) Due(t, last time.Time) bool { return last.IsZero() && !t.Before(a.t) }
func (a at) Next(after time.Time) time.Time {
	if after.Before(a.t) {
		return a.t.UTC()
	}
	return time.Time{}
}

// cron is an AWS-style 6-field expression: minutes hours day-of-month month
// day-of-week year, evaluated in loc. Day-of-month accepts L (last day), LW
// and nW (nearest weekday); day-of-week accepts nL (last such weekday of the
// month) and n#k (k-th such weekday).
type cron struct {
	fields    [6]map[int]bool
	anyDOM    bool
	anyDOW    bool
	lastDOM   bool
	lastWkday bool         // LW
	nearestWk map[int]bool // nW
	lastDOW   map[int]bool // nL
	nthDOW    map[[2]int]bool
	loc       *time.Location
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
	if !last.IsZero() && last.Truncate(time.Minute).Equal(t.Truncate(time.Minute)) {
		return false
	}
	return c.matches(t)
}

func daysIn(t time.Time) int {
	return time.Date(t.Year(), t.Month()+1, 0, 0, 0, 0, 0, t.Location()).Day()
}

// nearestWeekday returns the weekday closest to day d of t's month, without leaving the month.
func nearestWeekday(t time.Time, d int) int {
	n := daysIn(t)
	d = min(d, n)
	wd := time.Date(t.Year(), t.Month(), d, 0, 0, 0, 0, t.Location()).Weekday()
	switch wd {
	case time.Saturday:
		if d == 1 {
			return 3
		}
		return d - 1
	case time.Sunday:
		if d == n {
			return d - 2
		}
		return d + 1
	}
	return d
}

func (c cron) domMatch(t time.Time) bool {
	d := t.Day()
	if c.fields[2][d] {
		return true
	}
	if c.lastDOM && d == daysIn(t) {
		return true
	}
	if c.lastWkday && d == nearestWeekday(t, daysIn(t)) {
		return true
	}
	for n := range c.nearestWk {
		if d == nearestWeekday(t, n) {
			return true
		}
	}
	return false
}

func (c cron) dowMatch(t time.Time) bool {
	wd := int(t.Weekday()) + 1
	if c.fields[4][wd] {
		return true
	}
	if c.lastDOW[wd] && t.Day()+7 > daysIn(t) {
		return true
	}
	return c.nthDOW[[2]int{wd, (t.Day()-1)/7 + 1}]
}

func (c cron) matches(t time.Time) bool {
	loc := c.loc
	if loc == nil {
		loc = time.UTC
	}
	t = t.In(loc)
	if !c.fields[0][t.Minute()] || !c.fields[1][t.Hour()] || !c.fields[3][int(t.Month())] || !c.fields[5][t.Year()] {
		return false
	}
	switch {
	case c.anyDOM:
		return c.dowMatch(t)
	case c.anyDOW:
		return c.domMatch(t)
	}
	return c.domMatch(t) && c.dowMatch(t)
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

// ParseSchedule parses an EventBridge rule schedule: "rate(5 minutes)" or "cron(0 12 * * ? *)" (UTC).
func ParseSchedule(expr string) (Schedule, error) {
	if strings.HasPrefix(strings.TrimSpace(expr), "at(") {
		return nil, fmt.Errorf("rules take rate(...) or cron(...) schedules; use EventBridge Scheduler for one-time at(...) schedules")
	}
	return ParseScheduleIn(expr, time.UTC)
}

// ParseScheduleIn parses rate(), cron() or at() evaluated in loc.
func ParseScheduleIn(expr string, loc *time.Location) (Schedule, error) {
	expr = strings.TrimSpace(expr)
	if inner, ok := strings.CutPrefix(expr, "at("); ok && strings.HasSuffix(inner, ")") {
		t, err := time.ParseInLocation("2006-01-02T15:04:05", strings.TrimSuffix(inner, ")"), loc)
		if err != nil {
			return nil, fmt.Errorf("at expressions look like at(2026-01-01T09:00:00)")
		}
		return at{t: t}, nil
	}
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
		c := cron{loc: loc, nearestWk: map[int]bool{}, lastDOW: map[int]bool{}, nthDOW: map[[2]int]bool{}}
		// Special day-of-month forms.
		var domParts []string
		for _, p := range strings.Split(f[2], ",") {
			switch up := strings.ToUpper(p); {
			case up == "L":
				c.lastDOM = true
			case up == "LW":
				c.lastWkday = true
			case strings.HasSuffix(up, "W"):
				n, err := strconv.Atoi(strings.TrimSuffix(up, "W"))
				if err != nil || n < 1 || n > 31 {
					return nil, fmt.Errorf("field 3: bad value %q", p)
				}
				c.nearestWk[n] = true
			default:
				domParts = append(domParts, p)
			}
		}
		var dowParts []string
		for _, p := range strings.Split(f[4], ",") {
			up := strings.ToUpper(p)
			switch {
			case strings.Contains(up, "#"):
				d, k, _ := strings.Cut(up, "#")
				dn, err1 := dowValue(d)
				kn, err2 := strconv.Atoi(k)
				if err1 != nil || err2 != nil || kn < 1 || kn > 5 {
					return nil, fmt.Errorf("field 5: bad value %q", p)
				}
				c.nthDOW[[2]int{dn, kn}] = true
			case len(up) > 1 && strings.HasSuffix(up, "L"):
				dn, err := dowValue(strings.TrimSuffix(up, "L"))
				if err != nil {
					return nil, fmt.Errorf("field 5: bad value %q", p)
				}
				c.lastDOW[dn] = true
			case up == "L":
				c.fields[4] = map[int]bool{7: true} // L alone in day-of-week is Saturday
			default:
				dowParts = append(dowParts, p)
			}
		}
		limits := [6][2]int{{0, 59}, {0, 23}, {1, 31}, {1, 12}, {1, 7}, {1970, 2199}}
		for i, fld := range f {
			names := map[string]int(nil)
			switch i {
			case 2:
				fld = strings.Join(domParts, ",")
			case 3:
				names = monthNames
			case 4:
				names = dayNames
				fld = strings.Join(dowParts, ",")
			}
			if fld == "" {
				if c.fields[i] == nil {
					c.fields[i] = map[int]bool{}
				}
				continue
			}
			m, err := parseField(fld, limits[i][0], limits[i][1], names)
			if err != nil {
				return nil, fmt.Errorf("field %d: %v", i+1, err)
			}
			for k := range c.fields[i] {
				m[k] = true
			}
			c.fields[i] = m
		}
		c.anyDOM, c.anyDOW = f[2] == "?", f[4] == "?"
		if c.anyDOM {
			c.fields[2] = map[int]bool{}
		}
		if c.anyDOW {
			c.fields[4] = map[int]bool{}
		}
		return c, nil
	}
	return nil, fmt.Errorf("schedule must be rate(...) or cron(...)")
}

func dowValue(s string) (int, error) {
	if n, ok := dayNames[s]; ok {
		return n, nil
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < 1 || n > 7 {
		return 0, fmt.Errorf("bad day %q", s)
	}
	return n, nil
}
