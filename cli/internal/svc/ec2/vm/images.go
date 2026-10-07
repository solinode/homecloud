package vm

import (
	"fmt"
	"regexp"
	"strings"
)

// Base is an official cloud image, pinned to a release and verified by
// checksum after download. Bump the release, URLs and checksums together (the
// checksum files next to the images list them; ParseChecksums reads them and
// TestCatalogMatchesPublishedChecksums, with HC_TEST_NET=1, compares).
type Base struct {
	Key         string // stable key, used in cache file names
	Release     string
	DefaultUser string
	Files       map[Arch]File
	// AgentImage is the Docker image of the same distribution release: the
	// qemu-guest-agent packages (and the dependencies its minimal userland
	// lacks) are downloaded in it once and handed to the guest on the seed
	// disk, so installing the agent needs no network in the guest.
	AgentImage string
}

// AgentCacheName is the directory in the image cache that holds the guest
// agent's packages for an architecture.
func (b Base) AgentCacheName(a Arch) string {
	return fmt.Sprintf("%s-%s-qemu-guest-agent", b.Key, a.Deb())
}

// AgentFetchScript downloads qemu-guest-agent and the packages it needs that
// the distribution's minimal image lacks into /images/<$1> (run in a container
// of AgentImage). The file names are shortened: the seed disk is an ISO image.
const AgentFetchScript = `set -eu
dest=/images/$1
[ -f "$dest/.complete" ] && exit 0
rm -rf "$dest.part"
mkdir -p "$dest.part/partial"
export DEBIAN_FRONTEND=noninteractive
apt-get update -qq
apt-get install -y -qq --download-only --no-install-recommends -o Dir::Cache::archives="$dest.part" qemu-guest-agent >/dev/null
rm -rf "$dest.part/partial" "$dest.part/lock"
n=0
for f in "$dest.part"/*.deb; do n=$((n+1)); mv "$f" "$dest.part/p$n.deb"; done
[ "$n" -gt 0 ]
touch "$dest.part/.complete"
rm -rf "$dest"
mv "$dest.part" "$dest"
`

// File is one architecture's image.
type File struct {
	URL string
	// Sum is the hex SHA-256 (64 digits) or SHA-512 (128 digits) of the file.
	Sum string
}

// Name of the image file in the cache: unique per release, architecture and content.
func (b Base) CacheName(a Arch) string {
	f := b.Files[a]
	return fmt.Sprintf("%s-%s-%s-%.12s.qcow2", b.Key, a.Deb(), b.Release, f.Sum)
}

// For returns the image for an architecture.
func (b Base) For(a Arch) (File, error) {
	f, ok := b.Files[a]
	if !ok {
		return File{}, fmt.Errorf("no %s image for %s", b.Key, a)
	}
	return f, nil
}

// Bases are the VM images HomeCloud can launch, by key.
var Bases = map[string]Base{
	"ubuntu-24.04": {
		Key: "ubuntu-24.04", Release: "20260926", DefaultUser: "ubuntu", AgentImage: "ubuntu:24.04",
		Files: map[Arch]File{
			ArchAArch64: {
				URL: "https://cloud-images.ubuntu.com/releases/noble/release-20260926/ubuntu-24.04-server-cloudimg-arm64.img",
				Sum: "1d6bffe64b848468ac97f821d369a4846d983de1800ccf6b5ec8853e85cefc55",
			},
			ArchX8664: {
				URL: "https://cloud-images.ubuntu.com/releases/noble/release-20260926/ubuntu-24.04-server-cloudimg-amd64.img",
				Sum: "6a81c37564db9b1ee84e141922625e1d7c5b389b99bb3c572e0243607d5bb4d2",
			},
		},
	},
	"debian-12": {
		Key: "debian-12", Release: "20260923-2610", DefaultUser: "debian", AgentImage: "debian:12",
		Files: map[Arch]File{
			ArchAArch64: {
				URL: "https://cloud.debian.org/images/cloud/bookworm/20260923-2610/debian-12-genericcloud-arm64-20260923-2610.qcow2",
				Sum: "89a752d5c7d8e88bee39a57d88d496cca574f7d1e8df5df7a169825435f99feb11ed13ab27ec7bee96fe1584be4ff9479358fbca6cc859efa109f4aba18724c8",
			},
			ArchX8664: {
				URL: "https://cloud.debian.org/images/cloud/bookworm/20260923-2610/debian-12-genericcloud-amd64-20260923-2610.qcow2",
				Sum: "3d94c9dd66d8a283fde060b8810373b7ae04038b956ee00d553d1ae564f6fbdeaa2fd6e7404e87e53348b5a9509170f3ea7c0731ba737590c5c4cb8d559a47f8",
			},
		},
	},
}

var sumRE = regexp.MustCompile(`^(?:[0-9a-fA-F]{64}|[0-9a-fA-F]{128})$`)

// ValidSum reports whether s is a SHA-256 or SHA-512 hex digest.
func ValidSum(s string) bool { return sumRE.MatchString(s) }

// ParseChecksums reads a SHA256SUMS or SHA512SUMS file ("<hex>  <name>" or
// "<hex> *<name>", one per line; "# comments" and blank lines are skipped)
// into digests by file name.
func ParseChecksums(text string) (map[string]string, error) {
	out := map[string]string{}
	for n, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		sum, name, ok := strings.Cut(line, " ")
		name = strings.TrimSpace(name)
		name = strings.TrimPrefix(name, "*") // binary mode marker
		if !ok || name == "" || !ValidSum(sum) {
			return nil, fmt.Errorf("checksums line %d: %q is not \"<hex digest>  <file>\"", n+1, line)
		}
		out[name] = strings.ToLower(sum)
	}
	return out, nil
}
