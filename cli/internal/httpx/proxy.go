package httpx

import (
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"strings"
)

// ProxyTrust identifies reverse proxies whose X-Forwarded-For and
// X-Forwarded-Proto headers are believed. The zero value trusts nothing.
type ProxyTrust struct{ nets []netip.Prefix }

// ParseTrustedProxies parses CIDRs and bare IP addresses (a comma inside an
// entry also separates several).
func ParseTrustedProxies(list []string) (*ProxyTrust, error) {
	t := &ProxyTrust{}
	for _, item := range list {
		for _, e := range strings.Split(item, ",") {
			e = strings.TrimSpace(e)
			if e == "" {
				continue
			}
			if p, err := netip.ParsePrefix(e); err == nil {
				t.nets = append(t.nets, p.Masked())
				continue
			}
			a, err := netip.ParseAddr(e)
			if err != nil {
				return nil, fmt.Errorf("--trusted-proxies: %q is not a CIDR or IP address", e)
			}
			a = a.Unmap()
			t.nets = append(t.nets, netip.PrefixFrom(a, a.BitLen()))
		}
	}
	return t, nil
}

// Empty reports whether no proxy is trusted.
func (t *ProxyTrust) Empty() bool { return t == nil || len(t.nets) == 0 }

func (t *ProxyTrust) trusts(a netip.Addr) bool {
	if t == nil {
		return false
	}
	a = a.Unmap()
	for _, p := range t.nets {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

func parseHostAddr(s string) (netip.Addr, bool) {
	s = strings.TrimSpace(s)
	if h, _, err := net.SplitHostPort(s); err == nil {
		s = h
	}
	s = strings.Trim(s, "[]")
	if i := strings.IndexByte(s, '%'); i >= 0 {
		s = s[:i]
	}
	a, err := netip.ParseAddr(s)
	return a, err == nil
}

// Client resolves the real client of r: the connection's peer, unless the peer
// is a trusted proxy, in which case it is the rightmost X-Forwarded-For entry
// that is not itself a trusted proxy (entries further left are client-supplied
// and can be forged). secure reports whether the client used HTTPS, taken from
// X-Forwarded-Proto behind a trusted proxy and from the connection otherwise.
func (t *ProxyTrust) Client(r *http.Request) (ip string, secure bool) {
	peer := ClientIP(r)
	secure = r.TLS != nil
	pa, ok := parseHostAddr(peer)
	if !ok || !t.trusts(pa) {
		return peer, secure
	}
	if hs := r.Header.Values("X-Forwarded-Proto"); len(hs) > 0 {
		// The last value was appended by the nearest proxy.
		last := hs[len(hs)-1]
		if i := strings.LastIndexByte(last, ','); i >= 0 {
			last = last[i+1:]
		}
		switch strings.ToLower(strings.TrimSpace(last)) {
		case "https":
			secure = true
		case "http":
			secure = false
		}
	}
	var chain []string
	for _, h := range r.Header.Values("X-Forwarded-For") {
		for _, e := range strings.Split(h, ",") {
			if e = strings.TrimSpace(e); e != "" {
				chain = append(chain, e)
			}
		}
	}
	ip = peer
	for i := len(chain) - 1; i >= 0; i-- {
		a, ok := parseHostAddr(chain[i])
		if !ok {
			break // garbage from a proxy we trust: don't guess
		}
		ip = a.Unmap().String()
		if !t.trusts(a) {
			break
		}
	}
	return ip, secure
}

// Wrap makes handlers behind h see the real client: for requests from a trusted
// proxy, RemoteAddr becomes the client address and r.TLS is set (or cleared)
// to match X-Forwarded-Proto, so ClientIP and RequestContext (aws:SourceIp,
// aws:SecureTransport), sign-in throttling and the audit trail all use it.
func (t *ProxyTrust) Wrap(h http.Handler) http.Handler {
	if t.Empty() {
		return h
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		peer, ok := parseHostAddr(ClientIP(r))
		if !ok || !t.trusts(peer) {
			h.ServeHTTP(w, r)
			return
		}
		ip, secure := t.Client(r)
		r2 := r.WithContext(r.Context())
		r2.RemoteAddr = net.JoinHostPort(ip, "0")
		switch {
		case secure && r2.TLS == nil:
			r2.TLS = &tls.ConnectionState{HandshakeComplete: true} // terminated by the proxy
		case !secure:
			r2.TLS = nil
		}
		h.ServeHTTP(w, r2)
	})
}
