//go:build !linux && !darwin

package server

func fsUsage(string) (total, free uint64, ok bool) { return 0, 0, false }
func cpuSeconds() (float64, bool)                  { return 0, false }
func residentBytes() (float64, bool)               { return 0, false }
func openFDs() (float64, bool)                     { return 0, false }
