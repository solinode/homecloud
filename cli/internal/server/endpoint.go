package server

import (
	"encoding/json"
	"net"
	"os"
	"strings"

	"github.com/homecloudhq/homecloud/cli/internal/core"
)

// ClientEndpoint is the API URL recorded in the credentials file for the CLI.
// Listening on a wildcard address (0.0.0.0, ::) says nothing about where to
// connect, so the file records the public URL when one is set and loopback
// otherwise.
func ClientEndpoint(cfg core.Config) string {
	scheme := "http"
	if cfg.TLS() {
		scheme = "https"
	}
	host, port, err := net.SplitHostPort(cfg.APIAddr)
	if err != nil {
		return scheme + "://" + cfg.APIAddr
	}
	if isWildcardHost(host) {
		if cfg.PublicURL != "" {
			return strings.TrimRight(cfg.PublicURL, "/")
		}
		host = "127.0.0.1"
	}
	return scheme + "://" + net.JoinHostPort(host, port)
}

func isWildcardHost(h string) bool {
	if h == "" {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsUnspecified()
}

// fixWildcardEndpoint repairs a credentials file written by an older version
// that recorded a wildcard listen address (http://0.0.0.0:8080) as the endpoint.
func fixWildcardEndpoint(cfg core.Config) {
	path := CredentialsPath(cfg.DataDir)
	b, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var raw map[string]any
	if json.Unmarshal(b, &raw) != nil {
		return
	}
	ep, _ := raw["endpoint"].(string)
	host, _, err := net.SplitHostPort(strings.TrimPrefix(strings.TrimPrefix(ep, "https://"), "http://"))
	if err != nil || !isWildcardHost(host) {
		return
	}
	raw["endpoint"] = ClientEndpoint(cfg)
	out, _ := json.MarshalIndent(raw, "", "  ")
	_ = os.WriteFile(path, out, 0o600)
}
