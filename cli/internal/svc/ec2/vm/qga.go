package vm

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// The pure parts of talking to qemu-guest-agent (QGA) over the virtio-serial
// socket: request encoding, reply framing, guest-exec results and the parsers
// of the /proc sample the CloudWatch metrics come from. The connection itself
// (a docker exec of socat into the VM container) is in the ec2 package.

// QGARequest encodes a command as one line.
func QGARequest(cmd string, args any) []byte {
	m := map[string]any{"execute": cmd}
	if args != nil {
		m["arguments"] = args
	}
	b, _ := json.Marshal(m)
	return append(b, '\n')
}

// QGASync is the handshake that discards whatever a previous client left in
// the channel: the agent answers with 0xFF and the same id (guest-sync-delimited).
func QGASync(id int64) []byte { return QGARequest("guest-sync-delimited", map[string]int64{"id": id}) }

// QGAReply is a parsed reply line.
type QGAReply struct {
	Return json.RawMessage `json:"return"`
	Error  *struct {
		Class string `json:"class"`
		Desc  string `json:"desc"`
	} `json:"error"`
	// Synced is set for the reply to guest-sync-delimited (it starts with 0xFF).
	Synced bool `json:"-"`
}

// Err is the agent's error, if the command failed.
func (r QGAReply) Err() error {
	if r.Error == nil {
		return nil
	}
	return fmt.Errorf("guest agent: %s: %s", r.Error.Class, r.Error.Desc)
}

// ParseQGAReply parses one line of the agent's output. ok is false for
// anything that is not a reply (partial or garbled lines are skipped by the caller).
func ParseQGAReply(line []byte) (r QGAReply, ok bool) {
	line = bytes.TrimSpace(line)
	synced := false
	if i := bytes.IndexByte(line, 0xFF); i >= 0 {
		synced = true
		line = bytes.TrimSpace(line[i+1:])
	}
	if len(line) == 0 || line[0] != '{' || json.Unmarshal(line, &r) != nil {
		return QGAReply{}, false
	}
	r.Synced = synced
	return r, r.Return != nil || r.Error != nil
}

// QGAExecRequest starts a program in the guest and captures its output.
func QGAExecRequest(path string, args []string) []byte {
	return QGARequest("guest-exec", map[string]any{"path": path, "arg": args, "capture-output": true})
}

// QGAExecStatusRequest polls a guest-exec by pid.
func QGAExecStatusRequest(pid int64) []byte {
	return QGARequest("guest-exec-status", map[string]int64{"pid": pid})
}

// ParseExecPID is the pid in the reply to guest-exec.
func ParseExecPID(ret json.RawMessage) (int64, error) {
	var v struct {
		PID int64 `json:"pid"`
	}
	if err := json.Unmarshal(ret, &v); err != nil || v.PID == 0 {
		return 0, fmt.Errorf("guest-exec returned no pid: %s", ret)
	}
	return v.PID, nil
}

// ExecStatus is the result of guest-exec-status.
type ExecStatus struct {
	Exited    bool
	ExitCode  int
	Stdout    string
	Stderr    string
	Truncated bool
}

// ParseExecStatus decodes the reply of guest-exec-status.
func ParseExecStatus(ret json.RawMessage) (ExecStatus, error) {
	var v struct {
		Exited   bool   `json:"exited"`
		ExitCode int    `json:"exitcode"`
		Signal   int    `json:"signal"`
		OutData  string `json:"out-data"`
		ErrData  string `json:"err-data"`
		OutTrunc bool   `json:"out-truncated"`
		ErrTrunc bool   `json:"err-truncated"`
	}
	if err := json.Unmarshal(ret, &v); err != nil {
		return ExecStatus{}, fmt.Errorf("guest-exec-status: %w", err)
	}
	out, err := base64.StdEncoding.DecodeString(v.OutData)
	if err != nil {
		return ExecStatus{}, fmt.Errorf("guest-exec-status: out-data: %w", err)
	}
	errs, err := base64.StdEncoding.DecodeString(v.ErrData)
	if err != nil {
		return ExecStatus{}, fmt.Errorf("guest-exec-status: err-data: %w", err)
	}
	st := ExecStatus{Exited: v.Exited, ExitCode: v.ExitCode, Stdout: string(out), Stderr: string(errs), Truncated: v.OutTrunc || v.ErrTrunc}
	if v.Signal != 0 && v.ExitCode == 0 {
		st.ExitCode = 128 + v.Signal // the shell's convention
	}
	return st, nil
}

// ShellCommand is the guest-exec program and arguments that run a shell
// command with a timeout (coreutils timeout when present; it exits 124).
func ShellCommand(command string, timeoutSeconds int) (path string, args []string) {
	const wrapper = `if command -v timeout >/dev/null 2>&1; then exec timeout -k 5 "$1" /bin/sh -c "$2"; else exec /bin/sh -c "$2"; fi`
	return "/bin/sh", []string{"-c", wrapper, "hc", strconv.Itoa(timeoutSeconds), command}
}

