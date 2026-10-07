package vm

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestSeccompProfile(t *testing.T) {
	s, err := SeccompProfile()
	if err != nil {
		t.Fatal(err)
	}
	var p struct {
		DefaultAction string `json:"defaultAction"`
		Syscalls      []struct {
			Names  []string `json:"names"`
			Action string   `json:"action"`
			Args   []any    `json:"args"`
		} `json:"syscalls"`
	}
	if err := json.Unmarshal([]byte(s), &p); err != nil {
		t.Fatal(err)
	}
	if p.DefaultAction != "SCMP_ACT_ERRNO" {
		t.Errorf("default action %q: the profile must stay deny-by-default", p.DefaultAction)
	}
	last := p.Syscalls[len(p.Syscalls)-1]
	if last.Action != "SCMP_ACT_ALLOW" || len(last.Args) != 0 || strings.Join(last.Names, ",") != "unshare,mount,umount2,pivot_root" {
		t.Errorf("added rule %+v", last)
	}
	// Nothing else was loosened: the default profile's rules are all still there,
	// and dangerous calls it leaves out are not in the added rule.
	if len(p.Syscalls) < 5 {
		t.Errorf("only %d rules: Docker's default profile is missing", len(p.Syscalls))
	}
	for _, n := range last.Names {
		switch n {
		case "kexec_load", "init_module", "finit_module", "bpf", "keyctl", "perf_event_open", "reboot", "ptrace":
			t.Errorf("%s added", n)
		}
	}
}
