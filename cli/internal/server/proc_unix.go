//go:build linux || darwin

package server

import (
	"os"
	"strconv"
	"strings"
	"syscall"
)

// fsUsage returns the size and free space of the filesystem holding path.
func fsUsage(path string) (total, free uint64, ok bool) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, 0, false
	}
	bs := uint64(st.Bsize) //nolint:unconvert // int64 on Linux, uint32 on macOS
	return st.Blocks * bs, st.Bavail * bs, true
}

// cpuSeconds is the user and system CPU time the process used.
func cpuSeconds() (float64, bool) {
	var ru syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &ru); err != nil {
		return 0, false
	}
	tv := func(t syscall.Timeval) float64 { return float64(t.Sec) + float64(t.Usec)/1e6 }
	return tv(ru.Utime) + tv(ru.Stime), true
}

// residentBytes is the process's resident memory (Linux only).
func residentBytes() (float64, bool) {
	b, err := os.ReadFile("/proc/self/statm")
	if err != nil {
		return 0, false
	}
	f := strings.Fields(string(b))
	if len(f) < 2 {
		return 0, false
	}
	pages, err := strconv.ParseUint(f[1], 10, 64)
	if err != nil {
		return 0, false
	}
	return float64(pages * uint64(os.Getpagesize())), true
}

// openFDs is the number of open file descriptors (Linux only).
func openFDs() (float64, bool) {
	es, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		return 0, false
	}
	return float64(len(es)), true
}
