package http

import (
	"encoding/json"
	"io"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

func jsonEncode(w io.Writer, v any) error { return json.NewEncoder(w).Encode(v) }

// spa serves files from web and falls back to index.html for client routes.
func spa(web fs.FS) http.Handler {
	files := http.FileServer(http.FS(web))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		if name == "" {
			name = "index.html"
		}
		if _, err := fs.Stat(web, name); err != nil {
			r = r.Clone(r.Context())
			r.URL.Path = "/"
		} else if strings.HasPrefix(name, "assets/") {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		}
		files.ServeHTTP(w, r)
	})
}
