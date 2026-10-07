package vm

import (
	_ "embed"
	"encoding/json"
)

// dockerDefaultSeccomp is Docker's default seccomp profile (moby v27.4.1,
// profiles/seccomp/default.json, Apache-2.0).
//
//go:embed seccomp-docker-default.json
var dockerDefaultSeccomp []byte

// PasstSyscalls are what passt's self-sandboxing needs beyond the default
// profile, which allows them only to containers with CAP_SYS_ADMIN: it moves into
// new user, mount, IPC, UTS and PID namespaces (unshare, clone), mounts and
// pivots into an empty tmpfs there and drops to nobody. The kernel still checks
// capabilities: inside the namespaces it creates, never on the host.
//
// These four are the minimum, found by removing each from a wider list until
// passt failed: without unshare it cannot create its user namespace, without
// mount, umount2 or pivot_root its sandbox fails. (clone, clone3, setns, chroot
// and sethostname are not needed.)
var PasstSyscalls = []string{"unshare", "mount", "umount2", "pivot_root"}

// SeccompProfile is the seccomp profile of VM containers: Docker's default plus
// PasstSyscalls. Everything the default profile forbids stays forbidden
// (kexec, bpf, keyctl, perf_event_open, ptrace of others, module loading, ...).
func SeccompProfile() (string, error) {
	var p map[string]any
	if err := json.Unmarshal(dockerDefaultSeccomp, &p); err != nil {
		return "", err
	}
	rules, _ := p["syscalls"].([]any)
	p["syscalls"] = append(rules, map[string]any{
		"names":  PasstSyscalls,
		"action": "SCMP_ACT_ALLOW",
	})
	b, err := json.Marshal(p)
	return string(b), err
}
