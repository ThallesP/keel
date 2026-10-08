package http

import (
	"net/http"
	"strings"

	"github.com/ThallesP/keel/internal/app"
)

// InvalidateHeader names, on the response to a write, the realtime topics the write changed:
// comma-separated API path prefixes, the same ones the WebSocket publishes after it
// (docs/go/spec/web-data.md §9.3). The dashboard's fetch client refetches the active queries they
// cover before the mutation resolves, so a component never sees its own write missing.
const InvalidateHeader = "Keel-Invalidate"

// withInvalidations records what a write request under /api publishes and names it in
// InvalidateHeader. Only the caller's organization's topics are named; a caller who had no
// organization when the request began (sign-up, founding, joining) gets what was published,
// which can only concern the organization the request put them in. Runs inside withActor.
func withInvalidations(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
			next.ServeHTTP(w, r)
			return
		}
		if !strings.HasPrefix(r.URL.Path, "/api/") {
			next.ServeHTTP(w, r) // agent, proxy and OTLP callbacks
			return
		}
		ctx, rec := app.WithInvalidations(r.Context())
		iw := &invalidateWriter{ResponseWriter: w, rec: rec, org: ActorFrom(ctx).OrganizationID}
		next.ServeHTTP(iw, r.WithContext(ctx))
	})
}

// invalidateWriter sets InvalidateHeader when the response starts, after the use case returned.
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
