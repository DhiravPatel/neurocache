// Package webui embeds the built React dashboard and serves it with SPA
// fallback. The `dist` directory is populated by the Docker build (stage 1
// runs `pnpm build` for apps/web, then copies dist here).
//
// In development, the dist is a single placeholder page that tells the user
// to run the Vite dev server — or better, to do a production build.
package webui

import (
	"embed"
	"io"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

//go:embed all:dist
var distFS embed.FS

// Handler returns an http.Handler that serves the embedded dashboard with
// SPA fallback. API routes (prefixes in apiPrefixes) are delegated to next.
func Handler(next http.Handler, apiPrefixes ...string) http.Handler {
	sub, err := fs.Sub(distFS, "dist")
	if err != nil {
		return next
	}
	fileServer := http.FileServer(http.FS(sub))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for _, p := range apiPrefixes {
			if strings.HasPrefix(r.URL.Path, p) {
				next.ServeHTTP(w, r)
				return
			}
		}
		// Resolve against embedded FS.
		reqPath := strings.TrimPrefix(r.URL.Path, "/")
		if reqPath == "" {
			reqPath = "index.html"
		}
		if f, err := sub.Open(reqPath); err == nil {
			_ = f.Close()
			// Long-cache hashed assets, short-cache everything else.
			if strings.HasPrefix(reqPath, "assets/") {
				w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
			}
			fileServer.ServeHTTP(w, r)
			return
		}
		// SPA fallback — serve index.html for unknown paths.
		idx, err := sub.Open("index.html")
		if err != nil {
			http.Error(w, "dashboard not built", http.StatusNotFound)
			return
		}
		defer idx.Close()
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.Copy(w, idx)
	})
}

// Ensure path has no `..` escape attempts. Kept for future use if we accept
// untrusted paths from the request (currently we don't — we take r.URL.Path).
var _ = path.Clean
