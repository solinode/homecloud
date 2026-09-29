// Package web serves the web console, a static Next.js export embedded at build time.
package web

import (
	"embed"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

//go:embed all:dist
var dist embed.FS

// Handler serves the console. Unknown paths fall back to their .html page or
// to index.html so client-side routes survive a reload.
func Handler() http.Handler {
	root, _ := fs.Sub(dist, "dist")
	files := http.FileServerFS(root)
	if _, err := fs.Stat(root, "index.html"); err != nil {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Write([]byte(placeholder))
		})
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		if p == "" || p == "." {
			files.ServeHTTP(w, r)
			return
		}
		if _, err := fs.Stat(root, p); err != nil {
			for _, alt := range []string{p + ".html", p + "/index.html"} {
				if _, err := fs.Stat(root, alt); err == nil {
					r2 := r.Clone(r.Context())
					r2.URL.Path = "/" + alt
					files.ServeHTTP(w, r2)
					return
				}
			}
			r2 := r.Clone(r.Context())
			r2.URL.Path = "/"
			files.ServeHTTP(w, r2)
			return
		}
		if strings.HasPrefix(p, "_next/static/") {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		}
		files.ServeHTTP(w, r)
	})
}

const placeholder = `<!doctype html><html><head><meta charset="utf-8"><title>HomeCloud</title></head>
<body style="font-family:system-ui;padding:40px;max-width:640px">
<h1>HomeCloud is running</h1>
<p>The API is up at <code>/api/v1</code>, but this build does not bundle the web console.
Build it with <code>make console</code> in the repository, or run <code>npm run dev</code> in <code>console/</code>.</p>
</body></html>`
