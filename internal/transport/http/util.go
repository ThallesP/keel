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

// spa serves files from web and falls back to index.html for client routes, with nginx's old
// rules (docs/go/spec/web-data.md §2.2): /assets/* (content-hashed names) immutable for a year;
// index.html never cached, since after an upgrade it names assets the old one did not; and no
// directory listings (a directory is a client route like any other missing file).
func spa(web fs.FS) http.Handler {
	files := http.FileServer(http.FS(web))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		if info, err := fs.Stat(web, name); name == "" || name == "index.html" || err != nil || info.IsDir() {
			w.Header().Set("Cache-Control", "no-cache")
			r = r.Clone(r.Context())
			r.URL.Path = "/" // FileServer answers "/" with index.html
		} else if strings.HasPrefix(name, "assets/") {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		}
		files.ServeHTTP(w, r)
	})
}
