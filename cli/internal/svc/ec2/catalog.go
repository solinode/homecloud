package ec2

// InstanceType is a named CPU/memory shape.
type InstanceType struct {
	Name     string  `json:"name"`
	VCPUs    float64 `json:"vcpus"`
	MemoryMB int64   `json:"memory_mb"`
	Family   string  `json:"family"`
}

var instanceTypes = []InstanceType{
	{"t3.nano", 0.5, 512, "General purpose (burstable)"},
	{"t3.micro", 1, 1024, "General purpose (burstable)"},
	{"t3.small", 1, 2048, "General purpose (burstable)"},
	{"t3.medium", 2, 4096, "General purpose (burstable)"},
	{"t3.large", 2, 8192, "General purpose (burstable)"},
	{"t3.xlarge", 4, 16384, "General purpose (burstable)"},
	{"m5.large", 2, 8192, "General purpose"},
	{"m5.xlarge", 4, 16384, "General purpose"},
	{"c5.large", 2, 4096, "Compute optimized"},
	{"c5.xlarge", 4, 8192, "Compute optimized"},
	{"r5.large", 2, 16384, "Memory optimized"},
}

func findType(name string) (InstanceType, bool) {
	for _, t := range instanceTypes {
		if t.Name == name {
			return t, true
		}
	}
	return InstanceType{}, false
}

// Image is an AMI: a Docker image plus how to boot it.
type Image struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Ref         string `json:"ref"` // docker image reference
	Platform    string `json:"platform"`
	// KeepAlive boots the image like a VM: run user data once, then idle so the
	// instance stays up. Off for application images that run their own process.
	KeepAlive bool   `json:"keep_alive"`
	Owner     string `json:"owner"` // "homecloud" for the catalog, otherwise the account ID
	State     string `json:"state"`
	CreatedAt string `json:"created_at,omitempty"`
	// SourceInstance is set for images captured from an instance.
	SourceInstance string `json:"source_instance,omitempty"`
}

var catalog = []Image{
	{ID: "ami-ubuntu-24-04", Name: "Ubuntu Server 24.04 LTS", Description: "Canonical Ubuntu 24.04 (Noble Numbat)", Ref: "ubuntu:24.04", Platform: "linux", KeepAlive: true},
	{ID: "ami-ubuntu-22-04", Name: "Ubuntu Server 22.04 LTS", Description: "Canonical Ubuntu 22.04 (Jammy Jellyfish)", Ref: "ubuntu:22.04", Platform: "linux", KeepAlive: true},
	{ID: "ami-debian-12", Name: "Debian 12", Description: "Debian GNU/Linux 12 (bookworm)", Ref: "debian:12", Platform: "linux", KeepAlive: true},
	{ID: "ami-amazonlinux-2023", Name: "Amazon Linux 2023", Description: "Amazon Linux 2023 base image", Ref: "amazonlinux:2023", Platform: "linux", KeepAlive: true},
	{ID: "ami-rocky-9", Name: "Rocky Linux 9", Description: "Enterprise Linux compatible", Ref: "rockylinux:9", Platform: "linux", KeepAlive: true},
	{ID: "ami-fedora-41", Name: "Fedora 41", Description: "Fedora Linux 41", Ref: "fedora:41", Platform: "linux", KeepAlive: true},
	{ID: "ami-alpine-3-20", Name: "Alpine Linux 3.20", Description: "Minimal 5 MB Linux", Ref: "alpine:3.20", Platform: "linux", KeepAlive: true},
	{ID: "ami-nginx", Name: "NGINX web server", Description: "NGINX serving on port 80", Ref: "nginx:alpine", Platform: "linux", KeepAlive: false},
	{ID: "ami-python-3-12", Name: "Python 3.12", Description: "Debian with Python 3.12", Ref: "python:3.12-slim", Platform: "linux", KeepAlive: true},
	{ID: "ami-node-22", Name: "Node.js 22", Description: "Debian with Node.js 22 LTS", Ref: "node:22-slim", Platform: "linux", KeepAlive: true},
}

// bootScript runs user data once per instance, then idles until stopped.
const bootScript = `mkdir -p /var/lib/homecloud
if [ -s /var/lib/homecloud/user-data ] && [ ! -f /var/lib/homecloud/.user-data-done ]; then
  echo "[homecloud] running user data"
  sh /var/lib/homecloud/user-data 2>&1 | tee /var/log/homecloud-user-data.log
  touch /var/lib/homecloud/.user-data-done
  echo "[homecloud] user data finished"
fi
echo "[homecloud] instance ready"
trap 'exit 0' TERM INT
while :; do sleep 3600 & wait $!; done
`
