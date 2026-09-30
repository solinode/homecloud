package vm

import (
	"encoding/json"
	"strings"
)

// Seed is what cloud-init reads from the NoCloud datasource (an ISO with the
// label "cidata"). Guests can also reach the instance metadata service, but
// NoCloud comes first in the images' datasource lists, so cloud-init settles on
// the seed and never probes the network for a datasource.
type Seed struct {
	InstanceID string
	Hostname   string
	// PublicKey is authorized for the image's default user.
	PublicKey string
	// UserData is passed through untouched: a script (#!), a #cloud-config
	// document or any other format cloud-init understands.
	UserData string
}

// Files returns the seed's files by name (meta-data, user-data).
func (s Seed) Files() map[string][]byte {
	// JSON is valid YAML and quotes everything safely.
	q := func(v string) string { b, _ := json.Marshal(v); return string(b) }
	var meta strings.Builder
	meta.WriteString("instance-id: " + q(s.InstanceID) + "\n")
	meta.WriteString("local-hostname: " + q(s.Hostname) + "\n")
	meta.WriteString("hostname: " + q(s.Hostname) + "\n")
	if k := strings.TrimSpace(s.PublicKey); k != "" {
		meta.WriteString("public-keys:\n  - " + q(k) + "\n")
	}
	user := s.UserData
	if strings.TrimSpace(user) == "" {
		user = "#cloud-config\n{}\n"
	}
	return map[string][]byte{"meta-data": []byte(meta.String()), "user-data": []byte(user), "vendor-data": []byte(VendorData)}
}

// VendorData installs and starts qemu-guest-agent, which run-command and the
// CloudWatch guest metrics talk to. It is a script rather than cloud-config
// `packages:` because cloud-init does not merge list keys of vendor-data with
// the user's own cloud-config, which would silently drop the agent whenever
// the user data sets packages or runcmd. The agent is socket-activated by the
// virtio-serial port on most images, hence the explicit start.
const VendorData = `#!/bin/sh
# HomeCloud: the guest agent behind run-command and the instance's metrics.
if ! command -v qemu-ga >/dev/null 2>&1; then
  export DEBIAN_FRONTEND=noninteractive
  n=0
  until apt-get update -qq && apt-get install -y -qq qemu-guest-agent; do
    n=$((n+1)); [ $n -ge 5 ] && break; sleep 5
  done
fi
systemctl enable --now qemu-guest-agent 2>/dev/null || systemctl start qemu-guest-agent 2>/dev/null || true
`

// ReadyMarkers are what the guest prints on its serial console when it is up.
var (
	// CloudInitDone is printed by cloud-init's final stage.
	CloudInitDone = "finished at"
	// LoginPrompt is what getty prints; it is the fallback for images
	// without cloud-init.
	LoginPrompt = " login: "
)

// Ready inspects a boot's console output. done is true once cloud-init
// finished; prompt is true once a login prompt appeared (guests that never run
// cloud-init's final stage still get there).
func Ready(console string) (done, prompt bool) {
	for _, line := range strings.Split(console, "\n") {
		if strings.Contains(line, "Cloud-init v.") && strings.Contains(line, CloudInitDone) {
			done = true
		}
		if strings.Contains(line, LoginPrompt) {
			prompt = true
		}
	}
	return done, prompt
}
