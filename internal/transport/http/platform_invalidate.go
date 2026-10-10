package http

import (
	"net/http"
	"strings"

	"github.com/ThallesP/keel/internal/app"
)

const InvalidateHeader = "Keel-Invalidate"

func withInvalidations(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if authSafeMethod(r.Method) || !strings.HasPrefix(r.URL.Path, "/api/") {
			next.ServeHTTP(w, r)
			return
		}
		ctx, rec := app.WithInvalidations(r.Context())
		iw := &invalidateWriter{ResponseWriter: w, rec: rec, org: ActorFrom(ctx).OrganizationID}
		next.ServeHTTP(iw, r.WithContext(ctx))
	})
}

type invalidateWriter struct {
	http.ResponseWriter
	rec     *app.Invalidations
	org     string
	started bool
}

func (w *invalidateWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w *invalidateWriter) WriteHeader(code int) {
	w.start()
	w.ResponseWriter.WriteHeader(code)
}

func (w *invalidateWriter) Write(b []byte) (int, error) {
	w.start()
	return w.ResponseWriter.Write(b)
}

func (w *invalidateWriter) start() {
	if w.started {
		return
	}
	w.started = true
	if topics := w.rec.Topics(w.org); len(topics) > 0 {
		w.Header().Set(InvalidateHeader, strings.Join(topics, ","))
	}
}