// StatsCommand samples the guest's /proc for CloudWatch: CPU time twice, a
// second apart (so the utilization needs no state between samples), then memory,
// network, disks and the process count.
const StatsCommand = `cat /proc/stat; echo "==cpu2"; sleep 1; cat /proc/stat; echo "==meminfo"; cat /proc/meminfo; echo "==netdev"; cat /proc/net/dev; echo "==diskstats"; cat /proc/diskstats; echo "==procs"; ls -d /proc/[0-9]* | wc -l`

// GuestStats is one sample of the guest.
type GuestStats struct {
	CPUPercent  float64
	MemoryUsed  uint64 // bytes: MemTotal - MemAvailable
	MemoryTotal uint64
	NetRx       uint64 // cumulative bytes over all interfaces but lo
	NetTx       uint64
	DiskRead    uint64 // cumulative bytes over whole block devices
	DiskWrite   uint64
	Processes   uint64
}

// MemoryPercent is used memory as a share of the guest's memory.
func (g GuestStats) MemoryPercent() float64 {
	if g.MemoryTotal == 0 {
		return 0
	}
	return float64(g.MemoryUsed) / float64(g.MemoryTotal) * 100
}

// ParseGuestStats parses the output of StatsCommand.
func ParseGuestStats(out string) (GuestStats, error) {
	secs := map[string]string{}
	cur := "cpu1"
	var b strings.Builder
	flush := func() { secs[cur] = b.String(); b.Reset() }
	for _, l := range strings.Split(out, "\n") {
		if name, ok := strings.CutPrefix(l, "=="); ok {
			flush()
			cur = strings.TrimSpace(name)
			continue
		}
		b.WriteString(l + "\n")
	}
	flush()

	var g GuestStats
	t1, i1, ok1 := cpuTimes(secs["cpu1"])
	t2, i2, ok2 := cpuTimes(secs["cpu2"])
	if !ok1 || !ok2 {
		return g, fmt.Errorf("no cpu line in the guest's /proc/stat")
	}
	if t2 > t1 {
		busy := float64(t2-t1) - float64(i2-i1)
		g.CPUPercent = max(0, min(100, busy/float64(t2-t1)*100))
	}
	var avail uint64
	for _, l := range strings.Split(secs["meminfo"], "\n") {
		f := strings.Fields(l)
		if len(f) < 2 {
			continue
		}
		kb, _ := strconv.ParseUint(f[1], 10, 64)
		switch f[0] {
		case "MemTotal:":
			g.MemoryTotal = kb * 1024
		case "MemAvailable:":
			avail = kb * 1024
		}
	}
	if g.MemoryTotal == 0 {
		return g, fmt.Errorf("no MemTotal in the guest's /proc/meminfo")
	}
	g.MemoryUsed = g.MemoryTotal - min(avail, g.MemoryTotal)
	for _, l := range strings.Split(secs["netdev"], "\n") {
		name, rest, ok := strings.Cut(l, ":")
		if !ok || strings.TrimSpace(name) == "lo" {
			continue
		}
		f := strings.Fields(rest)
		if len(f) < 9 {
			continue
		}
		rx, _ := strconv.ParseUint(f[0], 10, 64)
		tx, _ := strconv.ParseUint(f[8], 10, 64)
		g.NetRx += rx
		g.NetTx += tx
	}
	for _, l := range strings.Split(secs["diskstats"], "\n") {
		f := strings.Fields(l)
		if len(f) < 10 || !wholeDisk(f[2]) {
			continue
		}
		r, _ := strconv.ParseUint(f[5], 10, 64) // sectors read
		w, _ := strconv.ParseUint(f[9], 10, 64) // sectors written
		g.DiskRead += r * 512
		g.DiskWrite += w * 512
	}
	g.Processes, _ = strconv.ParseUint(strings.TrimSpace(secs["procs"]), 10, 64)
	return g, nil
}

// cpuTimes returns the total and idle (idle + iowait) jiffies of the aggregate cpu line.
func cpuTimes(s string) (total, idle uint64, ok bool) {
	for _, l := range strings.Split(s, "\n") {
		f := strings.Fields(l)
		if len(f) < 5 || f[0] != "cpu" {
			continue
		}
		for i, v := range f[1:] {
			if i >= 8 { // guest and guest_nice are already part of user and nice
				break
			}
			n, err := strconv.ParseUint(v, 10, 64)
			if err != nil {
				return 0, 0, false
			}
			total += n
			if i == 3 || i == 4 {
				idle += n
			}
		}
		return total, idle, true
	}
	return 0, 0, false
}

// wholeDisk is true for virtio, SCSI/SATA and NVMe disks, not partitions, loop or device-mapper devices.
func wholeDisk(name string) bool {
	for _, p := range []string{"vd", "sd", "xvd", "hd"} {
		if rest, ok := strings.CutPrefix(name, p); ok && rest != "" {
			return strings.Trim(rest, "abcdefghijklmnopqrstuvwxyz") == ""
		}
	}
	if rest, ok := strings.CutPrefix(name, "nvme"); ok {
		// nvme0n1 is a disk, nvme0n1p1 a partition
		return !strings.Contains(rest, "p") && strings.Contains(rest, "n")
	}
	return false
}
