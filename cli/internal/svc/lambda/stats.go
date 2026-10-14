package lambda

import "sync/atomic"

// Invocations served by a new environment (cold) or an existing one (warm),
// since the server started.
var coldStarts, warmStarts atomic.Uint64

// EnvStats counts execution environments, for operator metrics.
type EnvStats struct {
	Idle       int    `json:"environments_idle"`
	Busy       int    `json:"environments_busy"`
	Starting   int    `json:"environments_starting"`
	ColdStarts uint64 `json:"cold_starts"`
	WarmStarts uint64 `json:"warm_starts"`
}

// EnvironmentStats counts the execution environments of every function.
func (s *Service) EnvironmentStats() EnvStats {
	st := EnvStats{ColdStarts: coldStarts.Load(), WarmStarts: warmStarts.Load()}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, p := range s.pools {
		st.Starting += p.starting
		for _, e := range p.envs {
			if e.busy {
				st.Busy++
			} else {
				st.Idle++
			}
		}
	}
	return st
}
