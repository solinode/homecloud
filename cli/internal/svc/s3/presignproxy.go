package s3

import (
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
)

// PresignPrefix is where presigned URLs made by the console's "presign" call
// are served: MinIO's port is published on loopback only (Docker bypasses host
// firewalls), so the API forwards them.
const PresignPrefix = "/_s3/"

// publicPresigned turns a URL presigned for MinIO's own address into one that
// goes through the API at its public base URL. The signature covers the path
// and MinIO's host, both of which the proxy restores.
func (s *Service) publicPresigned(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	out := s.env.Cfg.PublicBase() + strings.TrimSuffix(PresignPrefix, "/") + u.EscapedPath()
	if u.RawQuery != "" {
		out += "?" + u.RawQuery
	}
	return out
}

// ServePresigned forwards a presigned GET, HEAD or PUT to MinIO. MinIO checks
// the signature itself, and only URLs presigned with its own access key are
// forwarded, so this endpoint grants nothing a presigned URL would not.
func (s *Service) ServePresigned(w http.ResponseWriter, r *http.Request) {
	s.mu.RLock()
	target, user := s.direct, s.user
	s.mu.RUnlock()
	q := r.URL.Query()
	if target == "" || q.Get("X-Amz-Signature") == "" || !strings.HasPrefix(q.Get("X-Amz-Credential"), user+"/") ||
		(r.Method != http.MethodGet && r.Method != http.MethodHead && r.Method != http.MethodPut) {
		http.Error(w, "not a presigned request", http.StatusForbidden)
		return
	}
	// Object bytes come from users and share the API's origin: sandbox them like the console's inline views.
	w.Header().Set("Content-Security-Policy", "sandbox allow-scripts allow-forms allow-popups allow-modals allow-downloads")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	rp := &httputil.ReverseProxy{
		Director: func(req *http.Request) {
			req.URL.Scheme, req.URL.Host = "http", target
			req.URL.Path = "/" + strings.TrimPrefix(req.URL.Path, PresignPrefix)
			if raw := req.URL.RawPath; raw != "" {
				req.URL.RawPath = "/" + strings.TrimPrefix(raw, PresignPrefix)
			}
			req.Host = target // the signature covers MinIO's host
			req.Header.Del("X-Forwarded-For")
		},
	}
	rp.ServeHTTP(w, r)
}
