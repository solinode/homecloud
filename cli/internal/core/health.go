package core

import (
	"sort"
	"sync"
	"time"
)

// Background loops report each pass with Beat, so operators can see a
// reconciler that stopped (a deadlock, a panic loop) in /metrics and the
// system status page.

// Loop is the last pass of a background loop.
type Loop struct {
	Name     string
	Interval time.Duration
	LastRun  time.Time
}

var (
	loopMu sync.Mutex
	loops  = map[string]*Loop{}
)

// Beat records a pass of the loop name, which runs about every interval.
func Beat(name string, interval time.Duration) {
	now := time.Now()
	loopMu.Lock()
	defer loopMu.Unlock()
	l := loops[name]
	if l == nil {
		l = &Loop{Name: name}
		loops[name] = l
	}
	l.Interval, l.LastRun = interval, now
}

// Loops returns every loop that has reported, by name.
func Loops() []Loop {
	loopMu.Lock()
	defer loopMu.Unlock()
	out := make([]Loop, 0, len(loops))
	for _, l := range loops {
		out = append(out, *l)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Stalled reports whether a loop missed several passes.
func (l Loop) Stalled(now time.Time) bool {
	return now.Sub(l.LastRun) > 3*l.Interval+30*time.Second
}

// RecentError is an error the server logged, kept for the status page.
type RecentError struct {
	Time    time.Time `json:"time"`
	Source  string    `json:"source"`
	Message string    `json:"message"`
}

const maxRecentErrors = 50

var (
	errMu      sync.Mutex
	recentErrs []RecentError // oldest first
)

// ReportError keeps an error for the status page (the newest 50).
func ReportError(source, message string) {
	if len(message) > 500 {
		message = message[:500] + "..."
	}
	errMu.Lock()
	defer errMu.Unlock()
	recentErrs = append(recentErrs, RecentError{Time: time.Now().UTC(), Source: source, Message: message})
	if n := len(recentErrs); n > maxRecentErrors {
		recentErrs = append([]RecentError(nil), recentErrs[n-maxRecentErrors:]...)
	}
}

// RecentErrors returns the kept errors, newest first.
func RecentErrors() []RecentError {
	errMu.Lock()
	defer errMu.Unlock()
	out := make([]RecentError, len(recentErrs))
	for i, e := range recentErrs {
		out[len(out)-1-i] = e
	}
	return out
}
