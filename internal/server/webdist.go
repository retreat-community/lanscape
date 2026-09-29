package server

import (
	"embed"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

// webdist holds the built SPA (web/ is built into internal/server/webdist by CI and
// `make web`; the directory only contains .gitkeep in a fresh checkout).
//
//go:embed all:webdist
var webdist embed.FS

func (s *Server) spa() http.Handler {
	sub, _ := fs.Sub(webdist, "webdist")
	files := http.FileServer(http.FS(sub))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		if p == "" {
			p = "index.html"
		}
		if _, err := fs.Stat(sub, p); err != nil {
			// client-side routes fall back to the app shell
			if _, err := fs.Stat(sub, "index.html"); err != nil {
				http.Error(w, "web UI not built: run `make web`", http.StatusNotFound)
				return
			}
			r = r.Clone(r.Context())
			r.URL.Path = "/"
		}
		if strings.HasPrefix(p, "assets/") {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		} else {
			w.Header().Set("Cache-Control", "no-cache")
		}
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "same-origin")
		files.ServeHTTP(w, r)
	})
}
