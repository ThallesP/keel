package http

import (
	"encoding/json"
	"encoding/json/jsontext"
	jsonv2 "encoding/json/v2"
	"io"
	"io/fs"
	"net/http"
	"path"
	"strings"

	"github.com/danielgtaylor/huma/v2"
)

var wireJSON = jsonv2.JoinOptions(json.DefaultOptionsV1(), jsonv2.FormatNilSliceAsNull(false), jsontext.EscapeForHTML(false))

func jsonEncode(w io.Writer, v any) error {
	if err := jsonv2.MarshalWrite(w, v, wireJSON); err != nil {
		return err
	}
	_, err := io.WriteString(w, "\n")
	return err
}

var jsonFormat = huma.Format{Marshal: jsonEncode, Unmarshal: json.Unmarshal}

func spa(web fs.FS) http.Handler {
	files := http.FileServer(http.FS(web))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		switch info, err := fs.Stat(web, name); {
		case name == "index.html" || err != nil || info.IsDir():
			w.Header().Set("Cache-Control", "no-cache")
			r = r.Clone(r.Context())
			r.URL.Path = "/"
		case strings.HasPrefix(name, "assets/"):
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		}
		files.ServeHTTP(w, r)
	})
}
